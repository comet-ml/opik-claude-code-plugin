package main

import (
	"encoding/json"
	"testing"
)

func TestExtractAttributionEmptyAndNil(t *testing.T) {
	if got := ExtractAttribution(nil); got == nil || len(got.Skills) != 0 {
		t.Errorf("nil entries: want empty Attribution, got %+v", got)
	}
	if got := ExtractAttribution([]TranscriptEntry{}); got == nil || len(got.Skills) != 0 {
		t.Errorf("zero entries: want empty Attribution, got %+v", got)
	}
}

func TestExtractSkillEventsAgainstSampleFixture(t *testing.T) {
	entries, err := ReadTranscript("../test/sample-transcript.jsonl", 0)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("fixture empty")
	}
	skills := extractSkillEvents(entries)
	if len(skills) != 0 {
		t.Errorf("sample fixture has no skill_listing; want 0 events, got %d", len(skills))
	}
}

func TestExtractSkillEventsHandlesInlineAttachment(t *testing.T) {
	// Minimal handcrafted entry to confirm attachment parsing wiring.
	// Skills with no on-disk SKILL.md resolve as source=bundled with empty sha.
	raw := `{"type":"attachment","uuid":"a-1","timestamp":"2026-01-01T00:00:00Z","attachment":{"type":"skill_listing","content":"menu","skillCount":2,"isInitial":true,"names":["alpha-nonexistent","beta-nonexistent"]}}`
	var e TranscriptEntry
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if e.Attachment == nil {
		t.Fatal("attachment did not parse")
	}
	skills := extractSkillEvents([]TranscriptEntry{e})
	if len(skills) != 2 {
		t.Fatalf("want 2 skills, got %d", len(skills))
	}
	for _, s := range skills {
		if s.Source != "bundled" {
			t.Errorf("expected bundled for %s, got source=%s", s.Name, s.Source)
		}
		if s.SHA256 != "" {
			t.Errorf("bundled skill %s should have empty sha, got %s", s.Name, s.SHA256)
		}
	}
}
