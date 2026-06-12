package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"
)

// count_tokens anchoring (OPIK-6873 follow-up).
//
// Anthropic's /v1/messages/count_tokens endpoint is free (no token billing)
// and accepts Claude Code's own OAuth token, so we can measure the EXACT
// token cost of any text we possess instead of relying on calibrated
// chars/token ratios. Measurements are cached persistently keyed by
// (model, sha256) — static config content (skill menu blocks, memory files,
// agent blurbs, MCP instructions) is hash-stable, so it's measured once
// ever and steady-state API traffic is ~zero.
//
// The measurement pass runs in a detached child at turn end (same pattern
// as the /context fetcher): it re-derives the anchor candidates from the
// transcript, measures the biggest cache misses under a per-turn budget,
// and persists them. The NEXT flush picks them up via measuredOrEstimate.
// Totals are unaffected (per-call usage is already exact); anchors improve
// the split and shrink `unattributed` to genuinely unobserved content.
//
// As a bonus, on a session's cache-cold first call the bundled system
// prompt + built-in tool schemas — the one block we can never read — is
// derived as a residual: call₁ usage minus everything we CAN account for.
// Stored per CC version, replacing the hand-maintained constant table
// whenever a measurement exists.

const (
	countTokensEndpoint = "https://api.anthropic.com/v1/messages/count_tokens"
	anthropicVersion    = "2023-06-01"
	oauthBeta           = "oauth-2025-04-20"
	defaultCountModel   = "claude-fable-5"

	tokenCountBudget = 10 // API calls per measurement pass, biggest-first
	oauthSkewMs      = 60_000
)

// ---------------------------------------------------------------------------
// Persistent cache: { "<model>|<sha256>": tokens }

var (
	tokCacheOnce sync.Once
	tokCacheMu   sync.Mutex
	tokCacheMap  map[string]int
)

func tokenCachePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "opik-token-counts.json")
	}
	return filepath.Join(home, ".opik-token-counts.json")
}

func loadTokenCache() map[string]int {
	tokCacheOnce.Do(func() {
		tokCacheMap = map[string]int{}
		data, err := os.ReadFile(tokenCachePath())
		if err == nil {
			_ = json.Unmarshal(data, &tokCacheMap)
		}
	})
	return tokCacheMap
}

func tokenCacheGet(key string) (int, bool) {
	tokCacheMu.Lock()
	defer tokCacheMu.Unlock()
	v, ok := loadTokenCache()[key]
	return v, ok
}

// saveTokenCounts merges updates into the on-disk cache (reload + merge +
// atomic rename, so concurrent sessions don't clobber each other's rows).
func saveTokenCounts(updates map[string]int) {
	if len(updates) == 0 {
		return
	}
	tokCacheMu.Lock()
	defer tokCacheMu.Unlock()
	merged := map[string]int{}
	if data, err := os.ReadFile(tokenCachePath()); err == nil {
		_ = json.Unmarshal(data, &merged)
	}
	for k, v := range updates {
		merged[k] = v
	}
	loadTokenCache() // ensure the Once ran before replacing the map
	tokCacheMap = merged
	data, err := json.Marshal(merged)
	if err != nil {
		return
	}
	tmp := tokenCachePath() + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, tokenCachePath())
	}
}

// ---------------------------------------------------------------------------
// Lookup used by the attribution code

// billingModel is the model the current snapshot's measurements are keyed
// by; set once per computeBillingSnapshot from the transcript.
var billingModel = defaultCountModel

func setBillingModel(entries []TranscriptEntry) {
	if m := mostRecentModelFromEntries(entries); m != "" {
		billingModel = m
	}
}

// measuredOrEstimate returns the exact measured token count for text when
// one is cached for the current model, falling back to the calibrated
// ratio estimate.
func measuredOrEstimate(text, contentType string) int {
	if text == "" {
		return 0
	}
	if v, ok := tokenCacheGet(billingModel + "|" + sha256hex(text)); ok {
		return v
	}
	return tokEstimateAs(text, contentType)
}

// ---------------------------------------------------------------------------
// Auth: Claude Code's own OAuth credential (or ANTHROPIC_API_KEY)

func countTokensHeaders() map[string]string {
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return map[string]string{
			"x-api-key":         key,
			"anthropic-version": anthropicVersion,
			"content-type":      "application/json",
		}
	}
	if tok := claudeOAuthToken(); tok != "" {
		return map[string]string{
			"authorization":     "Bearer " + tok,
			"anthropic-version": anthropicVersion,
			"anthropic-beta":    oauthBeta,
			"content-type":      "application/json",
		}
	}
	return nil
}

func claudeOAuthToken() string {
	if blob := oauthBlobFromFile(); blob != nil {
		if tok := validOAuthToken(blob); tok != "" {
			return tok
		}
	}
	if runtime.GOOS == "darwin" {
		if blob := oauthBlobFromKeychain(); blob != nil {
			return validOAuthToken(blob)
		}
	}
	return ""
}

type oauthBlob struct {
	AccessToken string `json:"accessToken"`
	ExpiresAt   int64  `json:"expiresAt"`
}

func validOAuthToken(b *oauthBlob) string {
	if b.AccessToken == "" {
		return ""
	}
	if b.ExpiresAt > 0 && time.Now().UnixMilli() >= b.ExpiresAt-oauthSkewMs {
		return ""
	}
	return b.AccessToken
}

func oauthBlobFromFile() *oauthBlob {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		dir = filepath.Join(home, ".claude")
	}
	data, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		return nil
	}
	return parseOAuthBlob(data)
}

func oauthBlobFromKeychain() *oauthBlob {
	out, err := exec.Command("security", "find-generic-password",
		"-s", "Claude Code-credentials", "-w").Output()
	if err != nil {
		return nil
	}
	return parseOAuthBlob(out)
}

func parseOAuthBlob(data []byte) *oauthBlob {
	var wrapper struct {
		ClaudeAiOauth *oauthBlob `json:"claudeAiOauth"`
	}
	if json.Unmarshal(data, &wrapper) != nil {
		return nil
	}
	return wrapper.ClaudeAiOauth
}

// ---------------------------------------------------------------------------
// The endpoint

// countTokensHTTP is swappable for tests.
var countTokensHTTP = func(payload []byte, headers map[string]string) (int, error) {
	req, err := http.NewRequest("POST", countTokensEndpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("count_tokens: %s", resp.Status)
	}
	var out struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.InputTokens, nil
}

func countTokensFor(model, text string, headers map[string]string) (int, error) {
	payload, err := json.Marshal(map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": text},
		},
	})
	if err != nil {
		return 0, err
	}
	return countTokensHTTP(payload, headers)
}

// ---------------------------------------------------------------------------
// Measurement pass (runs in the detached child)

type anchorCandidate struct {
	sha, text, contentType string
}

// anchorCandidates gathers every text whose exact size improves attribution:
// transcript content (prompts, skill bodies, menu blocks, instruction
// blocks, string tool results) plus on-disk config (memory files, agent
// frontmatter is covered transitively by the agents extractor's content).
func anchorCandidates(entries []TranscriptEntry) []anchorCandidate {
	seen := map[string]bool{}
	var out []anchorCandidate
	add := func(text, contentType string) {
		if len(text) < 200 { // tiny texts: ratio error is ≤ a few tokens
			return
		}
		sha := sha256hex(text)
		if seen[sha] {
			return
		}
		seen[sha] = true
		out = append(out, anchorCandidate{sha, text, contentType})
	}

	skillBodyNames := skillBodyNameBySHA(entries)
	for _, e := range entries {
		switch e.Type {
		case "user":
			if e.Message == nil {
				continue
			}
			for _, c := range e.Message.Content {
				switch c.Type {
				case "text":
					if _, ok := skillBodyNames[sha256hex(c.Text)]; ok {
						add(c.Text, "skill_body")
					} else {
						add(c.Text, "user_prompt")
					}
				case "tool_result":
					if s, ok := c.Content.(string); ok {
						add(s, "tool_result")
					}
				}
			}
		case "attachment":
			if e.Attachment == nil {
				continue
			}
			switch e.Attachment.Type {
			case "skill_listing":
				for _, block := range parseSkillListingMenu(e.Attachment.ContentString(), e.Attachment.Names) {
					add(block, "skill_listing_menu")
				}
			case "mcp_instructions_delta":
				for _, b := range e.Attachment.AddedBlocks {
					add(b, "prose")
				}
			}
		}
	}

	// The reconstructed environment block — changes per session (git
	// status), so it mostly rides the ratio estimate, but stable repos
	// get anchored.
	add(environmentBlockText(), "prose")

	// On-disk config content: memory files + agent files.
	if m := extractMemorySnapshot(); m != nil {
		for _, f := range m["files"].([]map[string]interface{}) {
			if body, err := os.ReadFile(f["path"].(string)); err == nil {
				add(string(body), "memory_file")
			}
		}
	}
	if a := extractAgentsSnapshot(); a != nil {
		for _, ag := range a["agents"].([]map[string]interface{}) {
			if body, err := os.ReadFile(ag["path"].(string)); err == nil {
				if meta := frontmatter(string(body)); meta != "" {
					add(meta, "agent_frontmatter")
				}
			}
		}
	}
	return out
}

// runTokenCountPass measures the biggest uncached candidates under budget
// and persists them, then derives the cc_builtin cold-start residual when
// the session's first call is available. Returns the number of API calls
// spent (for tests/logging).
func runTokenCountPass(entries []TranscriptEntry, budget int) int {
	headers := countTokensHeaders()
	if headers == nil {
		debugLog("count_tokens: no credential — skipping")
		return 0
	}
	model := mostRecentModelFromEntries(entries)
	if model == "" {
		model = defaultCountModel
	}

	candidates := anchorCandidates(entries)
	var misses []anchorCandidate
	for _, c := range candidates {
		if _, ok := tokenCacheGet(model + "|" + c.sha); !ok {
			misses = append(misses, c)
		}
	}
	sort.Slice(misses, func(i, j int) bool {
		return len(misses[i].text) > len(misses[j].text)
	})

	updates := map[string]int{}
	spent := 0

	// Baseline: the envelope overhead of a single one-token user message.
	baselineKey := model + "|__baseline__"
	baseline, ok := tokenCacheGet(baselineKey)
	if !ok && len(misses) > 0 {
		b, err := countTokensFor(model, ".", headers)
		if err != nil {
			debugLog("count_tokens: baseline failed: %v", err)
			return 0
		}
		baseline = b
		updates[baselineKey] = b
		spent++
	}

	for _, c := range misses {
		if spent >= budget {
			break
		}
		n, err := countTokensFor(model, c.text, headers)
		if err != nil {
			debugLog("count_tokens: measure failed: %v", err)
			break
		}
		spent++
		tokens := n - baseline + 1 // "." ≈ 1 token of content
		if tokens < 0 {
			tokens = 0
		}
		updates[model+"|"+c.sha] = tokens
	}

	saveTokenCounts(updates)
	debugLog("count_tokens: measured %d of %d misses (budget %d)", len(updates), len(misses), budget)
	return spent
}

// ---------------------------------------------------------------------------
// Detached child plumbing

func spawnDetachedTokenCount(sessionID string) error {
	if sessionID == "" || os.Getenv("OPIK_CC_DISABLE_TOKEN_COUNT") == "true" {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(),
		"OPIK_CC_TOKEN_COUNT=1",
		"OPIK_CC_FETCH_SESSION_ID="+sessionID,
	)
	detachProcess(cmd)
	devnull, _ := os.Open(os.DevNull)
	if devnull != nil {
		cmd.Stdin = devnull
		cmd.Stdout = devnull
		cmd.Stderr = devnull
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// runTokenCountMode is the detached child's entry point.
func runTokenCountMode() {
	sid := os.Getenv("OPIK_CC_FETCH_SESSION_ID")
	if sid == "" {
		return
	}
	state, err := LoadState(sid)
	if err != nil || state == nil || state.Transcript == "" {
		return
	}
	entries, err := ReadTranscript(state.Transcript, 0)
	if err != nil || len(entries) == 0 {
		return
	}
	runTokenCountPass(entries, tokenCountBudget)
}
