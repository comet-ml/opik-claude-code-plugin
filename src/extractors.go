package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// extractMemorySnapshot reads CLAUDE.md / AGENTS.md / MEMORY.md from the
// standard locations the model would pick up at session start.
// `cc.memory.{summary, files}`.
func extractMemorySnapshot() map[string]interface{} {
	home, _ := os.UserHomeDir()
	cwd := inferCwd()

	paths := []string{}
	if home != "" {
		paths = append(paths, filepath.Join(home, ".claude", "CLAUDE.md"))
	}
	if cwd != "" {
		paths = append(paths,
			filepath.Join(cwd, "CLAUDE.md"),
			filepath.Join(cwd, ".claude", "CLAUDE.md"),
			filepath.Join(cwd, "AGENTS.md"),
		)
	}
	if home != "" && cwd != "" {
		// Memory files live at ~/.claude/projects/<cwd-with-slashes-as-dashes>/memory/*.md
		slug := strings.ReplaceAll(cwd, "/", "-")
		dir := filepath.Join(home, ".claude", "projects", slug, "memory")
		if matches, _ := filepath.Glob(filepath.Join(dir, "*.md")); matches != nil {
			paths = append(paths, matches...)
		}
	}

	seen := map[string]bool{}
	files := make([]map[string]interface{}, 0, len(paths))
	total := 0
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil || seen[abs] {
			continue
		}
		seen[abs] = true
		body, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := string(body)
		tokens := tokEstimateAs(s, "prose") // memory files are markdown prose
		files = append(files, map[string]interface{}{
			"path":        p,
			"sha256":      sha256hex(s),
			"body_tokens": tokens,
		})
		total += tokens
	}
	if len(files) == 0 {
		return nil
	}
	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_tokens": total,
			"file_count":   len(files),
		},
		"files": files,
	}
}

// extractThinkingSnapshot aggregates thinking-block tokens per model,
// driven off the SAME per-block attribution that lands on each span's
// cc.llm_call.attributed_output_tokens. This guarantees that
// Σ attributed[thinking] over the trace == cc.thinking.summary.total_tokens.
// `cc.thinking.{summary, by_model}`.
func extractThinkingSnapshot(entries []TranscriptEntry) map[string]interface{} {
	parsed := ParseAssistantMessages(entries)
	DeduplicateUsage(parsed)

	type group struct {
		tokens, blockCount int
	}
	byModel := map[string]*group{}
	totalTokens, totalBlocks := 0, 0

	for _, p := range parsed {
		if p.ContentType != "thinking" {
			continue
		}
		g, ok := byModel[p.Model]
		if !ok {
			g = &group{}
			byModel[p.Model] = g
		}
		g.tokens += p.AttributedOutputTokens
		g.blockCount++
		totalTokens += p.AttributedOutputTokens
		totalBlocks++
	}
	if totalBlocks == 0 {
		return nil
	}

	byModelOut := make([]map[string]interface{}, 0, len(byModel))
	for m, g := range byModel {
		byModelOut = append(byModelOut, map[string]interface{}{
			"model":       m,
			"tokens":      g.tokens,
			"block_count": g.blockCount,
		})
	}
	sort.Slice(byModelOut, func(i, j int) bool {
		return byModelOut[i]["tokens"].(int) > byModelOut[j]["tokens"].(int)
	})
	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_tokens": totalTokens,
			"block_count":  totalBlocks,
		},
		"by_model": byModelOut,
	}
}

// extractToolResultsSnapshot aggregates tool_result bytes grouped by the
// tool that produced them. `cc.tool_results.{summary, by_tool}`.
//
// Two result-delivery shapes need special handling:
//   - Normal tool_result: user message content block with type="tool_result".
//   - ToolSearch: result is delivered as a `deferred_tools_delta` attachment
//     (addedLines + addedNames). We attribute that delta's addedLines size
//     to the ToolSearch tool_use that immediately preceded it.
func extractToolResultsSnapshot(entries []TranscriptEntry) map[string]interface{} {
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

	type group struct {
		tokens, count int
	}
	byTool := map[string]*group{}
	totalTokens, totalCount := 0, 0

	// Walk entries in order so we can pair a ToolSearch tool_use with the
	// next deferred_tools_delta attachment that follows it.
	var pendingToolSearchID string
	resultedIDs := map[string]bool{}
	for _, e := range entries {
		switch e.Type {
		case "assistant":
			if e.Message == nil {
				continue
			}
			for _, c := range e.Message.Content {
				if c.Type == "tool_use" && c.Name == "ToolSearch" {
					pendingToolSearchID = c.ID
				}
			}
		case "user":
			if e.Message == nil {
				continue
			}
			for _, c := range e.Message.Content {
				if c.Type != "tool_result" {
					continue
				}
				name := toolNames[c.ToolUseID]
				if name == "" {
					name = "unknown"
				}
				tokens := resultTokens(c.Content)
				g, exists := byTool[name]
				if !exists {
					g = &group{}
					byTool[name] = g
				}
				g.tokens += tokens
				g.count++
				totalTokens += tokens
				totalCount++
				resultedIDs[c.ToolUseID] = true
				if c.ToolUseID == pendingToolSearchID {
					pendingToolSearchID = ""
				}
			}
		case "attachment":
			if e.Attachment == nil || e.Attachment.Type != "deferred_tools_delta" {
				continue
			}
			if pendingToolSearchID == "" {
				continue
			}
			// Synthesize a ToolSearch tool_result from the delta's added
			// lines (or names) — that's exactly the payload the model sees.
			payload := strings.Join(e.Attachment.AddedLines, "\n")
			if payload == "" {
				payload = strings.Join(e.Attachment.AddedNames, "\n")
			}
			if payload == "" {
				pendingToolSearchID = ""
				continue
			}
			tokens := tokEstimateAs(payload, "deferred_tools_payload")
			g, exists := byTool["ToolSearch"]
			if !exists {
				g = &group{}
				byTool["ToolSearch"] = g
			}
			g.tokens += tokens
			g.count++
			totalTokens += tokens
			totalCount++
			resultedIDs[pendingToolSearchID] = true
			pendingToolSearchID = ""
		}
	}
	if totalCount == 0 {
		return nil
	}

	byToolOut := make([]map[string]interface{}, 0, len(byTool))
	for name, g := range byTool {
		byToolOut = append(byToolOut, map[string]interface{}{
			"name":   name,
			"tokens": g.tokens,
			"count":  g.count,
		})
	}
	sort.Slice(byToolOut, func(i, j int) bool {
		return byToolOut[i]["tokens"].(int) > byToolOut[j]["tokens"].(int)
	})
	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_tokens": totalTokens,
			"count":        totalCount,
		},
		"by_tool": byToolOut,
	}
}

// resultTokens estimates the token cost of a tool_result.content payload,
// which may be a plain string or an array of `{type:"text", text:"…"}`
// blocks (or anything else, in which case we fall back to the raw JSON
// size as a proxy).
func resultTokens(content interface{}) int {
	switch v := content.(type) {
	case string:
		return tokEstimateAs(v, "tool_result")
	case []interface{}:
		total := 0
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if t, ok := m["text"].(string); ok {
					total += tokEstimateAs(t, "tool_result")
					continue
				}
			}
			raw, _ := json.Marshal(item)
			total += tokEstimateAs(string(raw), "tool_result")
		}
		return total
	default:
		raw, _ := json.Marshal(v)
		return tokEstimateAs(string(raw), "tool_result")
	}
}

// extractUserPromptsSnapshot returns the per-turn user-text contribution.
// Tool results don't count here (they're under cc.tool_results); skill
// bodies don't count either (they're under cc.skills.loaded). Without
// excluding skill bodies, a `Skill` tool_use would inflate user_prompts
// by the entire skill body — 100K+ tokens for claude-api.
// `cc.user_prompts.summary`.
func extractUserPromptsSnapshot(entries []TranscriptEntry) map[string]interface{} {
	skillBodyTexts := skillBodyTextSet(entries)

	totalTokens, count := 0, 0
	for _, e := range entries {
		if e.Type != "user" || e.Message == nil {
			continue
		}
		for _, c := range e.Message.Content {
			if c.Type != "text" {
				continue
			}
			if skillBodyTexts[c.Text] {
				continue
			}
			totalTokens += tokEstimateAs(c.Text, "user_prompt")
			count++
		}
	}
	if count == 0 {
		return nil
	}
	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_tokens": totalTokens,
			"count":        count,
			"bucket":       promptBucket(totalTokens),
		},
	}
}

// skillBodyTextSet returns the set of user-text strings that are skill
// bodies (the N+2 text after a Skill tool_use → tool_result pair).
// Callers use this to avoid counting skill bodies as user prompts.
func skillBodyTextSet(entries []TranscriptEntry) map[string]bool {
	bodies := buildSkillBodyMap(entries)
	out := make(map[string]bool, len(bodies))
	for _, body := range bodies {
		if body != "" {
			out[body] = true
		}
	}
	return out
}

func promptBucket(tokens int) string {
	switch {
	case tokens > 8000:
		return "xlarge"
	case tokens > 2000:
		return "large"
	case tokens > 500:
		return "medium"
	default:
		return "small"
	}
}

// extractFileAttachmentsSnapshot returns @-mentioned + system-injected file
// attachments this turn. Skill bodies are NOT here — they go under
// cc.skills.loaded. `cc.file_attachments.{summary, files}`.
func extractFileAttachmentsSnapshot(entries []TranscriptEntry) map[string]interface{} {
	files := []map[string]interface{}{}
	total := 0
	for _, e := range entries {
		if e.Type != "attachment" || e.Attachment == nil {
			continue
		}
		if e.Attachment.Type != "file" {
			continue
		}
		// File attachment shape: attachment.content is a JSON object with
		// a nested file.content string. The struct treats Content as
		// RawMessage so we decode lazily here.
		var wrapper struct {
			File struct {
				Path    string `json:"path,omitempty"`
				Content string `json:"content,omitempty"`
			} `json:"file"`
		}
		if err := json.Unmarshal(e.Attachment.Content, &wrapper); err != nil {
			continue
		}
		body := wrapper.File.Content
		// Auto-detect — file attachments vary (source code, markdown, JSON, …).
		tokens := tokEstimate(body)
		files = append(files, map[string]interface{}{
			"path":         wrapper.File.Path,
			"sha256":       sha256hex(body),
			"body_tokens":  tokens,
			"content_type": "source", // bucket classification deferred — single bucket for now
		})
		total += tokens
	}
	if len(files) == 0 {
		return nil
	}
	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_tokens": total,
			"file_count":   len(files),
		},
		"files": files,
	}
}

// extractPriorAssistantSnapshot is the cumulative cost of prior assistant
// output in the session — what gets replayed every turn.
// `cc.prior_assistant.summary`.
func extractPriorAssistantSnapshot(fullEntries, turnEntries []TranscriptEntry) map[string]interface{} {
	sessionTokens, sessionMsgs := assistantOutputTotals(fullEntries)
	turnTokens, turnMsgs := assistantOutputTotals(turnEntries)
	priorTokens := sessionTokens - turnTokens
	priorMsgs := sessionMsgs - turnMsgs
	if priorTokens < 0 {
		priorTokens = 0
	}
	if priorMsgs < 0 {
		priorMsgs = 0
	}
	if priorTokens == 0 && priorMsgs == 0 {
		return nil
	}
	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_tokens":  priorTokens,
			"message_count": priorMsgs,
		},
	}
}

func assistantOutputTotals(entries []TranscriptEntry) (tokens, msgs int) {
	for _, e := range entries {
		if e.Type != "assistant" || e.Message == nil || e.Message.Usage == nil {
			continue
		}
		tokens += e.Message.Usage.OutputTokens
		msgs++
	}
	return
}

// extractAssistantTextSnapshot returns the per-turn text-block contribution.
// `cc.assistant_text.summary`.
func extractAssistantTextSnapshot(entries []TranscriptEntry) map[string]interface{} {
	total, count := 0, 0
	for _, e := range entries {
		if e.Type != "assistant" || e.Message == nil {
			continue
		}
		for _, c := range e.Message.Content {
			if c.Type == "text" {
				total += tokEstimateAs(c.Text, "assistant_text")
				count++
			}
		}
	}
	if count == 0 {
		return nil
	}
	return map[string]interface{}{
		"summary": map[string]interface{}{
			"total_tokens": total,
			"block_count":  count,
		},
	}
}
