package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestDryRunOnTestThread(t *testing.T) {
	path := os.Getenv("OPIK_DRY_TRANSCRIPT")
	if path == "" {
		t.Skip("set OPIK_DRY_TRANSCRIPT")
	}
	entries, err := ReadTranscript(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	snaps := domainSnapshotsFromEntries(entries, entries)
	for _, domain := range []string{"tools", "skills", "user_prompts", "tool_results", "thinking", "memory", "agents", "cc_builtin", "assistant_text", "prior_assistant", "file_attachments", "output_tokens"} {
		fmt.Printf("--- %s ---\n", domain)
		if snaps[domain] == nil {
			fmt.Println("(nil)")
			continue
		}
		out := snaps[domain]
		if domain == "tools" {
			out = map[string]interface{}{"summary": snaps[domain]["summary"]}
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	}
}

// TestAttributionInvariant verifies that for every message_id group in
// the deduped parsed slice, Σ AttributedOutputTokens equals the anchor
// (block 0) Usage.OutputTokens — the D3 invariant from verify.py.
func TestAttributionInvariant(t *testing.T) {
	path := os.Getenv("OPIK_DRY_TRANSCRIPT")
	if path == "" {
		t.Skip("set OPIK_DRY_TRANSCRIPT")
	}
	entries, err := ReadTranscript(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	parsed := ParseAssistantMessages(entries)
	DeduplicateUsage(parsed)

	byMID := map[string][]ParsedEntry{}
	for _, p := range parsed {
		if p.MessageID == "" {
			continue
		}
		byMID[p.MessageID] = append(byMID[p.MessageID], p)
	}

	mismatches := 0
	checked := 0
	for mid, sps := range byMID {
		// Anchor: index 0 carries the usage post-dedup.
		var anchor int
		for _, p := range sps {
			if p.Usage != nil && p.Usage.OutputTokens > anchor {
				anchor = p.Usage.OutputTokens
			}
		}
		if anchor == 0 {
			continue
		}
		sum := 0
		for _, p := range sps {
			sum += p.AttributedOutputTokens
		}
		checked++
		if sum != anchor {
			mismatches++
			t.Errorf("mid %s: anchor=%d Σattr=%d Δ=%d", mid[:24], anchor, sum, sum-anchor)
		}
	}
	t.Logf("%d/%d LLM-call groups attribution-clean", checked-mismatches, checked)
	if mismatches > 0 {
		t.Errorf("%d mismatch(es)", mismatches)
	}
}

// TestTraceNameResolution verifies findSlug finds aiTitle in the new
// transcript format (Claude Code 2.1.150+) and that traceNameFromPrompt
// produces sensible output.
func TestTraceNameResolution(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "claude-code"},
		{"  ", "claude-code"},
		{"hello", "hello"},
		{"  hello  world  ", "hello world"},
	}
	for _, c := range cases {
		got := traceNameFromPrompt(c.in)
		if got != c.want {
			t.Errorf("traceNameFromPrompt(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	long := "a very long prompt that exceeds the eighty character maximum and should be truncated to something readable"
	got := traceNameFromPrompt(long)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) > 80 {
		t.Errorf("traceNameFromPrompt(long) = %q (len=%d), expected ≤80 runes ending in …", got, len([]rune(got)))
	}

	// findSlug should pick aiTitle when present.
	entries := []TranscriptEntry{
		{Type: "user"},
		{Type: "ai-title", AITitle: "Testing session for command execution"},
		{Type: "assistant"},
	}
	if got := findSlug(entries); got != "Testing session for command execution" {
		t.Errorf("findSlug picked %q, want aiTitle", got)
	}

	// Legacy slug still works when aiTitle is absent.
	legacy := []TranscriptEntry{{Type: "assistant", Slug: "happy-crafting-lamport"}}
	if got := findSlug(legacy); got != "happy-crafting-lamport" {
		t.Errorf("findSlug picked %q, want legacy slug", got)
	}
}

// TestToolResultDebug enumerates every tool_use → tool_result pair and
// flags any tool_use whose result the extractor isn't seeing.
func TestToolResultDebug(t *testing.T) {
	path := os.Getenv("OPIK_DRY_TRANSCRIPT")
	if path == "" {
		t.Skip("set OPIK_DRY_TRANSCRIPT")
	}
	entries, err := ReadTranscript(path, 0)
	if err != nil {
		t.Fatal(err)
	}

	toolNames := map[string]string{}
	for _, e := range entries {
		if e.Type != "assistant" || e.Message == nil {
			continue
		}
		for _, c := range e.Message.Content {
			if c.Type == "tool_use" && c.ID != "" {
				toolNames[c.ID] = c.Name
			}
		}
	}

	resultIDs := map[string]bool{}
	for _, e := range entries {
		if e.Type != "user" || e.Message == nil {
			continue
		}
		for _, c := range e.Message.Content {
			if c.Type == "tool_result" && c.ToolUseID != "" {
				resultIDs[c.ToolUseID] = true
			}
		}
	}

	fmt.Printf("tool_uses=%d, tool_results=%d\n", len(toolNames), len(resultIDs))
	for id, name := range toolNames {
		if !resultIDs[id] {
			fmt.Printf("  ✗ no result for tool_use %s (%s)\n", id, name)
		}
	}
	for id := range resultIDs {
		if _, ok := toolNames[id]; !ok {
			fmt.Printf("  ✗ no tool_use for result %s\n", id)
		}
	}
}
