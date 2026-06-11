package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Validation for OPIK-6873 mixed accounting: token sums are cumulative-to-
// date so SUM across a session's traces is billing-weighted (each item
// counted once per request it rides in), while counts are new-this-turn so
// the same SUM yields true item counts. The tests below simulate a session
// trace-by-trace: for turn t, fullEntries = entries[:end(t)] and
// turnEntries = entries[start(t):end(t)], exactly how postTraceMetrics
// slices the transcript.

func userPromptEntry(text string) TranscriptEntry {
	return TranscriptEntry{Type: "user", Message: &Message{Content: ContentSlice{
		{Type: "text", Text: text},
	}}}
}

func assistantEntry(msgID string, usage *Usage, blocks ...Content) TranscriptEntry {
	return TranscriptEntry{Type: "assistant", UUID: "uuid-" + msgID, Message: &Message{
		ID:      msgID,
		Usage:   usage,
		Content: ContentSlice(blocks),
	}}
}

func toolResultEntry(toolUseID, payload string) TranscriptEntry {
	return TranscriptEntry{Type: "user", Message: &Message{Content: ContentSlice{
		{Type: "tool_result", ToolUseID: toolUseID, Content: payload},
	}}}
}

func fileAttachmentEntry(path, content string) TranscriptEntry {
	raw, _ := json.Marshal(map[string]interface{}{
		"file": map[string]string{"path": path, "content": content},
	})
	return TranscriptEntry{Type: "attachment", Attachment: &Attachment{Type: "file", Content: raw}}
}

func summaryInt(snap map[string]interface{}, key string) int {
	return snap["summary"].(map[string]interface{})[key].(int)
}

func TestUserPromptsCumulativeTokensPerTurnCounts(t *testing.T) {
	shortPrompt := "hello there"
	longPrompt := strings.Repeat("paste of a very large blob of text ", 1200) // ~42k chars → xlarge

	turn1 := []TranscriptEntry{userPromptEntry(shortPrompt)}
	turn2 := []TranscriptEntry{userPromptEntry(longPrompt)}
	full := append(append([]TranscriptEntry{}, turn1...), turn2...)

	tokShort := tokEstimateAs(shortPrompt, "user_prompt")
	tokLong := tokEstimateAs(longPrompt, "user_prompt")

	snap1 := extractUserPromptsSnapshot(turn1, turn1)
	snap2 := extractUserPromptsSnapshot(full, turn2)

	// Per-trace: tokens cumulative, counts new-this-turn.
	if got := summaryInt(snap2, "total_tokens"); got != tokShort+tokLong {
		t.Errorf("turn2 total_tokens = %d, want cumulative %d", got, tokShort+tokLong)
	}
	if got := summaryInt(snap2, "count"); got != 1 {
		t.Errorf("turn2 count = %d, want 1 (new this turn)", got)
	}

	// Across traces: SUM(tokens) follows the replay formula
	// Σ size × (N − first_turn + 1); SUM(counts) is the true prompt count.
	sumTokens := summaryInt(snap1, "total_tokens") + summaryInt(snap2, "total_tokens")
	wantTokens := tokShort*2 + tokLong*1
	if sumTokens != wantTokens {
		t.Errorf("SUM(tokens) across traces = %d, want replay-weighted %d", sumTokens, wantTokens)
	}
	sumCounts := summaryInt(snap1, "count") + summaryInt(snap2, "count")
	if sumCounts != 2 {
		t.Errorf("SUM(counts) across traces = %d, want true count 2", sumCounts)
	}

	// by_size: per-prompt buckets, tokens cumulative, count new-this-turn.
	buckets := map[string]map[string]interface{}{}
	for _, row := range snap2["by_size"].([]map[string]interface{}) {
		buckets[row["bucket"].(string)] = row
	}
	if b := buckets["small"]; b == nil || b["tokens"].(int) != tokShort || b["count"].(int) != 0 {
		t.Errorf("small bucket = %+v, want tokens %d count 0", buckets["small"], tokShort)
	}
	if b := buckets["xlarge"]; b == nil || b["tokens"].(int) != tokLong || b["count"].(int) != 1 {
		t.Errorf("xlarge bucket = %+v, want tokens %d count 1", buckets["xlarge"], tokLong)
	}

	// Bucket tokens must partition the lane total — no double counting.
	bucketSum := 0
	for _, row := range snap2["by_size"].([]map[string]interface{}) {
		bucketSum += row["tokens"].(int)
	}
	if bucketSum != summaryInt(snap2, "total_tokens") {
		t.Errorf("Σ by_size tokens = %d != summary total %d", bucketSum, summaryInt(snap2, "total_tokens"))
	}
}

func TestToolResultsCumulativeTokensPerTurnCounts(t *testing.T) {
	payload := strings.Repeat("tool output line\n", 50)
	turn1 := []TranscriptEntry{
		assistantEntry("m1", &Usage{OutputTokens: 30},
			Content{Type: "tool_use", ID: "tu1", Name: "Bash", Input: map[string]interface{}{"command": "ls"}}),
		toolResultEntry("tu1", payload),
	}
	turn2 := []TranscriptEntry{userPromptEntry("follow-up question")}
	full := append(append([]TranscriptEntry{}, turn1...), turn2...)

	wantTokens := tokEstimateAs(payload, "tool_result")

	snap1 := extractToolResultsSnapshot(turn1, turn1)
	snap2 := extractToolResultsSnapshot(full, turn2)

	// Turn 2 had no tool activity: tokens persist (replayed), count is 0.
	if got := summaryInt(snap2, "total_tokens"); got != wantTokens {
		t.Errorf("turn2 total_tokens = %d, want cumulative %d", got, wantTokens)
	}
	if got := summaryInt(snap2, "count"); got != 0 {
		t.Errorf("turn2 count = %d, want 0", got)
	}

	// SUM(count) across traces = true number of calls.
	if sum := summaryInt(snap1, "count") + summaryInt(snap2, "count"); sum != 1 {
		t.Errorf("SUM(counts) = %d, want 1", sum)
	}

	// by_tool mirrors the split.
	rows := snap2["by_tool"].([]map[string]interface{})
	if len(rows) != 1 || rows[0]["name"] != "Bash" {
		t.Fatalf("by_tool = %+v, want single Bash row", rows)
	}
	if rows[0]["tokens"].(int) != wantTokens || rows[0]["count"].(int) != 0 {
		t.Errorf("Bash row = %+v, want tokens %d count 0", rows[0], wantTokens)
	}
}

func TestFileAttachmentsCumulativeTokensPerTurnCounts(t *testing.T) {
	content := strings.Repeat("col1,col2,col3\n", 100)
	turn1 := []TranscriptEntry{fileAttachmentEntry("/repo/data.csv", content)}
	turn2 := []TranscriptEntry{userPromptEntry("what does the data say?")}
	full := append(append([]TranscriptEntry{}, turn1...), turn2...)

	wantTokens := tokEstimate(content)

	snap1 := extractFileAttachmentsSnapshot(turn1, turn1)
	snap2 := extractFileAttachmentsSnapshot(full, turn2)

	if got := summaryInt(snap2, "total_tokens"); got != wantTokens {
		t.Errorf("turn2 total_tokens = %d, want cumulative %d", got, wantTokens)
	}
	if got := summaryInt(snap2, "file_count"); got != 0 {
		t.Errorf("turn2 file_count = %d, want 0", got)
	}
	if sum := summaryInt(snap1, "file_count") + summaryInt(snap2, "file_count"); sum != 1 {
		t.Errorf("SUM(file_count) = %d, want true count 1", sum)
	}

	rows := snap2["by_type"].([]map[string]interface{})
	if len(rows) != 1 || rows[0]["ext"] != ".csv" {
		t.Fatalf("by_type = %+v, want single .csv row", rows)
	}
	if rows[0]["tokens"].(int) != wantTokens || rows[0]["file_count"].(int) != 0 {
		t.Errorf(".csv row = %+v, want tokens %d file_count 0", rows[0], wantTokens)
	}
}

func TestPriorAssistantExcludesToolUseAndSplitsByContent(t *testing.T) {
	// One prior LLM call with all three block kinds sharing usage. The
	// tool_use share must NOT land in prior_assistant — it belongs to the
	// tool lanes; counting it here would double-attribute.
	usage := &Usage{OutputTokens: 200}
	prior := []TranscriptEntry{
		assistantEntry("m1", usage,
			Content{Type: "thinking", Thinking: "redacted"},
			Content{Type: "text", Text: strings.Repeat("visible answer ", 20)},
			Content{Type: "tool_use", ID: "tu1", Name: "Bash",
				Input: map[string]interface{}{"command": strings.Repeat("x", 200)}},
		),
	}
	turn := []TranscriptEntry{userPromptEntry("next question")}
	full := append(append([]TranscriptEntry{}, prior...), turn...)

	snap := extractPriorAssistantSnapshot(full, turn)
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	total := summaryInt(snap, "total_tokens")
	byContent := snap["by_content"].(map[string]interface{})
	text := byContent["assistant_text"].(int)
	thinking := byContent["thinking"].(int)

	// by_content partitions the lane total.
	if text+thinking != total {
		t.Errorf("by_content %d+%d != total %d", text, thinking, total)
	}
	if text == 0 || thinking == 0 {
		t.Errorf("expected both content kinds > 0, got text=%d thinking=%d", text, thinking)
	}

	// Conservation: text + thinking + tool_use share == the call's measured
	// output. The lane total must therefore be SMALLER than output_tokens
	// by exactly the tool_use share (no token in two lanes, none dropped).
	parsed := ParseAssistantMessages(prior)
	DeduplicateUsage(parsed)
	toolUseShare := 0
	for _, p := range parsed {
		if p.ContentType == "tool_use" {
			toolUseShare += p.AttributedOutputTokens
		}
	}
	if toolUseShare == 0 {
		t.Fatal("fixture broken: tool_use share should be > 0")
	}
	if total+toolUseShare != usage.OutputTokens {
		t.Errorf("total %d + tool_use share %d != output_tokens %d", total, toolUseShare, usage.OutputTokens)
	}
}

func TestUserPromptsExcludeSkillBodiesInCumulativePass(t *testing.T) {
	// A skill body loaded in turn 1 must stay excluded from the cumulative
	// token pass in every later trace — it's counted under cc.skills.loaded.
	body := "Base directory for this skill: /tmp/skills/opik\n\n" + strings.Repeat("skill body ", 300)
	turn1 := []TranscriptEntry{
		userPromptEntry("<command-message>opik:opik</command-message>\n<command-name>/opik:opik</command-name>"),
		userPromptEntry(body),
	}
	turn2 := []TranscriptEntry{userPromptEntry("real question")}
	full := append(append([]TranscriptEntry{}, turn1...), turn2...)

	snap := extractUserPromptsSnapshot(full, turn2)
	wantTokens := tokEstimateAs(turn1[0].Message.Content[0].Text, "user_prompt") +
		tokEstimateAs("real question", "user_prompt")
	if got := summaryInt(snap, "total_tokens"); got != wantTokens {
		t.Errorf("total_tokens = %d, want %d (skill body must not leak into user_prompts)", got, wantTokens)
	}
}

func TestRepeatSkillLoadsKeepDistinctEvents(t *testing.T) {
	slashLoad := func(name, body string) []TranscriptEntry {
		return []TranscriptEntry{
			userPromptEntry("<command-message>" + name + "</command-message>\n<command-name>/" + name + "</command-name>"),
			userPromptEntry("Base directory for this skill: /tmp/skills/x\n\n" + body),
		}
	}
	entries := append(slashLoad("opik:opik", "body one"), slashLoad("opik:opik", "body two")...)

	loaded := extractLoadedSkills(entries)
	if len(loaded) != 2 {
		t.Fatalf("want 2 load events for 2 slash loads, got %d", len(loaded))
	}
	if loaded[0].ToolUseID == loaded[1].ToolUseID {
		t.Errorf("load events must have distinct ids, both %q", loaded[0].ToolUseID)
	}
	for _, l := range loaded {
		if !strings.HasPrefix(l.ToolUseID, "slash:") {
			t.Errorf("slash load id = %q, want slash:<idx>", l.ToolUseID)
		}
	}

	// The snapshot's loaded_tokens must count both bodies — each injected
	// body replays from its load turn on.
	snap := BuildSkillsSnapshot(entries)
	sum := snap["summary"].(map[string]interface{})
	wantTokens := loaded[0].BodyTokens + loaded[1].BodyTokens
	if got := sum["loaded_tokens"].(int); got != wantTokens {
		t.Errorf("loaded_tokens = %d, want %d (both load events)", got, wantTokens)
	}
	if got := sum["loaded_count"].(int); got != 2 {
		t.Errorf("loaded_count = %d, want 2", got)
	}
}
