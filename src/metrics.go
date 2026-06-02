package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// EditAggregate accumulates per-trace edit data as we walk the transcript.
type EditAggregate struct {
	Files            map[string]struct{}
	LinesAuthored    int
	LinesOverwritten int
}

// aggregateEdits walks transcript entries from state.StartLine forward and
// returns counts for Edit/Write/MultiEdit tool calls Claude made in this trace.
func aggregateEdits(state *State) *EditAggregate {
	agg := &EditAggregate{Files: map[string]struct{}{}}
	entries, err := ReadTranscript(state.Transcript, state.StartLine)
	if err != nil {
		return agg
	}

	for _, entry := range entries {
		if entry.Type != "assistant" || entry.Message == nil {
			continue
		}
		for _, c := range entry.Message.Content {
			if c.Type != "tool_use" {
				continue
			}
			switch c.Name {
			case "Edit":
				path, _ := c.Input["file_path"].(string)
				newS, _ := c.Input["new_string"].(string)
				oldS, _ := c.Input["old_string"].(string)
				addEdit(agg, path, oldS, newS)
			case "Write":
				path, _ := c.Input["file_path"].(string)
				content, _ := c.Input["content"].(string)
				addEdit(agg, path, "", content)
			case "MultiEdit":
				path, _ := c.Input["file_path"].(string)
				edits, _ := c.Input["edits"].([]interface{})
				for _, re := range edits {
					m, ok := re.(map[string]interface{})
					if !ok {
						continue
					}
					newS, _ := m["new_string"].(string)
					oldS, _ := m["old_string"].(string)
					addEdit(agg, path, oldS, newS)
				}
			}
		}
	}
	return agg
}

func addEdit(agg *EditAggregate, path, oldS, newS string) {
	if path != "" {
		agg.Files[path] = struct{}{}
	}
	agg.LinesAuthored += stringLines(newS)
	agg.LinesOverwritten += stringLines(oldS)
}

func stringLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// git runs `git <args>` in cwd and returns trimmed stdout (empty on failure).
func git(cwd string, args ...string) string {
	if cwd == "" {
		return ""
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// parseShortstat extracts (files, insertions, deletions) from
// `1 file changed, 5 insertions(+), 3 deletions(-)`.
func parseShortstat(line string) (files, ins, dels int) {
	for _, part := range strings.Split(line, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		head := strings.SplitN(part, " ", 2)[0]
		n, err := strconv.Atoi(head)
		if err != nil {
			continue
		}
		switch {
		case strings.Contains(part, "file"):
			files = n
		case strings.Contains(part, "insertion"):
			ins = n
		case strings.Contains(part, "deletion"):
			dels = n
		}
	}
	return
}

// repoName normalizes the origin remote URL to host/org/repo (no scheme, no .git).
func repoName(cwd string) string {
	url := git(cwd, "remote", "get-url", "origin")
	if url == "" {
		return ""
	}
	var host, path string
	if strings.HasPrefix(url, "git@") {
		_, after, _ := strings.Cut(url, ":")
		host = strings.SplitN(url[4:], ":", 2)[0]
		path = after
	} else {
		rest := url
		if i := strings.Index(rest, "://"); i >= 0 {
			rest = rest[i+3:]
		}
		idx := strings.Index(rest, "/")
		if idx < 0 {
			return ""
		}
		host = rest[:idx]
		path = rest[idx+1:]
	}
	path = strings.TrimSuffix(path, ".git")
	return strings.Trim(host+"/"+path, "/")
}

// commitsBetween counts commits and total insertions/deletions between
// startSHA..endSHA. Returns zeros if either SHA is empty or they're identical.
func commitsBetween(cwd, startSHA, endSHA string) (count, ins, dels int) {
	if startSHA == "" || endSHA == "" || startSHA == endSHA {
		return 0, 0, 0
	}
	out := git(cwd, "log", startSHA+".."+endSHA, "--shortstat", "--format=__COMMIT__%H")
	if out == "" {
		return 0, 0, 0
	}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "__COMMIT__") {
			count++
		} else if strings.Contains(line, "file") && (strings.Contains(line, "insertion") || strings.Contains(line, "deletion")) {
			_, i, d := parseShortstat(line)
			ins += i
			dels += d
		}
	}
	return
}

// captureCwdAndHead returns the working directory + current HEAD SHA. Used at
// onPrompt to anchor the trace. Falls back through CLAUDE_PROJECT_DIR → Getwd().
func captureCwdAndHead() (cwd, head string) {
	cwd = inferCwd()
	head = git(cwd, "rev-parse", "HEAD")
	return
}

// putScore PUTs a single feedback score onto a trace. Errors are logged and
// swallowed — metric posting must never block trace flow.
func putScore(traceID, name string, value float64, category string) {
	body := map[string]interface{}{
		"name":   name,
		"value":  value,
		"source": "sdk",
	}
	if category != "" {
		if len(category) > 300 {
			category = category[:300]
		}
		body["category_name"] = category
	}
	if err := api.Put("/traces/"+traceID+"/feedback-scores", body); err != nil {
		debugLog("put_score %s=%v: %v", name, value, err)
	}
}

// postTraceMetrics computes git-grounded metrics for the closing trace and
// posts them as feedback scores. Cheap (~few hundred ms total).
func postTraceMetrics(state *State) {
	cwd := state.Cwd
	if cwd == "" {
		cwd = inferCwd()
	}
	if cwd == "" {
		debugLog("postTraceMetrics: no cwd")
		return
	}
	// Bail if not a git working tree.
	if git(cwd, "rev-parse", "--is-inside-work-tree") != "true" {
		debugLog("postTraceMetrics: %s is not a git work tree", cwd)
		return
	}

	agg := aggregateEdits(state)

	repo := repoName(cwd)
	branch := git(cwd, "branch", "--show-current")
	headEnd := git(cwd, "rev-parse", "HEAD")
	commits, insC, delC := commitsBetween(cwd, state.HeadSHAStart, headEnd)
	filesU, insU, delU := parseShortstat(git(cwd, "diff", "HEAD", "--shortstat"))

	if repo != "" {
		putScore(state.TraceID, "cc.repository", 1, repo)
	}
	if branch != "" {
		putScore(state.TraceID, "cc.branch", 1, branch)
	}
	if state.HeadSHAStart != "" {
		putScore(state.TraceID, "cc.head_sha_start", 1, shortSHA(state.HeadSHAStart))
	}
	if headEnd != "" {
		putScore(state.TraceID, "cc.head_sha_end", 1, shortSHA(headEnd))
	}
	putScore(state.TraceID, "cc.commits_in_trace", float64(commits), "")
	putScore(state.TraceID, "cc.lines_committed", float64(insC+delC), "")
	putScore(state.TraceID, "cc.uncommitted_lines", float64(insU+delU), "")
	putScore(state.TraceID, "cc.uncommitted_files", float64(filesU), "")
	putScore(state.TraceID, "cc.files_authored", float64(len(agg.Files)), "")
	putScore(state.TraceID, "cc.lines_authored", float64(agg.LinesAuthored), "")
	putScore(state.TraceID, "cc.lines_overwritten", float64(agg.LinesOverwritten), "")

	debugLog("metrics %s  repo=%s  branch=%s  commits=%d  +-=%d  uncommitted=%d  files=%d  authored=%d  overwritten=%d",
		state.TraceID[:8], repo, branch, commits, insC+delC, insU+delU, len(agg.Files), agg.LinesAuthored, agg.LinesOverwritten)
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// inferCwd prefers CLAUDE_PROJECT_DIR, falls back to os.Getwd().
func inferCwd() string {
	if d := os.Getenv("CLAUDE_PROJECT_DIR"); d != "" {
		return d
	}
	if d, err := os.Getwd(); err == nil {
		return d
	}
	return ""
}