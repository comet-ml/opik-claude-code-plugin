package main

import (
	"sort"
	"strings"
)

// extractToolsSnapshot walks the full transcript and returns the
// `cc.tools.*` block: {summary, available}. Built-ins and MCP tools live
// in one catalog, distinguished by `source`. No `calls[]` — every tool
// call is already a span (span.name == tool name), so the FE filters spans
// directly.
//
// Catalog reconstruction: walk every `deferred_tools_delta` attachment in
// order, applying addedNames/addedLines, removedNames, and readdedNames.
// The final state reflects what's actually available to the model NOW —
// which is what you want when the user toggles MCPs off mid-session.
//
// Schema-cost proxy: the deferred-tools-delta `addedLines` array carries
// the actual name+description text the model sees in its tool catalog.
// We track per-tool line text and convert chars→tokens with the
// calibrated `deferred_tools_payload` ratio. This is not the full JSON
// Schema (which Anthropic loads only via ToolSearch), but it IS the
// real upfront catalog cost — way more honest than tool_count * 15.
func extractToolsSnapshot(entries []TranscriptEntry) map[string]interface{} {
	// available[name] = description line text (or "" if not provided).
	// Walking deltas in order so removals applied AFTER an add take effect.
	available := map[string]string{}
	sawAnyDelta := false
	for _, e := range entries {
		if e.Type != "attachment" || e.Attachment == nil || e.Attachment.Type != "deferred_tools_delta" {
			continue
		}
		sawAnyDelta = true
		a := e.Attachment
		// Parallel arrays: AddedNames[i] gets AddedLines[i] as its text.
		for i, name := range a.AddedNames {
			line := ""
			if i < len(a.AddedLines) {
				line = a.AddedLines[i]
			}
			available[name] = line
		}
		for _, name := range a.RemovedNames {
			delete(available, name)
		}
		// Re-adds restore a name without re-supplying its line text. If we
		// have an earlier line cached, keep it; otherwise the name comes
		// back with empty text and contributes 0 to schema_tokens.
		for _, name := range a.ReaddedNames {
			if _, ok := available[name]; !ok {
				available[name] = ""
			}
		}
	}
	if !sawAnyDelta {
		return nil
	}

	type toolEntry struct {
		name, source, server, tool, line string
	}
	var avail []toolEntry
	builtinCount, mcpCount := 0, 0
	serverTools := map[string][]string{}    // server → tool names
	serverLines := map[string]string{}      // server → joined description lines
	builtinLines, mcpLines := "", ""        // sources → joined lines

	for name, line := range available {
		// SplitN("__", 3) — assumes server names contain no `__`. Tool
		// names CAN (absorbed into the 3rd part). Same assumption as
		// processToolUse in main.go; keep them aligned.
		if strings.HasPrefix(name, "mcp__") {
			parts := strings.SplitN(name, "__", 3)
			if len(parts) < 3 {
				continue
			}
			server, tool := parts[1], parts[2]
			avail = append(avail, toolEntry{
				name: name, source: "mcp", server: server, tool: tool, line: line,
			})
			mcpCount++
			serverTools[server] = append(serverTools[server], name)
			if line != "" {
				serverLines[server] += line + "\n"
				mcpLines += line + "\n"
			}
		} else {
			avail = append(avail, toolEntry{name: name, source: "builtin", line: line})
			builtinCount++
			if line != "" {
				builtinLines += line + "\n"
			}
		}
	}
	totalCount := builtinCount + mcpCount
	if totalCount == 0 {
		return nil
	}

	sort.Slice(avail, func(i, j int) bool {
		if avail[i].source != avail[j].source {
			return avail[i].source < avail[j].source
		}
		return avail[i].name < avail[j].name
	})

	builtinSchemaTokens := tokEstimateAs(builtinLines, "deferred_tools_payload")
	mcpSchemaTokens := tokEstimateAs(mcpLines, "deferred_tools_payload")

	var byServer []map[string]interface{}
	for server, tools := range serverTools {
		byServer = append(byServer, map[string]interface{}{
			"server":        server,
			"tool_count":    len(tools),
			"schema_tokens": tokEstimateAs(serverLines[server], "deferred_tools_payload"),
		})
	}
	sort.Slice(byServer, func(i, j int) bool {
		return byServer[i]["server"].(string) < byServer[j]["server"].(string)
	})

	availableOut := make([]map[string]interface{}, 0, len(avail))
	for _, t := range avail {
		entry := map[string]interface{}{
			"name":   t.name,
			"source": t.source,
		}
		if t.source == "mcp" {
			entry["server"] = t.server
			entry["tool"] = t.tool
		}
		availableOut = append(availableOut, entry)
	}

	summary := map[string]interface{}{
		"schema_tokens":   builtinSchemaTokens + mcpSchemaTokens,
		"total_tokens":    builtinSchemaTokens + mcpSchemaTokens,
		"available_count": totalCount,
		"by_source": map[string]interface{}{
			"builtin": map[string]interface{}{
				"available_count": builtinCount,
				"schema_tokens":   builtinSchemaTokens,
			},
			"mcp": map[string]interface{}{
				"available_count": mcpCount,
				"schema_tokens":   mcpSchemaTokens,
			},
		},
		"by_server": byServer,
	}

	return map[string]interface{}{
		"summary":   summary,
		"available": availableOut,
	}
}
