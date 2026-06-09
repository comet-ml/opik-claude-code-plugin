package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuildContextSnapshotShape exercises the categories we can compute
// from a minimal synthetic transcript. We don't pin exact tokens — those
// move whenever calibration ratios get tuned — but we DO assert that the
// expected categories show up and the total is the sum of the parts.
func TestBuildContextSnapshotShape(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_PROJECT_DIR", cwd)

	// One memory file so cc.memory has a non-zero total.
	writeFile(t, filepath.Join(cwd, ".claude", "rules", "demo.md"),
		"# Demo Rule\n- be helpful\n- be specific\n")

	// One project agent.
	writeFile(t, filepath.Join(cwd, ".claude", "agents", "demo.md"), `---
name: demo
description: A demo agent for testing the snapshot extractor.
---
body
`)

	// Synthetic transcript with a skill_listing attachment + a version
	// stamp so cc_builtin and skills both fire, plus user + assistant
	// content so the cumulative messages category is non-zero.
	transcript := filepath.Join(cwd, "transcript.jsonl")
	writeFile(t, transcript,
		`{"type":"user","version":"2.1.150","message":{"role":"user","content":"hi there, give me a moderately long prompt to test cumulative messages tally"}}
{"type":"assistant","version":"2.1.150","message":{"role":"assistant","content":[{"type":"text","text":"Sure here is a response"}],"usage":{"input_tokens":0,"output_tokens":42,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}
{"type":"attachment","attachment":{"type":"skill_listing","content":"- demo-skill: short description here","skillCount":1,"names":["demo-skill"],"isInitial":true}}
`)

	state := &State{Transcript: transcript}
	snap := buildContextSnapshot(state)
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	cats, _ := snap["categories"].(map[string]int)
	if cats == nil {
		t.Fatalf("expected categories map, got %v", snap)
	}

	wantPresent := []string{"system_prompt", "system_tools", "system_tools_deferred",
		"memory_files", "custom_agents", "skills_menu", "messages"}
	for _, k := range wantPresent {
		if cats[k] == 0 {
			t.Errorf("category %s missing or zero: %v", k, cats)
		}
	}

	// total_tokens must equal the always-on sum (excludes deferred
	// categories). deferred_tokens carries the rest. Together they
	// must equal the sum of every category — guards against an
	// off-by-one or silently lost category.
	wantAll, wantDeferred := 0, 0
	deferredKeys := map[string]bool{
		"system_tools_deferred": true,
		"mcp_tools_deferred":    true,
	}
	for k, v := range cats {
		wantAll += v
		if deferredKeys[k] {
			wantDeferred += v
		}
	}
	wantAlwaysOn := wantAll - wantDeferred
	if got := snap["total_tokens"].(int); got != wantAlwaysOn {
		t.Errorf("total_tokens = %d, want always-on sum %d (excludes deferred)", got, wantAlwaysOn)
	}
	if got := snap["deferred_tokens"].(int); got != wantDeferred {
		t.Errorf("deferred_tokens = %d, want %d", got, wantDeferred)
	}

	if src := snap["source"]; src != "estimated_sync" {
		t.Errorf("source = %v, want estimated_sync (distinguishes from context_runtime)", src)
	}
}

func TestBuildContextSnapshotMissingTranscript(t *testing.T) {
	state := &State{Transcript: "/this/path/does/not/exist"}
	if snap := buildContextSnapshot(state); snap != nil {
		t.Errorf("expected nil snapshot for missing transcript, got %+v", snap)
	}
}

func TestCumulativeMessagesTokens(t *testing.T) {
	// Three layers we want to add up:
	//   1. user text  — chars/4.3 estimate (user_prompt ratio)
	//   2. tool_result — chars/3.0 estimate (tool_result ratio)
	//   3. assistant output — usage.output_tokens (exact, not estimated)
	// Loaded skill bodies should be EXCLUDED because they live in
	// skills_loaded; double-counting them would inflate messages by 1k+.
	entries := []TranscriptEntry{
		// User text — counted
		{Type: "user", Message: &Message{Content: ContentSlice{
			{Type: "text", Text: "what color is grass and why"},
		}}},
		// Assistant — output_tokens counted (the 42 below) regardless of
		// the text we estimate; exactness comes from the API.
		{Type: "assistant", Message: &Message{
			Content: ContentSlice{{Type: "text", Text: "green, chlorophyll"}},
			Usage:   &Usage{OutputTokens: 42},
		}},
		// Tool result — counted
		{Type: "user", Message: &Message{Content: ContentSlice{
			{Type: "tool_result", Content: "the result text"},
		}}},
		// Slash-loaded skill preamble + body — body must NOT count here
		// (it's in skills_loaded).
		{Type: "user", Message: &Message{Content: ContentSlice{
			{Type: "text", Text: "<command-name>/test:skill</command-name>"},
		}}},
		{Type: "user", Message: &Message{Content: ContentSlice{
			{Type: "text", Text: "Base directory for this skill: /tmp/x\n\nthis large body is excluded"},
		}}},
	}
	total := cumulativeMessagesTokens(entries)
	if total < 40 {
		t.Errorf("messages total = %d, expected at least the 42 assistant output_tokens", total)
	}
	// Confirm the skill body's "this large body is excluded" text isn't
	// included — a heuristic: the body text length / 4.3 is ~7 tokens; if
	// total was that much larger we'd know it leaked through. Use the
	// shape sanity check: total should be close to (asst 42 + small user
	// + small tool_result + small command-name); upper bound ~80.
	if total > 80 {
		t.Errorf("messages total = %d, suspect the skill body leaked in (should be ~50-70)", total)
	}
}

func TestBuildContextSnapshotNoCategories(t *testing.T) {
	// HOME and cwd point at empty tmp dirs — extractors return nil — no
	// version stamp so cc_builtin also returns nil. Result: nil snapshot.
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_PROJECT_DIR", cwd)

	transcript := filepath.Join(cwd, "empty.jsonl")
	if err := os.WriteFile(transcript, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	state := &State{Transcript: transcript}
	if snap := buildContextSnapshot(state); snap != nil {
		t.Errorf("expected nil snapshot when extractors all return nil, got %+v", snap)
	}
}
