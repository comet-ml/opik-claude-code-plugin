package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// extractMemorySnapshot reads the memory/instruction files Claude Code
// actually assembles into the request at session start, matching what
// `/context` reports under "Memory files". `cc.memory.{summary, files}`.
//
// Important: only the files that are *loaded* count. From the auto-memory
// directory that is the MEMORY.md index alone — the individual fact files
// (feedback_*, reference_*, project_*) are recalled on demand, NOT loaded
// up front, so summing them all overcounts memory by several ktok. The
// `.claude/rules/**/*.md` tree is loaded because CLAUDE.md `@`-imports it,
// so it is included; a bare directory walk is a pragmatic stand-in for
// following every import. AGENTS.md is deliberately NOT read — Claude Code
// does not load it by default (confirmed absent from `/context`).
func extractMemorySnapshot() map[string]interface{} {
	home, _ := os.UserHomeDir()
	cwd := inferCwd()

	paths := []string{}
	if home != "" {
		paths = append(paths, filepath.Join(home, ".claude", "CLAUDE.md"))
	}
	if cwd != "" {
		paths = append(paths,
			filepath.Join(cwd, "CLAUDE.md"),
			filepath.Join(cwd, ".claude", "CLAUDE.md"),
		)
		// Rule files imported by CLAUDE.md. Walked recursively because
		// rules nest (e.g. .claude/rules/apps/opik-backend/*.md).
		rulesDir := filepath.Join(cwd, ".claude", "rules")
		_ = filepath.WalkDir(rulesDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() && strings.HasSuffix(p, ".md") {
				paths = append(paths, p)
			}
			return nil
		})
	}
	if home != "" && cwd != "" {
		// Auto-memory lives at ~/.claude/projects/<cwd-with-slashes-as-dashes>/memory/.
		// Only MEMORY.md (the index) is loaded into context; the fact files
		// beside it are recalled on demand and must NOT be summed here.
		slug := strings.ReplaceAll(cwd, "/", "-")
		paths = append(paths, filepath.Join(home, ".claude", "projects", slug, "memory", "MEMORY.md"))
	}

	seen := map[string]bool{}
	files := make([]map[string]interface{}, 0, len(paths))
	total := 0
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil || seen[abs] {
			continue
		}
		seen[abs] = true
		body, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := string(body)
		// memory_file (2.4) — these are .claude/rules/**/*.md plus the
		// auto-memory MEMORY.md index. They lean heavily on brackets,
		// dashes, short headings, and code conventions, all of which
		// tokenize as separate short tokens — denser than a skill body
		// (3.5) or prose (3.9). Calibrated against /context's "Memory
		// files" rows across 7 rule files (mean 2.37 chars/token).
		tokens := tokEstimateAs(s, "memory_file")
		files = append(files, map[string]interface{}{
			"path":        p,
			"sha256":      sha256hex(s),
			"body_tokens": tokens,
		})
		total += tokens
	}
	if len(files) == 0 {
		return nil
	}
	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_tokens": total,
			"file_count":   len(files),
		},
		"files": files,
	}
}

// extractAgentsSnapshot reads the custom subagent definitions Claude Code
// keeps available each turn, matching `/context`'s "Custom agents" block.
// Like memory, these are request-assembly content not present in the
// transcript, so they are read from the standard on-disk locations:
//   - project: <cwd>/.claude/agents/*.md
//   - user:    ~/.claude/agents/*.md
//   - plugin:  each *installed* plugin's pinned version dir, taken from
//     ~/.claude/plugins/installed_plugins.json (NOT a blind marketplace
//     glob — that would count dozens of uninstalled plugins). Namespaced
//     <plugin>:<agent> to match how /context lists them.
//
// Token cost is the frontmatter only (the always-loaded dispatch blurb),
// not the system-prompt body; we can't see the binary's exact accounting,
// so this is a close on-disk proxy. `cc.agents.{summary, agents}`.
func extractAgentsSnapshot() map[string]interface{} {
	home, _ := os.UserHomeDir()
	cwd := inferCwd()

	type agentFile struct {
		path     string
		basename string // filename without .md — fallback name
		nsPrefix string // "<plugin>:" for plugin agents, "" for project/user
		source   string
	}
	files := []agentFile{}

	addDir := func(dir, source, prefix string) {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.md"))
		sort.Strings(matches)
		for _, p := range matches {
			files = append(files, agentFile{p, strings.TrimSuffix(filepath.Base(p), ".md"), prefix, source})
		}
	}

	if cwd != "" {
		addDir(filepath.Join(cwd, ".claude", "agents"), "project", "")
	}
	if home != "" {
		addDir(filepath.Join(home, ".claude", "agents"), "user", "")
		// Plugin agents only from *enabled* plugins. The
		// installed_plugins.json manifest lists every install on disk;
		// `enabledPlugins` from the layered settings files decides which
		// actually load. Including disabled plugins (e.g. plugin-dev) was
		// producing ghost rows /context never shows.
		enabled := enabledPluginNames(home, cwd)
		for plugin, installPath := range installedPluginPaths(home) {
			if !enabled[plugin] {
				continue
			}
			addDir(filepath.Join(installPath, "agents"), "plugin", plugin+":")
		}
	}

	seen := map[string]bool{}
	agents := make([]map[string]interface{}, 0)
	total := 0
	for _, f := range files {
		abs, err := filepath.Abs(f.path)
		if err != nil || seen[abs] {
			continue
		}
		seen[abs] = true
		body, err := os.ReadFile(f.path)
		if err != nil {
			continue
		}
		s := string(body)
		// Only the YAML frontmatter (name + description + tools) is
		// always present — it's the dispatch blurb injected into the
		// Agent tool schema. The markdown body below the closing `---`
		// is the agent's system prompt, loaded on demand at invocation,
		// so summing the whole file overcounts (mirrors the MEMORY.md
		// fact-file trap above). Fall back to the full body if there is
		// no frontmatter.
		meta := frontmatter(s)
		if meta == "" {
			meta = s
		}
		// The display name in /context comes from the YAML `name:` field,
		// NOT the filename (e.g. meta-auditor.md exposes itself as
		// `config-auditor`). Falling back to the basename keeps
		// frontmatter-less files identifiable.
		displayName := frontmatterField(meta, "name")
		if displayName == "" {
			displayName = f.basename
		}
		tokens := tokEstimateAs(meta, "agent_frontmatter")
		agents = append(agents, map[string]interface{}{
			"name":        f.nsPrefix + displayName,
			"path":        f.path,
			"source":      f.source,
			"sha256":      sha256hex(s),
			"body_tokens": tokens,
		})
		total += tokens
	}
	if len(agents) == 0 {
		return nil
	}
	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_tokens": total,
			"agent_count":  len(agents),
		},
		"agents": agents,
	}
}

// frontmatter returns the YAML frontmatter block (the text between the
// leading `---` and the next `---`), or "" if the document has none. Used
// to size the always-loaded portion of an agent definition.
func frontmatter(s string) string {
	t := strings.TrimLeft(s, " \t\r\n")
	if !strings.HasPrefix(t, "---") {
		return ""
	}
	rest := t[3:]
	// Require the opening marker to be its own line.
	if i := strings.IndexByte(rest, '\n'); i >= 0 && strings.TrimSpace(rest[:i]) == "" {
		rest = rest[i+1:]
	} else {
		return ""
	}
	for _, marker := range []string{"\n---\n", "\n---\r\n", "\n...\n"} {
		if end := strings.Index(rest, marker); end >= 0 {
			return rest[:end]
		}
	}
	// Trailing closing marker with no newline after it (EOF).
	if strings.HasSuffix(strings.TrimRight(rest, " \t\r\n"), "\n---") {
		return strings.TrimSuffix(strings.TrimRight(rest, " \t\r\n"), "\n---")
	}
	return ""
}

// installedPluginPaths maps plugin name → the install dir of the version
// the session runs, read from ~/.claude/plugins/installed_plugins.json.
// Keys there are "<plugin>@<marketplace>"; we return just <plugin> because
// that is the namespace Claude Code uses for agents/skills (e.g. "opik" in
// "opik:agent-reviewer"). When a plugin has multiple install entries (e.g.
// user + project scope), the last one wins — scope precedence is not
// modeled, and duplicate agent files are de-duped by path downstream.
//
// Memoized per-home: skill resolution calls this once per available skill
// (100+/turn) and the manifest doesn't change within a turn. Keying on
// `home` (not a bare sync.Once) keeps tests deterministic when each
// subtest points HOME at a fresh tmp dir.
var (
	pluginPathsMu   sync.Mutex
	pluginPathsMemo map[string]map[string]string
)

func installedPluginPaths(home string) map[string]string {
	pluginPathsMu.Lock()
	defer pluginPathsMu.Unlock()
	if pluginPathsMemo == nil {
		pluginPathsMemo = map[string]map[string]string{}
	}
	if v, ok := pluginPathsMemo[home]; ok {
		return v
	}
	v := readInstalledPluginPaths(home)
	pluginPathsMemo[home] = v
	return v
}

func readInstalledPluginPaths(home string) map[string]string {
	out := map[string]string{}
	if home == "" {
		return out
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "plugins", "installed_plugins.json"))
	if err != nil {
		return out
	}
	var manifest struct {
		Plugins map[string][]struct {
			InstallPath string `json:"installPath"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return out
	}
	for key, entries := range manifest.Plugins {
		plugin, _, _ := strings.Cut(key, "@")
		for _, e := range entries {
			if e.InstallPath != "" {
				out[plugin] = e.InstallPath
			}
		}
	}
	return out
}

// extractThinkingSnapshot aggregates thinking-block tokens bucketed by effort
// level. Level is derived from actual thinking tokens per LLM call (the
// transcript does not expose the requested budget_tokens).
//
// Buckets: minimal ≤500, light 501–3 000, medium 3 001–10 000, heavy >10 000.
//
// `cc.thinking.{summary, by_level}`.
func extractThinkingSnapshot(entries []TranscriptEntry, parsed []ParsedEntry) map[string]interface{} {
	if parsed == nil {
		parsed = ParseAssistantMessages(entries)
		DeduplicateUsage(parsed)
	}

	// Sum thinking tokens per LLM call (MessageID).
	callThinking := map[string]int{}
	anonTokens := 0
	for _, p := range parsed {
		if p.ContentType != "thinking" {
			continue
		}
		if p.MessageID == "" {
			anonTokens += p.AttributedOutputTokens
			continue
		}
		callThinking[p.MessageID] += p.AttributedOutputTokens
	}
	if anonTokens > 0 {
		callThinking["__anon"] = anonTokens
	}
	if len(callThinking) == 0 {
		return nil
	}

	type levelGroup struct{ calls, tokens int }
	byLevel := map[string]*levelGroup{}
	totalTokens, totalCalls := 0, 0

	for _, tok := range callThinking {
		l := thinkingLevel(tok)
		g, ok := byLevel[l]
		if !ok {
			g = &levelGroup{}
			byLevel[l] = g
		}
		g.calls++
		g.tokens += tok
		totalTokens += tok
		totalCalls++
	}

	order := []string{"minimal", "light", "medium", "heavy"}
	byLevelOut := make([]map[string]interface{}, 0, len(byLevel))
	for _, l := range order {
		g, ok := byLevel[l]
		if !ok {
			continue
		}
		byLevelOut = append(byLevelOut, map[string]interface{}{
			"level":      l,
			"tokens":     g.tokens,
			"call_count": g.calls,
		})
	}

	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_tokens": totalTokens,
			"call_count":   totalCalls,
		},
		"by_level": byLevelOut,
	}
}

func thinkingLevel(tokens int) string {
	switch {
	case tokens > 10000:
		return "heavy"
	case tokens > 3000:
		return "medium"
	case tokens > 500:
		return "light"
	default:
		return "minimal"
	}
}

// extractToolResultsSnapshot aggregates tool_result bytes grouped by the
// tool that produced them. `cc.tool_results.{summary, by_tool}`.
//
// Mixed accounting (OPIK-6873): token sums are cumulative-to-date
// (fullEntries) because every prior result is replayed in each request —
// SUM of the per-trace value across a session's traces yields
// billing-weighted attribution. Counts are new-this-turn (turnEntries) so
// the same SUM yields true call counts instead of a quadratic blow-up.
func extractToolResultsSnapshot(fullEntries, turnEntries []TranscriptEntry) map[string]interface{} {
	full := toolResultGroups(fullEntries)
	if len(full) == 0 {
		return nil
	}
	turn := toolResultGroups(turnEntries)

	names := make([]string, 0, len(full)+len(turn))
	seen := map[string]bool{}
	for name := range full {
		names = append(names, name)
		seen[name] = true
	}
	// A result whose tool_use predates the turn boundary can land under a
	// different key in the suffix walk (e.g. "unknown") — keep the union so
	// its count isn't dropped.
	for name := range turn {
		if !seen[name] {
			names = append(names, name)
		}
	}

	totalTokens, totalCount := 0, 0
	byToolOut := make([]map[string]interface{}, 0, len(names))
	for _, name := range names {
		tokens, count := 0, 0
		if g := full[name]; g != nil {
			tokens = g.tokens
		}
		if g := turn[name]; g != nil {
			count = g.count
		}
		byToolOut = append(byToolOut, map[string]interface{}{
			"name":   name,
			"tokens": tokens,
			"count":  count,
		})
		totalTokens += tokens
		totalCount += count
	}
	sort.Slice(byToolOut, func(i, j int) bool {
		return byToolOut[i]["tokens"].(int) > byToolOut[j]["tokens"].(int)
	})
	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_tokens": totalTokens,
			"count":        totalCount,
		},
		"by_tool": byToolOut,
	}
}

type toolResultGroup struct {
	tokens, count int
}

// toolResultGroups walks entries in order and aggregates result tokens and
// call counts per tool name.
//
// Two result-delivery shapes need special handling:
//   - Normal tool_result: user message content block with type="tool_result".
//   - ToolSearch: result is delivered as a `deferred_tools_delta` attachment
//     (addedLines + addedNames). We attribute that delta's addedLines size
//     to the ToolSearch tool_use that immediately preceded it.
func toolResultGroups(entries []TranscriptEntry) map[string]*toolResultGroup {
	toolNames := map[string]string{}
	for _, e := range entries {
		if e.Type != "assistant" || e.Message == nil {
			continue
		}
		for _, c := range e.Message.Content {
			if c.Type == "tool_use" && c.ID != "" {
				toolNames[c.ID] = c.Name
			}
		}
	}

	type group = toolResultGroup
	byTool := map[string]*group{}

	// Walk entries in order. Pair ToolSearch tool_uses with the
	// `deferred_tools_delta` that immediately follows them (no intervening
	// non-delta event). Two protections against mis-attribution:
	//
	//   1. Pending IDs are a FIFO queue so back-to-back ToolSearches each
	//      get their own delta — neither is silently overwritten.
	//   2. Any event between the ToolSearch and the delta — a regular
	//      tool_result, a non-delta attachment, an assistant message —
	//      drains the queue, so an unrelated delta (e.g. one triggered by
	//      an MCP toggle) can't be mis-attributed to a stale ToolSearch.
	var pendingToolSearches []string // tool_use IDs awaiting a delta
	for _, e := range entries {
		switch e.Type {
		case "assistant":
			if e.Message == nil {
				// Empty assistant entry — drain pending queue (no
				// immediate delta means the ToolSearch's result must have
				// already arrived as a normal tool_result, handled below).
				pendingToolSearches = pendingToolSearches[:0]
				continue
			}
			drainedThisEntry := false
			for _, c := range e.Message.Content {
				if c.Type == "tool_use" && c.Name == "ToolSearch" {
					if !drainedThisEntry {
						pendingToolSearches = pendingToolSearches[:0]
						drainedThisEntry = true
					}
					pendingToolSearches = append(pendingToolSearches, c.ID)
				} else if c.Type == "tool_use" {
					// Some other tool_use sits between ToolSearch and a
					// future delta — drain.
					pendingToolSearches = pendingToolSearches[:0]
				}
			}
		case "user":
			if e.Message == nil {
				pendingToolSearches = pendingToolSearches[:0]
				continue
			}
			for _, c := range e.Message.Content {
				if c.Type != "tool_result" {
					continue
				}
				name := toolNames[c.ToolUseID]
				if name == "" {
					name = "unknown"
				}
				tokens := resultTokens(c.Content)
				g, exists := byTool[name]
				if !exists {
					g = &group{}
					byTool[name] = g
				}
				g.tokens += tokens
				g.count++
			}
			// Any user message (with tool_result OR otherwise) breaks the
			// ToolSearch→delta adjacency, so drain.
			pendingToolSearches = pendingToolSearches[:0]
		case "attachment":
			if e.Attachment == nil {
				continue
			}
			if e.Attachment.Type != "deferred_tools_delta" {
				// Non-delta attachment between ToolSearch and a delta —
				// breaks adjacency.
				pendingToolSearches = pendingToolSearches[:0]
				continue
			}
			if len(pendingToolSearches) == 0 {
				continue
			}
			// Synthesize a ToolSearch tool_result from the delta payload —
			// the addedLines text is exactly what the model sees.
			payload := strings.Join(e.Attachment.AddedLines, "\n")
			if payload == "" {
				payload = strings.Join(e.Attachment.AddedNames, "\n")
			}
			pendingToolSearches = pendingToolSearches[1:]
			if payload == "" {
				continue
			}
			tokens := tokEstimateAs(payload, "deferred_tools_payload")
			g, exists := byTool["ToolSearch"]
			if !exists {
				g = &group{}
				byTool["ToolSearch"] = g
			}
			g.tokens += tokens
			g.count++
		}
	}
	return byTool
}

// resultTokens estimates the token cost of a tool_result.content payload,
// which may be a plain string or an array of `{type:"text", text:"…"}`
// blocks (or anything else, in which case we fall back to the raw JSON
// size as a proxy).
func resultTokens(content interface{}) int {
	switch v := content.(type) {
	case string:
		return tokEstimateAs(v, "tool_result")
	case []interface{}:
		total := 0
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if t, ok := m["text"].(string); ok {
					total += tokEstimateAs(t, "tool_result")
					continue
				}
			}
			raw, _ := json.Marshal(item)
			total += tokEstimateAs(string(raw), "tool_result")
		}
		return total
	default:
		raw, _ := json.Marshal(v)
		return tokEstimateAs(string(raw), "tool_result")
	}
}

// extractUserPromptsSnapshot returns the user-text contribution.
// Tool results don't count here (they're under cc.tool_results); skill
// bodies don't count either (they're under cc.skills.loaded). Without
// excluding skill bodies, a `Skill` tool_use would inflate user_prompts
// by the entire skill body — 100K+ tokens for claude-api.
//
// Mixed accounting (OPIK-6873): token sums are cumulative-to-date
// (fullEntries) because every prior prompt is replayed in each request —
// SUM across a session's traces yields billing-weighted attribution.
// Counts are new-this-turn (turnEntries) so the same SUM yields true
// prompt counts. by_size buckets each prompt individually with the same
// split (tokens cumulative, count new-this-turn).
// `cc.user_prompts.{summary, by_size}`.
func extractUserPromptsSnapshot(fullEntries, turnEntries []TranscriptEntry) map[string]interface{} {
	// Hashes from the FULL transcript: a body loaded in an earlier turn
	// must still be excluded from this turn's count pass.
	skillBodyHashes := skillBodyHashSet(fullEntries)

	type bucketAgg struct{ tokens, count int }
	byBucket := map[string]*bucketAgg{}
	bucketFor := func(name string) *bucketAgg {
		b, ok := byBucket[name]
		if !ok {
			b = &bucketAgg{}
			byBucket[name] = b
		}
		return b
	}

	cumTokens, cumCount := 0, 0
	forEachUserPrompt(fullEntries, skillBodyHashes, func(tokens int) {
		cumTokens += tokens
		cumCount++
		bucketFor(promptBucket(tokens)).tokens += tokens
	})
	if cumCount == 0 {
		return nil
	}

	newTokens, newCount := 0, 0
	forEachUserPrompt(turnEntries, skillBodyHashes, func(tokens int) {
		newTokens += tokens
		newCount++
		bucketFor(promptBucket(tokens)).count++
	})

	bySize := make([]map[string]interface{}, 0, len(byBucket))
	for _, name := range []string{"small", "medium", "large", "xlarge"} {
		b, ok := byBucket[name]
		if !ok {
			continue
		}
		bySize = append(bySize, map[string]interface{}{
			"bucket": name,
			"tokens": b.tokens,
			"count":  b.count,
		})
	}

	summary := map[string]interface{}{
		"total_tokens": cumTokens,
		"count":        newCount,
	}
	if newCount > 0 {
		// Back-compat: bucket of this turn's new prompt mass.
		summary["bucket"] = promptBucket(newTokens)
	}
	return map[string]interface{}{
		"summary": summary,
		"by_size": bySize,
	}
}

// forEachUserPrompt invokes fn with the token estimate of every user text
// block that isn't an injected skill body.
func forEachUserPrompt(entries []TranscriptEntry, skillBodyHashes map[string]bool, fn func(tokens int)) {
	for _, e := range entries {
		if e.Type != "user" || e.Message == nil {
			continue
		}
		for _, c := range e.Message.Content {
			if c.Type != "text" {
				continue
			}
			if skillBodyHashes[sha256hex(c.Text)] {
				continue
			}
			fn(tokEstimateAs(c.Text, "user_prompt"))
		}
	}
}

// skillBodyHashSet returns the set of sha256 hashes of every skill body
// loaded this session — across both Skill-tool-use loads and slash-command
// loads. Comparing by hash (rather than the raw string) is safer against
// pathological cases where a user prompt happens to equal a small skill
// body; collisions on sha256 are vanishingly unlikely. Excluding both
// shapes is what keeps user_prompts from double-counting an injected
// skill body sitting alongside the actual user prompt.
func skillBodyHashSet(entries []TranscriptEntry) map[string]bool {
	loads := buildLoadedSkillBodies(entries)
	out := make(map[string]bool, len(loads))
	for _, l := range loads {
		if l.Body != "" {
			out[sha256hex(l.Body)] = true
		}
	}
	return out
}

func promptBucket(tokens int) string {
	switch {
	case tokens > 8000:
		return "xlarge"
	case tokens > 2000:
		return "large"
	case tokens > 500:
		return "medium"
	default:
		return "small"
	}
}

// extractFileAttachmentsSnapshot returns @-mentioned + system-injected file
// attachments grouped by file extension. Skill bodies are NOT here — they
// go under cc.skills.loaded.
//
// Mixed accounting (OPIK-6873): token sums are cumulative-to-date
// (fullEntries) — attachments replay in every request after they're added —
// while file counts are new-this-turn (turnEntries) so SUM across traces
// yields true file counts. `cc.file_attachments.{summary, by_type}`.
func extractFileAttachmentsSnapshot(fullEntries, turnEntries []TranscriptEntry) map[string]interface{} {
	fullByExt, cumTokens, cumCount := fileAttachmentGroups(fullEntries)
	if cumCount == 0 {
		return nil
	}
	turnByExt, _, newCount := fileAttachmentGroups(turnEntries)

	byTypeOut := make([]map[string]interface{}, 0, len(fullByExt))
	for ext, g := range fullByExt {
		count := 0
		if t := turnByExt[ext]; t != nil {
			count = t.count
		}
		byTypeOut = append(byTypeOut, map[string]interface{}{
			"ext":        ext,
			"tokens":     g.tokens,
			"file_count": count,
		})
	}
	sort.Slice(byTypeOut, func(i, j int) bool {
		return byTypeOut[i]["tokens"].(int) > byTypeOut[j]["tokens"].(int)
	})

	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_tokens": cumTokens,
			"file_count":   newCount,
		},
		"by_type": byTypeOut,
	}
}

type fileAttachmentGroup struct{ tokens, count int }

func fileAttachmentGroups(entries []TranscriptEntry) (map[string]*fileAttachmentGroup, int, int) {
	byExt := map[string]*fileAttachmentGroup{}
	total, fileCount := 0, 0

	for _, e := range entries {
		if e.Type != "attachment" || e.Attachment == nil {
			continue
		}
		if e.Attachment.Type != "file" {
			continue
		}
		var wrapper struct {
			File struct {
				Path    string `json:"path,omitempty"`
				Content string `json:"content,omitempty"`
			} `json:"file"`
		}
		if err := json.Unmarshal(e.Attachment.Content, &wrapper); err != nil {
			continue
		}
		tokens := tokEstimate(wrapper.File.Content)

		ext := strings.ToLower(filepath.Ext(wrapper.File.Path))
		if ext == "" {
			ext = "other"
		}

		g, ok := byExt[ext]
		if !ok {
			g = &fileAttachmentGroup{}
			byExt[ext] = g
		}
		g.tokens += tokens
		g.count++
		total += tokens
		fileCount++
	}

	return byExt, total, fileCount
}

// extractPriorAssistantSnapshot is the cumulative cost of prior assistant
// output in the session — what gets replayed every turn.
//
// Per-block attribution (OPIK-6873): only text and thinking blocks count.
// tool_use blocks are excluded — their cost belongs to the tool lanes, and
// counting them here would double-attribute the same tokens. Computed from
// AttributedOutputTokens (per-block shares of the LLM call's measured
// usage), not whole-message usage.output_tokens.
// `cc.prior_assistant.{summary, by_content}`.
func extractPriorAssistantSnapshot(fullEntries, turnEntries []TranscriptEntry) map[string]interface{} {
	fullText, fullThinking := attributedTextThinking(fullEntries)
	turnText, turnThinking := attributedTextThinking(turnEntries)
	priorText := max(0, fullText-turnText)
	priorThinking := max(0, fullThinking-turnThinking)

	_, sessionMsgs := assistantOutputTotals(fullEntries)
	_, turnMsgs := assistantOutputTotals(turnEntries)
	priorMsgs := max(0, sessionMsgs-turnMsgs)

	priorTokens := priorText + priorThinking
	if priorTokens == 0 && priorMsgs == 0 {
		return nil
	}
	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_tokens":  priorTokens,
			"message_count": priorMsgs,
		},
		"by_content": map[string]interface{}{
			"assistant_text": priorText,
			"thinking":       priorThinking,
		},
	}
}

// attributedTextThinking sums per-block attributed output tokens for text
// and thinking blocks. Per message, attributed shares sum to the call's
// usage.output_tokens, so text+thinking here equals output minus the
// tool_use share.
func attributedTextThinking(entries []TranscriptEntry) (text, thinking int) {
	parsed := ParseAssistantMessages(entries)
	DeduplicateUsage(parsed)
	for _, p := range parsed {
		switch p.ContentType {
		case "text":
			text += p.AttributedOutputTokens
		case "thinking":
			thinking += p.AttributedOutputTokens
		}
	}
	return
}

// assistantOutputTotals sums usage once per LLM call. The transcript
// repeats the same message.usage on EVERY entry of a multi-block message
// (one entry per content block, all sharing message.id) — summing per
// entry double-counted output by ~2x (OPIK-6873 audit: a 2-call session
// reported 2,302 prior tokens for 1,151 real ones).
func assistantOutputTotals(entries []TranscriptEntry) (tokens, msgs int) {
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Type != "assistant" || e.Message == nil || e.Message.Usage == nil {
			continue
		}
		id := e.Message.ID
		if id == "" {
			id = e.UUID
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		tokens += e.Message.Usage.OutputTokens
		msgs++
	}
	return
}

// extractAssistantTextSnapshot returns the per-turn text-block contribution.
// `cc.assistant_text.summary`.
func extractAssistantTextSnapshot(entries []TranscriptEntry) map[string]interface{} {
	total, count := 0, 0
	for _, e := range entries {
		if e.Type != "assistant" || e.Message == nil {
			continue
		}
		for _, c := range e.Message.Content {
			if c.Type == "text" {
				total += tokEstimateAs(c.Text, "assistant_text")
				count++
			}
		}
	}
	if count == 0 {
		return nil
	}
	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_tokens": total,
			"block_count":  count,
		},
	}
}

// extractOutputTokensSnapshot aggregates attributed output tokens by category
// at the trace level. This lets the Sankey visualization use
// sum(metadata.cc.output_tokens.by_category.*) directly without span
// aggregation. `cc.output_tokens.{summary, by_category}`.
//
// Categories:
//   - thinking         — extended thinking blocks
//   - assistant_text   — visible text responses
//   - builtin_tool_use — CC built-in tools (Bash, Read, Edit, …)
//   - mcp_tool_use     — MCP tool calls (name prefix "mcp__")
//   - skill_invocations — Skill tool invocations
//
// `parsed` should be the dedup-applied output of ParseAssistantMessages +
// DeduplicateUsage. Pass nil to reparse from entries.
func extractOutputTokensSnapshot(entries []TranscriptEntry, parsed []ParsedEntry) map[string]interface{} {
	if parsed == nil {
		parsed = ParseAssistantMessages(entries)
		DeduplicateUsage(parsed)
	}

	var (
		thinking         int
		assistantText    int
		builtinToolUse   int
		mcpToolUse       int
		skillInvocations int
	)

	for _, p := range parsed {
		tok := p.AttributedOutputTokens
		switch p.ContentType {
		case "thinking":
			thinking += tok
		case "text":
			assistantText += tok
		case "tool_use":
			switch {
			case strings.HasPrefix(p.Content.Name, "mcp__"):
				mcpToolUse += tok
			case p.Content.Name == "Skill":
				skillInvocations += tok
			default:
				builtinToolUse += tok
			}
		}
	}

	total := thinking + assistantText + builtinToolUse + mcpToolUse + skillInvocations
	if total == 0 {
		return nil
	}
	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_tokens": total,
		},
		"by_category": map[string]interface{}{
			"thinking":          thinking,
			"assistant_text":    assistantText,
			"builtin_tool_use":  builtinToolUse,
			"mcp_tool_use":      mcpToolUse,
			"skill_invocations": skillInvocations,
		},
	}
}
