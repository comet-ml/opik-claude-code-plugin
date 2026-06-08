package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// SkillEvent is one skill made available to the model in this turn.
// Identity is (name, sha256) — if a skill body changes across sessions
// we want to see two distinct hashes and treat them as different cost lines.
//
// Source distinguishes a passive listing (the menu of skills available)
// from an active invocation (Claude actually called the Skill tool and
// the body landed in context).
type SkillEvent struct {
	Name         string `json:"name"`
	SHA256       string `json:"sha256"`
	BodyTokens   int    `json:"body_tokens"`
	MenuTokens   int    `json:"menu_tokens"` // always-on menu cost: name + frontmatter description
	FirstSeenIdx int    `json:"first_seen_idx"`
	Source       string `json:"source"` // "project" | "user" | "plugin" | "bundled"
	Path         string `json:"path,omitempty"`
	ToolUseID    string `json:"tool_use_id,omitempty"` // toolu_… id of the load event
}

// extractSkillEvents emits one SkillEvent per unique (name, sha256) seen in
// any skill_listing attachment in this turn. Latest-wins semantics for the
// listing body, but we still record every skill name the listing exposes.
//
// Source shapes (real transcripts):
//
//	{type:"attachment", attachment:{type:"skill_listing", content:"...",
//	                                names:[...], skillCount:N, isInitial:bool}}
//
// We hash attachment.content as the canonical skill-listing body. For
// per-skill body hashes we'd need the on-disk skill files — that's a
// follow-up; this slice gives the FE drillable rows by name + listing hash.
// extractSkillEvents returns ONE event per distinct skill currently available
// to the model — derived from the latest skill_listing in the transcript.
// Actual skill invocations are not events here; the existing engine already
// emits a "Skill" tool_use span we enrich with a body hash in processToolUse.
func extractSkillEvents(entries []TranscriptEntry) []SkillEvent {
	if len(entries) == 0 {
		return nil
	}

	seen := make(map[string]bool) // dedup on name|sha256
	out := make([]SkillEvent, 0, 8)

	for i, entry := range entries {
		if entry.Type != "attachment" {
			continue
		}
		out = appendListingEvents(out, seen, entry, i)
	}
	return out
}

// extractLoadedSkills walks every `Skill` tool_use in the transcript and
// returns one SkillEvent per distinct skill that was actually pulled into
// context. The body that lands in the conversation right after the
// tool_result IS the canonical body the model has from that point on — we
// hash THAT, so both on-disk and bundled skills get a real sha + token
// count. The on-disk lookup is kept only as a useful identity tag (path,
// source=listing vs bundled).
func extractLoadedSkills(entries []TranscriptEntry) []SkillEvent {
	bodyByID := buildSkillBodyMap(entries)

	seen := map[string]int{}
	order := []string{}
	idByName := map[string]string{}
	for i, entry := range entries {
		if entry.Type != "assistant" || entry.Message == nil {
			continue
		}
		for _, c := range entry.Message.Content {
			if c.Type != "tool_use" || c.Name != "Skill" {
				continue
			}
			name := skillInputName(c.Input)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; !ok {
				order = append(order, name)
			}
			seen[name] = i
			idByName[name] = c.ID
		}
	}
	out := make([]SkillEvent, 0, len(order))
	for _, name := range order {
		body := bodyByID[idByName[name]]
		if body == "" {
			// Failed invocation (the Skill tool returned an error) — the
			// model never received a body, so it isn't "loaded".
			continue
		}
		path, _ := resolveSkillBody(name)
		source := "bundled"
		if path != "" {
			source = "listing"
		}
		out = append(out, SkillEvent{
			Name:         name,
			SHA256:       sha256hex(body),
			BodyTokens:   tokEstimateAs(body, "skill_body"),
			FirstSeenIdx: seen[name],
			Source:       source,
			Path:         path,
			ToolUseID:    idByName[name],
		})
	}
	return out
}

// buildSkillBodyMap pairs each `Skill` tool_use ID with the body text that
// arrives next in the conversation. The pattern is:
//
//	N   : assistant tool_use(name=Skill, id=X)
//	N+1 : user tool_result(tool_use_id=X, content="Launching skill: …")
//	N+2 : user text(content=<the actual skill body>)
//
// We track Skill tool_use IDs whose tool_result we've seen and assign the
// next user text block we encounter to the oldest such ID.
func buildSkillBodyMap(entries []TranscriptEntry) map[string]string {
	skillIDs := map[string]bool{}
	for _, e := range entries {
		if e.Type != "assistant" || e.Message == nil {
			continue
		}
		for _, c := range e.Message.Content {
			if c.Type == "tool_use" && c.Name == "Skill" {
				skillIDs[c.ID] = true
			}
		}
	}

	out := map[string]string{}
	pending := []string{}
	for _, e := range entries {
		if e.Type != "user" || e.Message == nil {
			continue
		}
		for _, c := range e.Message.Content {
			switch c.Type {
			case "tool_result":
				if c.ToolUseID == "" || !skillIDs[c.ToolUseID] {
					continue
				}
				// Failed Skill invocations (e.g. "<tool_use_error>plan is a
				// UI command…") return no body — don't queue them or we'd
				// steal the next successful skill's body.
				inner, _ := c.Content.(string)
				if strings.HasPrefix(strings.TrimSpace(inner), "<tool_use_error>") {
					continue
				}
				pending = append(pending, c.ToolUseID)
			case "text":
				if len(pending) == 0 {
					continue
				}
				id := pending[0]
				pending = pending[1:]
				if _, ok := out[id]; !ok {
					out[id] = c.Text
				}
			}
		}
	}
	return out
}

// extractSkillMenuTokens returns the token cost of the latest skill_listing
// content — what the model pays each turn just to see the available menu.
func extractSkillMenuTokens(entries []TranscriptEntry) int {
	var content string
	for _, e := range entries {
		if e.Type == "attachment" && e.Attachment != nil && e.Attachment.Type == "skill_listing" {
			content = e.Attachment.ContentString()
		}
	}
	return tokEstimateAs(content, "skill_listing_menu")
}

func appendListingEvents(out []SkillEvent, seen map[string]bool, entry TranscriptEntry, idx int) []SkillEvent {
	if entry.Attachment == nil || entry.Attachment.Type != "skill_listing" {
		return out
	}
	for _, name := range entry.Attachment.Names {
		path, body := resolveSkillBody(name)
		var bodyHash string
		var bodyTokens, menuTokens int
		if body != "" {
			bodyHash = sha256hex(body)
			bodyTokens = tokEstimateAs(body, "skill_body")
			menuTokens = skillMenuTokens(name, body)
		}
		key := name + "|" + bodyHash
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, SkillEvent{
			Name:         name,
			SHA256:       bodyHash,
			BodyTokens:   bodyTokens,
			MenuTokens:   menuTokens,
			FirstSeenIdx: idx,
			Source:       classifySkillSource(path),
			Path:         path,
		})
	}
	return out
}

// skillMenuTokens estimates what one skill costs in the always-on skill
// menu: its name plus the frontmatter `description` (the per-row text
// `/skills` shows). SKILL.md files carry a description; slash-command files
// usually don't, so they cost ~just the name — matching /context, where
// commands without a description sit in the "< 20 tokens" bucket. Bundled
// skills (no on-disk file — Claude Code ships them in the binary) return 0;
// we can't read the binary's copy, so that part of /context's total is a
// known, unrecoverable gap.
func skillMenuTokens(name, body string) int {
	if body == "" {
		return 0
	}
	fm := frontmatter(body)
	if fm == "" {
		// Slash-command files with no frontmatter cost ~just the name.
		return tokEstimateAs(name, "prose")
	}
	desc := frontmatterField(fm, "description")
	if desc == "" {
		return tokEstimateAs(name, "prose")
	}
	// Only name + description reach the menu — NOT other frontmatter fields
	// like `compatibility:` or `metadata:`, which some skills (the
	// signals-scout-* family) carry and which would otherwise inflate the
	// estimate well past what /context attributes.
	return tokEstimateAs(name+": "+desc, "skill_body")
}

// frontmatterField pulls a single scalar field out of a YAML frontmatter
// block. Handles the three shapes skill descriptions actually use:
//
//	description: plain text on one line
//	description: 'single' / "double" quoted
//	description: > | block scalar, value on the following indented lines
//
// A block scalar ends at the next top-level key (a line starting in column
// 0 with `key:`) or end of frontmatter. Good enough for menu sizing; not a
// general YAML parser.
func frontmatterField(fm, key string) string {
	lines := strings.Split(fm, "\n")
	for i, line := range lines {
		rest, ok := strings.CutPrefix(line, key+":")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		if rest == ">" || rest == "|" || rest == ">-" || rest == "|-" || rest == ">+" || rest == "|+" {
			var b strings.Builder
			for _, l := range lines[i+1:] {
				if l != "" && l[0] != ' ' && l[0] != '\t' {
					break // dedented → next top-level key
				}
				b.WriteString(strings.TrimSpace(l))
				b.WriteByte(' ')
			}
			return strings.TrimSpace(b.String())
		}
		// Inline scalar — strip a matching pair of surrounding quotes.
		if len(rest) >= 2 {
			if (rest[0] == '\'' && rest[len(rest)-1] == '\'') || (rest[0] == '"' && rest[len(rest)-1] == '"') {
				rest = rest[1 : len(rest)-1]
			}
		}
		return rest
	}
	return ""
}

// classifySkillSource buckets a resolved skill path into the groups
// /context uses. Empty path → bundled (built-in / in-binary).
func classifySkillSource(path string) string {
	if path == "" {
		return "bundled"
	}
	if strings.Contains(path, filepath.Join(".claude", "plugins")) {
		return "plugin"
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(path, filepath.Join(home, ".claude")) {
		return "user"
	}
	return "project"
}

// resolveSkillBody finds the on-disk SKILL.md for a listing name and returns
// (path, body). Returns ("", "") if the skill is bundled (no file on disk).
//
// Search order (project beats user beats plugin):
//  1. <cwd>/.claude/skills/<name>/SKILL.md
//  2. ~/.claude/skills/<name>/SKILL.md  (symlinks followed)
//  3. ~/.claude/plugins/marketplaces/<plugin>/skills/<name>/SKILL.md  when name is "<plugin>:<name>"
//
// Refuses traversal attempts (path separators or `..` in name). Trust
// model is Claude-Code-emits-the-name, but a typo or future API quirk
// shouldn't let us read arbitrary files.
func resolveSkillBody(name string) (string, string) {
	if !validSkillName(name) {
		return "", ""
	}
	for _, p := range skillCandidatePaths(name) {
		if data, err := os.ReadFile(p); err == nil {
			return p, string(data)
		}
	}
	return "", ""
}

// validSkillName rejects path separators and parent-dir traversal. The
// namespace separator `:` is allowed (skill names like `opik:opik` are
// the plugin-namespaced shape). Empty names rejected.
func validSkillName(name string) bool {
	if name == "" {
		return false
	}
	if strings.ContainsAny(name, "/\\") {
		return false
	}
	// Reject "..", "../foo", "foo/..", etc. — the path-separator check
	// already catches paths; this catches a bare ".." used as a name.
	for _, part := range strings.Split(name, ":") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	return true
}

func skillCandidatePaths(name string) []string {
	home, _ := os.UserHomeDir()
	cwd := inferCwd()
	ns, leaf, namespaced := strings.Cut(name, ":")

	paths := []string{}
	// Project-level skills + commands first.
	if cwd != "" {
		paths = append(paths, filepath.Join(cwd, ".claude", "skills", name, "SKILL.md"))
		paths = append(paths, filepath.Join(cwd, ".claude", "commands", name+".md"))
		if namespaced {
			paths = append(paths, filepath.Join(cwd, ".claude", "commands", ns, leaf+".md"))
		}
	}
	// User-level skills + commands.
	if home != "" {
		paths = append(paths, filepath.Join(home, ".claude", "skills", name, "SKILL.md"))
		paths = append(paths, filepath.Join(home, ".claude", "commands", name+".md"))
		if namespaced {
			paths = append(paths, filepath.Join(home, ".claude", "commands", ns, leaf+".md"))
		}
	}
	// Plugin-namespaced: opik:opik, posthog:querying-posthog-data, etc.
	// The skill/command file lives in the version the session actually runs,
	// which installed_plugins.json pins via installPath (the versioned cache
	// dir). The old marketplaces/<ns>/skills path was wrong — agents/skills
	// sit under marketplaces/<mp>/plugins/<plugin>/, not marketplaces/<ns>/ —
	// so every plugin skill fell through to "bundled" with zero tokens.
	if namespaced && home != "" {
		if ip := installedPluginPaths(home)[ns]; ip != "" {
			paths = append(paths,
				filepath.Join(ip, "skills", leaf, "SKILL.md"),
				filepath.Join(ip, "commands", leaf+".md"),
			)
		}
	}
	return paths
}

// skillInputName reads the skill identifier out of a Skill tool_use input.
// Claude Code uses `skill` for the slug; older payloads might use `name`.
func skillInputName(input map[string]interface{}) string {
	for _, k := range []string{"skill", "name"} {
		if v, ok := input[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func sha256hex(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
