package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// enabledPluginNames returns the set of plugin **short names** (the part
// before `@` in the installed_plugins.json key) that are enabled for the
// current session, derived from the layered settings.json files.
//
// Why this matters: installed_plugins.json lists every plugin install on
// disk, but `enabledPlugins[<plugin>@<marketplace>]` in settings decides
// which actually load. Reading the manifest alone surfaces ghost agents
// from disabled plugins (e.g. `plugin-dev@claude-plugins-official`).
//
// Resolution order (Claude Code's documented layering — later wins):
//
//   1. Managed (org-pushed)         OS-specific (see managedSettingsPaths)
//   2. User                         ~/.claude/settings.json
//   3. User local                   ~/.claude/settings.local.json
//   4. Project                      <cwd>/.claude/settings.json
//   5. Project local                <cwd>/.claude/settings.local.json
//
// A plugin counts as enabled iff the merged map's value is true (boolean
// or "true"/"1" string). Anything else — including unset, false, or an
// older shape — counts as disabled, matching what /context displays.
//
// Memoized per (home,cwd) tuple: settings don't change between the hook
// reading them and the trace flushing, and tests can vary the pair to
// exercise different layered configurations without leaking state.
var (
	enabledPluginsMu   sync.Mutex
	enabledPluginsMemo map[string]map[string]bool
)

func enabledPluginNames(home, cwd string) map[string]bool {
	key := home + "\x00" + cwd
	enabledPluginsMu.Lock()
	defer enabledPluginsMu.Unlock()
	if enabledPluginsMemo == nil {
		enabledPluginsMemo = map[string]map[string]bool{}
	}
	if v, ok := enabledPluginsMemo[key]; ok {
		return v
	}
	v := readEnabledPluginNames(home, cwd)
	enabledPluginsMemo[key] = v
	return v
}

func readEnabledPluginNames(home, cwd string) map[string]bool {
	merged := map[string]bool{}
	for _, p := range settingsFilePaths(home, cwd) {
		for key, v := range readEnabledPluginsField(p) {
			merged[key] = v
		}
	}
	out := map[string]bool{}
	for key, enabled := range merged {
		if !enabled {
			continue
		}
		plugin, _, _ := strings.Cut(key, "@")
		if plugin == "" {
			continue
		}
		out[plugin] = true
	}
	return out
}

// settingsFilePaths returns the layered settings file paths in precedence
// order (lowest → highest, so later writes win during merge).
func settingsFilePaths(home, cwd string) []string {
	paths := managedSettingsPaths()
	if home != "" {
		paths = append(paths,
			filepath.Join(home, ".claude", "settings.json"),
			filepath.Join(home, ".claude", "settings.local.json"),
		)
	}
	if cwd != "" {
		paths = append(paths,
			filepath.Join(cwd, ".claude", "settings.json"),
			filepath.Join(cwd, ".claude", "settings.local.json"),
		)
	}
	return paths
}

// managedSettingsPaths returns the platform-specific paths Claude Code
// looks at for org-pushed managed settings. Both macOS conventions are
// included because /Library is preferred on most installs; /etc is the
// Linux convention and is also checked on macOS for parity with other
// org-managed-config tools.
func managedSettingsPaths() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/Library/Application Support/ClaudeCode/managed-settings.json",
			"/etc/claude-code/managed-settings.json",
		}
	case "windows":
		return []string{
			filepath.Join(os.Getenv("ProgramData"), "ClaudeCode", "managed-settings.json"),
		}
	default:
		return []string{
			"/etc/claude-code/managed-settings.json",
		}
	}
}

// readEnabledPluginsField returns the `enabledPlugins` map from a single
// settings file. Tolerates string-shaped booleans ("true"/"false") since
// older config tooling sometimes writes them.
func readEnabledPluginsField(path string) map[string]bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		EnabledPlugins map[string]any `json:"enabledPlugins"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	out := map[string]bool{}
	for k, raw := range doc.EnabledPlugins {
		switch v := raw.(type) {
		case bool:
			out[k] = v
		case string:
			out[k] = strings.EqualFold(v, "true") || v == "1"
		}
	}
	return out
}
