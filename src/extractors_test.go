package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestExtractAgentsSnapshotPrefersFrontmatterName(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_PROJECT_DIR", cwd)

	// File on disk is meta-auditor.md, but its frontmatter announces the
	// agent as `config-auditor`. /context uses the frontmatter name, so we
	// must too — otherwise dashboards drift from what users see.
	writeFile(t, filepath.Join(cwd, ".claude", "agents", "meta-auditor.md"), `---
name: config-auditor
description: |
  Audits Claude config files for duplication.
---
# Body — not counted toward the always-on cost
`)

	snap := extractAgentsSnapshot()
	if snap == nil {
		t.Fatal("expected agents snapshot")
	}
	agents, _ := snap["agents"].([]map[string]interface{})
	if len(agents) != 1 {
		t.Fatalf("want 1 agent, got %d", len(agents))
	}
	if name, _ := agents[0]["name"].(string); name != "config-auditor" {
		t.Errorf("name = %q, want config-auditor", name)
	}
}

func TestExtractAgentsSnapshotFiltersDisabledPlugins(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_PROJECT_DIR", cwd)

	// Two plugins on disk via installed_plugins.json — only one enabled
	// via the settings layer. Disabled plugin's agent must NOT appear.
	enabledPlugin := filepath.Join(home, "plugins", "good")
	disabledPlugin := filepath.Join(home, "plugins", "bad")
	writeFile(t, filepath.Join(enabledPlugin, "agents", "real-reviewer.md"), `---
name: real-reviewer
description: Active reviewer.
---
`)
	writeFile(t, filepath.Join(disabledPlugin, "agents", "ghost.md"), `---
name: ghost
description: Should not show up.
---
`)

	manifest := map[string]interface{}{
		"plugins": map[string]interface{}{
			"good@mp": []interface{}{map[string]interface{}{"installPath": enabledPlugin}},
			"bad@mp":  []interface{}{map[string]interface{}{"installPath": disabledPlugin}},
		},
	}
	mb, _ := json.Marshal(manifest)
	writeFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), string(mb))

	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{
		"enabledPlugins": {"good@mp": true, "bad@mp": false}
	}`)

	snap := extractAgentsSnapshot()
	if snap == nil {
		t.Fatal("expected agents snapshot")
	}
	agents, _ := snap["agents"].([]map[string]interface{})

	names := map[string]bool{}
	for _, a := range agents {
		if n, ok := a["name"].(string); ok {
			names[n] = true
		}
	}
	if !names["good:real-reviewer"] {
		t.Errorf("good:real-reviewer should be present, got %v", names)
	}
	if names["bad:ghost"] {
		t.Errorf("bad:ghost must be filtered out (disabled plugin), got %v", names)
	}
}

