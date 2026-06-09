package main

import (
	"encoding/json"
	"testing"
)

func TestExtractToolsSnapshotMCP(t *testing.T) {
	// Synthesize the two attachments CC writes when an MCP server attaches:
	//   1. deferred_tools_delta with the MCP tool names (addedLines = name only)
	//   2. mcp_instructions_delta with the server's instructions block
	deferred := Attachment{
		Type:       "deferred_tools_delta",
		AddedNames: []string{"mcp__demo__alpha", "mcp__demo__beta", "WebSearch"},
		AddedLines: []string{"mcp__demo__alpha", "mcp__demo__beta", "WebSearch"},
	}
	instr := Attachment{
		Type:        "mcp_instructions_delta",
		AddedNames:  []string{"demo"},
		AddedBlocks: []string{"## demo server\nLine 1\nLine 2\n" + repeatStr("x", 600)},
	}

	entries := []TranscriptEntry{
		{Type: "attachment", Attachment: &deferred},
		{Type: "attachment", Attachment: &instr},
	}

	snap := extractToolsSnapshot(entries)
	if snap == nil {
		t.Fatal("expected tools snapshot")
	}
	sum := snap["summary"].(map[string]interface{})
	bySource := sum["by_source"].(map[string]interface{})
	mcp := bySource["mcp"].(map[string]interface{})

	if mcp["available_count"].(int) != 2 {
		t.Errorf("mcp.available_count = %v, want 2", mcp["available_count"])
	}
	// Per-tool overhead surfaced at expected magnitude.
	if est := mcp["estimated_schema_tokens"].(int); est != 2*mcpPerToolEstimatedTokens {
		t.Errorf("estimated_schema_tokens = %d, want %d", est, 2*mcpPerToolEstimatedTokens)
	}
	if instrTok := mcp["instructions_tokens"].(int); instrTok < 100 {
		t.Errorf("instructions_tokens = %d, expected the long demo block to score >100", instrTok)
	}
	if est := mcp["estimated_deferred_tokens"].(int); est < mcp["estimated_schema_tokens"].(int) {
		t.Errorf("estimated_deferred_tokens should include addedLines + schema overhead, got %d", est)
	}
	if est, _ := mcp["estimated"].(bool); !est {
		t.Error("estimated flag should be true when MCP tools present")
	}

	// by_server entry for "demo" carries the per-server breakdown.
	byServer, _ := sum["by_server"].([]map[string]interface{})
	if len(byServer) != 1 || byServer[0]["server"].(string) != "demo" {
		t.Fatalf("by_server should have one entry for `demo`, got %v", byServer)
	}
}

func TestExtractToolsSnapshotMCPInstructionsRemoval(t *testing.T) {
	add := Attachment{
		Type:        "mcp_instructions_delta",
		AddedNames:  []string{"demo"},
		AddedBlocks: []string{"## demo\nInstructions go here"},
	}
	deferred := Attachment{
		Type:       "deferred_tools_delta",
		AddedNames: []string{"mcp__demo__one"},
		AddedLines: []string{"mcp__demo__one"},
	}
	remove := Attachment{
		Type:         "mcp_instructions_delta",
		RemovedNames: []string{"demo"},
	}
	entries := []TranscriptEntry{
		{Type: "attachment", Attachment: &add},
		{Type: "attachment", Attachment: &deferred},
		{Type: "attachment", Attachment: &remove},
	}
	snap := extractToolsSnapshot(entries)
	mcp := snap["summary"].(map[string]interface{})["by_source"].(map[string]interface{})["mcp"].(map[string]interface{})
	if got := mcp["instructions_tokens"].(int); got != 0 {
		t.Errorf("after remove, instructions_tokens should be 0, got %d", got)
	}
}

func TestAttachmentAddedBlocksRoundtrip(t *testing.T) {
	// Defensive: make sure AddedBlocks survives JSON parsing — without this
	// field on the struct the production extractor silently dropped the
	// MCP instruction blocks even though they were in the transcript.
	raw := `{"type":"attachment","attachment":{"type":"mcp_instructions_delta","addedNames":["demo"],"addedBlocks":["hello block"]}}`
	var e TranscriptEntry
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatal(err)
	}
	if e.Attachment == nil || len(e.Attachment.AddedBlocks) != 1 {
		t.Fatalf("AddedBlocks did not parse: %+v", e.Attachment)
	}
	if e.Attachment.AddedBlocks[0] != "hello block" {
		t.Errorf("AddedBlocks[0] = %q", e.Attachment.AddedBlocks[0])
	}
}

func repeatStr(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
