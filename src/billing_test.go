package main

import (
	"strings"
	"testing"
)

// Exactness contract: per call (and therefore per trace), the by_lane
// columns — including the explicit `unattributed` row — sum EXACTLY to the
// API-reported usage: input_tokens, cache_read, cache_creation, output.

func billingColumnSums(snap map[string]interface{}) (read, write, fresh, output int) {
	for _, row := range snap["by_lane"].([]map[string]interface{}) {
		read += row["cache_read"].(int)
		write += row["cache_creation"].(int)
		fresh += row["fresh"].(int)
		output += row["output"].(int)
	}
	return
}

func assistantCall(t *testing.T, id string, usage *Usage, blocks ...Content) []TranscriptEntry {
	t.Helper()
	// One entry per block, all sharing message.id and usage — the real
	// transcript shape.
	entries := make([]TranscriptEntry, 0, len(blocks))
	for i, b := range blocks {
		uuid := "u-" + id
		if i > 0 {
			uuid += "#"
		}
		entries = append(entries, TranscriptEntry{
			Type: "assistant", UUID: uuid,
			Message: &Message{ID: id, Usage: usage, Content: ContentSlice{b}},
		})
	}
	return entries
}

func TestBillingSumsExactlyToUsage(t *testing.T) {
	// Realistic mess: estimates won't match usage (unattributed absorbs),
	// multi-block calls, tool results, two calls with growing cache.
	u1 := &Usage{InputTokens: 900, CacheCreationInputTokens: 30_000, OutputTokens: 250}
	u2 := &Usage{InputTokens: 40, CacheReadInputTokens: 31_150,
		CacheCreationInputTokens: 5_000, OutputTokens: 700}

	entries := []TranscriptEntry{userPromptEntry("please do the thing")}
	entries = append(entries, assistantCall(t, "m1", u1,
		Content{Type: "thinking", Thinking: "redacted"},
		Content{Type: "text", Text: strings.Repeat("plan ", 40)},
		Content{Type: "tool_use", ID: "tu1", Name: "Read",
			Input: map[string]interface{}{"file_path": "/x"}},
	)...)
	entries = append(entries, toolResultEntry("tu1", strings.Repeat("file content\n", 200)))
	entries = append(entries, assistantCall(t, "m2", u2,
		Content{Type: "text", Text: strings.Repeat("answer ", 60)},
	)...)

	snap := computeBillingSnapshot(entries, entries)
	if snap == nil {
		t.Fatal("expected billing snapshot")
	}
	if got := snap["llm_calls"].(int); got != 2 {
		t.Fatalf("llm_calls = %d, want 2", got)
	}

	wantRead := u1.CacheReadInputTokens + u2.CacheReadInputTokens
	wantWrite := u1.CacheCreationInputTokens + u2.CacheCreationInputTokens
	wantFresh := u1.InputTokens + u2.InputTokens
	wantOut := u1.OutputTokens + u2.OutputTokens

	totals := snap["totals"].(map[string]interface{})
	if totals["cache_read"].(int) != wantRead || totals["cache_creation"].(int) != wantWrite ||
		totals["fresh"].(int) != wantFresh || totals["output"].(int) != wantOut {
		t.Errorf("totals = %v, want read=%d write=%d fresh=%d output=%d",
			totals, wantRead, wantWrite, wantFresh, wantOut)
	}

	// THE invariant: by_lane columns (incl. unattributed) sum to usage,
	// allowing ±1 per lane row for integer rounding of float cuts.
	read, write, fresh, output := billingColumnSums(snap)
	rows := len(snap["by_lane"].([]map[string]interface{}))
	closeEnough := func(got, want int) bool {
		d := got - want
		if d < 0 {
			d = -d
		}
		return d <= rows
	}
	if !closeEnough(read, wantRead) || !closeEnough(write, wantWrite) ||
		!closeEnough(fresh, wantFresh) || !closeEnough(output, wantOut) {
		t.Errorf("Σ by_lane = read %d / write %d / fresh %d / output %d, want %d/%d/%d/%d (±%d rounding)",
			read, write, fresh, output, wantRead, wantWrite, wantFresh, wantOut, rows)
	}
}

func TestBillingPositionalCutMovesContentToCacheRead(t *testing.T) {
	// Deterministic layout: no HOME/cwd config, no CC version → the only
	// pieces are the conversation. Call 1 bills the prompt as fresh input;
	// call 2 re-reads it (plus call 1's output) from cache.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_PROJECT_DIR", t.TempDir())

	prompt := "please do the thing"
	pTok := tokEstimateAs(prompt, "user_prompt")
	out1 := 30

	u1 := &Usage{InputTokens: pTok, OutputTokens: out1} // cold: all fresh
	u2 := &Usage{CacheReadInputTokens: pTok + out1, InputTokens: 5, OutputTokens: 10}

	entries := []TranscriptEntry{userPromptEntry(prompt)}
	entries = append(entries, assistantCall(t, "m1", u1,
		Content{Type: "text", Text: strings.Repeat("ok ", 15)})...)
	entries = append(entries, userPromptEntry("and?"))
	entries = append(entries, assistantCall(t, "m2", u2,
		Content{Type: "text", Text: "done"})...)

	snap := computeBillingSnapshot(entries, entries)
	lanes := map[string]map[string]interface{}{}
	for _, row := range snap["by_lane"].([]map[string]interface{}) {
		lanes[row["lane"].(string)] = row
	}

	up := lanes["user_prompts"]
	if up == nil {
		t.Fatalf("no user_prompts lane: %v", lanes)
	}
	// Fresh: prompt 1 billed cold on call 1, prompt 2 billed in call 2's tail.
	wantFresh := pTok + tokEstimateAs("and?", "user_prompt")
	if up["fresh"].(int) != wantFresh {
		t.Errorf("user_prompts fresh = %d, want %d", up["fresh"], wantFresh)
	}
	if up["cache_read"].(int) != pTok {
		t.Errorf("user_prompts cache_read = %d, want %d (call 2 replay)", up["cache_read"], pTok)
	}
	// Call 1's output replays inside call 2's cached prefix.
	pa := lanes["prior_assistant"]
	if pa == nil || pa["cache_read"].(int) != out1 {
		t.Errorf("prior_assistant cache_read = %v, want %d", pa, out1)
	}
	// Output side: booked once per call, exact.
	if o := lanes["output"]; o == nil || o["output"].(int) != out1+10 {
		t.Errorf("output lane = %v, want output %d", o, out1+10)
	}
}

func TestBillingUnattributedAbsorbsUnknownMass(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_PROJECT_DIR", t.TempDir())

	prompt := "hi"
	pTok := tokEstimateAs(prompt, "user_prompt")
	// Usage says far more input than we can see — e.g. system reminders.
	u := &Usage{InputTokens: pTok + 5_000, OutputTokens: 5}

	entries := []TranscriptEntry{userPromptEntry(prompt)}
	entries = append(entries, assistantCall(t, "m1", u, Content{Type: "text", Text: "yo"})...)

	snap := computeBillingSnapshot(entries, entries)
	for _, row := range snap["by_lane"].([]map[string]interface{}) {
		if row["lane"] == unattributedLane {
			if row["fresh"].(int) != 5_000 {
				t.Errorf("unattributed fresh = %d, want 5000", row["fresh"])
			}
			return
		}
	}
	t.Fatal("expected an unattributed lane row")
}
