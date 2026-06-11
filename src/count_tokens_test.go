package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// resetTokenCache points the cache at a fresh temp HOME and clears the
// in-memory state between tests (the production code loads once per
// process).
func resetTokenCache(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_PROJECT_DIR", t.TempDir())
	tokCacheMu.Lock()
	tokCacheMap = map[string]int{}
	tokCacheMu.Unlock()
	billingModel = defaultCountModel
}

func TestMeasuredOrEstimatePrefersAnchor(t *testing.T) {
	resetTokenCache(t)
	text := strings.Repeat("some skill menu line\n", 30)

	est := measuredOrEstimate(text, "skill_listing_menu")
	if est != tokEstimateAs(text, "skill_listing_menu") {
		t.Fatalf("without anchor, want ratio estimate %d, got %d",
			tokEstimateAs(text, "skill_listing_menu"), est)
	}

	saveTokenCounts(map[string]int{billingModel + "|" + sha256hex(text): 4242})
	if got := measuredOrEstimate(text, "skill_listing_menu"); got != 4242 {
		t.Errorf("with anchor, want 4242, got %d", got)
	}
}

func TestSaveTokenCountsMergesOnDisk(t *testing.T) {
	resetTokenCache(t)
	saveTokenCounts(map[string]int{"m|a": 1})
	saveTokenCounts(map[string]int{"m|b": 2})

	// Reload from disk into a fresh map to prove both rows persisted.
	var onDisk map[string]int
	data, err := jsonReadFile(tokenCachePath())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk["m|a"] != 1 || onDisk["m|b"] != 2 {
		t.Errorf("on-disk cache = %v, want both rows", onDisk)
	}
}

func TestRunTokenCountPassBudgetAndBaseline(t *testing.T) {
	resetTokenCache(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key") // auth via env, no keychain

	// Mock the endpoint: baseline (".") costs 8; any text costs 8 - 1 + its
	// length/4 so measured = len/4 exactly after baseline subtraction.
	calls := 0
	old := countTokensHTTP
	countTokensHTTP = func(payload []byte, headers map[string]string) (int, error) {
		calls++
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(payload, &req)
		text := req.Messages[0].Content
		if text == "." {
			return 8, nil
		}
		return 8 - 1 + len(text)/4, nil
	}
	defer func() { countTokensHTTP = old }()

	big := strings.Repeat("a long tool result line\n", 100)     // 2400 chars
	small := strings.Repeat("a smaller prompt body here\n", 10) // 270 chars
	entries := []TranscriptEntry{
		userPromptEntry(small),
		toolResultEntry("tu1", big),
		assistantCall(t, "m1", &Usage{InputTokens: 50, OutputTokens: 5},
			Content{Type: "tool_use", ID: "tu1", Name: "Read",
				Input: map[string]interface{}{"f": "x"}})[0],
	}

	// Budget of 2 = baseline + ONE measurement; biggest-first → the big
	// tool result gets measured, the small prompt does not.
	spent := runTokenCountPass(entries, 2)
	if spent != 2 {
		t.Fatalf("spent = %d, want 2 (baseline + 1 text)", spent)
	}
	if got, ok := tokenCacheGet(defaultCountModel + "|" + sha256hex(big)); !ok || got != len(big)/4 {
		t.Errorf("big text anchor = %d/%v, want %d", got, ok, len(big)/4)
	}
	if _, ok := tokenCacheGet(defaultCountModel + "|" + sha256hex(small)); ok {
		t.Error("small text should not have been measured under budget")
	}

	// Second pass: baseline + big are cached; remaining budget measures small.
	spent = runTokenCountPass(entries, 2)
	if _, ok := tokenCacheGet(defaultCountModel + "|" + sha256hex(small)); !ok {
		t.Error("second pass should measure the remaining miss")
	}
	_ = spent
}

func TestDeriveBuiltinResidualFromColdFirstCall(t *testing.T) {
	resetTokenCache(t)

	// Big enough that the residual stays under the 95%-of-total sanity cap.
	prompt := strings.Repeat("hello there friend ", 120)
	pTok := tokEstimateAs(prompt, "user_prompt")
	// Cold first call: read=0; total input = prompt + 6,000 of bundled
	// system prompt/schemas we can't see.
	entries := []TranscriptEntry{userPromptEntry(prompt)}
	call := assistantCall(t, "m1", &Usage{InputTokens: 100, CacheCreationInputTokens: pTok + 5_900, OutputTokens: 5},
		Content{Type: "text", Text: "hi"})
	entries = append(entries, call...)
	entries[1].Version = "9.9.9" // unknown to the table — residual is the only source

	deriveBuiltinResidual(entries)

	got, ok := measuredBuiltinStatic("9.9.9")
	if !ok {
		t.Fatal("expected a stored residual")
	}
	want := 100 + pTok + 5_900 - pTok // total - known(prompt)
	if got != want {
		t.Errorf("residual = %d, want %d", got, want)
	}

	// And billing now uses it as the static_overhead definition piece.
	snap := computeBillingSnapshot(entries, entries)
	so := snap["lanes"].(map[string]interface{})["static_overhead"].(map[string]interface{})
	if so["total"].(int) != want {
		t.Errorf("static_overhead total = %d, want measured %d", so["total"], want)
	}
}

func jsonReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
