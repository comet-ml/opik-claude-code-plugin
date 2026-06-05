package main

// Attribution carries the per-turn token attribution that feeds the Coding
// Harness dashboard. Each FE drillKey maps to one field here. The walking
// skeleton populates Skills; the rest land as we fan out.
type Attribution struct {
	Skills []SkillEvent `json:"skills,omitempty"`
}

// ExtractAttribution walks one turn's entries and returns per-category events.
// Single pass; safe to call after the existing transcript parse.
func ExtractAttribution(entries []TranscriptEntry) *Attribution {
	return &Attribution{
		Skills: extractSkillEvents(entries),
	}
}

// BuildSkillsSnapshot returns the {summary, available, loaded} block that
// lands at `metadata.cc.skills` on every per-message span and on the trace.
// Same shape both places. Scans the full transcript because skill_listing
// lives at line ~0 and Skill invocations spread throughout the session.
func BuildSkillsSnapshot(allEntries []TranscriptEntry) map[string]interface{} {
	listing := extractSkillEvents(allEntries)
	loaded := extractLoadedSkills(allEntries)
	menuTokens := extractSkillMenuTokens(allEntries)
	if len(listing) == 0 && len(loaded) == 0 && menuTokens == 0 {
		return nil
	}

	available := make([]map[string]interface{}, 0, len(listing))
	for _, s := range listing {
		e := map[string]interface{}{
			"name":   s.Name,
			"source": s.Source,
		}
		if s.SHA256 != "" {
			e["sha256"] = s.SHA256
		}
		if s.Path != "" {
			e["path"] = s.Path
		}
		available = append(available, e)
	}

	loadedOut := make([]map[string]interface{}, 0, len(loaded))
	loadedTokens := 0
	for _, s := range loaded {
		loadedTokens += s.BodyTokens
		e := map[string]interface{}{
			"name":        s.Name,
			"source":      s.Source,
			"sha256":      s.SHA256,
			"body_tokens": s.BodyTokens,
			"tool_use_id": s.ToolUseID,
		}
		if s.Path != "" {
			e["path"] = s.Path
		}
		loadedOut = append(loadedOut, e)
	}

	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_tokens":    menuTokens + loadedTokens,
			"menu_tokens":     menuTokens,
			"loaded_tokens":   loadedTokens,
			"available_count": len(available),
			"loaded_count":    len(loadedOut),
		},
		"available": available,
		"loaded":    loadedOut,
	}
}

// tokEstimate returns a token estimate using a content-type-aware
// chars/token ratio. Calibrated against Anthropic's count_tokens API on
// 643 samples drawn from real Claude Code transcripts (skill bodies, tool
// inputs, tool results, user prompts, assistant text, etc.). Naive
// `chars/4` averaged 20.5% error; per-type ratios below bring the median
// error under 5%.
//
// When the content type isn't known, callers pass "" — we auto-detect a
// few obvious cases (JSON by leading `{` or `[`) and otherwise use 3.6
// (the overall median ratio across all sampled types).
func tokEstimate(s string) int {
	return tokEstimateAs(s, "")
}

// tokEstimateAs lets the caller name the content type. Values used:
//
//	"json"                    →  2.8  (tool_use input, MCP payload)
//	"deferred_tools_payload"  →  2.5  (pure JSON list of names)
//	"tool_result"             →  3.0  (mixed text + JSON-ish output)
//	"skill_body"              →  3.5  (markdown w/ code blocks)
//	"assistant_text"          →  3.9  (prose with occasional code)
//	"skill_listing_menu"      →  3.9  (name + short description lines)
//	"prose"                   →  3.9
//	"user_prompt"             →  4.3  (natural English from a user)
//	"" / unknown              →  3.6  (overall calibrated median)
func tokEstimateAs(s, contentType string) int {
	if s == "" {
		return 0
	}
	cpt := charsPerToken(s, contentType)
	n := int(float64(len(s)) / cpt)
	if n == 0 {
		return 1
	}
	return n
}

func charsPerToken(s, contentType string) float64 {
	if contentType == "" {
		// Auto-detect JSON-shaped content — the biggest source of error
		// with chars/4. Anything starting with `{` or `[` gets the JSON
		// ratio; everything else falls back to the overall median.
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
				continue
			}
			if c == '{' || c == '[' {
				contentType = "json"
			}
			break
		}
	}
	switch contentType {
	case "json", "tool_use_input":
		return 2.8
	case "deferred_tools_payload":
		return 2.5
	case "tool_result":
		return 3.0
	case "skill_body":
		return 3.5
	case "assistant_text", "prose", "skill_listing_menu":
		return 3.9
	case "user_prompt":
		return 4.3
	}
	return 3.6
}
