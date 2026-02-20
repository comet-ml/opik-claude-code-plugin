package main

import (
	"bufio"
	"encoding/json"
	"os"
)

type TranscriptEntry struct {
	Type          string         `json:"type"`
	UUID          string         `json:"uuid"`
	Timestamp     string         `json:"timestamp"`
	Slug          string         `json:"slug,omitempty"`
	Message       *Message       `json:"message,omitempty"`
	ToolUseResult *ToolUseResult `json:"toolUseResult,omitempty"`
}

type Message struct {
	ID      string    `json:"id,omitempty"`
	Content []Content `json:"content"`
	Usage   *Usage    `json:"usage,omitempty"`
	Model   string    `json:"model,omitempty"`
}

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

type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type ToolUseResult struct {
	Content     []ResultContent `json:"content,omitempty"`
	TotalTokens int             `json:"totalTokens,omitempty"`
}

type ResultContent struct {
	Text string `json:"text,omitempty"`
}

type ParsedEntry struct {
	UUID        string
	Timestamp   string
	ContentType string
	Content     Content
	Usage       *Usage
	Model       string
	MessageID   string
}

type ToolResultInfo struct {
	Result    string
	IsError   bool
	Timestamp string
}

func ReadTranscript(path string, startLine int) ([]TranscriptEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []TranscriptEntry
	scanner := bufio.NewScanner(file)
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
			MessageID:   entry.Message.ID,
		})
	}

	return parsed
}

func DeduplicateUsage(parsed []ParsedEntry) {
	type group struct {
		indices []int
	}
	groups := make(map[string]*group)
	var order []string

	for i := range parsed {
		mid := parsed[i].MessageID
		if mid == "" {
			continue
		}
		g, exists := groups[mid]
		if !exists {
			g = &group{}
			groups[mid] = g
			order = append(order, mid)
		}
		g.indices = append(g.indices, i)
	}

	for _, mid := range order {
		g := groups[mid]
		if len(g.indices) < 2 {
			continue
		}
		lastIdx := g.indices[len(g.indices)-1]
		finalUsage := parsed[lastIdx].Usage

		parsed[g.indices[0]].Usage = finalUsage
		for _, idx := range g.indices[1:] {
			parsed[idx].Usage = nil
		}
	}
}

func FindModel(entries []TranscriptEntry) string {
	for _, entry := range entries {
		if entry.Type == "assistant" && entry.Message != nil && entry.Message.Model != "" {
			return entry.Message.Model
		}
	}
	return ""
}
