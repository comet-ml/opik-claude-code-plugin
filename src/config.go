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

// LoadConfig loads configuration from environment variables and ~/.opik.config.
// Returns (nil, nil) if OPIK_BASE_URL is not set (plugin disabled).
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

	if proj := getEnvOrConfig("OPIK_CC_PROJECT", fileConfig, "project_name"); proj != "" {
		cfg.Project = proj
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

// tracingState holds the result of checking the tracing file
type tracingState struct {
	enabled bool
	debug   bool
}

// checkTracingFile checks a single tracing file and returns its state
func checkTracingFile(path string) (tracingState, bool) {
	if _, err := os.Stat(path); err == nil {
		// File exists = tracing enabled
		state := tracingState{enabled: true}
		// Check if content is "debug"
		if data, err := os.ReadFile(path); err == nil {
			state.debug = strings.TrimSpace(string(data)) == "debug"
		}
		return state, true
	}
	return tracingState{}, false
}

// getTracingState checks tracing state from state files.
// Precedence: project-level > user-level > default (disabled)
func getTracingState() tracingState {
	// Check project-level first (current working directory)
	if state, found := checkTracingFile(".claude/.opik-tracing-enabled"); found {
		return state
	}

	// Fall back to user-level
	if homeDir, err := os.UserHomeDir(); err == nil {
		if state, found := checkTracingFile(filepath.Join(homeDir, ".claude", ".opik-tracing-enabled")); found {
			return state
		}
	}

	return tracingState{}
}
