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
		tokens := measuredOrEstimate(s, "memory_file")
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
		tokens := measuredOrEstimate(meta, "agent_frontmatter")
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

type toolResultGroup struct {
	tokens, count int
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

type fileAttachmentGroup struct{ tokens, count int }
