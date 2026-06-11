package main

import (
	"strconv"
	"strings"
)

// ccBuiltinConstants holds the always-on costs Claude Code charges every
// turn that we structurally cannot read from the transcript:
//
//   - SystemPromptTokens         the bundled default system prompt
//   - SystemToolsTokens          full JSON schemas for the default tools
//     (Read, Edit, Bash, …) — not the names alone
//   - SystemToolsDeferredTokens  the catalog of deferred tool definitions
//     (Cron*, Task*, Web*, Monitor, …) plus any
//     schemas Claude Code injects on demand
//
// These are taken from `/context` for a known CC version. They drift with
// each binary release, so the table below should grow over time.
type ccBuiltinConstants struct {
	SystemPromptTokens        int
	SystemToolsTokens         int
	SystemToolsDeferredTokens int
}

// ccBuiltinByVersion is a small versioned table — keys are exact CC
// CLI versions (the `version` field stamped on every transcript entry).
// When the binary changes its bundled prompt or tool catalog, /context's
// numbers shift; add an entry here with the new ones.
//
// Source: `claude -p "/context"` in a project with NO MCPs connected, on
// the listed CC version + Opus model. The two figures together cover
// >65% of /context's accounted-for tokens.
var ccBuiltinByVersion = map[string]ccBuiltinConstants{
	"2.1.150": {
		SystemPromptTokens:        8000,
		SystemToolsTokens:         17600,
		SystemToolsDeferredTokens: 19200,
	},
	// 2.1.173 moved most of the built-in tool catalog behind deferral:
	// always-on schemas dropped 17.6k → 1.1k. Captured from /context on
	// 2.1.173 + Fable (cc.context_runtime cross-check, OPIK-6873 audit).
	"2.1.173": {
		SystemPromptTokens:        4800,
		SystemToolsTokens:         1100,
		SystemToolsDeferredTokens: 11300,
	},
}

// ccBuiltinFor returns the constants for the given CC version. Falls
// through to the highest known version (sorted lexically — fine while
// the table is small) when an exact match isn't found; returns the zero
// value when the table is empty. Callers should check `version != ""` on
// the returned snapshot to distinguish "estimated" from "unknown".
func ccBuiltinFor(version string) (ccBuiltinConstants, string) {
	if c, ok := ccBuiltinByVersion[version]; ok {
		return c, version
	}
	// Patch-version fallback: 2.1.151 → use the closest known 2.1.* row.
	// Major.Minor match is a reasonable proxy because the system prompt
	// and tool catalog rarely change inside a minor.
	if best, key := closestKnownVersion(version); key != "" {
		return best, key
	}
	return ccBuiltinConstants{}, ""
}

// closestKnownVersion returns the highest known version that shares the
// same major.minor as want. Returns ("", "") if no such version exists.
func closestKnownVersion(want string) (ccBuiltinConstants, string) {
	if want == "" {
		return ccBuiltinConstants{}, ""
	}
	wantMaj, wantMin, _ := splitSemver(want)
	if wantMaj < 0 {
		return ccBuiltinConstants{}, ""
	}
	bestKey := ""
	var bestVal ccBuiltinConstants
	bestPatch := -1
	for k, v := range ccBuiltinByVersion {
		maj, min, patch := splitSemver(k)
		if maj != wantMaj || min != wantMin {
			continue
		}
		if patch > bestPatch {
			bestPatch = patch
			bestKey = k
			bestVal = v
		}
	}
	return bestVal, bestKey
}

func splitSemver(v string) (maj, min, patch int) {
	maj, min, patch = -1, -1, -1
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return
	}
	var err error
	if maj, err = strconv.Atoi(parts[0]); err != nil {
		maj = -1
		return
	}
	if min, err = strconv.Atoi(parts[1]); err != nil {
		min = -1
		return
	}
	if len(parts) == 3 {
		patch, _ = strconv.Atoi(parts[2])
	}
	return
}

// findCCVersion returns the first non-empty `version` field stamped on
// any transcript entry. Claude Code puts the CLI version on every user
// and assistant entry, so any pass-through tells us the binary that wrote
// the session.
func findCCVersion(entries []TranscriptEntry) string {
	for _, e := range entries {
		if e.Version != "" {
			return e.Version
		}
	}
	return ""
}

// extractCCBuiltinSnapshot returns the `cc.cc_builtin` block —
// approximated, version-keyed costs for the bundled system prompt and
// tool catalog. Marked `estimated: true` so dashboards can distinguish
// these from transcript-derived numbers. Returns nil when the version
// is unknown (better to under-report than ship made-up numbers).
//
// total_tokens splits the same way /context does: always_on is what
// ships in the request envelope every turn (and bills against
// input_tokens / cache_*); deferred_tools is what WOULD cost if loaded
// via ToolSearch, but normally doesn't ship at all. Summing total +
// deferred would over-count by the deferred bucket — same trap we
// addressed in buildContextSnapshot.
func extractCCBuiltinSnapshot(entries []TranscriptEntry) map[string]interface{} {
	version := findCCVersion(entries)
	consts, matched := ccBuiltinFor(version)
	if matched == "" {
		return nil
	}
	alwaysOn := consts.SystemPromptTokens + consts.SystemToolsTokens
	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_tokens":    alwaysOn, // ← matches /context and API billing
			"deferred_tokens": consts.SystemToolsDeferredTokens,
			"estimated":       true,
			"cc_version":      version,
			"matched_table":   matched,
			"system_prompt":   consts.SystemPromptTokens,
			"system_tools":    consts.SystemToolsTokens,
			"deferred_tools":  consts.SystemToolsDeferredTokens,
		},
	}
}
