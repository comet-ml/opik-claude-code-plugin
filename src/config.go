package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Config holds the Opik configuration
type Config struct {
	URL       string
	Project   string
	APIKey    string
	Workspace string
	Debug     bool
	Truncate  bool
}

const truncateMsg = "[ TRUNCATED -- set OPIK_CC_TRUNCATE_FIELDS=false ]"

// LoadConfig loads configuration from environment variables and ~/.opik.config
func LoadConfig() *Config {
	cfg := &Config{
		Project:  "claude-code",
		Truncate: true,
	}

	// Load from config file first
	homeDir, _ := os.UserHomeDir()
	configPath := filepath.Join(homeDir, ".opik.config")
	fileConfig := parseConfigFile(configPath)

	// URL (required)
	cfg.URL = getEnvOrConfig("OPIK_BASE_URL", fileConfig, "url_override")
	if cfg.URL == "" {
		return nil
	}
	cfg.URL = strings.TrimSuffix(cfg.URL, "/") + "/v1/private"

	// Project
	if proj := getEnvOrConfig("OPIK_PROJECT", fileConfig, "project_name"); proj != "" {
		cfg.Project = proj
	}

	// API Key
	cfg.APIKey = getEnvOrConfig("OPIK_API_KEY", fileConfig, "api_key")

	// Workspace
	cfg.Workspace = getEnvOrConfig("OPIK_WORKSPACE", fileConfig, "workspace")

	// Debug
	cfg.Debug = os.Getenv("OPIK_CC_DEBUG") == "true"

	// Truncate
	if os.Getenv("OPIK_CC_TRUNCATE_FIELDS") == "false" {
		cfg.Truncate = false
	}

	return cfg
}

// parseConfigFile reads key=value pairs from a config file
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
		
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			result[key] = value
		}
	}
	
	return result
}

// getEnvOrConfig returns env var value or falls back to config file
func getEnvOrConfig(envVar string, fileConfig map[string]string, configKey string) string {
	if val := os.Getenv(envVar); val != "" {
		return val
	}
	return fileConfig[configKey]
}
