package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	// CatalogBodyTokens is the on_invoke token count from
	// plugin-catalog-cache.json (CC's own tokenizer output), populated for
	// marketplace skills only. Compare against BodyTokens to spot
	// calibration drift.
	CatalogBodyTokens int    `json:"catalog_body_tokens,omitempty"`
	CatalogSource     string `json:"catalog_source,omitempty"` // full plugin key in the cache
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

	// Pre-parse the latest skill_listing attachment into per-skill blocks.
	// The attachment text IS the menu the model sees — its size /context
	// reports under "Skills" — so using it as the source of truth handles
	// every skill source uniformly (project, user, plugin, bundled). The
	// previous on-disk frontmatter reconstruction couldn't size bundled
	// skills (no on-disk file) and was systematically under for the rest.
	menuBlocks := latestSkillListingMenu(entries)

	seen := make(map[string]bool) // dedup on name|sha256
	out := make([]SkillEvent, 0, 8)

	for i, entry := range entries {
		if entry.Type != "attachment" {
			continue
		}
		out = appendListingEvents(out, seen, entry, i, menuBlocks)
	}
	return out
}

// latestSkillListingMenu returns the per-skill menu-block text taken from
// the most recent skill_listing attachment. Map keys are the skill names
// from the attachment's `names` array (so namespacing — e.g. `opik:opik` —
// is preserved). Each block starts with `- <name>:` and runs until the
// next such line OR the end of the listing content; multi-line description
// continuations stay with their owner.
func latestSkillListingMenu(entries []TranscriptEntry) map[string]string {
	var content string
	var names []string
	for _, e := range entries {
		if e.Type == "attachment" && e.Attachment != nil && e.Attachment.Type == "skill_listing" {
			content = e.Attachment.ContentString()
			names = e.Attachment.Names
		}
	}
	return parseSkillListingMenu(content, names)
}

// parseSkillListingMenu splits the skill_listing.content body into per-skill
// blocks. A block opens on a line shaped `- <name>:` (where <name> is one
// of the canonical `names` from the attachment) and stays open until the
// next opener — so wrapped descriptions land in the right bucket.
//
// Recognition matches against the names array rather than a regex, because
// namespaced skills (e.g. `comet:create-jira-ticket`) contain `:` inside
// the name; a naive "first colon" split would strip everything past the
// namespace and collapse 10 distinct skills into one bucket.
func parseSkillListingMenu(content string, names []string) map[string]string {
	out := map[string]string{}
	if content == "" || len(names) == 0 {
		return out
	}
	// Longest-first so `comet:create-jira-ticket` is matched before `comet`
	// would-be name (defensive — Claude Code's attachment doesn't include
	// the bare prefix today, but ordering by length keeps us correct if it
	// ever does).
	ordered := append([]string(nil), names...)
	sort.Slice(ordered, func(i, j int) bool {
		return len(ordered[i]) > len(ordered[j])
	})
	lines := strings.Split(content, "\n")
	curName := ""
	curStart := 0
	flush := func(end int) {
		if curName == "" {
			return
		}
		out[curName] = strings.Join(lines[curStart:end], "\n")
	}
	for i, l := range lines {
		if name := skillBulletNameFromSet(l, ordered); name != "" {
			flush(i)
			curName = name
			curStart = i
		}
	}
	flush(len(lines))
	return out
}

// skillBulletNameFromSet looks for the longest `<name>:` (or `<name>: …`)
// match from `ordered` at the start of `line`, after the `- ` bullet
// marker. Returns the matched name or "" if the line is not a recognized
// bullet opener.
func skillBulletNameFromSet(line string, ordered []string) string {
	const prefix = "- "
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	rest := line[len(prefix):]
	for _, n := range ordered {
		if !strings.HasPrefix(rest, n) {
			continue
		}
		after := rest[len(n):]
		if after == "" || after[0] == ':' {
			return n
		}
	}
	return ""
}

// extractLoadedSkills walks every skill load in the transcript — both
// Skill-tool-use loads (Claude calls the Skill tool) and slash-command
// loads (user types `/opik:opik`) — and returns one SkillEvent per
// distinct skill that was actually pulled into context. The body that
// lands in the conversation IS the canonical body the model has from
// that point on; we hash THAT so both on-disk and bundled skills get a
// real sha + token count. The on-disk lookup is kept only as a useful
// identity tag (path, source=listing vs bundled).
func extractLoadedSkills(entries []TranscriptEntry) []SkillEvent {
	loads := buildLoadedSkillBodies(entries)
	if len(loads) == 0 {
		return nil
	}
	// One event per load (OPIK-6873): repeat loads of the same skill each
	// inject their body into context and each replays from then on, so
	// collapsing to latest-wins under-counted both tokens and load counts.
	// Every event carries a stable unique ToolUseID (toolu_… or
	// "slash:<idx>") so consumers can dedupe events across the cumulative
	// per-trace loaded[] arrays.
	out := make([]SkillEvent, 0, len(loads))
	for _, l := range loads {
		path, _ := resolveSkillBody(l.Name)
		source := "bundled"
		if path != "" {
			source = "listing"
		}
		ev := SkillEvent{
			Name:         l.Name,
			SHA256:       sha256hex(l.Body),
			BodyTokens:   tokEstimateAs(l.Body, "skill_body"),
			FirstSeenIdx: l.Index,
			Source:       source,
			Path:         path,
			ToolUseID:    l.ToolUseID,
		}
		// When the loaded skill belongs to a marketplace plugin whose
		// metadata sits in plugin-catalog-cache.json, attach the
		// catalog-derived `on_invoke` token count too. Same field as a
		// cross-check — CC's own tokenizer produced that number when it
		// built the cache, so it's a tighter signal than our 3.5 ratio
		// for skills with rich code blocks or markdown tables.
		if pluginShort, leaf, ok := splitNamespacedSkillName(l.Name); ok {
			home, _ := os.UserHomeDir()
			model := mostRecentModelFromEntries(entries)
			if comp, fullKey, hit := pluginCatalogLookup(home, pluginShort, "skill", leaf); hit {
				_, onInvoke := catalogComponentTokens(home, pluginShort, model, comp)
				if onInvoke > 0 {
					ev.CatalogBodyTokens = onInvoke
					ev.CatalogSource = fullKey
				}
			}
		}
		out = append(out, ev)
	}
	return out
}

// splitNamespacedSkillName splits `<plugin>:<skill>` into its parts.
// Returns ok=false for un-namespaced skills (project/user/bundled), which
// can't appear in the marketplace catalog by definition.
func splitNamespacedSkillName(name string) (plugin, leaf string, ok bool) {
	for i := 0; i < len(name); i++ {
		if name[i] == ':' {
			return name[:i], name[i+1:], true
		}
	}
	return "", "", false
}

// mostRecentModelFromEntries returns the model field of the latest
// assistant entry. Catalog token counts are keyed per model (opus vs
// sonnet quantize differently), so the resolved ratio must match the
// model the session is running.
func mostRecentModelFromEntries(entries []TranscriptEntry) string {
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Type == "assistant" && e.Message != nil && e.Message.Model != "" {
			return e.Message.Model
		}
	}
	return ""
}

// loadedSkillBody is one resolved skill load. Index is the transcript entry
// index of the user message that carried the body — used by FirstSeenIdx
// in extractLoadedSkills for stable ordering. ToolUseID is the toolu_… id
// for Skill-tool-use loads and a synthetic "slash:<entry index>" id for
// slash-command loads, so every load event has a stable unique identity.
type loadedSkillBody struct {
	Name      string
	Body      string
	ToolUseID string
	Index     int
}

// buildLoadedSkillBodies returns every skill load in the transcript across
// the two invocation paths Claude Code supports:
//
//  1. Skill tool_use: the model calls the `Skill` tool. The result comes
//     back as a user tool_result acknowledging the launch, immediately
//     followed by a user text block carrying the skill body. The pattern
//     is captured by buildSkillBodyMap (legacy name, kept for callers
//     elsewhere).
//
//  2. Slash command: the USER types `/opik:opik`. Claude Code synthesizes
//     a user message containing `<command-name>/opik:opik</command-name>`,
//     then injects a follow-up user text block prefixed
//     `Base directory for this skill: <path>\n\n<body>`. No tool_use is
//     generated, so the legacy path missed these entirely — leaving
//     cc.skills.loaded empty even when the body was clearly in context,
//     and double-counting the body under cc.user_prompts.
//
// Both shapes funnel through this single emitter so downstream code
// (extractLoadedSkills, skillBodyHashSet) sees one uniform stream.
func buildLoadedSkillBodies(entries []TranscriptEntry) []loadedSkillBody {
	var out []loadedSkillBody

	// Pass 1: Skill tool_use loads via the existing pairing logic.
	idToName := map[string]string{}
	idToIndex := map[string]int{}
	for i, e := range entries {
		if e.Type != "assistant" || e.Message == nil {
			continue
		}
		for _, c := range e.Message.Content {
			if c.Type == "tool_use" && c.Name == "Skill" {
				idToName[c.ID] = skillInputName(c.Input)
				idToIndex[c.ID] = i
			}
		}
	}
	bodyByID, indexByID := buildSkillBodyMapWithIndex(entries)
	for id, body := range bodyByID {
		name := idToName[id]
		if name == "" || body == "" {
			continue
		}
		out = append(out, loadedSkillBody{
			Name:      name,
			Body:      body,
			ToolUseID: id,
			Index:     indexByID[id],
		})
	}

	// Pass 2: slash-command loads. The `<command-name>/<name></command-name>`
	// preamble and the `Base directory for this skill:` follow-up arrive in
	// adjacent user text blocks; any other user content in between drains
	// the pending slot so an unrelated `/help` (no body follow-up) doesn't
	// steal the next skill's body.
	out = append(out, slashCommandSkillLoads(entries)...)
	return out
}

// buildSkillBodyMap is preserved for any caller that wants the old
// tool_use-only map. Returns the body text keyed by the Skill tool_use ID.
func buildSkillBodyMap(entries []TranscriptEntry) map[string]string {
	bodies, _ := buildSkillBodyMapWithIndex(entries)
	return bodies
}

// buildSkillBodyMapWithIndex returns the same map as buildSkillBodyMap
// plus, for each ID, the transcript entry index where the body landed
// (used by buildLoadedSkillBodies for stable FirstSeenIdx ordering).
func buildSkillBodyMapWithIndex(entries []TranscriptEntry) (map[string]string, map[string]int) {
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

	bodies := map[string]string{}
	indices := map[string]int{}
	pending := []string{}
	for i, e := range entries {
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
				if _, ok := bodies[id]; !ok {
					bodies[id] = c.Text
					indices[id] = i
				}
			}
		}
	}
	return bodies, indices
}

// slashCommandPrefix is the prefix Claude Code stamps on the system-
// injected user text that carries a slash-loaded skill body. Anchoring on
// this exact string means non-skill slash commands (`/context`, `/help`)
// are naturally ignored — they have no such follow-up text.
const slashCommandPrefix = "Base directory for this skill:"

// slashCommandSkillLoads scans for slash-command-style skill loads. Pattern:
//
//	user text: "<command-name>/<skill-name></command-name>…"
//	user text: "Base directory for this skill: <path>\n\n<skill body>"
//
// Yields one loadedSkillBody per pair. Pending state is reset on any
// intervening user content so `/help` (no body follow-up) can't claim the
// next skill load.
func slashCommandSkillLoads(entries []TranscriptEntry) []loadedSkillBody {
	var out []loadedSkillBody
	pendingName := ""
	for i, e := range entries {
		if e.Type != "user" || e.Message == nil {
			continue
		}
		for _, c := range e.Message.Content {
			if c.Type != "text" {
				continue
			}
			if name := slashCommandName(c.Text); name != "" {
				pendingName = name
				continue
			}
			if pendingName == "" {
				continue
			}
			if strings.HasPrefix(c.Text, slashCommandPrefix) {
				out = append(out, loadedSkillBody{
					Name: pendingName,
					Body: c.Text,
					// Synthetic load id (OPIK-6873): slash loads have no
					// tool_use, but consumers dedupe load events across the
					// cumulative per-trace loaded[] arrays via this id.
					// The transcript entry index is stable for the session
					// (append-only file), so the same load keeps the same
					// id on every later trace.
					ToolUseID: "slash:" + strconv.Itoa(i),
					Index:     i,
				})
			}
			// Whether we matched a body or saw an unrelated text block,
			// clear the pending slot — slash loads are immediately
			// adjacent, so any other content means the preamble wasn't
			// for a skill.
			pendingName = ""
		}
	}
	return out
}

// slashCommandName extracts the skill name from a `<command-name>/<X></command-name>`
// preamble. Returns "" if the text isn't a slash-command preamble.
func slashCommandName(text string) string {
	const open = "<command-name>/"
	const close = "</command-name>"
	i := strings.Index(text, open)
	if i < 0 {
		return ""
	}
	rest := text[i+len(open):]
	j := strings.Index(rest, close)
	if j <= 0 {
		return ""
	}
	name := rest[:j]
	// Reject names containing whitespace or angle brackets — those
	// indicate the preamble didn't actually wrap a single command (e.g.
	// truncated input). Allow `:`, `-`, `_`, `.` for namespaced skills.
	for k := 0; k < len(name); k++ {
		c := name[k]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == ':' || c == '-' || c == '_' || c == '.':
		default:
			return ""
		}
	}
	return name
}

// extractSkillMenuTokens returns the token cost of the latest skill_listing
// content — what the model pays each turn just to see the available menu.
// Kept for cross-checking the per-skill sum against the whole-attachment
// estimate; they use the same ratio so big drifts mean something is
// being mis-attributed.
func extractSkillMenuTokens(entries []TranscriptEntry) int {
	var content string
	for _, e := range entries {
		if e.Type == "attachment" && e.Attachment != nil && e.Attachment.Type == "skill_listing" {
			content = e.Attachment.ContentString()
		}
	}
	return tokEstimateAs(content, "skill_listing_menu")
}

func appendListingEvents(out []SkillEvent, seen map[string]bool, entry TranscriptEntry, idx int, menuBlocks map[string]string) []SkillEvent {
	if entry.Attachment == nil || entry.Attachment.Type != "skill_listing" {
		return out
	}
	for _, name := range entry.Attachment.Names {
		path, body := resolveSkillBody(name)
		var bodyHash string
		var bodyTokens int
		if body != "" {
			bodyHash = sha256hex(body)
			bodyTokens = tokEstimateAs(body, "skill_body")
		}
		// MenuTokens come from the actual attachment block this skill
		// occupies — that text IS what the model sees in its always-on
		// skill menu, and /context's "Skills" rows are sized against it.
		// Falling back to on-disk reconstruction handles the (rare) case
		// where a skill is invocable but missing from the latest listing.
		menuTokens := 0
		if block, ok := menuBlocks[name]; ok && block != "" {
			menuTokens = tokEstimateAs(block, "skill_listing_menu")
		} else if body != "" {
			menuTokens = skillMenuTokensFromBody(name, body)
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

// skillMenuTokensFromBody is the fallback used when a skill exists on disk
// but isn't present in the latest skill_listing attachment (most often
// when extraction runs before the first listing has landed). Same shape
// the listing uses — `- <name>: <description>` — so the same
// skill_listing_menu ratio applies.
func skillMenuTokensFromBody(name, body string) int {
	if body == "" {
		return 0
	}
	fm := frontmatter(body)
	if fm == "" {
		// Slash-command files with no frontmatter cost ~just the name.
		return tokEstimateAs("- "+name+":", "skill_listing_menu")
	}
	desc := frontmatterField(fm, "description")
	if desc == "" {
		return tokEstimateAs("- "+name+":", "skill_listing_menu")
	}
	return tokEstimateAs("- "+name+": "+desc, "skill_listing_menu")
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
