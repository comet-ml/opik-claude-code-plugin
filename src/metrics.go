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

// postTraceMetrics computes per-trace metadata for the closing trace and
// merges it into trace.metadata.cc. Git-grounded metrics are best-effort —
// they only land when cwd is inside a git work tree — while the per-domain
// transcript snapshots (skills, tools, memory, …) always run because they
// read only the transcript and the user's filesystem, not git.
func postTraceMetrics(state *State) {
	cwd := state.Cwd
	if cwd == "" {
		cwd = inferCwd()
	}

	metrics := map[string]interface{}{}

	var repo, branch string
	var commits, insC, delC int
	var agg *EditAggregate
	if cwd != "" && git(cwd, "rev-parse", "--is-inside-work-tree") == "true" {
		agg = aggregateEdits(state)

		repo = repoName(cwd)
		branch = git(cwd, "branch", "--show-current")
		// HEAD as of trace close; pairs with state.HeadSHAStart captured at onPrompt.
		headEnd := git(cwd, "rev-parse", "HEAD")
		commits, insC, delC = commitsBetween(cwd, state.HeadSHAStart, headEnd)
		filesU, insU, delU := parseShortstat(git(cwd, "diff", "HEAD", "--shortstat"))

		gitMetrics := map[string]interface{}{
			"commits_in_trace":  commits,
			"lines_committed":   insC + delC,
			"uncommitted_lines": insU + delU,
			"uncommitted_files": filesU,
			"files_authored":    len(agg.Files),
			"lines_authored":    agg.LinesAuthored,
			"lines_overwritten": agg.LinesOverwritten,
		}
		if repo != "" {
			gitMetrics["repository"] = repo
		}
		if branch != "" {
			gitMetrics["branch"] = branch
		}
		if state.HeadSHAStart != "" {
			gitMetrics["head_sha_start"] = shortSHA(state.HeadSHAStart)
		}
		if headEnd != "" {
			gitMetrics["head_sha_end"] = shortSHA(headEnd)
		}
		metrics["git"] = gitMetrics
	} else {
		debugLog("postTraceMetrics: skipping git block (cwd=%q not a git work tree)", cwd)
	}

	fullEntries, _ := ReadTranscript(state.Transcript, 0)
	turnEntries, _ := ReadTranscript(state.Transcript, state.StartLine)
	for domain, snap := range domainSnapshotsFromEntries(fullEntries, turnEntries) {
		if snap != nil {
			metrics[domain] = snap
		}
	}

	if len(metrics) == 0 {
		return
	}

	mergeMetadataCC(state.TraceID, metrics)

	var files, authored, overwritten int
	if agg != nil {
		files, authored, overwritten = len(agg.Files), agg.LinesAuthored, agg.LinesOverwritten
	}
	debugLog("metrics %s  repo=%s  branch=%s  commits=%d  +-=%d  files=%d  authored=%d  overwritten=%d  domains=%d",
		state.TraceID[:8], repo, branch, commits, insC+delC, files, authored, overwritten, len(metrics))
}


// mergeMetadataCC reads the trace's current metadata, merges new keys into
// the `cc` block (preserving identity already written at trace creation and
// any non-cc metadata Opik may have added), and PATCHes the trace.
func mergeMetadataCC(traceID string, addCC map[string]interface{}) {
	var trace struct {
		Metadata map[string]interface{} `json:"metadata"`
	}
	if err := api.Get("/traces/"+traceID, &trace); err != nil {
		debugLog("mergeMetadataCC get: %v", err)
		// Best-effort: still PATCH our keys under cc with no merge.
		trace.Metadata = map[string]interface{}{}
	}
	if trace.Metadata == nil {
		trace.Metadata = map[string]interface{}{}
	}
	existingCC, _ := trace.Metadata["cc"].(map[string]interface{})
	if existingCC == nil {
		existingCC = map[string]interface{}{}
	}
	for k, v := range addCC {
		existingCC[k] = v
	}
	trace.Metadata["cc"] = existingCC
	if err := api.Patch("/traces/"+traceID, map[string]interface{}{
		"project_name": config.Project,
		"metadata":     trace.Metadata,
	}); err != nil {
		debugLog("mergeMetadataCC patch: %v", err)
	}
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