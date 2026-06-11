package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSkillListingMenu(t *testing.T) {
	content := strings.Join([]string{
		"- find-skills: Helps users discover skills.",
		"- comet:create-jira-ticket: Create Jira Ticket",
		"- comet:create-pr: Create PR",
		"- opik:opik: Multi-line description",
		"continuation line for opik:opik that should stay with its owner",
		"- run: Last skill",
	}, "\n")
	names := []string{"find-skills", "comet:create-jira-ticket", "comet:create-pr", "opik:opik", "run"}

	got := parseSkillListingMenu(content, names)

	// Five distinct owners — no collapsing on the inner `:` of comet:* names.
	if len(got) != 5 {
		t.Fatalf("want 5 blocks, got %d: %+v", len(got), keysOf(got))
	}
	if !strings.HasPrefix(got["find-skills"], "- find-skills:") {
		t.Errorf("find-skills block missing leader: %q", got["find-skills"])
	}
	// Namespaced skills must keep their full name as the key, not "comet".
	if _, ok := got["comet"]; ok {
		t.Errorf("`comet` should not be a top-level key — namespaced skills must stay intact")
	}
	if got["comet:create-jira-ticket"] != "- comet:create-jira-ticket: Create Jira Ticket" {
		t.Errorf("create-jira-ticket block wrong: %q", got["comet:create-jira-ticket"])
	}
	// Multi-line description sticks with its opener.
	if !strings.Contains(got["opik:opik"], "continuation line for opik:opik") {
		t.Errorf("opik:opik should absorb continuation: %q", got["opik:opik"])
	}
	// Continuation didn't leak into the next skill.
	if strings.Contains(got["run"], "continuation line") {
		t.Errorf("continuation leaked into `run`: %q", got["run"])
	}
}

func TestParseSkillListingMenuTokenizationMatchesContext(t *testing.T) {
	// Sanity check on the 3.0 chars/token ratio: feed a known block size
	// and verify tokEstimateAs returns roughly chars/3.0. Locks in the
	// calibration so a future ratio drift here is caught loudly.
	block := "- update-config: " + strings.Repeat("x", 720)
	tok := tokEstimateAs(block, "skill_listing_menu")
	// 737 chars / 3.0 ≈ 245
	if tok < 230 || tok > 260 {
		t.Errorf("expected ~245 tokens for a 737-char block, got %d", tok)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestBuildLoadedSkillBodiesSlashCommand(t *testing.T) {
	// User types `/opik:opik` → CC injects a `<command-name>` preamble user
	// message, then a `Base directory for this skill:` follow-up user
	// message carrying the body. No Skill tool_use is emitted.
	entries := []TranscriptEntry{
		{
			Type: "user", Message: &Message{Content: ContentSlice{
				{Type: "text", Text: "<command-message>opik:opik</command-message>\n<command-name>/opik:opik</command-name>"},
			}},
		},
		{
			Type: "user", Message: &Message{Content: ContentSlice{
				{Type: "text", Text: "Base directory for this skill: /tmp/skills/opik\n\nbody-contents-here"},
			}},
		},
	}
	loads := buildLoadedSkillBodies(entries)
	if len(loads) != 1 {
		t.Fatalf("want 1 slash-command load, got %d: %+v", len(loads), loads)
	}
	if loads[0].Name != "opik:opik" {
		t.Errorf("name = %q, want opik:opik", loads[0].Name)
	}
	if !strings.HasPrefix(loads[0].Body, "Base directory") {
		t.Errorf("body should start with the prefix the model actually sees; got %q", loads[0].Body[:30])
	}
	if !strings.HasPrefix(loads[0].ToolUseID, "slash:") {
		t.Errorf("slash-command load should have a synthetic slash:<idx> ToolUseID, got %q", loads[0].ToolUseID)
	}
}

func TestBuildLoadedSkillBodiesIgnoresNonSkillSlashCommands(t *testing.T) {
	// `/context` is a slash command but not a skill — no "Base directory"
	// follow-up. Must NOT produce a phantom skill load.
	entries := []TranscriptEntry{
		{
			Type: "user", Message: &Message{Content: ContentSlice{
				{Type: "text", Text: "<command-name>/context</command-name>"},
			}},
		},
		{
			Type: "user", Message: &Message{Content: ContentSlice{
				{Type: "text", Text: "tool_result from /context: usage stats..."},
			}},
		},
	}
	if loads := buildLoadedSkillBodies(entries); len(loads) != 0 {
		t.Fatalf("want 0 loads for /context, got %d: %+v", len(loads), loads)
	}
}

func TestSkillBodyHashSetCoversBothLoadShapes(t *testing.T) {
	slashBody := "Base directory for this skill: /tmp/skills/opik\n\nbody"
	toolBody := "tool-use loaded body text"
	entries := []TranscriptEntry{
		// Slash-command load
		{Type: "user", Message: &Message{Content: ContentSlice{
			{Type: "text", Text: "<command-name>/opik:opik</command-name>"},
		}}},
		{Type: "user", Message: &Message{Content: ContentSlice{
			{Type: "text", Text: slashBody},
		}}},
		// Skill tool_use load
		{Type: "assistant", Message: &Message{Content: ContentSlice{
			{Type: "tool_use", ID: "toolu_X", Name: "Skill", Input: map[string]interface{}{"skill": "agent-ops"}},
		}}},
		{Type: "user", Message: &Message{Content: ContentSlice{
			{Type: "tool_result", ToolUseID: "toolu_X", Content: "Launching skill: agent-ops"},
			{Type: "text", Text: toolBody},
		}}},
	}
	set := skillBodyHashSet(entries)
	if !set[sha256hex(slashBody)] {
		t.Error("slash-loaded body should be in the exclusion set")
	}
	if !set[sha256hex(toolBody)] {
		t.Error("tool-use-loaded body should be in the exclusion set")
	}
}

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
