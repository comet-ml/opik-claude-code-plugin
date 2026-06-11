package main

import (
	"testing"
)

const sampleContextMarkdown = `## Context Usage

**Model:** claude-opus-4-7
**Tokens:** 35.3k / 1m (4%)

### Estimated usage by category

| Category | Tokens | Percentage |
|----------|--------|------------|
| System prompt | 8k | 0.8% |
| System tools | 17.6k | 1.8% |
| MCP tools (deferred) | 2.5k | 0.3% |
| System tools (deferred) | 19.2k | 1.9% |
| Custom agents | 3.3k | 0.3% |
| Memory files | 2.9k | 0.3% |
| Skills | 2.1k | 0.2% |
| Messages | 6.1k | 0.6% |
| Free space | 928.6k | 92.9% |
| Autocompact buffer | 33k | 3.3% |

### Custom Agents

| Agent Type | Source | Tokens |
|------------|--------|--------|
| architect | Project | 459 |
| build-fixer | Project | 403 |

### Memory Files

| Type | Path | Tokens |
|------|------|--------|
| Project | /tmp/rules/security.md | 149 |
| AutoMem | /tmp/MEMORY.md | 225 |

### Skills

| Skill | Source | Tokens |
|-------|--------|--------|
| find-skills | User | ~110 |
| comet:create-pr | Project | < 20 |
| update-config | Built-in | ~240 |

### MCP Tools

| Tool | Server | Tokens |
|------|--------|--------|
| mcp__everything__echo | everything | 123 |
| mcp__everything__gzip-file-as-resource | everything | 431 |
`

func TestParseContextMarkdownExtractsTotals(t *testing.T) {
	out := parseContextMarkdown(sampleContextMarkdown)
	if out == nil {
		t.Fatal("parseContextMarkdown returned nil")
	}
	if got := out["model"]; got != "claude-opus-4-7" {
		t.Errorf("model = %v, want claude-opus-4-7", got)
	}
	if got := out["total_tokens_used"]; got != 35300 {
		t.Errorf("total_tokens_used = %v, want 35300", got)
	}
	if got := out["context_window_tokens"]; got != 1_000_000 {
		t.Errorf("context_window_tokens = %v, want 1_000_000", got)
	}
	if got := out["percentage_used"]; got != 4 {
		t.Errorf("percentage_used = %v, want 4", got)
	}
}

func TestParseContextMarkdownCategories(t *testing.T) {
	out := parseContextMarkdown(sampleContextMarkdown)
	cats, ok := out["categories"].(map[string]int)
	if !ok {
		t.Fatalf("categories missing or wrong type: %T", out["categories"])
	}
	wants := map[string]int{
		"system_prompt":         8000,
		"system_tools":          17600,
		"mcp_tools_deferred":    2500,
		"system_tools_deferred": 19200,
		"custom_agents":         3300,
		"memory_files":          2900,
		"skills":                2100,
		"messages":              6100,
		"free_space":            928600,
		"autocompact_buffer":    33000,
	}
	for k, want := range wants {
		if got := cats[k]; got != want {
			t.Errorf("categories[%q] = %d, want %d", k, got, want)
		}
	}
}

func TestParseContextMarkdownDetailTables(t *testing.T) {
	out := parseContextMarkdown(sampleContextMarkdown)

	agents, _ := out["agents"].([]map[string]interface{})
	if len(agents) != 2 {
		t.Fatalf("agents len=%d, want 2", len(agents))
	}
	if agents[0]["agent_type"] != "architect" || agents[0]["tokens"] != 459 {
		t.Errorf("agents[0] = %+v", agents[0])
	}

	memory, _ := out["memory_files"].([]map[string]interface{})
	if len(memory) != 2 || memory[1]["tokens"] != 225 {
		t.Errorf("memory_files unexpected: %+v", memory)
	}

	skills, _ := out["skills"].([]map[string]interface{})
	if len(skills) != 3 {
		t.Fatalf("skills len=%d, want 3", len(skills))
	}
	// `~110` should normalize to 110.
	if skills[0]["tokens"] != 110 {
		t.Errorf("skills[0].tokens = %v, want 110", skills[0]["tokens"])
	}
	// `< 20` should normalize to 20 (best-effort floor).
	if skills[1]["tokens"] != 20 {
		t.Errorf("skills[1].tokens = %v, want 20", skills[1]["tokens"])
	}
	// `~240` → 240.
	if skills[2]["tokens"] != 240 {
		t.Errorf("skills[2].tokens = %v, want 240", skills[2]["tokens"])
	}

	mcp, _ := out["mcp_tools"].([]map[string]interface{})
	if len(mcp) != 2 || mcp[0]["server"] != "everything" || mcp[1]["tokens"] != 431 {
		t.Errorf("mcp_tools unexpected: %+v", mcp)
	}
}

func TestParseTokens(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"35.3k", 35300},
		{"1m", 1_000_000},
		{"2.5k", 2500},
		{"~110", 110},
		{"< 20", 20},
		{"<20", 20},
		{"13", 13},
		{"", 0},
		{"  ", 0},
		{"garbage", 0},
	}
	for _, c := range cases {
		if got := parseTokens(c.in); got != c.want {
			t.Errorf("parseTokens(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestNormalizeCategoryKey(t *testing.T) {
	cases := map[string]string{
		"System prompt":           "system_prompt",
		"System tools (deferred)": "system_tools_deferred",
		"MCP tools (deferred)":    "mcp_tools_deferred",
		"Custom agents":           "custom_agents",
		"Free space":              "free_space",
		"Autocompact buffer":      "autocompact_buffer",
	}
	for in, want := range cases {
		if got := normalizeCategoryKey(in); got != want {
			t.Errorf("normalizeCategoryKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseContextJSONHandlesErrors(t *testing.T) {
	// is_error true → reject.
	_, err := parseContextJSON([]byte(`{"is_error":true,"result":"some output"}`))
	if err == nil {
		t.Error("expected error for is_error=true")
	}
	// Empty result → reject (not a panic, not a half-parsed snapshot).
	_, err = parseContextJSON([]byte(`{"is_error":false,"result":""}`))
	if err == nil {
		t.Error("expected error for empty result")
	}
	// Malformed JSON → wrapped error.
	_, err = parseContextJSON([]byte(`not json`))
	if err == nil {
		t.Error("expected error for non-JSON input")
	}
}
