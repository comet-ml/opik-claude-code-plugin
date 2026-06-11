package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Shared fixtures + regressions that guard the billing block's attribution
// rules (the composition emitters they originally covered were consolidated
// into cc.billing — see billing.go).

func userPromptEntry(text string) TranscriptEntry {
	return TranscriptEntry{Type: "user", Message: &Message{Content: ContentSlice{
		{Type: "text", Text: text},
	}}}
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

func TestBillingNoUsageDoubleCountAcrossMultiBlockEntries(t *testing.T) {
	// The transcript repeats the same message.usage on EVERY entry of a
	// multi-block message. Regression for the 2x inflation found in the
	// OPIK-6873 audit: usage must be booked once per message.id.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_PROJECT_DIR", t.TempDir())

	usage := &Usage{InputTokens: 100, OutputTokens: 1000}
	entries := []TranscriptEntry{
		userPromptEntry("go"),
		{Type: "assistant", UUID: "u1", Message: &Message{ID: "m1", Usage: usage, Content: ContentSlice{
			{Type: "thinking", Thinking: "redacted"},
		}}},
		{Type: "assistant", UUID: "u1#1", Message: &Message{ID: "m1", Usage: usage, Content: ContentSlice{
			{Type: "text", Text: strings.Repeat("answer ", 50)},
		}}},
	}

	snap := computeBillingSnapshot(entries, entries)
	if got := snap["llm_calls"].(int); got != 1 {
		t.Errorf("llm_calls = %d, want 1 (two entries, one message)", got)
	}
	totals := snap["totals"].(map[string]interface{})
	if totals["output"].(int) != 1000 {
		t.Errorf("output total = %d, want 1000 booked once", totals["output"])
	}
	if totals["input"].(int) != 100 {
		t.Errorf("fresh total = %d, want 100 booked once", totals["input"])
	}
}

func TestBillingExcludesSkillBodiesFromUserPrompts(t *testing.T) {
	// A slash-loaded skill body is a user text block in the transcript; it
	// must be attributed to the skills lane, never to user_prompts.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_PROJECT_DIR", t.TempDir())

	body := "Base directory for this skill: /tmp/skills/opik\n\n" + strings.Repeat("skill body ", 300)
	bodyTok := tokEstimateAs(body, "skill_body")
	promptTok := tokEstimateAs("real question", "user_prompt")

	turn1 := []TranscriptEntry{
		userPromptEntry("<command-message>opik:opik</command-message>\n<command-name>/opik:opik</command-name>"),
		userPromptEntry(body),
	}
	entries := append(turn1, userPromptEntry("real question"))
	call := assistantCall(t, "m1", &Usage{InputTokens: 5_000, OutputTokens: 10},
		Content{Type: "text", Text: "ok"})
	entries = append(entries, call...)

	snap := computeBillingSnapshot(entries, entries)
	lanes := snap["lanes"].(map[string]interface{})

	skills := lanes["skills"].(map[string]interface{})
	if skills["total"].(int) < bodyTok {
		t.Errorf("skills total = %d, want >= body %d", skills["total"], bodyTok)
	}
	up := lanes["user_prompts"].(map[string]interface{})
	// user_prompts must hold only the preamble + real question, not the body.
	if up["total"].(int) >= bodyTok {
		t.Errorf("user_prompts total = %d — skill body leaked in (body=%d, prompt=%d)",
			up["total"], bodyTok, promptTok)
	}

	// And the load is counted once, on the skills item.
	for _, v := range skills["items"].([]map[string]interface{}) {
		if v["name"] == "opik:opik" && v["kind"] == kindUsage {
			if v["count"].(int) != 1 {
				t.Errorf("skill load count = %d, want 1", v["count"])
			}
			return
		}
	}
	t.Error("expected a skills usage item for opik:opik")
}

func TestRepeatSkillLoadsKeepDistinctEvents(t *testing.T) {
	slashLoad := func(name, body string) []TranscriptEntry {
		return []TranscriptEntry{
			userPromptEntry("<command-message>" + name + "</command-message>\n<command-name>/" + name + "</command-name>"),
			userPromptEntry("Base directory for this skill: /tmp/skills/x\n\n" + body),
		}
	}
	entries := append(slashLoad("opik:opik", "body one"), slashLoad("opik:opik", "body two")...)

	loads := buildLoadedSkillBodies(entries)
	if len(loads) != 2 {
		t.Fatalf("want 2 load events for 2 slash loads, got %d", len(loads))
	}
	if loads[0].ToolUseID == loads[1].ToolUseID {
		t.Errorf("load events must have distinct ids, both %q", loads[0].ToolUseID)
	}
	for _, l := range loads {
		if !strings.HasPrefix(l.ToolUseID, "slash:") {
			t.Errorf("slash load id = %q, want slash:<idx>", l.ToolUseID)
		}
	}
}
