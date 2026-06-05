package main

import (
	"bufio"
	"bytes"
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
	Attachment    *Attachment    `json:"attachment,omitempty"`
}

// Attachment covers the subset of `type:"attachment"` records we extract
// attribution from. Content is RawMessage because some shapes use a string
// (skill_listing) and others use an array (task_reminder).
//
// `deferred_tools_delta` carries the catalog mutations: addedNames /
// addedLines (parallel arrays — names[i] has description text lines[i]),
// removedNames (names dropped, e.g. when the user toggles an MCP off),
// readdedNames (names re-enabled after a prior removal), and
// pendingMcpServers (servers connecting; their tools not yet visible).
type Attachment struct {
	Type              string          `json:"type"`
	Content           json.RawMessage `json:"content,omitempty"`
	Names             []string        `json:"names,omitempty"`
	SkillCount        int             `json:"skillCount,omitempty"`
	IsInitial         bool            `json:"isInitial,omitempty"`
	AddedNames        []string        `json:"addedNames,omitempty"`
	AddedLines        []string        `json:"addedLines,omitempty"`
	RemovedNames      []string        `json:"removedNames,omitempty"`
	ReaddedNames      []string        `json:"readdedNames,omitempty"`
	PendingMcpServers []string        `json:"pendingMcpServers,omitempty"`
}

// ContentString decodes Content as a string. Returns "" if Content is missing
// or shaped as something other than a string (e.g. task_reminder uses []).
func (a *Attachment) ContentString() string {
	if a == nil || len(a.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(a.Content, &s); err != nil {
		return ""
	}
	return s
}

type Message struct {
	ID      string       `json:"id,omitempty"`
	Content ContentSlice `json:"content"`
	Usage   *Usage       `json:"usage,omitempty"`
	Model   string       `json:"model,omitempty"`
}

// ContentSlice accepts both shapes Claude Code uses:
//   - array: `[{"type":"text","text":"…"}, {"type":"tool_use", …}]`
//   - string: `"hello world"` — wrapped into a single text Content
type ContentSlice []Content

func (cs *ContentSlice) UnmarshalJSON(data []byte) error {
	t := bytes.TrimSpace(data)
	if len(t) == 0 || string(t) == "null" {
		*cs = nil
		return nil
	}
	if t[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*cs = ContentSlice{{Type: "text", Text: s}}
		return nil
	}
	var arr []Content
	if err := json.Unmarshal(data, &arr); err != nil {
		return err
	}
	*cs = ContentSlice(arr)
	return nil
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

// ToolUseResult is the top-level `toolUseResult` field on `type:"user"`
// entries. Two shapes in the wild:
//   - object form (used for Task/Agent tools and most built-ins):
//     `{"content":[{"text":"..."}], "totalTokens": 123}`
//   - string form (used for some MCP tools that return primitives):
//     `"{\"result\":\"[]\"}"`
//
// Without a custom unmarshaler, hitting the string form fails the whole
// entry — and we silently drop it from ReadTranscript, taking the
// tool_result content block with it. The custom unmarshaler tolerates
// both shapes; the string form leaves Content/TotalTokens empty (the
// content block under e.Message.Content[i] carries the same payload).
type ToolUseResult struct {
	Content     []ResultContent `json:"content,omitempty"`
	TotalTokens int             `json:"totalTokens,omitempty"`
}

func (r *ToolUseResult) UnmarshalJSON(data []byte) error {
	t := bytes.TrimSpace(data)
	if len(t) == 0 || string(t) == "null" {
		return nil
	}
	if t[0] == '"' {
		// String form — discard, the body is mirrored on the
		// e.Message.Content[i].Content field.
		return nil
	}
	type alias ToolUseResult
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*r = ToolUseResult(a)
	return nil
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
	BlockIndex  int // 0-based position of this block within its message_id group

	// MeasuredOutputTokens is the chars/4 estimate of this block's content
	// (0 for thinking, since the text is server-redacted).
	MeasuredOutputTokens int

	// AttributedOutputTokens is this block's share of the LLM call's
	// `message.usage.output_tokens`. For text and tool_use blocks it equals
	// MeasuredOutputTokens; for thinking blocks it's the leftover after
	// subtracting the measured non-thinking blocks (split across thinking
	// blocks if there are multiple).
	AttributedOutputTokens int
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
		// Assign block_index regardless of group size — even single-block
		// messages have block_index 0, which is correct.
		for pos, idx := range g.indices {
			parsed[idx].BlockIndex = pos
		}

		// Measure each block.
		var thinkingIdxs, nonThinkingIdxs []int
		measuredNonThinking := 0
		for _, idx := range g.indices {
			m := measureBlockOutput(parsed[idx])
			parsed[idx].MeasuredOutputTokens = m
			if parsed[idx].ContentType == "thinking" {
				thinkingIdxs = append(thinkingIdxs, idx)
			} else {
				nonThinkingIdxs = append(nonThinkingIdxs, idx)
				measuredNonThinking += m
			}
		}
		// Pick the message's total output_tokens — usage may be on any block
		// of the group; take the max non-zero we find (they should match).
		totalOutput := 0
		for _, idx := range g.indices {
			if u := parsed[idx].Usage; u != nil && u.OutputTokens > totalOutput {
				totalOutput = u.OutputTokens
			}
		}
		distributeAttribution(parsed, thinkingIdxs, nonThinkingIdxs, measuredNonThinking, totalOutput)

		// Dedup the legacy `Usage` so Opik's span.usage still represents the
		// whole-LLM-call total on a single span (the anchor), preserving
		// existing analytics. Per-block attribution lives in cc.llm_call.
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

// measureBlockOutput returns a chars/4 estimate of this block's contribution
// to the LLM call's output. Thinking content is server-redacted, so we
// return 0; the attribution pass back-fills thinking blocks from the
// leftover after subtracting all measured non-thinking blocks.
func measureBlockOutput(p ParsedEntry) int {
	switch p.ContentType {
	case "text":
		return tokEstimateAs(p.Content.Text, "assistant_text")
	case "tool_use":
		raw, _ := json.Marshal(p.Content.Input)
		return tokEstimateAs(string(raw), "tool_use_input")
	default:
		return 0
	}
}

// distributeAttribution assigns each block its share of an LLM call's
// `output_tokens`. Goal: sum(attributed) == totalOutput exactly when
// totalOutput > 0. Algorithm:
//   - no totalOutput available → passthrough measured (best we can do)
//   - has thinking + measured ≤ total → leftover goes to thinking blocks
//   - has thinking + measured  > total → clamp thinking to 0, scale non-thinking down
//   - no thinking → scale non-thinking proportionally so sum == total
func distributeAttribution(parsed []ParsedEntry, thinkingIdxs, nonThinkingIdxs []int, measuredNonThinking, totalOutput int) {
	// Passthrough: no usage data yet — just use measured values.
	if totalOutput == 0 {
		for _, idx := range nonThinkingIdxs {
			parsed[idx].AttributedOutputTokens = parsed[idx].MeasuredOutputTokens
		}
		return
	}
	// No thinking blocks — non-thinking blocks must sum to total.
	if len(thinkingIdxs) == 0 {
		scaleAttribution(parsed, nonThinkingIdxs, measuredNonThinking, totalOutput)
		return
	}
	leftover := totalOutput - measuredNonThinking
	if leftover < 0 {
		// Measured overshot total. Thinking gets 0; scale non-thinking down.
		for _, idx := range thinkingIdxs {
			parsed[idx].AttributedOutputTokens = 0
		}
		scaleAttribution(parsed, nonThinkingIdxs, measuredNonThinking, totalOutput)
		return
	}
	// Leftover ≥ 0: non-thinking get measured, thinking blocks split leftover.
	for _, idx := range nonThinkingIdxs {
		parsed[idx].AttributedOutputTokens = parsed[idx].MeasuredOutputTokens
	}
	if len(thinkingIdxs) == 1 {
		parsed[thinkingIdxs[0]].AttributedOutputTokens = leftover
		return
	}
	per := leftover / len(thinkingIdxs)
	for i, idx := range thinkingIdxs {
		v := per
		if i == len(thinkingIdxs)-1 {
			v = leftover - per*(len(thinkingIdxs)-1) // last block gets the remainder
		}
		parsed[idx].AttributedOutputTokens = v
	}
}

// scaleAttribution proportionally distributes `target` tokens across the
// listed indices using their MeasuredOutputTokens as weights. Last block
// absorbs the rounding remainder so the sum equals `target` exactly.
func scaleAttribution(parsed []ParsedEntry, idxs []int, measuredSum, target int) {
	if len(idxs) == 0 {
		return
	}
	if measuredSum == 0 {
		// Degenerate: no measured weight — divide equally.
		per := target / len(idxs)
		for i, idx := range idxs {
			v := per
			if i == len(idxs)-1 {
				v = target - per*(len(idxs)-1)
			}
			parsed[idx].AttributedOutputTokens = v
		}
		return
	}
	used := 0
	for i, idx := range idxs {
		var v int
		if i == len(idxs)-1 {
			v = target - used
		} else {
			v = int(float64(parsed[idx].MeasuredOutputTokens) * float64(target) / float64(measuredSum))
			used += v
		}
		parsed[idx].AttributedOutputTokens = v
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
