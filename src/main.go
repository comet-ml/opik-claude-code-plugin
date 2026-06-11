package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	initialBufferSize = 1 << 20  // 1 MB
	maxBufferSize     = 10 << 20 // 10 MB
	maxLogFileSize    = 1 << 20  // 1 MB
	flushInterval     = 5 * time.Second
)

type HookInput struct {
	HookEventName       string `json:"hook_event_name"`
	SessionID           string `json:"session_id"`
	TranscriptPath      string `json:"transcript_path"`
	Prompt              string `json:"prompt"`
	AgentID             string `json:"agent_id"`
	AgentType           string `json:"agent_type"`
	AgentTranscriptPath  string `json:"agent_transcript_path"`
	CustomInstructions   string `json:"custom_instructions"`
}

var (
	config *Config
	api    *API
	input  HookInput
)

func main() {
	// OPIK_CC_SKIP is the recursion guard: it's set on the env we hand to
	// any sub-claude we spawn (e.g. the /context capture fork), so when CC
	// fires its UserPromptSubmit / Stop / SessionEnd hooks inside that
	// fork they all short-circuit here. Without this, fetchRuntimeContext
	// would infinitely re-enter itself.
	if os.Getenv("OPIK_CC_SKIP") == "1" {
		os.Exit(0)
	}

	// Detached context-fetch mode: a previous invocation forked us as a
	// background subprocess to ask claude `/context` and PATCH the result
	// onto the trace. This path never reads stdin; it shells out to claude
	// and exits.
	if os.Getenv("OPIK_CC_CONTEXT_FETCH") == "1" {
		var err error
		config, err = LoadConfig()
		if err != nil || config == nil {
			os.Exit(0)
		}
		api = NewAPI(config)
		runContextFetchMode()
		os.Exit(0)
	}

	var err error
	config, err = LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "opik: %v\n", err)
		os.Exit(1)
	}
	if config == nil || !config.Enabled {
		os.Exit(0)
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "opik: failed to read stdin: %v\n", err)
		os.Exit(1)
	}

	if err := json.Unmarshal(data, &input); err != nil {
		fmt.Fprintf(os.Stderr, "opik: failed to parse input: %v\n", err)
		os.Exit(1)
	}

	debugLog("=== %s ===", input.HookEventName)

	api = NewAPI(config)

	switch input.HookEventName {
	case "UserPromptSubmit":
		onPrompt()
	case "PostToolUse", "PostToolUseFailure":
		onTool()
	case "SubagentStart":
		onSubagentStart()
	case "SubagentStop":
		onSubagentStop()
	case "Stop":
		onStop()
	case "SessionEnd":
		onSessionEnd()
	case "PreCompact":
		onCompact()
	default:
		debugLog("unknown event: %s", input.HookEventName)
	}
}

func onPrompt() {
	// Duplicate-fire guard: Claude Code's `claude -p --resume` path fires
	// UserPromptSubmit twice within ~2ms with the same prompt — a race
	// that can't be caught by state-file dedup alone. Defense in depth:
	//
	//   1. Deterministic trace ID below — both concurrent calls compute
	//      the same toV7 output, so a duplicate POST either no-ops on the
	//      server (409 on existing ID) or upserts onto the same row.
	//   2. State-based dedup — when the second call DOES manage to read
	//      the first's saved state, return early without re-POSTing.
	//
	// Bucket window: 5 seconds. Same session + prompt within 5s → same
	// trace ID. Different bucket → fresh trace (so a user typing the
	// same prompt twice on purpose still gets distinct traces).
	promptHash := sha256hex(input.Prompt)
	now := time.Now().Unix()
	if prev, err := LoadState(input.SessionID); err == nil && prev != nil {
		if prev.PromptHash == promptHash && now-prev.StartUnix <= 5 {
			debugLog("onPrompt: duplicate within %ds — reusing trace=%s", now-prev.StartUnix, prev.TraceID)
			return
		}
	}

	startLine := 0
	if input.TranscriptPath != "" {
		startLine = countLines(input.TranscriptPath)
	}

	traceID := config.ParentTraceID
	if traceID == "" {
		bucket := now / 5
		// Embed the bucket-aligned time (ms) so the v7 timestamp matches the
		// trace's start_time. Bucket alignment keeps both concurrent onPrompt
		// fires on the same timestamp, preserving the deterministic-ID dedup.
		traceID = toV7(fmt.Sprintf("trace:%s:%s:%d", input.SessionID, promptHash, bucket), bucket*5*1000)
	}
	ts := isoNow()

	cwd, headStart := captureCwdAndHead()
	state := &State{
		TraceID:      traceID,
		StartTime:    ts,
		StartUnix:    now,
		PromptHash:   promptHash,
		SessionID:    input.SessionID,
		Transcript:   input.TranscriptPath,
		StartLine:    startLine,
		LastFlush:    now,
		Cwd:          cwd,
		HeadSHAStart: headStart,
	}
	if err := SaveState(state); err != nil {
		debugLog("save state: %v", err)
	}

	debugLog("trace=%s start=%d parent=%s", traceID, startLine, config.ParentTraceID)

	if config.ParentTraceID == "" {
		trace := Trace{
			ID:          traceID,
			Name:        traceNameFromPrompt(input.Prompt),
			StartTime:   ts,
			ProjectName: config.Project,
			ThreadID:    input.SessionID,
			Tags:        []string{"claude-code"},
			Input:       map[string]string{"text": input.Prompt},
		}
		loadClaudeIdentity().applyToTrace(&trace)
		if err := api.Post("/traces", trace); err != nil {
			debugLog("create trace: %v", err)
		}
	}
}

func onTool() {
	state, err := LoadState(input.SessionID)
	if err != nil {
		debugLog("load state: %v", err)
		return
	}

	now := time.Now().Unix()
	if time.Since(time.Unix(state.LastFlush, 0)) >= flushInterval {
		debugLog("flushing (%ds)", now-state.LastFlush)
		flush(state)
		state.LastFlush = now
	}

	if err := SaveState(state); err != nil {
		debugLog("save state: %v", err)
	}
}

func onStop() {
	time.Sleep(100 * time.Millisecond)

	state, err := LoadState(input.SessionID)
	if err != nil {
		debugLog("load state: %v", err)
		return
	}

	flush(state)
	postTraceMetrics(state)

	output := getTurnOutput(state)
	ts := isoNow()
	finalUpdate := map[string]interface{}{
		"project_name": config.Project,
		"end_time":     ts,
		"output":       map[string]string{"text": output},
	}

	// Name was set from the user prompt at creation; don't overwrite here.

	if err := api.Patch("/traces/"+state.TraceID, finalUpdate); err != nil {
		debugLog("update trace: %v", err)
	}

	// Fire-and-forget: detach a child process to ask `claude /context` for
	// the trace's actual numbers and PATCH them onto metadata.cc.context_runtime.
	// Adds ~1s of work, but it runs in the background — this hook returns
	// immediately, claude continues, and the trace gets the exact figures
	// shortly after. Failures are logged and ignored so a misconfigured
	// claude binary can't break tracing.
	if err := spawnDetachedContextFetch(state.SessionID, state.TraceID, state.Cwd); err != nil {
		debugLog("spawn context fetch: %v", err)
	}

	debugLog("done")
}

func onSessionEnd() {
	state, err := LoadState(input.SessionID)
	if err == nil {
		flush(state)
		postTraceMetrics(state)
		ts := isoNow()
		finalUpdate := map[string]interface{}{
			"project_name": config.Project,
			"end_time":     ts,
		}
		if err := api.Patch("/traces/"+state.TraceID, finalUpdate); err != nil {
			debugLog("session end update trace: %v", err)
		}
	}
	DeleteState(input.SessionID)
	debugLog("session ended")
}

func onCompact() {
	state, err := LoadState(input.SessionID)
	if err != nil {
		debugLog("compact: no state, bootstrapping: %v", err)
		traceID := config.ParentTraceID
		if traceID == "" {
			traceID = uuid7()
		}
		ts := isoNow()
		startLine := countLines(input.TranscriptPath)
		cwd, headStart := captureCwdAndHead()
		state = &State{
			TraceID:      traceID,
			StartTime:    ts,
			SessionID:    input.SessionID,
			Transcript:   input.TranscriptPath,
			StartLine:    startLine,
			LastFlush:    time.Now().Unix(),
			Cwd:          cwd,
			HeadSHAStart: headStart,
		}

		if config.ParentTraceID == "" {
			// Compaction has no user prompt — keep the default name and
			// let the slug/aiTitle PATCH below fill it in if we find one.
			trace := Trace{
				ID:          traceID,
				Name:        "claude-code",
				StartTime:   ts,
				ProjectName: config.Project,
				ThreadID:    input.SessionID,
				Tags:        []string{"claude-code"},
			}
			loadClaudeIdentity().applyToTrace(&trace)
			if err := api.Post("/traces", trace); err != nil {
				debugLog("compact: create trace: %v", err)
			}
		}

		if err := SaveState(state); err != nil {
			debugLog("save state: %v", err)
		}
	} else {
		flush(state)
		postTraceMetrics(state)
	}

	compactTraceID := uuid7()
	ts := isoNow()
	// Compaction has no per-turn user prompt — fall back to the session-level
	// aiTitle (or legacy slug) so the trace at least has a meaningful label.
	traceName := "Compaction"
	allEntries, err := ReadTranscript(state.Transcript, 0)
	if err == nil {
		if slug := findSlug(allEntries); slug != "" {
			traceName = slug
		}
	}
	trace := Trace{
		ID:          compactTraceID,
		Name:        traceName,
		StartTime:   ts,
		EndTime:     ts,
		ProjectName: config.Project,
		ThreadID:    input.SessionID,
		Tags:        []string{"claude-code", "compaction"},
	}
	loadClaudeIdentity().applyToTrace(&trace)
	if err := api.Post("/traces", trace); err != nil {
		debugLog("compact: create trace: %v", err)
	}

	span := Span{
		ID:          uuid7(),
		TraceID:     compactTraceID,
		Name:        "Compaction",
		Type:        "general",
		StartTime:   ts,
		EndTime:     ts,
		ProjectName: config.Project,
		Input:       map[string]interface{}{"text": compactInput(input.CustomInstructions)},
		Output:      map[string]interface{}{"status": "compacted"},
	}
	if err := api.Post("/spans/batch", SpanBatch{Spans: []Span{span}}); err != nil {
		debugLog("send compaction span: %v", err)
	}

	state.TraceID = compactTraceID
	state.StartLine = countLines(input.TranscriptPath)
	state.LastFlush = time.Now().Unix()
	state.Cwd, state.HeadSHAStart = captureCwdAndHead()
	if err := SaveState(state); err != nil {
		debugLog("save state: %v", err)
	}
}

func onSubagentStart() {
	if input.AgentID == "" {
		return
	}
	debugLog("subagent_start: %s (%s)", input.AgentID, input.AgentType)

	// Mapping is deferred to onSubagentStop since the Task tool_use
	// may not be in the transcript yet when SubagentStart fires.
	agents := LoadAgentMap(input.SessionID)
	agents[input.AgentID] = ""
	if err := SaveAgentMap(input.SessionID, agents); err != nil {
		debugLog("save agent map: %v", err)
	}
}

func onSubagentStop() {
	debugLog("subagent_stop: %s", input.AgentID)

	if input.AgentID == "" || input.AgentTranscriptPath == "" {
		return
	}

	state, err := LoadState(input.SessionID)
	if err != nil {
		debugLog("load state: %v", err)
		return
	}

	agents := LoadAgentMap(input.SessionID)
	if _, ok := agents[input.AgentID]; !ok {
		return
	}

	parentUUID := agents[input.AgentID]
	if parentUUID == "" {
		parentUUID = findTaskUUID(agents)
		if parentUUID == "" {
			debugLog("subagent_stop: no matching Task found for %s", input.AgentID)
			return
		}
		agents[input.AgentID] = parentUUID
		if err := SaveAgentMap(input.SessionID, agents); err != nil {
			debugLog("save agent map: %v", err)
		}
	}

	// Read the parent transcript up front so we can recover the parent Task
	// entry's timestamp — toV7 embeds it, so the reference here must use the
	// same value the parent span ID was built with (see span creation in
	// processTranscript). Reused below to patch the parent span's output.
	parentEntries, parentErr := ReadTranscript(input.TranscriptPath, 0)
	parentMs := int64(0)
	for _, entry := range parentEntries {
		if entry.UUID == parentUUID {
			parentMs = millisFromISO(entry.Timestamp)
			break
		}
	}

	parentSpanID := toV7(parentUUID, parentMs)
	debugLog("processing subagent with parent=%s", parentSpanID)

	spans := processTranscript(state.TraceID, input.AgentTranscriptPath, 0, parentSpanID)
	if len(spans) == 0 {
		return
	}

	debugLog("subagent flush: %d spans", len(spans))
	if err := api.Post("/spans/batch", SpanBatch{Spans: spans}); err != nil {
		debugLog("send subagent spans: %v", err)
	}

	// Patch the parent Task span with output (it was sent before the subagent completed).
	if parentErr == nil {
		taskResults := BuildTaskResults(parentEntries)
		for _, entry := range parentEntries {
			if entry.UUID != parentUUID || entry.Type != "assistant" || entry.Message == nil {
				continue
			}
			for _, content := range entry.Message.Content {
				if content.Type == "tool_use" && (content.Name == "Agent" || content.Name == "Task") {
					if result, ok := taskResults[content.ID]; ok && result != nil {
						resp := ""
						if len(result.Content) > 0 {
							resp = result.Content[0].Text
						}
						update := map[string]interface{}{
							"output": map[string]interface{}{"response": resp},
						}
						if result.TotalTokens > 0 {
							update["usage"] = map[string]int{"total_tokens": result.TotalTokens}
						}
						if err := api.Patch("/spans/"+parentSpanID, update); err != nil {
							debugLog("update task span output: %v", err)
						}
					}
				}
			}
			break
		}
	}
}

// findTaskUUID matches this subagent to its parent Task tool_use entry
// by comparing the subagent's prompt against Task inputs in the parent transcript.
func findTaskUUID(agents AgentMap) string {
	subPrompt := extractSubagentPrompt(input.AgentTranscriptPath)

	entries, err := ReadTranscript(input.TranscriptPath, 0)
	if err != nil {
		return ""
	}

	claimed := make(map[string]bool, len(agents))
	for _, uuid := range agents {
		if uuid != "" {
			claimed[uuid] = true
		}
	}

	var promptMatch, typeMatch, fallbackUUID string
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.Type != "assistant" || entry.Message == nil {
			continue
		}
		for _, content := range entry.Message.Content {
			if content.Type != "tool_use" || (content.Name != "Agent" && content.Name != "Task") {
				continue
			}
			if claimed[entry.UUID] {
				continue
			}
			if promptMatch == "" && subPrompt != "" {
				if p, ok := content.Input["prompt"].(string); ok && p == subPrompt {
					promptMatch = entry.UUID
				}
			}
			if typeMatch == "" {
				if st, ok := content.Input["subagent_type"].(string); ok && st == input.AgentType {
					typeMatch = entry.UUID
				}
			}
			if fallbackUUID == "" {
				fallbackUUID = entry.UUID
			}
		}
		if promptMatch != "" {
			break
		}
	}

	if promptMatch != "" {
		return promptMatch
	}
	if typeMatch != "" {
		return typeMatch
	}
	return fallbackUUID
}

// extractSubagentPrompt reads the first user message from a subagent transcript.
func extractSubagentPrompt(path string) string {
	if path == "" {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, initialBufferSize)
	scanner.Buffer(buf, maxBufferSize)

	for scanner.Scan() {
		var raw struct {
			Type    string          `json:"type"`
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil || raw.Type != "user" || raw.Message == nil {
			continue
		}

		var msg struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw.Message, &msg); err != nil || msg.Content == nil {
			continue
		}

		var str string
		if err := json.Unmarshal(msg.Content, &str); err == nil && str != "" {
			return str
		}

		var contents []Content
		if err := json.Unmarshal(msg.Content, &contents); err == nil {
			for _, c := range contents {
				if c.Type == "text" && c.Text != "" {
					return c.Text
				}
			}
		}
	}
	return ""
}

func flush(state *State) {
	// Trace name is set at creation from the user prompt (or to "claude-code"
	// for compaction). We don't overwrite it later with the session-level
	// aiTitle — that would collapse every per-turn trace name into the same
	// title. The slug PATCH path is preserved only to backfill the model
	// once we see an assistant message.
	if !state.SlugSent {
		allEntries, err := ReadTranscript(state.Transcript, 0)
		if err == nil && len(allEntries) > 0 {
			updates := map[string]interface{}{
				"project_name": config.Project,
			}

			if config.ParentTraceID == "" {
				if model := FindModel(allEntries); model != "" {
					updates["model"] = model
				}
			}

			if len(updates) > 1 { // More than just project_name
				if err := api.Patch("/traces/"+state.TraceID, updates); err != nil {
					debugLog("update trace metadata: %v", err)
				} else {
					state.SlugSent = true
				}
			}
		}
	}

	entries, err := ReadTranscript(state.Transcript, state.StartLine)
	if err != nil || len(entries) == 0 {
		return
	}

	// Domain snapshots are written to trace.metadata.cc by postTraceMetrics
	// (called on Stop / SessionEnd). Per-span metadata stays minimal:
	// cc.llm_call on every span, cc.skills.load on Skill tool_uses, and
	// cc.tool on MCP tool_uses. Shipping the full snapshot on every span
	// inflates the upload payload N× for no extra information.
	spans := processTranscriptEntries(state.TraceID, entries, "")
	if len(spans) == 0 {
		return
	}

	// Stamp every LLM span (those that carry usage data — one per LLM
	// call after DeduplicateUsage moves the anchor to the first block of
	// each message_id group) with a snapshot of the request-context
	// breakdown by category. Lets cost dashboards attribute per-span
	// billed tokens to categories with a single-row query, no JOIN back
	// to trace.cc.context_runtime. See context_snapshot.go for the
	// accuracy tradeoff.
	if snapshot := buildContextSnapshot(state); snapshot != nil {
		for i := range spans {
			if spans[i].Usage == nil {
				continue
			}
			cc := ensureCCMap(&spans[i])
			cc["context_snapshot"] = snapshot
		}
	}

	debugLog("flush: %d spans", len(spans))
	if err := api.Post("/spans/batch", SpanBatch{Spans: spans}); err != nil {
		debugLog("send spans: %v", err)
	}
}

// findSlug returns the best per-session identifier available on the
// transcript. Historic shape: per-entry `slug` (session-stable kebab-case).
// Claude Code 2.1.150+ shape: dedicated `type:"ai-title"` events carrying
// `aiTitle` (human-meaningful session title). Both supported.
func findSlug(entries []TranscriptEntry) string {
	for _, entry := range entries {
		if entry.AITitle != "" {
			return entry.AITitle
		}
		if entry.Slug != "" {
			return entry.Slug
		}
	}
	return ""
}

// traceNameFromPrompt returns a trace-friendly name from a user prompt.
// Trims, collapses whitespace, truncates so the trace list stays readable.
// Falls back to "claude-code" for empty prompts.
func traceNameFromPrompt(prompt string) string {
	s := strings.TrimSpace(prompt)
	if s == "" {
		return "claude-code"
	}
	s = strings.Join(strings.Fields(s), " ")
	const maxLen = 80
	if len(s) > maxLen {
		s = s[:maxLen-1] + "…"
	}
	return s
}

func processTranscript(traceID, path string, startLine int, parentSpanID string) []Span {
	entries, err := ReadTranscript(path, startLine)
	if err != nil || len(entries) == 0 {
		return nil
	}
	return processTranscriptEntries(traceID, entries, parentSpanID)
}

func processTranscriptEntries(traceID string, entries []TranscriptEntry, parentSpanID string) []Span {
	toolResults := BuildToolResults(entries)
	taskResults := BuildTaskResults(entries)
	parsed := ParseAssistantMessages(entries)
	DeduplicateUsage(parsed)
	skillBodies := buildSkillBodyMap(entries)

	effectiveParentSpanID := parentSpanID
	if effectiveParentSpanID == "" && config.RootSpanID != "" {
		effectiveParentSpanID = config.RootSpanID
	}

	spans := make([]Span, 0, len(parsed))
	for i, p := range parsed {
		endTime := p.Timestamp
		if i+1 < len(parsed) {
			endTime = parsed[i+1].Timestamp
		}

		if p.ContentType == "tool_use" {
			if result, ok := toolResults[p.Content.ID]; ok && result != nil && result.Timestamp != "" {
				endTime = result.Timestamp
			}
		}

		span := Span{
			ID:          toV7(p.UUID, millisFromISO(p.Timestamp)),
			TraceID:     traceID,
			StartTime:   p.Timestamp,
			EndTime:     endTime,
			ProjectName: config.Project,
		}
		if effectiveParentSpanID != "" {
			span.ParentSpanID = effectiveParentSpanID
		}

		switch p.ContentType {
		case "thinking":
			span.Name = "Thinking"
			span.Type = "llm"
			span.Input = map[string]interface{}{}
			span.Output = map[string]interface{}{"thinking": p.Content.Thinking}

		case "text":
			span.Name = "Text"
			span.Type = "general"
			span.Input = map[string]interface{}{}
			span.Output = map[string]interface{}{"text": p.Content.Text}

		case "tool_use":
			processToolUse(&span, p, toolResults, taskResults, skillBodies)

		default:
			continue
		}

		applyLLMCallMetadata(&span, p)

		if p.Usage != nil && span.Usage == nil {
			span.Usage = map[string]int{
				"prompt_tokens":     p.Usage.InputTokens,
				"completion_tokens": p.Usage.OutputTokens,
				"total_tokens":      p.Usage.InputTokens + p.Usage.OutputTokens,
				"original_usage.input_tokens":               p.Usage.InputTokens,
				"original_usage.output_tokens":              p.Usage.OutputTokens,
				"original_usage.cache_read_input_tokens":    p.Usage.CacheReadInputTokens,
				"original_usage.cache_creation_input_tokens": p.Usage.CacheCreationInputTokens,
			}
			span.Provider = "anthropic"
			if p.Model != "" {
				span.Model = p.Model
			}
		}

		spans = append(spans, span)
	}

	return spans
}

// domainSnapshotsFromEntries returns every per-domain snapshot keyed by
// domain name. Called by postTraceMetrics for the trace-level write and
// by dryrun_test for offline validation. The parsed+deduped slice is
// computed once and passed to the few extractors that need it
// (extractThinkingSnapshot), avoiding redundant work.
func domainSnapshotsFromEntries(fullEntries, turnEntries []TranscriptEntry) map[string]map[string]interface{} {
	parsedTurn := ParseAssistantMessages(turnEntries)
	DeduplicateUsage(parsedTurn)

	return map[string]map[string]interface{}{
		"skills":           BuildSkillsSnapshot(fullEntries),
		"tools":            extractToolsSnapshot(fullEntries),
		"memory":           extractMemorySnapshot(),
		"agents":           extractAgentsSnapshot(),
		"thinking":         extractThinkingSnapshot(turnEntries, parsedTurn),
		"tool_results":     extractToolResultsSnapshot(fullEntries, turnEntries),
		"user_prompts":     extractUserPromptsSnapshot(fullEntries, turnEntries),
		"file_attachments": extractFileAttachmentsSnapshot(fullEntries, turnEntries),
		"prior_assistant":  extractPriorAssistantSnapshot(fullEntries, turnEntries),
		"assistant_text":   extractAssistantTextSnapshot(turnEntries),
		"output_tokens":    extractOutputTokensSnapshot(turnEntries, parsedTurn),
		// cc_builtin covers the bundled system-prompt + tool-catalog cost
		// /context reports under "System prompt" / "System tools" /
		// "System tools (deferred)". These never appear in the transcript
		// (the binary holds the schemas internally), so values are
		// version-keyed approximations — marked `estimated: true` in the
		// payload so the FE can render them as such.
		"cc_builtin":       extractCCBuiltinSnapshot(fullEntries),
	}
}

// applyLLMCallMetadata tags every per-message span with its origin in the
// transcript. Critical for analysis: Claude Code splits a single LLM call
// into multiple transcript entries (one per content block), all sharing
// the same `message.id` and `message.usage`. DeduplicateUsage attaches the
// total usage to only the first block, so naive `GROUP BY span.name` over
// `usage` looks like all tokens came from Thinking. Consumers should
// `GROUP BY cc.llm_call.message_id` to reconstruct the true per-call
// totals.
func applyLLMCallMetadata(span *Span, p ParsedEntry) {
	if p.MessageID == "" {
		return
	}
	cc := ensureCCMap(span)
	cc["llm_call"] = map[string]interface{}{
		"message_id":               p.MessageID,
		"block_index":              p.BlockIndex,
		"block_kind":               p.ContentType, // "thinking" | "text" | "tool_use"
		"measured_output_tokens":   p.MeasuredOutputTokens,
		"attributed_output_tokens": p.AttributedOutputTokens,
	}
}

func ensureCCMap(span *Span) map[string]interface{} {
	if span.Metadata == nil {
		span.Metadata = map[string]interface{}{}
	}
	cc, ok := span.Metadata["cc"].(map[string]interface{})
	if !ok {
		cc = map[string]interface{}{}
		span.Metadata["cc"] = cc
	}
	return cc
}

func processToolUse(span *Span, p ParsedEntry, toolResults map[string]*ToolResultInfo, taskResults map[string]*ToolUseResult, skillBodies map[string]string) {
	rawName := p.Content.Name
	span.Name = rawName
	if span.Name == "" {
		span.Name = "Tool"
	}
	// MCP tools come over the wire as `mcp__<server>__<tool>`. Display the
	// bare tool name; surface server + full name as metadata so analytics
	// can group by server and filtering by the full canonical name still
	// works.
	//
	// SplitN with limit 3 assumes server names contain no `__`. Tool names
	// CAN contain `__` — they get absorbed into the third part, which is
	// correct. If an MCP server ever uses `__` in its name (rare), this
	// would mis-attribute the prefix; fix would be configurable parsing or
	// a server allowlist.
	if strings.HasPrefix(rawName, "mcp__") {
		parts := strings.SplitN(rawName, "__", 3)
		if len(parts) == 3 {
			cc := ensureCCMap(span)
			cc["tool"] = map[string]interface{}{
				"name":   parts[2],
				"server": parts[1],
				"source": "mcp",
				"full":   rawName,
			}
			span.Name = parts[2]
		}
	}
	span.Type = "tool"
	span.Input = p.Content.Input
	toolID := p.Content.ID

	switch span.Name {
	case "Edit":
		if config.Truncate {
			span.Input = map[string]interface{}{
				"file_path":  span.Input["file_path"],
				"old_string": truncateMsg,
				"new_string": truncateMsg,
			}
		}
		if config.Truncate {
			span.Output = map[string]interface{}{"result": truncateMsg}
		}

	case "Write":
		if config.Truncate {
			span.Input = map[string]interface{}{
				"file_path": span.Input["file_path"],
				"content":   truncateMsg,
			}
			span.Output = map[string]interface{}{"result": truncateMsg}
		}

	case "Read":
		if config.Truncate {
			span.Output = map[string]interface{}{"result": truncateMsg}
		}

	case "Agent", "Task":
		subType := "Task"
		if st, ok := span.Input["subagent_type"].(string); ok && st != "" {
			subType = st
		}
		span.Name = subType + " Subagent"

		prompt := ""
		if pr, ok := span.Input["prompt"].(string); ok {
			prompt = pr
		}
		span.Input = map[string]interface{}{"prompt": prompt}

		if result, ok := taskResults[toolID]; ok && result != nil {
			resp := ""
			if len(result.Content) > 0 {
				resp = result.Content[0].Text
			}
			span.Output = map[string]interface{}{"response": resp}
			if result.TotalTokens > 0 {
				span.Usage = map[string]int{"total_tokens": result.TotalTokens}
			}
		} else {
			span.Output = map[string]interface{}{}
		}

	default:
		if result, ok := toolResults[toolID]; ok && result != nil {
			span.Output = map[string]interface{}{"result": result.Result}
			if result.IsError {
				span.Error = categorizeError(result.Result)
			}
		} else {
			span.Output = map[string]interface{}{}
		}
	}

	if span.Name == "Skill" {
		enrichSkillSpan(span, p, skillBodies)
	}
}

// enrichSkillSpan stamps a `Skill` tool_use span with the load event under
// `cc.skills.load`. Same identity shape as a `cc.skills.loaded[]` row, so
// querying for "every span that loaded opik-backend" is one filter on
// `cc.skills.load.name`.
func enrichSkillSpan(span *Span, p ParsedEntry, skillBodies map[string]string) {
	skillName := skillInputName(p.Content.Input)
	body := skillBodies[p.Content.ID]
	path, _ := resolveSkillBody(skillName)
	source := "bundled"
	if path != "" {
		source = "listing"
	}
	load := map[string]interface{}{
		"name":        skillName,
		"source":      source,
		"sha256":      sha256hex(body),
		"body_tokens": tokEstimateAs(body, "skill_body"),
		"tool_use_id": p.Content.ID,
	}
	if path != "" {
		load["path"] = path
	}

	cc := ensureCCMap(span)
	skills, ok := cc["skills"].(map[string]interface{})
	if !ok {
		skills = map[string]interface{}{}
		cc["skills"] = skills
	}
	skills["load"] = load
}

func compactInput(customInstructions string) string {
	if customInstructions != "" {
		return "/compact " + customInstructions
	}
	return "/compact"
}

func categorizeError(errMsg string) *SpanError {
	errType := "tool_error"

	switch {
	case containsAny(errMsg, "timeout", "timed out", "deadline exceeded"):
		errType = "timeout"
	case containsAny(errMsg, "permission denied", "access denied", "forbidden", "not authorized"):
		errType = "permission_denied"
	case containsAny(errMsg, "not found", "no such file", "does not exist", "ENOENT"):
		errType = "not_found"
	case containsAny(errMsg, "connection refused", "network error", "unreachable"):
		errType = "network_error"
	}

	return &SpanError{
		Type:    errType,
		Message: truncateString(errMsg, 500),
	}
}

func containsAny(s string, substrs ...string) bool {
	lower := strings.ToLower(s)
	for _, sub := range substrs {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}

func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// getTurnOutput returns the assistant text that the trace's `output.text`
// field should display. Concatenates every assistant text block in the
// turn — a single turn can produce multiple text responses when the user
// interrupts mid-flight, or when Claude responds with text both before
// and after a tool sequence. Iterates every content block so multi-block
// entries are not silently dropped.
func getTurnOutput(state *State) string {
	entries, err := ReadTranscript(state.Transcript, state.StartLine)
	if err != nil {
		return ""
	}

	var parts []string
	for _, entry := range entries {
		if entry.Type != "assistant" || entry.Message == nil {
			continue
		}
		for _, c := range entry.Message.Content {
			if c.Type == "text" && c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func countLines(path string) int {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, initialBufferSize)
	scanner.Buffer(buf, maxBufferSize)

	count := 0
	for scanner.Scan() {
		count++
	}
	if err := scanner.Err(); err != nil {
		debugLog("scan %s: %v", path, err)
	}
	return count
}

func isoNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func debugLog(format string, args ...interface{}) {
	if config == nil || !config.Debug {
		return
	}

	logPath := filepath.Join(os.TempDir(), "opik-debug.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err == nil && info.Size() > maxLogFileSize {
		if err := f.Truncate(0); err != nil {
			return
		}
		if _, err := f.Seek(0, 0); err != nil {
			return
		}
	}

	ts := time.Now().Format("15:04:05")
	fmt.Fprintf(f, "[%s] ", ts)
	fmt.Fprintf(f, format+"\n", args...)
}

