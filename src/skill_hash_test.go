package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExtractSkillEventsAgainstRealTranscript runs the extractor over a real
// transcript from ~/.claude/projects/-Users-collinc-code-opik/ if present.
// Skips when no transcript is around — keeps CI sane.
func TestExtractSkillEventsAgainstRealTranscript(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	dir := filepath.Join(home, ".claude/projects/-Users-collinc-code-opik")
	matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(matches) == 0 {
		t.Skip("no real transcripts available")
	}

	// Pick the most recent .jsonl.
	var newest string
	var newestMtime int64
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		if fi.ModTime().Unix() > newestMtime {
			newestMtime = fi.ModTime().Unix()
			newest = m
		}
	}
	if newest == "" {
		t.Skip("no readable transcripts")
	}

	entries, err := ReadTranscript(newest, 0)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if len(entries) == 0 {
		t.Skip("transcript empty")
	}

	skills := extractSkillEvents(entries)
	t.Logf("transcript: %s", newest)
	t.Logf("entries: %d", len(entries))
	t.Logf("skill events: %d", len(skills))
	if len(skills) == 0 {
		t.Fatalf("expected at least one skill_listing in a real transcript")
	}
	distinctShas := map[string]bool{}
	bundled := 0
	for i, s := range skills {
		if s.Name == "" {
			t.Errorf("skill[%d] missing name", i)
		}
		if s.Source == "bundled" {
			bundled++
		} else if s.SHA256 != "" {
			distinctShas[s.SHA256] = true
		}
		if i < 8 {
			t.Logf("  %s sha=%s tokens=%d source=%s path=%s",
				s.Name, truncSha(s.SHA256), s.BodyTokens, s.Source, s.Path)
		}
	}
	t.Logf("on-disk skills with distinct sha256s: %d", len(distinctShas))
	t.Logf("bundled skills (no on-disk body): %d", bundled)
	// The whole point of this refactor: each on-disk skill should have its
	// own hash. If we somehow collapsed them all to one, fail loudly.
	if len(distinctShas) < 3 {
		t.Errorf("expected multiple distinct skill SHAs from on-disk bodies; got %d", len(distinctShas))
	}
}

func truncSha(s string) string {
	if len(s) < 12 {
		return s
	}
	return s[:12]
}
