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
	flushIntervalSecs = 5
)

// HookInput represents the input received from Claude Code hooks
type HookInput struct {
	HookEventName       string `json:"hook_event_name"`
	SessionID           string `json:"session_id"`
	TranscriptPath      string `json:"transcript_path"`
	Prompt              string `json:"prompt"`
	AgentID             string `json:"agent_id"`
	AgentType           string `json:"agent_type"`
	AgentTranscriptPath string `json:"agent_transcript_path"`
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
	case "Stop", "SessionEnd":
		onStop()
	case "PreCompact":
		onCompact()
	default:
		debugLog("unknown event: %s", input.HookEventName)
	}
}

func onPrompt() {
	// Use parent trace ID if provided via env var, otherwise generate new
	traceID := config.ParentTraceID
	if traceID == "" {
		traceID = uuid7()
	}
	ts := isoNow()

	startLine := 0
	if input.TranscriptPath != "" {
		startLine = countLines(input.TranscriptPath)
	}

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

	// Only create new trace if not using parent trace
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
	if now-state.LastFlush >= flushIntervalSecs {
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
	if err := api.Patch("/traces/"+state.TraceID, map[string]interface{}{
		"project_name": config.Project,
		"end_time":     ts,
		"output":       map[string]string{"text": output},
	}); err != nil {
		debugLog("update trace: %v", err)
	}

	DeleteState(input.SessionID)
	debugLog("done")
}

func onCompact() {
	state, err := LoadState(input.SessionID)
	if err != nil {
		debugLog("load state: %v", err)
		return
	}

	flush(state)
	if err := SaveState(state); err != nil {
		debugLog("save state: %v", err)
	}

	ts := isoNow()
	span := Span{
		ID:          uuid7(),
		TraceID:     state.TraceID,
		Name:        "Compaction",
		Type:        "general",
		StartTime:   ts,
		EndTime:     ts,
		ProjectName: config.Project,
		Input:       map[string]interface{}{"event": "context_compacted"},
		Output:      map[string]interface{}{"status": "compacted"},
	}

	if err := api.Post("/spans/batch", SpanBatch{Spans: []Span{span}}); err != nil {
		debugLog("send compaction span: %v", err)
	}
}

func onSubagentStart() {
	if input.AgentID == "" {
		return
	}

	debugLog("subagent_start: %s (%s)", input.AgentID, input.AgentType)

	entries, err := ReadTranscriptReverse(input.TranscriptPath)
	if err != nil {
		debugLog("read transcript: %v", err)
		return
	}

	var taskUUID string
	for _, entry := range entries {
		if entry.Type != "assistant" || entry.Message == nil || len(entry.Message.Content) == 0 {
			continue
		}
		content := entry.Message.Content[0]
		if content.Type == "tool_use" && content.Name == "Task" {
			taskUUID = entry.UUID
			break
		}
	}

	if taskUUID == "" {
		return
	}

	debugLog("map: %s -> %s", input.AgentID, taskUUID)

	agents := LoadAgentMap(input.SessionID)
	agents[input.AgentID] = taskUUID
	if err := SaveAgentMap(input.SessionID, agents); err != nil {
		debugLog("save agent map: %v", err)
	}
}

func onSubagentStop() {
	state, err := LoadState(input.SessionID)
	if err != nil {
		debugLog("load state: %v", err)
		return
	}

	debugLog("subagent_stop: %s", input.AgentID)

	if input.AgentID == "" || input.AgentTranscriptPath == "" {
		return
	}

	agents := LoadAgentMap(input.SessionID)
	parentUUID, ok := agents[input.AgentID]
	if !ok || parentUUID == "" {
		return
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
}

func flush(state *State) {
	entries, err := ReadTranscript(state.Transcript, state.StartLine)
	if err != nil || len(entries) == 0 {
		return
	}

	// Update trace metadata (name and model) if not already sent
	if !state.SlugSent {
		updates := map[string]interface{}{
			"project_name": config.Project,
		}

		if slug := findSlug(entries); slug != "" {
			updates["name"] = slug
		}

		// Only update model for traces we own (not parent traces)
		if config.ParentTraceID == "" {
			if model := FindModel(entries); model != "" {
				updates["model"] = model
			}
		}

		if len(updates) > 1 { // More than just project_name
			if err := api.Patch("/traces/"+state.TraceID, updates); err != nil {
				debugLog("update trace: %v", err)
			} else {
				state.SlugSent = true
				debugLog("updated trace metadata")
			}
		}
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

	// Use root span ID from config if provided and no explicit parent
	effectiveParentSpanID := parentSpanID
	if effectiveParentSpanID == "" && config.RootSpanID != "" {
		effectiveParentSpanID = config.RootSpanID
	}

	spans := make([]Span, 0, len(parsed))
	for i, p := range parsed {
		// Calculate end time: use next entry's timestamp if available
		endTime := p.Timestamp
		if i+1 < len(parsed) {
			endTime = parsed[i+1].Timestamp
		}

		// For tool_use, try to get end time from tool result
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
			if p.Usage != nil {
				inp := p.Usage.InputTokens + p.Usage.CacheCreationInputTokens
				out := p.Usage.OutputTokens
				span.Usage = map[string]int{
					"prompt_tokens":     inp,
					"completion_tokens": out,
					"total_tokens":      inp + out,
				}
			}

		case "text":
			span.Name = "Text"
			span.Type = "general"
			span.Input = map[string]interface{}{}
			span.Output = map[string]interface{}{"text": p.Content.Text}

		case "tool_use":
			span.Name, span.Type, span.Input, span.Output, span.Usage, span.Error = processToolUse(p, toolResults, taskResults)

		default:
			continue
		}

		spans = append(spans, span)
	}

	return spans
}

func processToolUse(p ParsedEntry, toolResults map[string]*ToolResultInfo, taskResults map[string]*ToolUseResult) (name, typ string, input, output map[string]interface{}, usage map[string]int, spanErr *SpanError) {
	name = p.Content.Name
	if name == "" {
		name = "Tool"
	}
	typ = "tool"
	toolID := p.Content.ID
	input = p.Content.Input

	switch name {
	case "Edit":
		if config.Truncate {
			input = map[string]interface{}{
				"file_path":  input["file_path"],
				"old_string": truncateMsg,
				"new_string": truncateMsg,
			}
		}
		output = map[string]interface{}{"result": truncateMsg}

	case "Write":
		if config.Truncate {
			input = map[string]interface{}{
				"file_path": input["file_path"],
				"content":   truncateMsg,
			}
		}
		output = map[string]interface{}{"result": truncateMsg}

	case "Read":
		output = map[string]interface{}{"result": truncateMsg}

	case "Task":
		subType := "Task"
		if st, ok := input["subagent_type"].(string); ok && st != "" {
			subType = st
		}
		name = subType + " Subagent"

		prompt := ""
		if pr, ok := input["prompt"].(string); ok {
			prompt = pr
		}
		input = map[string]interface{}{"prompt": prompt}

		if result, ok := taskResults[toolID]; ok && result != nil {
			resp := ""
			if len(result.Content) > 0 {
				resp = result.Content[0].Text
			}
			output = map[string]interface{}{"response": resp}
			if result.TotalTokens > 0 {
				usage = map[string]int{"total_tokens": result.TotalTokens}
			}
		} else {
			output = map[string]interface{}{}
		}

	default:
		if result, ok := toolResults[toolID]; ok && result != nil {
			output = map[string]interface{}{"result": result.Result}

			// Check if tool result indicates an error
			if result.IsError {
				spanErr = categorizeError(result.Result)
			}
		} else {
			output = map[string]interface{}{}
		}
	}

	return name, typ, input, output, usage, spanErr
}

// categorizeError analyzes error messages and returns categorized error info
func categorizeError(errMsg string) *SpanError {
	errType := "tool_error"

	// Categorize based on common error patterns
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

// containsAny checks if s contains any of the substrings (case-insensitive)
func containsAny(s string, substrs ...string) bool {
	lower := strings.ToLower(s)
	for _, sub := range substrs {
		if strings.Contains(lower, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

// truncateString truncates a string to maxLen characters
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
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
		f.Truncate(0)
		f.Seek(0, 0)
	}

	ts := time.Now().Format("15:04:05")
	fmt.Fprintf(f, "[%s] %s\n", ts, fmt.Sprintf(format, args...))
}
