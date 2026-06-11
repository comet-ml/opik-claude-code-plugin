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
	// "definition" = always-on config that ships regardless of activity;
	// "usage" = conversation-driven content. The UI stacks the two.
	kind string
}

type billingPiece struct {
	key    billingKey
	tokens float64
	exact  bool // usage-derived (assistant blocks): never rescaled
}

const (
	unattributedLane = "unattributed"
	kindDefinition   = "definition"
	kindUsage        = "usage"
)

// computeBillingSnapshot returns the `cc.billing` block for the turn, or nil
// when the turn contains no usage-bearing LLM calls.
func computeBillingSnapshot(fullEntries, turnEntries []TranscriptEntry) map[string]interface{} {
	calls := llmCallsInTurn(fullEntries, turnEntries)
	if len(calls) == 0 {
		return nil
	}

	setBillingModel(fullEntries) // anchors are keyed (model, sha)
	staticPieces := staticPrefixPieces(fullEntries)
	skillBodyNames := skillBodyNameBySHA(fullEntries)
	toolNames := toolUseNames(fullEntries)
	counts := countNewEvents(turnEntries, skillBodyNames, toolNames)

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

	return renderBillingSnapshot(len(calls), totals, acc, counts)
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
			out = append(out, billingPiece{billingKey{lane, entity, kindDefinition}, float64(tokens), false})
		}
	}

	if consts, matched := ccBuiltinFor(findCCVersion(fullEntries)); matched != "" {
		if len(consts.Components) > 0 {
			// Per-release itemization from the calibration capture
			// (docs/builtin-calibration.md).
			names := make([]string, 0, len(consts.Components))
			for name := range consts.Components {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				add("static_overhead", name, consts.Components[name])
			}
		} else {
			// Tier-1 itemization: the environment block (cwd, platform, git
			// status snapshot) is dynamic but locally reconstructible —
			// carve it out of the bundled prompt so it shows as its own
			// item; the rest stays the per-version core estimate.
			envTokens := 0
			if envText := environmentBlockText(); envText != "" {
				envTokens = measuredOrEstimate(envText, "prose")
				if max := consts.SystemPromptTokens / 2; envTokens > max {
					envTokens = max // clamp: env can't dominate the prompt
				}
				add("static_overhead", "environment", envTokens)
			}
			add("static_overhead", "core_prompt", consts.SystemPromptTokens-envTokens)
			add("static_overhead", "builtin_tool_schemas", consts.SystemToolsTokens)
		}
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
	add := func(lane, entity, kind string, tokens float64, exact bool) {
		if tokens > 0 {
			out = append(out, billingPiece{billingKey{lane, entity, kind}, tokens, exact})
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
						add("skills", name, kindUsage, float64(measuredOrEstimate(c.Text, "skill_body")), false)
					} else {
						tokens := measuredOrEstimate(c.Text, "user_prompt")
						add("user_prompts", promptBucket(tokens), kindUsage, float64(tokens), false)
					}
				case "tool_result":
					lane, entity := toolLane(toolNames[c.ToolUseID])
					add(lane, entity, kindUsage, float64(measuredResultTokens(c.Content)), false)
				}
			}
		case "attachment":
			if e.Attachment == nil {
				continue
			}
			switch e.Attachment.Type {
			case "skill_listing":
				// Per-skill DEFINITION pieces: the listing is one attachment,
				// but each skill owns its menu block — that's what makes the
				// stacked definition/usage bars and the unused badge work
				// straight from billing.
				blocks := parseSkillListingMenu(e.Attachment.ContentString(), e.Attachment.Names)
				if len(blocks) == 0 {
					add("skills", "menu", kindDefinition,
						float64(tokEstimateAs(e.Attachment.ContentString(), "skill_listing_menu")), false)
					break
				}
				names := make([]string, 0, len(blocks))
				for name := range blocks {
					names = append(names, name)
				}
				sort.Strings(names) // deterministic layout
				for _, name := range names {
					add("skills", name, kindDefinition,
						float64(measuredOrEstimate(blocks[name], "skill_listing_menu")), false)
				}
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
					add("file_attachments", ext, kindUsage, float64(tokEstimate(w.File.Content)), false)
				}
			case "deferred_tools_delta":
				// The deferred catalog mixes built-in tool names with MCP
				// ones — split so each lands in its lane (built-in names are
				// part of Claude Code's own overhead, not MCP rent).
				lines := e.Attachment.AddedLines
				names := e.Attachment.AddedNames
				if len(lines) != len(names) {
					lines = names // fall back to names-only sizing
				}
				var builtinPayload, mcpPayload []string
				for i, name := range names {
					line := name
					if i < len(lines) {
						line = lines[i]
					}
					if strings.HasPrefix(name, "mcp__") {
						mcpPayload = append(mcpPayload, line)
					} else {
						builtinPayload = append(builtinPayload, line)
					}
				}
				add("static_overhead", "deferred_tool_names", kindDefinition,
					float64(measuredOrEstimate(strings.Join(builtinPayload, "\n"), "deferred_tools_payload")), false)
				add("mcp_servers", "catalog_deltas", kindDefinition,
					float64(measuredOrEstimate(strings.Join(mcpPayload, "\n"), "deferred_tools_payload")), false)
			case "mcp_instructions_delta":
				// Per-server when the parallel arrays line up.
				if len(e.Attachment.AddedNames) == len(e.Attachment.AddedBlocks) && len(e.Attachment.AddedNames) > 0 {
					for i, name := range e.Attachment.AddedNames {
						add("mcp_servers", name, kindDefinition,
							float64(measuredOrEstimate(e.Attachment.AddedBlocks[i], "prose")), false)
					}
				} else {
					add("mcp_servers", "instructions", kindDefinition,
						float64(tokEstimateAs(strings.Join(e.Attachment.AddedBlocks, "\n"), "prose")), false)
				}
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
					add("prior_assistant", p.ContentType, kindUsage, float64(p.AttributedOutputTokens), true)
				case "tool_use":
					lane, entity := toolLane(p.Content.Name)
					add(lane, entity, kindUsage, float64(p.AttributedOutputTokens), true)
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
			billingKey{unattributedLane, "", kindUsage}, total - sum, false})
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
			key = billingKey{"output", "thinking", kindUsage}
		case "text":
			key = billingKey{"output", "assistant_text", kindUsage}
		case "tool_use":
			lane, entity := toolLane(p.Content.Name)
			key = billingKey{"output", lane + "/" + entity, kindUsage}
		default:
			continue
		}
		tierFor(acc, key).output += float64(p.AttributedOutputTokens)
	}
}

// countNewEvents returns the number of NEW events this turn per usage key:
// prompts per bucket, tool calls per tool/server, files per ext, skill loads
// per skill. Additive across traces (each event counted once, in its turn),
// so plain SUM yields true counts — the same split rule as everywhere else.
func countNewEvents(turnEntries []TranscriptEntry, skillBodyNames map[string]string,
	toolNames map[string]string) map[billingKey]int {

	counts := map[billingKey]int{}
	bump := func(lane, entity string) {
		counts[billingKey{lane, entity, kindUsage}]++
	}

	for _, e := range turnEntries {
		switch e.Type {
		case "user":
			if e.Message == nil {
				continue
			}
			for _, c := range e.Message.Content {
				switch c.Type {
				case "text":
					if _, ok := skillBodyNames[sha256hex(c.Text)]; ok {
						continue // loads counted via buildLoadedSkillBodies below
					}
					bump("user_prompts", promptBucket(tokEstimateAs(c.Text, "user_prompt")))
				case "tool_result":
					lane, entity := toolLane(toolNames[c.ToolUseID])
					bump(lane, entity)
				}
			}
		case "attachment":
			if e.Attachment == nil || e.Attachment.Type != "file" {
				continue
			}
			var w struct {
				File struct {
					Path string `json:"path,omitempty"`
				} `json:"file"`
			}
			if json.Unmarshal(e.Attachment.Content, &w) == nil {
				ext := strings.ToLower(filepath.Ext(w.File.Path))
				if ext == "" {
					ext = "other"
				}
				bump("file_attachments", ext)
			}
		}
	}
	for _, l := range buildLoadedSkillBodies(turnEntries) {
		bump("skills", l.Name)
	}
	return counts
}

func tierFor(acc map[billingKey]*billingTier, key billingKey) *billingTier {
	t, ok := acc[key]
	if !ok {
		t = &billingTier{}
		acc[key] = t
	}
	return t
}

// measuredResultTokens is resultTokens with anchor lookup for the common
// string-payload shape.
func measuredResultTokens(content interface{}) int {
	if s, ok := content.(string); ok {
		return measuredOrEstimate(s, "tool_result")
	}
	return resultTokens(content)
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

// renderBillingSnapshot emits a SQL-first shape (OPIK-6870):
//
//	cc.billing.lanes.<laneKey>.{total, cache_read, cache_creation, fresh, output}
//	cc.billing.lanes.<laneKey>.items[] {name, total, cache_read, ...}
//
// Lane values are FIXED JSON paths so the BE's composition query stays one
// `SUM(JSONExtractInt(metadata,'cc','billing','lanes','<lane>','<col>'))`
// per cell — no ARRAY JOIN needed for totals. Breakdowns use the existing
// generic pattern: ARRAY JOIN over `...,'lanes','<lane>','items'` with
// label field `name`. `total` is precomputed (sum of the four columns) so
// the Sankey/lane-card query is also a single path.
func renderBillingSnapshot(callCount int, totals billingTier,
	acc map[billingKey]*billingTier, counts map[billingKey]int) map[string]interface{} {

	tierFields := func(t *billingTier) map[string]interface{} {
		return map[string]interface{}{
			"total":          round(t.cacheRead + t.cacheCreation + t.fresh + t.output),
			"cache_read":     round(t.cacheRead),
			"cache_creation": round(t.cacheCreation),
			"input":          round(t.fresh),
			"output":         round(t.output),
		}
	}

	// A key with new events this turn but no tier mass yet (e.g. the very
	// first event landed after the last call's request) must still surface.
	for key := range counts {
		tierFor(acc, key)
	}

	laneTiers := map[string]*billingTier{}
	laneItems := map[string][]map[string]interface{}{}
	for key, t := range acc {
		lt, ok := laneTiers[key.lane]
		if !ok {
			lt = &billingTier{}
			laneTiers[key.lane] = lt
		}
		lt.cacheRead += t.cacheRead
		lt.cacheCreation += t.cacheCreation
		lt.fresh += t.fresh
		lt.output += t.output
		if key.entity != "" {
			item := tierFields(t)
			item["name"] = key.entity
			item["kind"] = key.kind
			item["count"] = counts[key]
			laneItems[key.lane] = append(laneItems[key.lane], item)
		}
	}

	lanes := map[string]interface{}{}
	for lane, t := range laneTiers {
		obj := tierFields(t)
		if items := laneItems[lane]; len(items) > 0 {
			sort.Slice(items, func(i, j int) bool {
				return items[i]["total"].(int) > items[j]["total"].(int)
			})
			obj["items"] = items
		}
		lanes[lane] = obj
	}

	return map[string]interface{}{
		"llm_calls": callCount,
		// The session's model — lets consumers price the tier columns
		// without joining back to spans.
		"model": billingModel,
		"totals": map[string]interface{}{
			"total": round(totals.cacheRead + totals.cacheCreation +
				totals.fresh + totals.output),
			"cache_read":     round(totals.cacheRead),
			"cache_creation": round(totals.cacheCreation),
			"input":          round(totals.fresh),
			"output":         round(totals.output),
		},
		"lanes": lanes,
	}
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
