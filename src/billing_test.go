package main

import (
	"strings"
	"testing"
)

// Exactness contract: per call (and therefore per trace), the lane
// columns — including the explicit `unattributed` row — sum EXACTLY to the
// API-reported usage: input_tokens, cache_read, cache_creation, output.

func billingColumnSums(snap map[string]interface{}) (read, write, fresh, output, rows int) {
	for _, v := range snap["lanes"].(map[string]interface{}) {
		row := v.(map[string]interface{})
		read += row["cache_read"].(int)
		write += row["cache_creation"].(int)
		fresh += row["input"].(int)
		output += row["output"].(int)
		rows++
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
		totals["input"].(int) != wantFresh || totals["output"].(int) != wantOut {
		t.Errorf("totals = %v, want read=%d write=%d fresh=%d output=%d",
			totals, wantRead, wantWrite, wantFresh, wantOut)
	}

	// THE invariant: lane columns (incl. unattributed) sum to usage,
	// allowing ±1 per lane row for integer rounding of float cuts.
	read, write, fresh, output, rows := billingColumnSums(snap)
	closeEnough := func(got, want int) bool {
		d := got - want
		if d < 0 {
			d = -d
		}
		return d <= rows
	}
	if !closeEnough(read, wantRead) || !closeEnough(write, wantWrite) ||
		!closeEnough(fresh, wantFresh) || !closeEnough(output, wantOut) {
		t.Errorf("Σ lanes = read %d / write %d / fresh %d / output %d, want %d/%d/%d/%d (±%d rounding)",
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
	for lane, v := range snap["lanes"].(map[string]interface{}) {
		lanes[lane] = v.(map[string]interface{})
	}

	up := lanes["user_prompts"]
	if up == nil {
		t.Fatalf("no user_prompts lane: %v", lanes)
	}
	// Fresh: prompt 1 billed cold on call 1, prompt 2 billed in call 2's tail.
	wantFresh := pTok + tokEstimateAs("and?", "user_prompt")
	if up["input"].(int) != wantFresh {
		t.Errorf("user_prompts fresh = %d, want %d", up["input"], wantFresh)
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
	row, ok := snap["lanes"].(map[string]interface{})[unattributedLane].(map[string]interface{})
	if !ok {
		t.Fatal("expected an unattributed lane entry")
	}
	if row["input"].(int) != 5_000 {
		t.Errorf("unattributed fresh = %d, want 5000", row["input"])
	}
	if row["total"].(int) != 5_000 {
		t.Errorf("unattributed total = %d, want 5000", row["total"])
	}
}

func TestStaticOverheadItemization(t *testing.T) {
	resetTokenCache(t)

	// Unknown components: env carve-out + core remainder, conserving the
	// table's system_prompt total.
	ccBuiltinByVersion["8.8.8"] = ccBuiltinConstants{SystemPromptTokens: 4000, SystemToolsTokens: 900}
	defer delete(ccBuiltinByVersion, "8.8.8")

	entries := []TranscriptEntry{userPromptEntry("hi")}
	call := assistantCall(t, "m1", &Usage{InputTokens: 10_000, OutputTokens: 5},
		Content{Type: "text", Text: "yo"})
	entries = append(entries, call...)
	entries[1].Version = "8.8.8"

	snap := computeBillingSnapshot(entries, entries)
	so := snap["lanes"].(map[string]interface{})["static_overhead"].(map[string]interface{})
	byName := map[string]int{}
	for _, it := range so["items"].([]map[string]interface{}) {
		byName[it["name"].(string)] = it["total"].(int)
	}
	if byName["builtin_tool_schemas"] == 0 {
		t.Errorf("missing builtin_tool_schemas item: %v", byName)
	}
	// env + core must conserve the prompt constant (env may be 0 in a
	// temp-dir cwd, in which case core carries it all).
	if got := byName["environment"] + byName["core_prompt"]; got != 4000 {
		t.Errorf("environment+core_prompt = %d, want 4000", got)
	}

	// With a calibrated Components table, items follow it instead.
	ccBuiltinByVersion["8.8.8"] = ccBuiltinConstants{
		SystemPromptTokens: 4000, SystemToolsTokens: 900,
		Components: map[string]int{
			"identity_and_rules": 2100, "memory_instructions": 800,
			"environment_template": 200, "session_guidance": 900,
			"builtin_tool_schemas": 900,
		},
	}
	snap = computeBillingSnapshot(entries, entries)
	so = snap["lanes"].(map[string]interface{})["static_overhead"].(map[string]interface{})
	if so["total"].(int) != 4900 {
		t.Errorf("components total = %d, want 4900", so["total"])
	}
	if len(so["items"].([]map[string]interface{})) != 5 {
		t.Errorf("want 5 component items, got %v", so["items"])
	}
}

func TestDeferredCatalogSplitsBuiltinFromMcp(t *testing.T) {
	resetTokenCache(t)

	delta := TranscriptEntry{Type: "attachment", Attachment: &Attachment{
		Type:       "deferred_tools_delta",
		AddedNames: []string{"WebSearch", "mcp__slack__send", "Monitor"},
		AddedLines: []string{
			"WebSearch: search the web for things and stuff",
			"mcp__slack__send: send a slack message to a channel",
			"Monitor: watch a long-running script for events",
		},
	}}
	entries := []TranscriptEntry{userPromptEntry("hi"), delta}
	entries = append(entries, assistantCall(t, "m1", &Usage{InputTokens: 5_000, OutputTokens: 5},
		Content{Type: "text", Text: "ok"})...)

	snap := computeBillingSnapshot(entries, entries)
	lanes := snap["lanes"].(map[string]interface{})
	soItems := lanes["static_overhead"].(map[string]interface{})["items"].([]map[string]interface{})
	foundBuiltin := false
	for _, it := range soItems {
		if it["name"] == "deferred_tool_names" && it["total"].(int) > 0 {
			foundBuiltin = true
		}
	}
	if !foundBuiltin {
		t.Errorf("expected deferred_tool_names under static_overhead: %v", soItems)
	}
	mcp, ok := lanes["mcp_servers"].(map[string]interface{})
	if !ok {
		t.Fatal("expected mcp_servers lane")
	}
	foundMcp := false
	for _, it := range mcp["items"].([]map[string]interface{}) {
		if it["name"] == "catalog_deltas" && it["total"].(int) > 0 {
			foundMcp = true
		}
	}
	if !foundMcp {
		t.Errorf("expected catalog_deltas under mcp_servers: %v", mcp["items"])
	}
}

// After /compact the request contains only the summary + post-compact
// entries. The replay must truncate at the boundary: pre-compact content
// must not be laid out, and the summary lands in prior_assistant.
func TestBillingCompactBoundaryTruncatesReplay(t *testing.T) {
	preCompact := []TranscriptEntry{userPromptEntry("the original ask")}
	// A huge pre-compact call: its blocks are usage-derived (exact) replay
	// pieces. Before the fix these overflowed the next call's cut into the
	// fresh-input tier.
	preCompact = append(preCompact, assistantCall(t, "m1",
		&Usage{InputTokens: 500, CacheCreationInputTokens: 10_000, OutputTokens: 80_000},
		Content{Type: "thinking", Thinking: "redacted"},
		Content{Type: "text", Text: strings.Repeat("big ", 50)},
	)...)

	boundary := TranscriptEntry{Type: "system", Subtype: "compact_boundary"}
	summary := TranscriptEntry{Type: "user", IsCompactSummary: true,
		Message: &Message{Content: ContentSlice{
			{Type: "text", Text: strings.Repeat("summary of prior work ", 100)},
		}}}

	u2 := &Usage{InputTokens: 60, CacheReadInputTokens: 9_000,
		CacheCreationInputTokens: 400, OutputTokens: 30}
	post := assistantCall(t, "m2", u2, Content{Type: "text", Text: "continuing"})

	entries := append(append(preCompact, boundary, summary), post...)
	turn := entries[len(entries)-len(post):]

	snap := computeBillingSnapshot(entries, turn)
	if snap == nil {
		t.Fatal("expected billing snapshot")
	}

	read, write, fresh, output, rows := billingColumnSums(snap)
	closeEnough := func(got, want int) bool {
		d := got - want
		if d < 0 {
			d = -d
		}
		return d <= rows
	}
	if !closeEnough(read, u2.CacheReadInputTokens) || !closeEnough(write, u2.CacheCreationInputTokens) ||
		!closeEnough(fresh, u2.InputTokens) || !closeEnough(output, u2.OutputTokens) {
		t.Errorf("Σ lanes = read %d / write %d / fresh %d / output %d, want %d/%d/%d/%d (±%d)",
			read, write, fresh, output, u2.CacheReadInputTokens, u2.CacheCreationInputTokens,
			u2.InputTokens, u2.OutputTokens, rows)
	}

	lanes := snap["lanes"].(map[string]interface{})
	pa, ok := lanes["prior_assistant"].(map[string]interface{})
	if !ok {
		t.Fatal("expected prior_assistant lane (compact summary)")
	}
	foundSummary := false
	for _, it := range pa["items"].([]map[string]interface{}) {
		if it["name"] == "compact_summary" && it["total"].(int) > 0 {
			foundSummary = true
		}
		if it["name"] == "thinking" && it["total"].(int) > 0 {
			t.Errorf("pre-compact thinking leaked into the replay: %v", it)
		}
	}
	if !foundSummary {
		t.Errorf("expected compact_summary item in prior_assistant: %v", pa["items"])
	}
	if up, ok := lanes["user_prompts"].(map[string]interface{}); ok {
		if up["total"].(int) > 0 {
			t.Errorf("pre-compact user prompt leaked into the replay: %v", up)
		}
	}
}

// cc.billing.reconciliation reports Σ lanes minus usage per tier column —
// the monitorable health flag. Healthy attribution reconciles to zero;
// when usage-derived pieces exceed the billed prompt (a truncation we
// don't detect), consistent flips false and the input delta is positive.
func TestBillingReconciliationFlag(t *testing.T) {
	u1 := &Usage{InputTokens: 900, CacheCreationInputTokens: 30_000, OutputTokens: 250}
	entries := []TranscriptEntry{userPromptEntry("please do the thing")}
	entries = append(entries, assistantCall(t, "m1", u1,
		Content{Type: "text", Text: strings.Repeat("plan ", 40)})...)

	snap := computeBillingSnapshot(entries, entries)
	recon := snap["reconciliation"].(map[string]interface{})
	if !recon["consistent"].(bool) {
		t.Errorf("healthy turn must reconcile, got %v", recon)
	}

	// Now an undetected truncation: a 50k-output call (thinking carries the
	// usage-derived mass) replayed against a tiny billed prompt.
	big := &Usage{InputTokens: 900, CacheCreationInputTokens: 30_000, OutputTokens: 50_000}
	entries = []TranscriptEntry{userPromptEntry("please do the thing")}
	entries = append(entries, assistantCall(t, "m1", big,
		Content{Type: "thinking", Thinking: "redacted"},
		Content{Type: "text", Text: "done"})...)
	u2 := &Usage{InputTokens: 40, CacheReadInputTokens: 1_000, OutputTokens: 10}
	entries = append(entries, assistantCall(t, "m2", u2, Content{Type: "text", Text: "ok"})...)

	snap = computeBillingSnapshot(entries, entries)
	recon = snap["reconciliation"].(map[string]interface{})
	if recon["consistent"].(bool) {
		t.Fatalf("exact overshoot must flip consistent=false, got %v", recon)
	}
	if recon["input_delta"].(int) <= 0 {
		t.Errorf("overshoot must surface as positive input_delta, got %v", recon)
	}
}
