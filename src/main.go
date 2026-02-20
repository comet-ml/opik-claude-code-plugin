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
		threadID := input.SessionID
		if config.ThreadID != "" {
			threadID = config.ThreadID
		}
		trace := Trace{
			ID:          traceID,
			Name:        "claude-code",
			StartTime:   ts,
			ProjectName: config.Project,
			ThreadID:    threadID,
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
	// Brief delay to ensure transcript is fully written before reading
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

	// If slug was never sent, try one more time in the final update
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

	entries, err := ReadTranscript(input.TranscriptPath, 0)
	if err != nil {
		debugLog("read transcript: %v", err)
		return
	}

	var taskUUID string
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
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
	// Update trace metadata (name and model) if not already sent
	// Do this first, reading ALL entries, before checking for new spans
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

			// Only update model for traces we own (not parent traces)
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

	// Now process new entries for spans
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
			processToolUse(&span, p, toolResults, taskResults)

		default:
			continue
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
		span.Output = map[string]interface{}{"result": truncateMsg}

	case "Write":
		if config.Truncate {
			span.Input = map[string]interface{}{
				"file_path": span.Input["file_path"],
				"content":   truncateMsg,
			}
		}
		span.Output = map[string]interface{}{"result": truncateMsg}

	case "Read":
		span.Output = map[string]interface{}{"result": truncateMsg}

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

// containsAny reports whether s contains any of the given lowercase substrings.
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
