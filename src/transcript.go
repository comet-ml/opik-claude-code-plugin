package main

import (
	"bufio"
	"encoding/json"
	"os"
)

// TranscriptEntry represents a single entry in the transcript JSONL
type TranscriptEntry struct {
	Type          string         `json:"type"`
	UUID          string         `json:"uuid"`
	Timestamp     string         `json:"timestamp"`
	Slug          string         `json:"slug,omitempty"`
	Message       *Message       `json:"message,omitempty"`
	ToolUseResult *ToolUseResult `json:"toolUseResult,omitempty"`
}

// Message represents the message field in a transcript entry
type Message struct {
	Content []Content `json:"content"`
	Usage   *Usage    `json:"usage,omitempty"`
	Model   string    `json:"model,omitempty"`
}

// Content represents a content block in a message
type Content struct {
	Type      string                 `json:"type"`
	ID        string                 `json:"id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Text      string                 `json:"text,omitempty"`
	Thinking  string                 `json:"thinking,omitempty"`
	Input     map[string]interface{} `json:"input,omitempty"`
	ToolUseID string                 `json:"tool_use_id,omitempty"`
	Content   interface{}            `json:"content,omitempty"`
	IsError   bool                   `json:"is_error,omitempty"`
}

// Usage represents token usage
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// ToolUseResult represents task result data
type ToolUseResult struct {
	Content     []ResultContent `json:"content,omitempty"`
	TotalTokens int             `json:"totalTokens,omitempty"`
}

// ResultContent represents content in a tool use result
type ResultContent struct {
	Text string `json:"text,omitempty"`
}

// ParsedEntry holds parsed data for span generation
type ParsedEntry struct {
	UUID        string
	Timestamp   string
	ContentType string
	Content     Content
	Usage       *Usage
	Model       string
}

// ToolResultInfo holds tool result data including error info and timestamp
type ToolResultInfo struct {
	Result    string
	IsError   bool
	Timestamp string
}

// ReadTranscript reads and parses a transcript file from a given line offset
func ReadTranscript(path string, startLine int) ([]TranscriptEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []TranscriptEntry
	scanner := bufio.NewScanner(file)
	// Increase buffer size for large lines
	buf := make([]byte, 0, initialBufferSize)
	scanner.Buffer(buf, maxBufferSize)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum <= startLine {
			continue
		}

		var entry TranscriptEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}

	return entries, scanner.Err()
}

// BuildToolResults builds a map of tool_use_id -> ToolResultInfo from user messages
func BuildToolResults(entries []TranscriptEntry) map[string]*ToolResultInfo {
	results := make(map[string]*ToolResultInfo)

	for _, entry := range entries {
		if entry.Type != "user" || entry.Message == nil || len(entry.Message.Content) == 0 {
			continue
		}

		content := entry.Message.Content[0]
		if content.Type == "tool_result" && content.ToolUseID != "" {
			info := &ToolResultInfo{
				IsError:   content.IsError,
				Timestamp: entry.Timestamp,
			}
			if str, ok := content.Content.(string); ok {
				info.Result = str
			}
			results[content.ToolUseID] = info
		}
	}

	return results
}

// BuildTaskResults builds a map of tool_use_id -> ToolUseResult from user messages
func BuildTaskResults(entries []TranscriptEntry) map[string]*ToolUseResult {
	results := make(map[string]*ToolUseResult)

	for _, entry := range entries {
		if entry.Type != "user" || entry.ToolUseResult == nil {
			continue
		}
		if entry.Message != nil && len(entry.Message.Content) > 0 {
			results[entry.Message.Content[0].ToolUseID] = entry.ToolUseResult
		}
	}

	return results
}

// ParseAssistantMessages extracts parsed entries from assistant messages
func ParseAssistantMessages(entries []TranscriptEntry) []ParsedEntry {
	var parsed []ParsedEntry

	for _, entry := range entries {
		if entry.Type != "assistant" || entry.Message == nil || len(entry.Message.Content) == 0 {
			continue
		}

		content := entry.Message.Content[0]
		if content.Type == "" {
			continue
		}

		parsed = append(parsed, ParsedEntry{
			UUID:        entry.UUID,
			Timestamp:   entry.Timestamp,
			ContentType: content.Type,
			Content:     content,
			Usage:       entry.Message.Usage,
			Model:       entry.Message.Model,
		})
	}

	return parsed
}

// FindModel extracts the model name from transcript entries
func FindModel(entries []TranscriptEntry) string {
	for _, entry := range entries {
		if entry.Type == "assistant" && entry.Message != nil && entry.Message.Model != "" {
			return entry.Message.Model
		}
	}
	return ""
}
