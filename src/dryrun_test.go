package main

import (
	"encoding/json"
	"fmt"
	"os"
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
	for _, domain := range []string{"tools", "skills", "user_prompts", "tool_results", "thinking"} {
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
