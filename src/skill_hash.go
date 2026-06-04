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
	FirstSeenIdx int    `json:"first_seen_idx"`
	Source       string `json:"source"` // "listing" | "bundled"
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
			BodyTokens:   tokEstimate(body),
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
	return tokEstimate(content)
}

func appendListingEvents(out []SkillEvent, seen map[string]bool, entry TranscriptEntry, idx int) []SkillEvent {
	if entry.Attachment == nil || entry.Attachment.Type != "skill_listing" {
		return out
	}
	for _, name := range entry.Attachment.Names {
		path, body := resolveSkillBody(name)
		var bodyHash string
		var bodyTokens int
		source := "listing"
		if body == "" {
			// Bundled skill (no on-disk file) — Claude Code ships the body
			// in the binary. Mark it so the FE can render it differently.
			source = "bundled"
		} else {
			bodyHash = sha256hex(body)
			bodyTokens = tokEstimate(body)
		}
		key := name + "|" + bodyHash
		if seen[key] {
			continue
		}
		seen[key] = true
		evt := SkillEvent{
			Name:         name,
			SHA256:       bodyHash,
			BodyTokens:   bodyTokens,
			FirstSeenIdx: idx,
			Source:       source,
		}
		evt.Path = path
		out = append(out, evt)
	}
	return out
}

// resolveSkillBody finds the on-disk SKILL.md for a listing name and returns
// (path, body). Returns ("", "") if the skill is bundled (no file on disk).
//
// Search order (project beats user beats plugin):
//  1. <cwd>/.claude/skills/<name>/SKILL.md
//  2. ~/.claude/skills/<name>/SKILL.md  (symlinks followed)
//  3. ~/.claude/plugins/marketplaces/<plugin>/skills/<name>/SKILL.md  when name is "<plugin>:<name>"
func resolveSkillBody(name string) (string, string) {
	for _, p := range skillCandidatePaths(name) {
		if data, err := os.ReadFile(p); err == nil {
			return p, string(data)
		}
	}
	return "", ""
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
	// Plugin-namespaced: opik:opik → marketplaces/opik/{skills/opik/SKILL.md, commands/opik.md}.
	if namespaced && home != "" {
		paths = append(paths,
			filepath.Join(home, ".claude", "plugins", "marketplaces", ns, "skills", leaf, "SKILL.md"),
			filepath.Join(home, ".claude", "plugins", "marketplaces", ns, "commands", leaf+".md"),
		)
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
