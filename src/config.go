package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	URL           string
	Project       string
	APIKey        string
	Workspace     string
	Debug         bool
	Truncate      bool
	Enabled       bool
	ParentTraceID string
	RootSpanID    string
}

const truncateMsg = "[ TRUNCATED -- set OPIK_CC_TRUNCATE_FIELDS=false ]"

func LoadConfig() (*Config, error) {
	homeDir, _ := os.UserHomeDir()
	var fileConfig map[string]string
	if homeDir != "" {
		fileConfig = parseConfigFile(filepath.Join(homeDir, ".opik.config"))
	}

	url := getEnvOrConfig("OPIK_BASE_URL", fileConfig, "url_override")
	if url == "" {
		return nil, nil
	}

	tracing := getTracingState()

	cfg := &Config{
		URL:           strings.TrimSuffix(url, "/") + "/v1/private",
		Project:       "claude-code",
		APIKey:        getEnvOrConfig("OPIK_API_KEY", fileConfig, "api_key"),
		Workspace:     getEnvOrConfig("OPIK_WORKSPACE", fileConfig, "workspace"),
		Debug:         os.Getenv("OPIK_CC_DEBUG") == "true" || tracing.debug,
		Truncate:      os.Getenv("OPIK_CC_TRUNCATE_FIELDS") != "false",
		Enabled:       tracing.enabled,
		ParentTraceID: os.Getenv("OPIK_CC_PARENT_TRACE_ID"),
		RootSpanID:    os.Getenv("OPIK_CC_ROOT_SPAN_ID"),
	}

	// OPIK_CC_PROJECT / cc_project are plugin-scoped and don't affect the Opik
	// SDK. project_name is kept as a fallback for backward compatibility, but it
	// is shared with the Opik SDK config in ~/.opik.config.
	if proj := getEnvOrConfig("OPIK_CC_PROJECT", fileConfig, "cc_project_name"); proj != "" {
		// {field} tokens resolve against Claude's OAuth identity so admins can
		// route per-user projects (e.g. "cc-{username}") from managed settings.
		cfg.Project = expandProjectTemplate(proj, loadClaudeIdentity())
	} else if proj := fileConfig["project_name"]; proj != "" {
		cfg.Project = proj
	}

	// Allow overriding the workspace for the Claude Code plugin only, without
	// affecting the global OPIK_WORKSPACE / ~/.opik.config used by the Opik SDK.
	if ws := getEnvOrConfig("OPIK_CC_WORKSPACE", fileConfig, "cc_workspace"); ws != "" {
		cfg.Workspace = ws
	}

	return cfg, nil
}

func parseConfigFile(path string) map[string]string {
	result := make(map[string]string)
	file, err := os.Open(path)
	if err != nil {
		return result
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if parts := strings.SplitN(line, "=", 2); len(parts) == 2 {
			result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return result
}

func getEnvOrConfig(envVar string, fileConfig map[string]string, configKey string) string {
	if val := os.Getenv(envVar); val != "" {
		return val
	}
	return fileConfig[configKey]
}

type tracingState struct {
	enabled bool
	debug   bool
}

func checkTracingFile(path string) (tracingState, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return tracingState{}, false
	}
	content := strings.TrimSpace(string(data))
	// Explicit opt-out: lets a single project turn tracing off even when it is
	// enabled globally via ~/.claude/.opik-tracing-enabled.
	if content == "off" || content == "disabled" {
		return tracingState{enabled: false}, true
	}
	return tracingState{enabled: true, debug: content == "debug"}, true
}

func getTracingState() tracingState {
	// Project-level marker takes precedence, including an explicit "off" opt-out
	// that wins over a global enable.
	if projectDir := os.Getenv("CLAUDE_PROJECT_DIR"); projectDir != "" {
		if state, found := checkTracingFile(filepath.Join(projectDir, ".claude", ".opik-tracing-enabled")); found {
			return state
		}
	}

	// Org-wide enable/disable via managed settings env. Sits between project-
	// level (explicit per-repo opt-out still wins) and user-level files.
	if v := os.Getenv("OPIK_CC_TRACING_ENABLED"); v != "" {
		return tracingState{enabled: strings.EqualFold(v, "true") || v == "1"}
	}

	// Fall back to a user-level marker so tracing can be enabled for all projects.
	if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
		if state, found := checkTracingFile(filepath.Join(homeDir, ".claude", ".opik-tracing-enabled")); found {
			return state
		}
	}

	return tracingState{}
}

// expandProjectTemplate substitutes {field} tokens in the project name from
// the loaded Claude identity. Unknown tokens are left as-is so misconfigs are
// visible in Opik instead of silently producing a half-empty name.
func expandProjectTemplate(template string, id ClaudeIdentity) string {
	if !strings.ContainsRune(template, '{') {
		return template
	}
	username := id.Email
	if at := strings.IndexByte(username, '@'); at > 0 {
		username = username[:at]
	}
	replacements := map[string]string{
		"{email}":        id.Email,
		"{user_email}":   id.Email,
		"{username}":     username,
		"{user_uuid}":    id.UserUUID,
		"{display_name}": id.DisplayName,
		"{org_name}":     id.OrgName,
		"{org_uuid}":     id.OrgUUID,
	}
	for token, value := range replacements {
		template = strings.ReplaceAll(template, token, value)
	}
	return template
}
