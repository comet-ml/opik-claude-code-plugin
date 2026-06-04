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

// tokEstimate is a chars/4 token approximation (matches attribute_tokens.py).
// Centralized so we can swap to count_tokens later without touching call sites.
func tokEstimate(s string) int {
	if s == "" {
		return 0
	}
	n := len(s) / 4
	if n == 0 {
		return 1
	}
	return n
}
