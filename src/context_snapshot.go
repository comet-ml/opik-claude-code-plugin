package main

// buildContextSnapshot returns a per-category breakdown of the request
// context as it stood at this flush — the same shape we eventually patch
// onto the trace via /context, but computed synchronously from the
// extractors so it can be stamped on every LLM span at span-creation
// time. Dashboards can then attribute per-span billed tokens to
// categories without joining back to the trace's context_runtime:
//
//	span_category_cost = span.usage.input_tokens
//	                   × snapshot.categories[X] / snapshot.total_tokens
//	                   × rate_for_cache_bucket
//
// Accuracy: synchronously-computed estimates carry the same drift as
// every other cc.* metric (typically 1-15% per category). When the
// detached /context fetcher lands, it patches the more accurate numbers
// at the trace level; per-span snapshots stay at sync values to avoid
// patching N spans after the fact.
//
// nil is returned when the snapshot would have zero categories (very
// short sessions, no extractors had data) — callers should skip stamping
// in that case rather than write an empty block.
func buildContextSnapshot(state *State) map[string]interface{} {
	fullEntries, err := ReadTranscript(state.Transcript, 0)
	if err != nil {
		return nil
	}

	// deferredKeys marks categories whose tokens DON'T ship in the per-
	// turn LLM request envelope: deferred tool schemas are loaded only on
	// demand via ToolSearch, so they don't bill the model unless the
	// model explicitly fetches them. /context displays them in its
	// breakdown table but excludes them from the visible "Tokens used"
	// line — we do the same here so total_tokens matches the API's
	// actual billed `input_tokens + cache_*`.
	deferredKeys := map[string]bool{
		"system_tools_deferred": true,
		"mcp_tools_deferred":    true,
	}

	cats := map[string]int{}
	addCat := func(key string, v int) {
		if v > 0 {
			cats[key] = v
		}
	}

	// System prompt + bundled tool catalog — only knowable via the
	// versioned constant table; the transcript doesn't echo schemas.
	if cc := extractCCBuiltinSnapshot(fullEntries); cc != nil {
		sum := cc["summary"].(map[string]interface{})
		if v, ok := sum["system_prompt"].(int); ok {
			addCat("system_prompt", v)
		}
		if v, ok := sum["system_tools"].(int); ok {
			addCat("system_tools", v)
		}
		if v, ok := sum["deferred_tools"].(int); ok {
			addCat("system_tools_deferred", v)
		}
	}

	if m := extractMemorySnapshot(); m != nil {
		if sum, ok := m["summary"].(map[string]interface{}); ok {
			if v, ok := sum["total_tokens"].(int); ok {
				addCat("memory_files", v)
			}
		}
	}

	if a := extractAgentsSnapshot(); a != nil {
		if sum, ok := a["summary"].(map[string]interface{}); ok {
			if v, ok := sum["total_tokens"].(int); ok {
				addCat("custom_agents", v)
			}
		}
	}

	if s := BuildSkillsSnapshot(fullEntries); s != nil {
		if sum, ok := s["summary"].(map[string]interface{}); ok {
			if v, ok := sum["menu_tokens"].(int); ok {
				addCat("skills_menu", v)
			}
			if v, ok := sum["loaded_tokens"].(int); ok {
				addCat("skills_loaded", v)
			}
		}
	}

	// MCP "deferred" cost — addedLines we observed + per-tool overhead
	// estimate. Like system_tools_deferred, these schemas don't ship
	// unless ToolSearch pulls them, so they go in the deferred bucket.
	if t := extractToolsSnapshot(fullEntries); t != nil {
		if sum, ok := t["summary"].(map[string]interface{}); ok {
			if bySource, ok := sum["by_source"].(map[string]interface{}); ok {
				if mcp, ok := bySource["mcp"].(map[string]interface{}); ok {
					if v, ok := mcp["estimated_deferred_tokens"].(int); ok {
						addCat("mcp_tools_deferred", v)
					}
				}
			}
		}
	}

	// Messages — cumulative conversation content across the whole session
	// (every prior user turn, every prior assistant turn, every tool_result).
	// Without this, the per-span snapshot's total under-states API billing
	// for long sessions: the always-on categories are flat per turn but
	// `messages` grows linearly with conversation length, and a 50-turn
	// session can accumulate thousands of tokens here. Loaded skill bodies
	// are excluded from this category because they're already in
	// skills_loaded — adding both would double-count.
	addCat("messages", cumulativeMessagesTokens(fullEntries))

	if len(cats) == 0 {
		return nil
	}

	// total_tokens = what's actually in the LLM request envelope each
	// turn (excludes deferred — matches /context's visible total and
	// the API's billed input_tokens + cache_*). deferred_tokens is
	// surfaced separately for the "if I loaded all of these, they'd
	// cost X" view dashboards may want.
	alwaysOn, deferred := 0, 0
	for k, v := range cats {
		if deferredKeys[k] {
			deferred += v
		} else {
			alwaysOn += v
		}
	}
	return map[string]interface{}{
		"categories":       cats,
		"total_tokens":     alwaysOn, // ← matches /context and the API
		"deferred_tokens":  deferred, // ← informational; loaded on demand
		"source":           "estimated_sync",
	}
}

// cumulativeMessagesTokens sums every piece of conversation content from
// the session-so-far transcript: user text (minus loaded skill bodies),
// tool results, and prior assistant output. Mirrors what /context counts
// under its "Messages" row.
//
// Assistant output uses the actual `usage.output_tokens` because
// Anthropic counted them — more accurate than re-estimating via
// tokEstimateAs on text + thinking + tool_use bodies. User and tool_result
// fall back to our calibrated estimates.
//
// Loaded skill bodies are tracked in cats["skills_loaded"] above and
// excluded here so the two categories sum to the right total when both
// are present. Without that split, /opik:opik would double-count the
// 1000+ token skill body in both buckets.
func cumulativeMessagesTokens(entries []TranscriptEntry) int {
	if len(entries) == 0 {
		return 0
	}
	skillBodies := skillBodyHashSet(entries)

	total := 0
	for _, e := range entries {
		switch e.Type {
		case "user":
			if e.Message == nil {
				continue
			}
			for _, c := range e.Message.Content {
				switch c.Type {
				case "text":
					if skillBodies[sha256hex(c.Text)] {
						continue // counted under skills_loaded
					}
					total += tokEstimateAs(c.Text, "user_prompt")
				case "tool_result":
					total += resultTokens(c.Content)
				}
			}
		case "assistant":
			if e.Message == nil || e.Message.Usage == nil {
				continue
			}
			// Anthropic's own count for this LLM call's output —
			// exact, no estimation drift.
			total += e.Message.Usage.OutputTokens
		}
	}
	return total
}
