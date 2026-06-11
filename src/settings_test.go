package main

import (
	"path/filepath"
	"testing"
)

func TestReadEnabledPluginsField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeFile(t, path, `{
		"enabledPlugins": {
			"on@mp": true,
			"off@mp": false,
			"strtrue@mp": "true",
			"str1@mp": "1",
			"strfalse@mp": "false",
			"weird@mp": 42
		}
	}`)
	got := readEnabledPluginsField(path)
	wantEnabled := map[string]bool{
		"on@mp":       true,
		"off@mp":      false,
		"strtrue@mp":  true,
		"str1@mp":     true,
		"strfalse@mp": false,
	}
	for k, want := range wantEnabled {
		if got[k] != want {
			t.Errorf("%s: got %v, want %v", k, got[k], want)
		}
	}
	if _, ok := got["weird@mp"]; ok {
		t.Errorf("numeric values should be skipped, got %v", got["weird@mp"])
	}

	// Missing file → nil/empty map.
	if r := readEnabledPluginsField(filepath.Join(dir, "missing.json")); len(r) != 0 {
		t.Errorf("missing file should yield empty map, got %v", r)
	}
}

func TestReadEnabledPluginNamesLayering(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	// User layer: disables plugin-dev, leaves opik unset.
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{
		"enabledPlugins": {"plugin-dev@cpo": false, "stale@cpo": true}
	}`)
	// User local layer: enables opik.
	writeFile(t, filepath.Join(home, ".claude", "settings.local.json"), `{
		"enabledPlugins": {"opik@opik": true}
	}`)
	// Project layer overrides stale → off.
	writeFile(t, filepath.Join(cwd, ".claude", "settings.json"), `{
		"enabledPlugins": {"stale@cpo": false}
	}`)

	enabled := readEnabledPluginNames(home, cwd)

	if !enabled["opik"] {
		t.Errorf("opik should be enabled, got %v", enabled)
	}
	if enabled["plugin-dev"] {
		t.Errorf("plugin-dev should be disabled, got %v", enabled)
	}
	if enabled["stale"] {
		t.Errorf("project layer should win, stale should be disabled, got %v", enabled)
	}
}
