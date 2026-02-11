package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
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
	// Load configuration
	config = LoadConfig()
	if config == nil {
		os.Exit(0)
	}

	// Read input from stdin
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(0)
	}

	if err := json.Unmarshal(data, &input); err != nil {
		os.Exit(0)
	}

	debugLog("=== %s ===", input.HookEventName)

	// Initialize API client
	api = NewAPI(config)

	// Dispatch to event handler
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

// onPrompt handles UserPromptSubmit - creates a new trace
func onPrompt() {
	traceID := uuid7()
	ts := isoNow()
	
	// Count existing lines in transcript
	startLine := 0
	if input.TranscriptPath != "" {
		startLine = countLines(input.TranscriptPath)
	}
	
	// Save state
	state := &State{
		TraceID:    traceID,
		StartTime:  ts,
		SessionID:  input.SessionID,
		Transcript: input.TranscriptPath,
		StartLine:  startLine,
		LastFlush:  time.Now().Unix(),
	}
	SaveState(state)

	debugLog("trace=%s start=%d", traceID, startLine)

	// Create trace (synchronous - must exist before spans)
	trace := Trace{
		ID:          traceID,
		Name:        "claude-code",
		StartTime:   ts,
		ProjectName: config.Project,
		ThreadID:    input.SessionID,
		Tags:        []string{"claude-code"},
		Input:       map[string]string{"text": input.Prompt},
	}
	api.Post("/traces", trace)
}

// onTool handles PostToolUse/PostToolUseFailure - time-based flush
func onTool() {
	state, err := LoadState(input.SessionID)
	if err != nil {
		return
	}

	now := time.Now().Unix()
	if now-state.LastFlush >= 5 {
		debugLog("flushing (%ds)", now-state.LastFlush)
		flush(state)
		state.LastFlush = now
		SaveState(state)
	}
}

// onStop handles Stop/SessionEnd - final flush and trace update
func onStop() {
	state, err := LoadState(input.SessionID)
	if err != nil {
		return
	}

	flush(state)

	// Get final output (last text from assistant)
	output := getLastOutput(state)

	ts := isoNow()
	api.Patch("/traces/"+state.TraceID, map[string]interface{}{
		"project_name": config.Project,
		"end_time":     ts,
		"output":       map[string]string{"text": output},
	})

	DeleteState(input.SessionID)
	debugLog("done")
}

// onCompact handles PreCompact - creates compaction span
func onCompact() {
	state, err := LoadState(input.SessionID)
	if err != nil {
		return
	}

	flush(state)

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

	api.Post("/spans/batch", SpanBatch{Spans: []Span{span}})
}

// onSubagentStart handles SubagentStart - maps agent_id to parent task UUID
func onSubagentStart() {
	if input.AgentID == "" {
		return
	}

	debugLog("subagent_start: %s (%s)", input.AgentID, input.AgentType)

	// Find the most recent Task tool_use in the transcript
	entries, err := ReadTranscriptReverse(input.TranscriptPath)
	if err != nil {
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
	SaveAgentMap(input.SessionID, agents)
}

// onSubagentStop handles SubagentStop - processes subagent transcript
func onSubagentStop() {
	state, err := LoadState(input.SessionID)
	if err != nil {
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
	api.Post("/spans/batch", SpanBatch{Spans: spans})
}

// flush processes the transcript and sends spans to the API
func flush(state *State) {
	spans := processTranscript(state.TraceID, state.Transcript, state.StartLine, "")
	if len(spans) == 0 {
		return
	}

	debugLog("flush: %d spans", len(spans))
	api.Post("/spans/batch", SpanBatch{Spans: spans})
}

// processTranscript reads the transcript and generates spans
func processTranscript(traceID, path string, startLine int, parentSpanID string) []Span {
	entries, err := ReadTranscript(path, startLine)
	if err != nil || len(entries) == 0 {
		return nil
	}

	toolResults := BuildToolResults(entries)
	taskResults := BuildTaskResults(entries)
	parsed := ParseAssistantMessages(entries)

	var spans []Span
	for _, p := range parsed {
		spanID := toV7(p.UUID)

		var span Span
		span.ID = spanID
		span.TraceID = traceID
		span.StartTime = p.Timestamp
		span.EndTime = p.Timestamp
		span.ProjectName = config.Project
		if parentSpanID != "" {
			span.ParentSpanID = parentSpanID
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
			name := p.Content.Name
			if name == "" {
				name = "Tool"
			}
			toolID := p.Content.ID
			toolInput := p.Content.Input
			var toolOutput map[string]interface{}
			var usage map[string]int

			switch name {
			case "Edit":
				if config.Truncate {
					toolInput = map[string]interface{}{
						"file_path":  toolInput["file_path"],
						"old_string": truncateMsg,
						"new_string": truncateMsg,
					}
				}
				toolOutput = map[string]interface{}{"result": truncateMsg}

			case "Write":
				if config.Truncate {
					toolInput = map[string]interface{}{
						"file_path": toolInput["file_path"],
						"content":   truncateMsg,
					}
				}
				toolOutput = map[string]interface{}{"result": truncateMsg}

			case "Read":
				toolOutput = map[string]interface{}{"result": truncateMsg}

			case "Task":
				subType := "Task"
				if st, ok := toolInput["subagent_type"].(string); ok && st != "" {
					subType = st
				}
				name = subType + " Subagent"
				
				prompt := ""
				if pr, ok := toolInput["prompt"].(string); ok {
					prompt = pr
				}
				toolInput = map[string]interface{}{"prompt": prompt}

				if result, ok := taskResults[toolID]; ok && result != nil {
					resp := ""
					if len(result.Content) > 0 {
						resp = result.Content[0].Text
					}
					toolOutput = map[string]interface{}{"response": resp}
					if result.TotalTokens > 0 {
						usage = map[string]int{"total_tokens": result.TotalTokens}
					}
				} else {
					toolOutput = map[string]interface{}{}
				}

			default:
				if result, ok := toolResults[toolID]; ok {
					toolOutput = map[string]interface{}{"result": result}
				} else {
					toolOutput = map[string]interface{}{}
				}
			}

			span.Name = name
			span.Type = "tool"
			span.Input = toolInput
			span.Output = toolOutput
			span.Usage = usage

		default:
			continue
		}

		spans = append(spans, span)
	}

	return spans
}

// getLastOutput finds the last text output from the assistant in the transcript
func getLastOutput(state *State) string {
	entries, err := ReadTranscript(state.Transcript, state.StartLine)
	if err != nil {
		return ""
	}

	lastText := ""
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

// countLines counts the number of lines in a file
func countLines(path string) int {
	entries, err := ReadTranscript(path, 0)
	if err != nil {
		return 0
	}
	return len(entries)
}

// isoNow returns current time in ISO8601 format with milliseconds
func isoNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// debugLog writes to the debug log file if debugging is enabled
func debugLog(format string, args ...interface{}) {
	if config == nil || !config.Debug {
		return
	}

	f, err := os.OpenFile("/tmp/opik-debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	// Check file size and rotate if needed
	info, err := f.Stat()
	if err == nil && info.Size() > 1024*1024 { // 1MB
		// Simple rotation: truncate
		f.Truncate(0)
		f.Seek(0, 0)
	}

	ts := time.Now().Format("15:04:05")
	fmt.Fprintf(f, "[%s] %s\n", ts, fmt.Sprintf(format, args...))
}
