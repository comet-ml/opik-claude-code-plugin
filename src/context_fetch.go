package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// fetchRuntimeContext runs the user's claude binary in a side-effect-free
// forked session to capture the actual `/context` numbers for the trace
// that just closed, then PATCHes them onto trace.metadata.cc.context_runtime.
// Used by the detached subprocess spawned at the tail of onStop.
//
// Why this exists: every estimate we ship (cc_builtin constants, the
// 130-token MCP-per-tool guess, our calibrated chars/token ratios) is at
// best within ~15% of /context. Claude Code's own /context is essentially
// free to invoke ($0, ~900ms, doesn't hit the API) so we can ground every
// trace in the real numbers instead.
//
// Invocation: `claude --resume <sid> --fork-session --no-session-persistence --output-format json -p /context`.
//   - --fork-session: parent session unaffected, no transcript pollution
//   - --no-session-persistence: the fork lives only in memory
//   - OPIK_CC_SKIP=1 in the child env stops our own hook from re-entering
//     and triggering infinite recursion
func fetchRuntimeContext(ctx context.Context, sessionID, traceID, cwd string) error {
	if sessionID == "" || traceID == "" {
		return fmt.Errorf("fetchRuntimeContext: empty sessionID or traceID")
	}
	out, err := runClaudeContextCommand(ctx, sessionID, cwd)
	if err != nil {
		return fmt.Errorf("run claude /context: %w", err)
	}
	parsed, err := parseContextJSON(out)
	if err != nil {
		return fmt.Errorf("parse /context output: %w", err)
	}
	if parsed == nil {
		return nil
	}
	mergeMetadataCC(traceID, map[string]interface{}{
		"context_runtime": parsed,
	})
	return nil
}

// runClaudeContextCommand spawns `claude … -p /context` and returns the
// raw JSON output. The default 15s timeout covers a cold fork with full
// plugin load (typical: ~1s) without hanging the detached subprocess
// indefinitely if claude misbehaves.
func runClaudeContextCommand(ctx context.Context, sessionID, cwd string) ([]byte, error) {
	claudePath := os.Getenv("OPIK_CC_CLAUDE_BIN")
	if claudePath == "" {
		claudePath = "claude"
	}
	cmd := exec.CommandContext(ctx, claudePath,
		"--resume", sessionID,
		"--fork-session",
		"--no-session-persistence",
		"--output-format", "json",
		"-p", "/context",
	)
	if cwd != "" {
		cmd.Dir = cwd
	}
	// OPIK_CC_SKIP shorts our own hook out at the top of main() so the
	// forked claude's UserPromptSubmit / Stop / SessionEnd events don't
	// recursively call back into fetchRuntimeContext.
	cmd.Env = append(os.Environ(), "OPIK_CC_SKIP=1")
	cmd.Stderr = nil
	return cmd.Output()
}

// claudeContextJSON is the subset of `claude --output-format json` we use.
type claudeContextJSON struct {
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
	SessionID string `json:"session_id"`
}

// parseContextJSON consumes `claude -p /context --output-format json` and
// returns the structured category breakdown. Returns nil (no error) when
// the response lacks a usage table — better to skip the patch than write
// half-parsed garbage.
func parseContextJSON(raw []byte) (map[string]interface{}, error) {
	var doc claudeContextJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc.IsError || doc.Result == "" {
		return nil, fmt.Errorf("claude returned error or empty result")
	}
	return parseContextMarkdown(doc.Result), nil
}

// parseContextMarkdown extracts category → token mappings from the markdown
// `/context` renders. Three sections we care about right now:
//
//   - "### Estimated usage by category"  -> categories[]
//   - "### Custom Agents"                -> agents[]
//   - "### Memory Files"                 -> memory_files[]
//   - "### Skills"                       -> skills[]
//   - "### MCP Tools"                    -> mcp_tools[]
//
// Plus the top-line `**Tokens:** 35.3k / 1m (4%)` for totals. Token values
// in /context use "k" (thousands) and "<" (capped buckets like "< 20")
// which parseTokens normalizes.
func parseContextMarkdown(md string) map[string]interface{} {
	if md == "" {
		return nil
	}
	out := map[string]interface{}{
		"raw_markdown": md,
		"source":       "claude_context_command",
	}

	if m := reTopTokens.FindStringSubmatch(md); m != nil {
		used := parseTokens(m[1])
		total := parseTokens(m[2])
		pct, _ := strconv.Atoi(m[3])
		out["total_tokens_used"] = used
		out["context_window_tokens"] = total
		out["percentage_used"] = pct
	}
	if m := reModel.FindStringSubmatch(md); m != nil {
		out["model"] = strings.TrimSpace(m[1])
	}

	// Section parsers all do the same thing: pull the rows of a markdown
	// table that lives under a `### Heading` and convert each row to a
	// {name, source?, tokens} record. Heading match is anchored so a
	// "Custom Agents" mention in another section's body doesn't false-fire.
	if rows := extractTableRows(md, "Estimated usage by category"); len(rows) > 0 {
		cats := map[string]int{}
		for _, r := range rows {
			if len(r) < 2 {
				continue
			}
			cats[normalizeCategoryKey(r[0])] = parseTokens(r[1])
		}
		out["categories"] = cats
	}
	out["agents"] = parseDetailTable(md, "Custom Agents", []string{"agent_type", "source", "tokens"})
	out["memory_files"] = parseDetailTable(md, "Memory Files", []string{"type", "path", "tokens"})
	out["skills"] = parseDetailTable(md, "Skills", []string{"skill", "source", "tokens"})
	out["mcp_tools"] = parseDetailTable(md, "MCP Tools", []string{"tool", "server", "tokens"})

	return out
}

var (
	reTopTokens = regexp.MustCompile(`\*\*Tokens:\*\*\s+([\d.kKmM]+)\s*/\s*([\d.kKmM]+)\s*\((\d+)%\)`)
	reModel     = regexp.MustCompile(`\*\*Model:\*\*\s+([^\n*]+)`)
)

// normalizeCategoryKey lowercases and replaces spaces+punct with underscores
// so the FE can address categories programmatically without worrying about
// the human-readable variant Claude Code prints (`System tools (deferred)`
// → `system_tools_deferred`).
func normalizeCategoryKey(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	s = strings.NewReplacer(
		" (", "_",
		")", "",
		" ", "_",
		"-", "_",
	).Replace(s)
	return s
}

// extractTableRows returns the data-row cells (skipping header + separator)
// of the first markdown table appearing directly under the given heading.
// "Directly under" means the next `|`-led block; intervening blank lines
// are tolerated.
func extractTableRows(md, heading string) [][]string {
	idx := strings.Index(md, "### "+heading)
	if idx < 0 {
		return nil
	}
	rest := md[idx+len("### "+heading):]
	// Cut at the next `### ` so we don't bleed into the following section.
	if next := strings.Index(rest, "\n### "); next >= 0 {
		rest = rest[:next]
	}
	lines := strings.Split(rest, "\n")
	var rows [][]string
	tableStarted := false
	sepSeen := false
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			if tableStarted {
				break // blank line ends the table
			}
			continue
		}
		if !strings.HasPrefix(l, "|") {
			if tableStarted {
				break
			}
			continue
		}
		tableStarted = true
		// Separator row: `|----|----|`
		if strings.Contains(l, "---") {
			sepSeen = true
			continue
		}
		if !sepSeen {
			continue // header row
		}
		// Split on | and trim. Drop the empty leading/trailing entries
		// from the |…|…| shape.
		parts := strings.Split(l, "|")
		var cells []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			cells = append(cells, p)
		}
		// Trim leading/trailing empty cells caused by the leading/trailing |.
		for len(cells) > 0 && cells[0] == "" {
			cells = cells[1:]
		}
		for len(cells) > 0 && cells[len(cells)-1] == "" {
			cells = cells[:len(cells)-1]
		}
		if len(cells) > 0 {
			rows = append(rows, cells)
		}
	}
	return rows
}

// parseDetailTable extracts a row-keyed list for one of /context's sub-
// tables. `cols` names the columns left→right. Numeric columns named
// `tokens` are parsed through parseTokens so `~110` and `< 20` come out
// as ints (110 and 20 respectively — best-effort).
func parseDetailTable(md, heading string, cols []string) []map[string]interface{} {
	rows := extractTableRows(md, heading)
	if len(rows) == 0 {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(rows))
	for _, r := range rows {
		entry := map[string]interface{}{}
		for i, c := range cols {
			if i >= len(r) {
				break
			}
			val := r[i]
			if c == "tokens" {
				entry[c] = parseTokens(val)
			} else {
				entry[c] = val
			}
		}
		out = append(out, entry)
	}
	return out
}

// parseTokens normalizes the shapes /context uses for token counts:
//
//	"35.3k"  → 35300
//	"1m"     → 1000000
//	"2.5k"   → 2500
//	"~110"   → 110     (approx marker stripped)
//	"< 20"   → 20      (cap marker stripped — best-effort floor)
//	"13"     → 13
//	""       → 0
func parseTokens(s string) int {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "~")
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mul := 1.0
	last := s[len(s)-1]
	switch last {
	case 'k', 'K':
		mul = 1_000
		s = s[:len(s)-1]
	case 'm', 'M':
		mul = 1_000_000
		s = s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int(f * mul)
}

// spawnDetachedContextFetch re-invokes our own binary in a detached child
// process so the actual /context fetch can run outside the lifetime of the
// current hook invocation. The parent (this process) returns immediately,
// claude proceeds, the child does its ~1s of work and PATCHes the trace
// when it can.
//
// Why re-invoke instead of just `go func()`: the parent process exits as
// soon as main() returns, which would kill any goroutine still running.
// Detaching via Setsid lets the child outlive its parent without being
// reparented to the user's interactive shell (where Ctrl-C would kill it).
func spawnDetachedContextFetch(sessionID, traceID, cwd string) error {
	if sessionID == "" || traceID == "" {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	debugLog("context_fetch: spawning self=%s session=%s trace=%s", self, sessionID, traceID[:8])
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(),
		"OPIK_CC_CONTEXT_FETCH=1",
		"OPIK_CC_FETCH_SESSION_ID="+sessionID,
		"OPIK_CC_FETCH_TRACE_ID="+traceID,
		"OPIK_CC_FETCH_CWD="+cwd,
	)
	// Detach: new session/process group so signals to the parent shell
	// don't kill us, and stdio drops so we don't keep the parent's pipes
	// open. The actual SysProcAttr fields differ per OS (Setsid on Unix,
	// CREATE_NEW_PROCESS_GROUP on Windows) — see detachProcess_*.go.
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
	// Don't Wait — we want the child to outlive us. Release the os.Process
	// handle so its exit isn't held in our process table.
	_ = cmd.Process.Release()
	return nil
}

// runContextFetchMode is the entry the detached child runs when invoked
// with OPIK_CC_CONTEXT_FETCH=1. It's the only execution path that
// actually shells out to claude /context.
func runContextFetchMode() {
	sid := os.Getenv("OPIK_CC_FETCH_SESSION_ID")
	tid := os.Getenv("OPIK_CC_FETCH_TRACE_ID")
	cwd := os.Getenv("OPIK_CC_FETCH_CWD")
	debugLog("context_fetch: starting session=%s trace=%s cwd=%s", sid, tid[:8], cwd)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := time.Now()
	if err := fetchRuntimeContext(ctx, sid, tid, cwd); err != nil {
		debugLog("context_fetch: failed after %s: %v", time.Since(start).Round(time.Millisecond), err)
		return
	}
	debugLog("context_fetch: ok in %s trace=%s", time.Since(start).Round(time.Millisecond), tid[:8])
}
