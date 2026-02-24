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
	startLine := 0
	if input.TranscriptPath != "" {
		startLine = countLines(input.TranscriptPath)
	}

	traceID := config.ParentTraceID
	if traceID == "" {
		traceID = uuid7()
	}
	ts := isoNow()

	state := &State{
		TraceID:    traceID,
		StartTime:  ts,
		SessionID:  input.SessionID,
		Transcript: input.TranscriptPath,
		StartLine:  startLine,
		LastFlush:  time.Now().Unix(),
	}
	if err := SaveState(state); err != nil {
		debugLog("save state: %v", err)
	}

	debugLog("trace=%s start=%d parent=%s", traceID, startLine, config.ParentTraceID)

	if config.ParentTraceID == "" {
		trace := Trace{
			ID:          traceID,
			Name:        "claude-code",
			StartTime:   ts,
			ProjectName: config.Project,
			ThreadID:    input.SessionID,
			Tags:        []string{"claude-code"},
			Input:       map[string]string{"text": input.Prompt},
		}
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
		if err := SaveState(state); err != nil {
			debugLog("save state: %v", err)
		}
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

	output := getLastOutput(state)
	ts := isoNow()
	finalUpdate := map[string]interface{}{
		"project_name": config.Project,
		"end_time":     ts,
		"output":       map[string]string{"text": output},
	}

	if !state.SlugSent {
		allEntries, err := ReadTranscript(state.Transcript, 0)
		if err == nil {
			if slug := findSlug(allEntries); slug != "" {
				finalUpdate["name"] = slug
			}
		}
	}

	if err := api.Patch("/traces/"+state.TraceID, finalUpdate); err != nil {
		debugLog("update trace: %v", err)
	}

	debugLog("done")
}

func onSessionEnd() {
	state, err := LoadState(input.SessionID)
	if err == nil {
		flush(state)
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
		state = &State{
			TraceID:    traceID,
			StartTime:  ts,
			SessionID:  input.SessionID,
			Transcript: input.TranscriptPath,
			StartLine:  countLines(input.TranscriptPath),
			LastFlush:  time.Now().Unix(),
		}

		if config.ParentTraceID == "" {
			trace := Trace{
				ID:          traceID,
				Name:        "claude-code",
				StartTime:   ts,
				ProjectName: config.Project,
				ThreadID:    input.SessionID,
				Tags:        []string{"claude-code"},
			}
			if err := api.Post("/traces", trace); err != nil {
				debugLog("compact: create trace: %v", err)
			}
		}

		if err := SaveState(state); err != nil {
			debugLog("save state: %v", err)
		}
	} else {
		flush(state)
	}

	compactTraceID := uuid7()
	ts := isoNow()
	traceName := "claude-code"
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

	parentSpanID := toV7(parentUUID)
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
	parentEntries, err := ReadTranscript(input.TranscriptPath, 0)
	if err == nil {
		taskResults := BuildTaskResults(parentEntries)
		for _, entry := range parentEntries {
			if entry.UUID != parentUUID || entry.Type != "assistant" || entry.Message == nil {
				continue
			}
			for _, content := range entry.Message.Content {
				if content.Type == "tool_use" && content.Name == "Task" {
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
			if content.Type != "tool_use" || content.Name != "Task" {
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
	if !state.SlugSent {
		allEntries, err := ReadTranscript(state.Transcript, 0)
		if err == nil && len(allEntries) > 0 {
			updates := map[string]interface{}{
				"project_name": config.Project,
			}

			slug := findSlug(allEntries)
			debugLog("findSlug: allEntries=%d slug=%q", len(allEntries), slug)
			if slug != "" {
				updates["name"] = slug
			}

			if config.ParentTraceID == "" {
				if model := FindModel(allEntries); model != "" {
					updates["model"] = model
				}
			}

			if len(updates) > 1 { // More than just project_name
				if err := api.Patch("/traces/"+state.TraceID, updates); err != nil {
					debugLog("update trace metadata: %v", err)
				} else if slug != "" {
					state.SlugSent = true
				}
			}
		}
	}

	entries, err := ReadTranscript(state.Transcript, state.StartLine)
	if err != nil || len(entries) == 0 {
		return
	}

	spans := processTranscriptEntries(state.TraceID, entries, "")
	if len(spans) == 0 {
		return
	}

	debugLog("flush: %d spans", len(spans))
	if err := api.Post("/spans/batch", SpanBatch{Spans: spans}); err != nil {
		debugLog("send spans: %v", err)
	}
}

func findSlug(entries []TranscriptEntry) string {
	for _, entry := range entries {
		if entry.Slug != "" {
			return entry.Slug
		}
	}
	return ""
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
			ID:          toV7(p.UUID),
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
			processToolUse(&span, p, toolResults, taskResults)

		default:
			continue
		}

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

func processToolUse(span *Span, p ParsedEntry, toolResults map[string]*ToolResultInfo, taskResults map[string]*ToolUseResult) {
	span.Name = p.Content.Name
	if span.Name == "" {
		span.Name = "Tool"
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

	case "Task":
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

func getLastOutput(state *State) string {
	entries, err := ReadTranscript(state.Transcript, state.StartLine)
	if err != nil {
		return ""
	}

	var lastText string
	for _, entry := range entries {
		if entry.Type != "assistant" || entry.Message == nil || len(entry.Message.Content) == 0 {
			continue
		}
		if entry.Message.Content[0].Type == "text" {
			lastText = entry.Message.Content[0].Text
		}
	}
	return lastText
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
