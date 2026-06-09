package main

import "testing"

func TestFindCCVersion(t *testing.T) {
	entries := []TranscriptEntry{
		{Type: "queue-operation"},
		{Type: "user", Version: "2.1.150"},
		{Type: "assistant", Version: "2.1.150"},
	}
	if got := findCCVersion(entries); got != "2.1.150" {
		t.Errorf("findCCVersion = %q, want 2.1.150", got)
	}
	if got := findCCVersion(nil); got != "" {
		t.Errorf("findCCVersion(nil) = %q, want empty", got)
	}
}

func TestCCBuiltinFor(t *testing.T) {
	// Exact match.
	c, key := ccBuiltinFor("2.1.150")
	if key != "2.1.150" || c.SystemPromptTokens == 0 {
		t.Errorf("exact: got key=%q tokens=%+v", key, c)
	}
	// Patch fallback inside the same major.minor.
	c, key = ccBuiltinFor("2.1.151")
	if key == "" || c.SystemPromptTokens == 0 {
		t.Errorf("patch fallback: got key=%q tokens=%+v", key, c)
	}
	// Unknown major.minor → no fallback.
	c, key = ccBuiltinFor("3.0.0")
	if key != "" || c.SystemPromptTokens != 0 {
		t.Errorf("unknown major: expected zero, got key=%q tokens=%+v", key, c)
	}
	// Empty version.
	c, key = ccBuiltinFor("")
	if key != "" {
		t.Errorf("empty version: expected miss, got %q", key)
	}
}

func TestExtractCCBuiltinSnapshotShape(t *testing.T) {
	snap := extractCCBuiltinSnapshot([]TranscriptEntry{{Version: "2.1.150"}})
	if snap == nil {
		t.Fatal("expected snapshot for known version")
	}
	sum := snap["summary"].(map[string]interface{})
	if est, _ := sum["estimated"].(bool); !est {
		t.Error("expected estimated:true")
	}
	if v, _ := sum["cc_version"].(string); v != "2.1.150" {
		t.Errorf("cc_version = %q, want 2.1.150", v)
	}
	// total_tokens excludes deferred (matches /context's visible total
	// and API billing). For 2.1.150: system_prompt + system_tools.
	total, _ := sum["total_tokens"].(int)
	sp, _ := sum["system_prompt"].(int)
	st, _ := sum["system_tools"].(int)
	if total != sp+st {
		t.Errorf("total_tokens = %d, want system_prompt(%d) + system_tools(%d) = %d", total, sp, st, sp+st)
	}
	deferred, _ := sum["deferred_tokens"].(int)
	df, _ := sum["deferred_tools"].(int)
	if deferred != df {
		t.Errorf("deferred_tokens = %d, want deferred_tools = %d", deferred, df)
	}
	if extractCCBuiltinSnapshot(nil) != nil {
		t.Error("nil entries should yield nil snapshot")
	}
	if extractCCBuiltinSnapshot([]TranscriptEntry{{Version: "99.99.99"}}) != nil {
		t.Error("unknown version should yield nil snapshot")
	}
}
