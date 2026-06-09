package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestExtractOutputTokensSnapshot(t *testing.T) {
	// One LLM call with thinking + text + builtin tool + MCP tool + Skill.
	// All blocks share the same message.id so DeduplicateUsage can attribute.
	const msgID = "msg_abc123"
	entries := []TranscriptEntry{
		{
			Type: "assistant",
			UUID: "u1",
			Message: &Message{
				ID:    msgID,
				Model: "claude-opus-4-8",
				Usage: &Usage{OutputTokens: 1000},
				Content: ContentSlice{
					{Type: "thinking", Thinking: "..."},
					{Type: "text", Text: "hello world"},
					{Type: "tool_use", ID: "t1", Name: "Bash", Input: map[string]interface{}{"command": "ls"}},
					{Type: "tool_use", ID: "t2", Name: "mcp__slack__send", Input: map[string]interface{}{}},
					{Type: "tool_use", ID: "t3", Name: "Skill", Input: map[string]interface{}{}},
				},
			},
		},
	}

	parsed := ParseAssistantMessages(entries)
	DeduplicateUsage(parsed)

	snap := extractOutputTokensSnapshot(entries, parsed)
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}

	summary, _ := snap["summary"].(map[string]interface{})
	if summary == nil {
		t.Fatal("missing summary")
	}
	total, _ := summary["total_tokens"].(int)
	if total != 1000 {
		t.Errorf("total_tokens = %d, want 1000", total)
	}

	cat, _ := snap["by_category"].(map[string]interface{})
	if cat == nil {
		t.Fatal("missing by_category")
	}

	// Sum of all categories must equal total.
	catSum := 0
	for _, key := range []string{"thinking", "assistant_text", "builtin_tool_use", "mcp_tool_use", "skill_invocations"} {
		v, _ := cat[key].(int)
		catSum += v
	}
	if catSum != total {
		t.Errorf("sum(by_category) = %d, want %d (total_tokens)", catSum, total)
	}

	// thinking must be > 0 (leftover after non-thinking blocks).
	if thinking, _ := cat["thinking"].(int); thinking == 0 {
		t.Error("thinking should be > 0")
	}

	// Each non-thinking category must have been assigned something.
	for _, key := range []string{"assistant_text", "builtin_tool_use", "mcp_tool_use", "skill_invocations"} {
		if v, _ := cat[key].(int); v == 0 {
			t.Errorf("by_category[%s] = 0, expected > 0", key)
		}
	}
}

func TestExtractOutputTokensSnapshotNilOnEmpty(t *testing.T) {
	snap := extractOutputTokensSnapshot(nil, nil)
	if snap != nil {
		t.Errorf("expected nil on empty entries, got %v", snap)
	}
}

func TestExtractThinkingSnapshotByLevel(t *testing.T) {
	// Three LLM calls with different thinking budgets:
	//   msg1 → 200 tokens  (minimal)
	//   msg2 → 2000 tokens (light)
	//   msg3 → 15000 tokens (heavy)
	makeEntry := func(msgID string, thinkingTokens int) TranscriptEntry {
		return TranscriptEntry{
			Type: "assistant",
			Message: &Message{
				ID:    msgID,
				Model: "claude-sonnet-4-6",
				Usage: &Usage{OutputTokens: thinkingTokens},
				Content: ContentSlice{
					{Type: "thinking", Thinking: "..."},
				},
			},
		}
	}
	entries := []TranscriptEntry{
		makeEntry("msg1", 200),
		makeEntry("msg2", 2000),
		makeEntry("msg3", 15000),
	}
	parsed := ParseAssistantMessages(entries)
	DeduplicateUsage(parsed)

	snap := extractThinkingSnapshot(entries, parsed)
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}

	summary := snap["summary"].(map[string]interface{})
	if total := summary["total_tokens"].(int); total != 17200 {
		t.Errorf("total_tokens = %d, want 17200", total)
	}
	if calls := summary["call_count"].(int); calls != 3 {
		t.Errorf("call_count = %d, want 3", calls)
	}

	byLevel := snap["by_level"].([]map[string]interface{})
	levels := map[string]map[string]interface{}{}
	for _, l := range byLevel {
		levels[l["level"].(string)] = l
	}

	if _, ok := levels["minimal"]; !ok {
		t.Error("expected minimal level")
	}
	if _, ok := levels["light"]; !ok {
		t.Error("expected light level")
	}
	if _, ok := levels["heavy"]; !ok {
		t.Error("expected heavy level")
	}
	if _, ok := levels["medium"]; ok {
		t.Error("unexpected medium level")
	}

	if levels["minimal"]["call_count"].(int) != 1 {
		t.Errorf("minimal call_count = %d, want 1", levels["minimal"]["call_count"])
	}
	if levels["heavy"]["tokens"].(int) != 15000 {
		t.Errorf("heavy tokens = %d, want 15000", levels["heavy"]["tokens"])
	}
}

func TestExtractFileAttachmentsSnapshotByType(t *testing.T) {
	makeAttachment := func(path, content string) TranscriptEntry {
		raw, _ := json.Marshal(map[string]interface{}{
			"file": map[string]string{"path": path, "content": content},
		})
		return TranscriptEntry{
			Type: "attachment",
			Attachment: &Attachment{
				Type:    "file",
				Content: raw,
			},
		}
	}
	entries := []TranscriptEntry{
		makeAttachment("/repo/main.go", "package main\nfunc main() {}"),
		makeAttachment("/repo/util.go", "package main"),
		makeAttachment("/repo/README.md", "# Hello"),
		makeAttachment("/repo/Makefile", "build:"),  // no extension → "other"
	}

	snap := extractFileAttachmentsSnapshot(entries)
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}

	summary := snap["summary"].(map[string]interface{})
	if fc := summary["file_count"].(int); fc != 4 {
		t.Errorf("file_count = %d, want 4", fc)
	}

	byType := snap["by_type"].([]map[string]interface{})
	exts := map[string]map[string]interface{}{}
	for _, row := range byType {
		exts[row["ext"].(string)] = row
	}

	if _, ok := exts[".go"]; !ok {
		t.Error("expected .go entry")
	}
	if _, ok := exts[".md"]; !ok {
		t.Error("expected .md entry")
	}
	if _, ok := exts["other"]; !ok {
		t.Error("expected other entry for Makefile")
	}
	if exts[".go"]["file_count"].(int) != 2 {
		t.Errorf(".go file_count = %d, want 2", exts[".go"]["file_count"])
	}
}

func TestExtractAgentsSnapshotPrefersFrontmatterName(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_PROJECT_DIR", cwd)

	// File on disk is meta-auditor.md, but its frontmatter announces the
	// agent as `config-auditor`. /context uses the frontmatter name, so we
	// must too — otherwise dashboards drift from what users see.
	writeFile(t, filepath.Join(cwd, ".claude", "agents", "meta-auditor.md"), `---
name: config-auditor
description: |
  Audits Claude config files for duplication.
---
# Body — not counted toward the always-on cost
`)

	snap := extractAgentsSnapshot()
	if snap == nil {
		t.Fatal("expected agents snapshot")
	}
	agents, _ := snap["agents"].([]map[string]interface{})
	if len(agents) != 1 {
		t.Fatalf("want 1 agent, got %d", len(agents))
	}
	if name, _ := agents[0]["name"].(string); name != "config-auditor" {
		t.Errorf("name = %q, want config-auditor", name)
	}
}

func TestExtractAgentsSnapshotFiltersDisabledPlugins(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_PROJECT_DIR", cwd)

	// Two plugins on disk via installed_plugins.json — only one enabled
	// via the settings layer. Disabled plugin's agent must NOT appear.
	enabledPlugin := filepath.Join(home, "plugins", "good")
	disabledPlugin := filepath.Join(home, "plugins", "bad")
	writeFile(t, filepath.Join(enabledPlugin, "agents", "real-reviewer.md"), `---
name: real-reviewer
description: Active reviewer.
---
`)
	writeFile(t, filepath.Join(disabledPlugin, "agents", "ghost.md"), `---
name: ghost
description: Should not show up.
---
`)

	manifest := map[string]interface{}{
		"plugins": map[string]interface{}{
			"good@mp": []interface{}{map[string]interface{}{"installPath": enabledPlugin}},
			"bad@mp":  []interface{}{map[string]interface{}{"installPath": disabledPlugin}},
		},
	}
	mb, _ := json.Marshal(manifest)
	writeFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), string(mb))

	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{
		"enabledPlugins": {"good@mp": true, "bad@mp": false}
	}`)

	snap := extractAgentsSnapshot()
	if snap == nil {
		t.Fatal("expected agents snapshot")
	}
	agents, _ := snap["agents"].([]map[string]interface{})

	names := map[string]bool{}
	for _, a := range agents {
		if n, ok := a["name"].(string); ok {
			names[n] = true
		}
	}
	if !names["good:real-reviewer"] {
		t.Errorf("good:real-reviewer should be present, got %v", names)
	}
	if names["bad:ghost"] {
		t.Errorf("bad:ghost must be filtered out (disabled plugin), got %v", names)
	}
}

