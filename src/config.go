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

	cfg := &Config{
		URL:           strings.TrimSuffix(url, "/") + "/v1/private",
		Project:       "claude-code",
		APIKey:        getEnvOrConfig("OPIK_API_KEY", fileConfig, "api_key"),
		Workspace:     getEnvOrConfig("OPIK_WORKSPACE", fileConfig, "workspace"),
		Debug:         os.Getenv("OPIK_CC_DEBUG") == "true",
		Truncate:      os.Getenv("OPIK_CC_TRUNCATE_FIELDS") != "false",
		Enabled:       isTracingEnabled(),
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

// isTracingEnabled checks if tracing is enabled via state files.
// Precedence: project-level > user-level > default (false)
func isTracingEnabled() bool {
	// Check project-level first (current working directory)
	if data, err := os.ReadFile(".claude/.opik-tracing-enabled"); err == nil {
		return strings.TrimSpace(string(data)) == "true"
	}

	// Fall back to user-level
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	if data, err := os.ReadFile(filepath.Join(homeDir, ".claude", ".opik-tracing-enabled")); err == nil {
		return strings.TrimSpace(string(data)) == "true"
	}

	return false
}
