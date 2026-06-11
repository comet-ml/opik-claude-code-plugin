package main

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

// cc.billing — exact, billing-native attribution (OPIK-6873 follow-up).
//
// Anthropic's prompt caching is prefix-based, so a request's usage splits it
// into three POSITIONAL segments: [0,R) billed as cache_read, [R,R+W) as
// cache_creation, the tail as fresh input. For every LLM call in the turn we
// lay the request out as an ordered list of pieces (static config prefix,
// then the conversation in transcript order), reconcile the piece sizes to
// the call's measured usage, and cut by position. Tier tokens are therefore
// per-call billing events: additive across calls, traces and periods, and
// they sum EXACTLY to the API-reported usage at every level.
//
// Exactness contract, per call:
//   input:  Σ piece tiers (incl. the explicit `unattributed` tail) ==
//           input_tokens + cache_read + cache_creation
//   output: Σ per-block attributed tokens == output_tokens
//           (DeduplicateUsage normalizes block shares to the measured total)
//
// Estimated pieces are scaled down proportionally only when they OVERSHOOT
// the measured total (assistant blocks are usage-derived and never scaled);
// undershoot is parked in `unattributed` — a visible bucket holding what we
// don't parse yet (system reminders, request envelope) plus residual
// estimation error, placed at the tail of the layout, which is where
// unobserved content actually sits in the request.

type billingTier struct {
	cacheRead, cacheCreation, fresh, output float64
}

type billingKey struct {
	lane, entity string
}

type billingPiece struct {
	key    billingKey
	tokens float64
	exact  bool // usage-derived (assistant blocks): never rescaled
}

const unattributedLane = "unattributed"

// computeBillingSnapshot returns the `cc.billing` block for the turn, or nil
// when the turn contains no usage-bearing LLM calls.
func computeBillingSnapshot(fullEntries, turnEntries []TranscriptEntry) map[string]interface{} {
	calls := llmCallsInTurn(fullEntries, turnEntries)
	if len(calls) == 0 {
		return nil
	}

	staticPieces := staticPrefixPieces(fullEntries)
	skillBodyNames := skillBodyNameBySHA(fullEntries)
	toolNames := toolUseNames(fullEntries)

	acc := map[billingKey]*billingTier{}
	totals := billingTier{}
	for _, call := range calls {
		pieces := append(append([]billingPiece{}, staticPieces...),
			conversationPieces(fullEntries[:call.entryIdx], skillBodyNames, toolNames)...)
		pieces = reconcileToUsage(pieces, float64(call.read+call.write+call.fresh))
		cutByPosition(pieces, float64(call.read), float64(call.write), acc)

		attributeOutput(fullEntries[call.entryIdx:call.entryEnd], acc)

		totals.cacheRead += float64(call.read)
		totals.cacheCreation += float64(call.write)
		totals.fresh += float64(call.fresh)
		totals.output += float64(call.output)
	}

	return renderBillingSnapshot(len(calls), totals, acc)
}

type billingCall struct {
	entryIdx                   int // index in fullEntries of the call's FIRST entry: its request is the prefix before it
	entryEnd                   int // one past the call's LAST entry
	read, write, fresh, output int
}

// llmCallsInTurn returns the turn's LLM calls in order, one per message.id,
// with usage and the message's contiguous entry span within fullEntries.
// The transcript repeats the same usage on every entry of a multi-block
// message, so usage is taken once from the first entry seen.
func llmCallsInTurn(fullEntries, turnEntries []TranscriptEntry) []billingCall {
	offset := len(fullEntries) - len(turnEntries)
	var calls []billingCall
	index := map[string]int{}
	for i, e := range turnEntries {
		if e.Type != "assistant" || e.Message == nil {
			continue
		}
		id := e.Message.ID
		if id == "" {
			id = e.UUID
		}
		if pos, ok := index[id]; ok {
			calls[pos].entryEnd = offset + i + 1
			continue
		}
		if e.Message.Usage == nil {
			continue
		}
		u := e.Message.Usage
		index[id] = len(calls)
		calls = append(calls, billingCall{
			entryIdx: offset + i,
			entryEnd: offset + i + 1,
			read:     u.CacheReadInputTokens,
			write:    u.CacheCreationInputTokens,
			fresh:    u.InputTokens,
			output:   u.OutputTokens,
		})
	}
	return calls
}

// staticPrefixPieces is the request prefix that precedes the conversation:
// the bundled system prompt + built-in tool schemas (version-keyed
// estimates), memory files, agent dispatch blurbs, and MCP schemas. Ordering
// inside the prefix only matters on cache-cold calls (when the R or R+W
// boundary falls inside it); on warm calls the whole prefix is cache_read.
// An unknown cc_builtin version contributes no pieces — that mass then shows
// up in `unattributed` instead of silently vanishing.
func staticPrefixPieces(fullEntries []TranscriptEntry) []billingPiece {
	var out []billingPiece
	add := func(lane, entity string, tokens int) {
		if tokens > 0 {
			out = append(out, billingPiece{billingKey{lane, entity}, float64(tokens), false})
		}
	}

	if consts, matched := ccBuiltinFor(findCCVersion(fullEntries)); matched != "" {
		add("static_overhead", "system_prompt", consts.SystemPromptTokens)
		add("static_overhead", "builtin_tool_schemas", consts.SystemToolsTokens)
	}
	if m := extractMemorySnapshot(); m != nil {
		for _, f := range m["files"].([]map[string]interface{}) {
			add("memory", filepath.Base(f["path"].(string)), f["body_tokens"].(int))
		}
	}
	if a := extractAgentsSnapshot(); a != nil {
		for _, ag := range a["agents"].([]map[string]interface{}) {
			add("custom_agents", ag["name"].(string), ag["body_tokens"].(int))
		}
	}
	if t := extractToolsSnapshot(fullEntries); t != nil {
		sum := t["summary"].(map[string]interface{})
		if bySource, ok := sum["by_source"].(map[string]interface{}); ok {
			if b, ok := bySource["builtin"].(map[string]interface{}); ok {
				add("static_overhead", "observed_builtin_schemas", b["schema_tokens"].(int))
			}
		}
		if byServer, ok := sum["by_server"].([]map[string]interface{}); ok {
			for _, s := range byServer {
				add("mcp_servers", s["server"].(string),
					s["schema_tokens"].(int)+s["instructions_tokens"].(int))
			}
		}
	}
	return out
}

// conversationPieces replays entries in transcript order, emitting one piece
// per content unit with the same lane attribution rules as the composition
// extractors. Order is what makes the positional tier cut valid. Assistant
// blocks use per-block attributed tokens (usage-derived → exact); parsedIdx
// advances in lockstep with ParseAssistantMessages' emission rules.
func conversationPieces(entries []TranscriptEntry, skillBodyNames map[string]string,
	toolNames map[string]string) []billingPiece {

	parsed := ParseAssistantMessages(entries)
	DeduplicateUsage(parsed)
	parsedIdx := 0

	var out []billingPiece
	add := func(lane, entity string, tokens float64, exact bool) {
		if tokens > 0 {
			out = append(out, billingPiece{billingKey{lane, entity}, tokens, exact})
		}
	}

	for _, e := range entries {
		switch e.Type {
		case "user":
			if e.Message == nil {
				continue
			}
			for _, c := range e.Message.Content {
				switch c.Type {
				case "text":
					if name, ok := skillBodyNames[sha256hex(c.Text)]; ok {
						add("skills", name, float64(tokEstimateAs(c.Text, "skill_body")), false)
					} else {
						tokens := tokEstimateAs(c.Text, "user_prompt")
						add("user_prompts", promptBucket(tokens), float64(tokens), false)
					}
				case "tool_result":
					lane, entity := toolLane(toolNames[c.ToolUseID])
					add(lane, entity, float64(resultTokens(c.Content)), false)
				}
			}
		case "attachment":
			if e.Attachment == nil {
				continue
			}
			switch e.Attachment.Type {
			case "skill_listing":
				add("skills", "menu",
					float64(tokEstimateAs(e.Attachment.ContentString(), "skill_listing_menu")), false)
			case "file":
				var w struct {
					File struct {
						Path    string `json:"path,omitempty"`
						Content string `json:"content,omitempty"`
					} `json:"file"`
				}
				if json.Unmarshal(e.Attachment.Content, &w) == nil {
					ext := strings.ToLower(filepath.Ext(w.File.Path))
					if ext == "" {
						ext = "other"
					}
					add("file_attachments", ext, float64(tokEstimate(w.File.Content)), false)
				}
			case "deferred_tools_delta":
				payload := strings.Join(e.Attachment.AddedLines, "\n")
				if payload == "" {
					payload = strings.Join(e.Attachment.AddedNames, "\n")
				}
				add("mcp_servers", "catalog_deltas",
					float64(tokEstimateAs(payload, "deferred_tools_payload")), false)
			case "mcp_instructions_delta":
				payload := strings.Join(e.Attachment.AddedBlocks, "\n")
				add("mcp_servers", "instructions",
					float64(tokEstimateAs(payload, "prose")), false)
			}
		case "assistant":
			if e.Message == nil || len(e.Message.Content) == 0 {
				continue
			}
			for _, c := range e.Message.Content {
				if c.Type == "" {
					continue
				}
				if parsedIdx >= len(parsed) {
					break
				}
				p := parsed[parsedIdx]
				parsedIdx++
				switch p.ContentType {
				case "text", "thinking":
					add("prior_assistant", p.ContentType, float64(p.AttributedOutputTokens), true)
				case "tool_use":
					lane, entity := toolLane(p.Content.Name)
					add(lane, entity, float64(p.AttributedOutputTokens), true)
				}
			}
		}
	}
	return out
}

func toolLane(name string) (string, string) {
	switch {
	case name == "":
		return "built_in_tools", "unknown"
	case strings.HasPrefix(name, "mcp__"):
		parts := strings.SplitN(name, "__", 3)
		if len(parts) >= 2 {
			return "mcp_servers", parts[1]
		}
		return "mcp_servers", "unknown"
	case name == "Skill":
		return "skills", "Skill"
	default:
		return "built_in_tools", name
	}
}

// reconcileToUsage makes Σ pieces == total exactly. Overshoot shrinks only
// the estimated pieces (usage-derived ones are already exact); undershoot
// appends the explicit `unattributed` tail piece.
func reconcileToUsage(pieces []billingPiece, total float64) []billingPiece {
	sum, estSum := 0.0, 0.0
	for _, p := range pieces {
		sum += p.tokens
		if !p.exact {
			estSum += p.tokens
		}
	}
	switch {
	case sum > total && estSum > 0:
		target := total - (sum - estSum)
		if target < 0 {
			target = 0
		}
		scale := target / estSum
		for i := range pieces {
			if !pieces[i].exact {
				pieces[i].tokens *= scale
			}
		}
	case sum < total:
		pieces = append(pieces, billingPiece{
			billingKey{unattributedLane, ""}, total - sum, false})
	}
	return pieces
}

// cutByPosition assigns each piece's overlap with the cache_read segment
// [0,R), the cache_creation segment [R,R+W), and the fresh tail.
func cutByPosition(pieces []billingPiece, read, write float64, acc map[billingKey]*billingTier) {
	pos := func(x float64) float64 {
		if x < 0 {
			return 0
		}
		return x
	}
	off := 0.0
	for _, p := range pieces {
		s, e := off, off+p.tokens
		t := tierFor(acc, p.key)
		t.cacheRead += pos(minF(e, read) - s)
		t.cacheCreation += pos(minF(e, read+write) - maxF(s, read))
		t.fresh += pos(e - maxF(s, read+write))
		off = e
	}
}

// attributeOutput books the call's own blocks against output. callEntries is
// the contiguous span of the call's entries, so per-block attributed shares
// sum to the call's usage.output_tokens by construction.
func attributeOutput(callEntries []TranscriptEntry, acc map[billingKey]*billingTier) {
	parsed := ParseAssistantMessages(callEntries)
	DeduplicateUsage(parsed)
	for _, p := range parsed {
		var key billingKey
		switch p.ContentType {
		case "thinking":
			key = billingKey{"output", "thinking"}
		case "text":
			key = billingKey{"output", "assistant_text"}
		case "tool_use":
			lane, entity := toolLane(p.Content.Name)
			key = billingKey{"output", lane + "/" + entity}
		default:
			continue
		}
		tierFor(acc, key).output += float64(p.AttributedOutputTokens)
	}
}

func tierFor(acc map[billingKey]*billingTier, key billingKey) *billingTier {
	t, ok := acc[key]
	if !ok {
		t = &billingTier{}
		acc[key] = t
	}
	return t
}

func skillBodyNameBySHA(entries []TranscriptEntry) map[string]string {
	out := map[string]string{}
	for _, l := range buildLoadedSkillBodies(entries) {
		if l.Body != "" {
			out[sha256hex(l.Body)] = l.Name
		}
	}
	return out
}

func toolUseNames(entries []TranscriptEntry) map[string]string {
	out := map[string]string{}
	for _, e := range entries {
		if e.Type != "assistant" || e.Message == nil {
			continue
		}
		for _, c := range e.Message.Content {
			if c.Type == "tool_use" && c.ID != "" {
				out[c.ID] = c.Name
			}
		}
	}
	return out
}

func renderBillingSnapshot(callCount int, totals billingTier,
	acc map[billingKey]*billingTier) map[string]interface{} {

	byLane := map[string]*billingTier{}
	byEntity := make([]map[string]interface{}, 0, len(acc))
	for key, t := range acc {
		lt, ok := byLane[key.lane]
		if !ok {
			lt = &billingTier{}
			byLane[key.lane] = lt
		}
		lt.cacheRead += t.cacheRead
		lt.cacheCreation += t.cacheCreation
		lt.fresh += t.fresh
		lt.output += t.output
		byEntity = append(byEntity, map[string]interface{}{
			"lane":           key.lane,
			"entity":         key.entity,
			"cache_read":     round(t.cacheRead),
			"cache_creation": round(t.cacheCreation),
			"fresh":          round(t.fresh),
			"output":         round(t.output),
		})
	}
	byLaneOut := make([]map[string]interface{}, 0, len(byLane))
	for lane, t := range byLane {
		byLaneOut = append(byLaneOut, map[string]interface{}{
			"lane":           lane,
			"cache_read":     round(t.cacheRead),
			"cache_creation": round(t.cacheCreation),
			"fresh":          round(t.fresh),
			"output":         round(t.output),
		})
	}
	sort.Slice(byLaneOut, func(i, j int) bool {
		return billingRowTotal(byLaneOut[i]) > billingRowTotal(byLaneOut[j])
	})
	sort.Slice(byEntity, func(i, j int) bool {
		return billingRowTotal(byEntity[i]) > billingRowTotal(byEntity[j])
	})

	return map[string]interface{}{
		"llm_calls": callCount,
		"totals": map[string]interface{}{
			"cache_read":     round(totals.cacheRead),
			"cache_creation": round(totals.cacheCreation),
			"fresh":          round(totals.fresh),
			"output":         round(totals.output),
		},
		"by_lane":   byLaneOut,
		"by_entity": byEntity,
	}
}

func billingRowTotal(row map[string]interface{}) int {
	return row["cache_read"].(int) + row["cache_creation"].(int) +
		row["fresh"].(int) + row["output"].(int)
}

func round(f float64) int { return int(f + 0.5) }

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
