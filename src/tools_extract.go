package main

import (
	"sort"
	"strings"
)

// mcpPerToolEstimatedTokens is the calibrated approximation of one MCP
// tool's full JSON-Schema cost in /context's "MCP tools (deferred)"
// bucket. /context bills each tool by its description + parameter schema,
// neither of which the transcript exposes — Claude Code keeps them
// internally after handshake. Calibrated against the @modelcontextprotocol
// /server-everything reference server (15 tools, /context = 2.5k tokens,
// instructions block = 1588 chars ≈ 530 tokens, addedLines ≈ 255 tokens):
// per-tool overhead = (2500 - 530 - 255) / 15 ≈ 117 tokens. Rounded to
// 130 to cover servers with denser schemas. Honest gap: real schemas can
// vary 3x+ across tools (echo: 123 vs gzip-file-as-resource: 431) so the
// per-tool figure is a coarse estimate flagged `estimated: true` in the
// payload. Without a runtime tools/list query we can't do better.
const mcpPerToolEstimatedTokens = 130

// extractToolsSnapshot walks the full transcript and returns the
// `cc.tools.*` block: {summary, available, mcp}. Built-ins and MCP tools
// live in one catalog, distinguished by `source`. No `calls[]` — every
// tool call is already a span (span.name == tool name), so the FE filters
// spans directly. MCP-specific accounting (server instructions text + per-
// tool schema estimate) lives under `cc.tools.mcp` so the FE can render
// the same "MCP tools (deferred)" breakdown /context shows.
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
	// MCP server instructions are delivered as a parallel attachment
	// stream (`mcp_instructions_delta` with AddedBlocks). Server name
	// uniquely keys the block; the most recent block wins if a server
	// re-attaches mid-session.
	mcpInstructions := map[string]string{}
	for _, e := range entries {
		if e.Type != "attachment" || e.Attachment == nil {
			continue
		}
		a := e.Attachment
		switch a.Type {
		case "deferred_tools_delta":
			sawAnyDelta = true
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
		case "mcp_instructions_delta":
			for i, server := range a.AddedNames {
				if i < len(a.AddedBlocks) {
					mcpInstructions[server] = a.AddedBlocks[i]
				}
			}
			for _, server := range a.RemovedNames {
				delete(mcpInstructions, server)
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
	serverTools := map[string][]string{} // server → tool names
	serverLines := map[string]string{}   // server → joined description lines
	builtinLines, mcpLines := "", ""     // sources → joined lines

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

	// Estimated MCP /context cost per server: deferred-line text we DID
	// observe + the server instructions block + an overhead per tool for
	// the JSON Schema we can't see. Sum equals what /context labels "MCP
	// tools (deferred)" (plus the instructions piece, which /context folds
	// elsewhere on some versions). Calibrated against
	// @modelcontextprotocol/server-everything (15 tools, 2.5k tokens).
	var byServer []map[string]interface{}
	mcpInstructionsTokensTotal := 0
	mcpEstimatedSchemaTokensTotal := 0
	for server, tools := range serverTools {
		instrTokens := tokEstimateAs(mcpInstructions[server], "prose")
		estSchema := len(tools) * mcpPerToolEstimatedTokens
		mcpInstructionsTokensTotal += instrTokens
		mcpEstimatedSchemaTokensTotal += estSchema
		byServer = append(byServer, map[string]interface{}{
			"server":                  server,
			"tool_count":              len(tools),
			"schema_tokens":           tokEstimateAs(serverLines[server], "deferred_tools_payload"),
			"instructions_tokens":     instrTokens,
			"estimated_schema_tokens": estSchema,
			"estimated_total_tokens":  instrTokens + estSchema,
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
				"available_count":         mcpCount,
				"schema_tokens":           mcpSchemaTokens,
				"instructions_tokens":     mcpInstructionsTokensTotal,
				"estimated_schema_tokens": mcpEstimatedSchemaTokensTotal,
				// estimated_deferred_tokens is the closest equivalent to
				// /context's "MCP tools (deferred)" row — addedLines we saw
				// + estimated full-schema overhead per tool. The
				// instructions block lives at by_source.mcp.instructions_tokens
				// because /context buckets it under "System tools (deferred)"
				// on some CC versions; surfacing both lets the FE pick.
				"estimated_deferred_tokens": mcpSchemaTokens + mcpEstimatedSchemaTokensTotal,
				"estimated":                 mcpCount > 0,
			},
		},
		"by_server": byServer,
	}

	return map[string]interface{}{
		"summary":   summary,
		"available": availableOut,
	}
}
