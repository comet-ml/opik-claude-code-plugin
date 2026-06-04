package main

import (
	"encoding/json"
	"sort"
	"strings"
)

// extractToolsSnapshot walks the full transcript and returns the
// `cc.tools.*` block: {summary, available}. Built-ins and MCP tools live
// in one catalog, distinguished by `source`. No `calls[]` — every tool
// call is already a span (span.name == tool name), so the FE filters spans
// directly.
//
// Schema-cost proxy: the deferred_tools_delta payload itself, since the
// actual JSON-Schema sent in the API `tools` param isn't in the transcript.
// Prorated per-tool by name count.
func extractToolsSnapshot(entries []TranscriptEntry) map[string]interface{} {
	// Walk all deferred_tools_delta attachments. Each delta ADDS names that
	// remain available for the rest of the session. Track the latest cumulative
	// snapshot.
	addedNames := map[string]bool{}
	var deltaPayloads []string
	for _, e := range entries {
		if e.Type != "attachment" || e.Attachment == nil || e.Attachment.Type != "deferred_tools_delta" {
			continue
		}
		for _, n := range e.Attachment.AddedNames {
			addedNames[n] = true
		}
		// Reconstruct payload size estimate from the attachment json
		raw, _ := json.Marshal(e.Attachment)
		deltaPayloads = append(deltaPayloads, string(raw))
	}
	if len(addedNames) == 0 && len(deltaPayloads) == 0 {
		return nil
	}

	// Sum payload tokens across all deferred_tools_delta events seen this
	// session — that's the real measured cost of tool catalog growth.
	totalSchemaTokens := 0
	for _, p := range deltaPayloads {
		totalSchemaTokens += tokEstimate(p)
	}

	// Classify names: builtin (no mcp__ prefix) vs mcp (mcp__<server>__<tool>).
	type toolEntry struct {
		name, source, server, tool string
	}
	var available []toolEntry
	builtinCount := 0
	mcpCount := 0
	serverTools := map[string]int{}
	for name := range addedNames {
		if strings.HasPrefix(name, "mcp__") {
			parts := strings.SplitN(name, "__", 3)
			if len(parts) < 3 {
				continue
			}
			server, tool := parts[1], parts[2]
			available = append(available, toolEntry{
				name: name, source: "mcp", server: server, tool: tool,
			})
			mcpCount++
			serverTools[server]++
		} else {
			available = append(available, toolEntry{name: name, source: "builtin"})
			builtinCount++
		}
	}
	totalCount := builtinCount + mcpCount
	if totalCount == 0 {
		return nil
	}

	// Sort available[] for stable output: source first (builtin > mcp), then name.
	sort.Slice(available, func(i, j int) bool {
		if available[i].source != available[j].source {
			return available[i].source < available[j].source
		}
		return available[i].name < available[j].name
	})

	// Prorate schema_tokens by tool count.
	tokensPerTool := 0
	if totalCount > 0 {
		tokensPerTool = totalSchemaTokens / totalCount
	}
	builtinSchemaTokens := tokensPerTool * builtinCount
	mcpSchemaTokens := tokensPerTool * mcpCount

	// Per-server breakdown.
	var byServer []map[string]interface{}
	for server, count := range serverTools {
		byServer = append(byServer, map[string]interface{}{
			"server":        server,
			"tool_count":    count,
			"schema_tokens": tokensPerTool * count,
		})
	}
	sort.Slice(byServer, func(i, j int) bool {
		return byServer[i]["server"].(string) < byServer[j]["server"].(string)
	})

	availableOut := make([]map[string]interface{}, 0, len(available))
	for _, t := range available {
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
		"total_tokens":    totalSchemaTokens,
		"schema_tokens":   totalSchemaTokens,
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
