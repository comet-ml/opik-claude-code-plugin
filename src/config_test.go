package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandProjectTemplate(t *testing.T) {
	id := ClaudeIdentity{
		UserUUID:    "uuid-123",
		Email:       "collinc@comet.com",
		DisplayName: "Collin",
		OrgUUID:     "org-456",
		OrgName:     "Comet",
	}

	cases := []struct {
		name     string
		template string
		identity ClaudeIdentity
		want     string
	}{
		{"no tokens passes through", "claude-code", id, "claude-code"},
		{"username strips domain", "cc-{username}", id, "cc-collinc"},
		{"full email token", "cc-{email}", id, "cc-collinc@comet.com"},
		{"user_email alias", "cc-{user_email}", id, "cc-collinc@comet.com"},
		{"user_uuid token", "{user_uuid}", id, "uuid-123"},
		{"display_name token", "{display_name}", id, "Collin"},
		{"org_name token", "{org_name}-cc", id, "Comet-cc"},
		{"org_uuid token", "{org_uuid}", id, "org-456"},
		{"multiple tokens compose", "{org_name}/{username}", id, "Comet/collinc"},
		{"unknown token left as-is", "cc-{not_a_field}", id, "cc-{not_a_field}"},
		{"email without @ uses full value", "cc-{username}", ClaudeIdentity{Email: "noatsign"}, "cc-noatsign"},
		{"empty identity yields empty values", "cc-{username}-{email}", ClaudeIdentity{}, "cc--"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expandProjectTemplate(tc.template, tc.identity)
			if got != tc.want {
				t.Errorf("expandProjectTemplate(%q) = %q, want %q", tc.template, got, tc.want)
			}
		})
	}
}

// writeFile creates a file with the given content, failing the test on error.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGetTracingState(t *testing.T) {
	// Build a fresh project + home dir per subtest so file fallbacks are
	// deterministic and isolated. t.Setenv restores env after each subtest.
	cases := []struct {
		name        string
		envVar      string // OPIK_CC_TRACING_ENABLED value; "" means unset
		projectFile string // contents to write to <projectDir>/.claude/.opik-tracing-enabled, "" to skip
		userFile    string // contents to write to <home>/.claude/.opik-tracing-enabled, "" to skip
		wantEnabled bool
	}{
		{name: "env true enables", envVar: "true", wantEnabled: true},
		{name: "env 1 enables", envVar: "1", wantEnabled: true},
		{name: "env TRUE case insensitive", envVar: "TRUE", wantEnabled: true},
		{name: "env false disables", envVar: "false", wantEnabled: false},
		{name: "env 0 disables", envVar: "0", wantEnabled: false},
		{name: "no signals defaults disabled", wantEnabled: false},
		{name: "user file enables when env unset", userFile: "on", wantEnabled: true},
		{name: "project file enables", projectFile: "on", wantEnabled: true},
		{name: "project file off overrides env true", envVar: "true", projectFile: "off", wantEnabled: false},
		{name: "project file off overrides user enable", projectFile: "off", userFile: "on", wantEnabled: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projectDir := t.TempDir()
			homeDir := t.TempDir()
			t.Setenv("CLAUDE_PROJECT_DIR", projectDir)
			t.Setenv("HOME", homeDir)
			t.Setenv("OPIK_CC_TRACING_ENABLED", tc.envVar)

			if tc.projectFile != "" {
				writeFile(t, filepath.Join(projectDir, ".claude", ".opik-tracing-enabled"), tc.projectFile)
			}
			if tc.userFile != "" {
				writeFile(t, filepath.Join(homeDir, ".claude", ".opik-tracing-enabled"), tc.userFile)
			}

			state := getTracingState()
			if state.enabled != tc.wantEnabled {
				t.Errorf("enabled = %v, want %v", state.enabled, tc.wantEnabled)
			}
		})
	}
}
