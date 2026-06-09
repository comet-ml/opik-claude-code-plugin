package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// pluginCatalogEntry mirrors the per-plugin shape Claude Code writes to
// ~/.claude/plugins/plugin-catalog-cache.json. The cache is populated for
// every plugin in the official marketplace (and any other marketplace CC
// has fetched metadata for), even when those plugins are not installed
// locally — fields we consume are the model-resolved token counts plus
// the per-component char counts that feed them.
//
// Two cost dimensions:
//   - always_on: what /context attributes per-turn to the plugin's
//     menu/discovery presence (skill listing line, agent dispatch blurb)
//   - on_invoke: the full body cost paid only when the component is
//     actually loaded into context (Skill tool_use, Agent invocation)
type pluginCatalogEntry struct {
	Tokens     map[string]pluginCatalogTokens `json:"tokens,omitempty"`
	Components struct {
		Skills   []pluginCatalogComponent `json:"skills,omitempty"`
		Agents   []pluginCatalogComponent `json:"agents,omitempty"`
		Commands []pluginCatalogComponent `json:"commands,omitempty"`
	} `json:"components,omitempty"`
}

type pluginCatalogTokens struct {
	AlwaysOn int `json:"always_on"`
	OnInvoke int `json:"on_invoke"`
}

type pluginCatalogComponent struct {
	Name  string `json:"name"`
	Chars pluginCatalogTokens `json:"chars"`
}

// loadPluginCatalog reads the cache file once per (home,) process and
// memoizes it. Returns an empty map (not nil) on any read/parse error so
// callers can do a single map lookup without a nil check.
var (
	pluginCatalogMu   sync.Mutex
	pluginCatalogMemo map[string]map[string]pluginCatalogEntry
)

func loadPluginCatalog(home string) map[string]pluginCatalogEntry {
	pluginCatalogMu.Lock()
	defer pluginCatalogMu.Unlock()
	if pluginCatalogMemo == nil {
		pluginCatalogMemo = map[string]map[string]pluginCatalogEntry{}
	}
	if v, ok := pluginCatalogMemo[home]; ok {
		return v
	}
	v := readPluginCatalog(home)
	pluginCatalogMemo[home] = v
	return v
}

func readPluginCatalog(home string) map[string]pluginCatalogEntry {
	out := map[string]pluginCatalogEntry{}
	if home == "" {
		return out
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "plugins", "plugin-catalog-cache.json"))
	if err != nil {
		return out
	}
	var doc struct {
		Catalog struct {
			Plugins map[string]pluginCatalogEntry `json:"plugins"`
		} `json:"catalog"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return out
	}
	return doc.Catalog.Plugins
}

// pluginCatalogLookup finds a component (skill/agent/command) by plugin
// short-name + component-name. Returns (entry, true) when matched. The
// plugin key in the cache is "<short>@<marketplace>" — we accept the
// short name on its own and match any marketplace it appears under,
// which is how installed_plugins.json keys plugins too.
func pluginCatalogLookup(home, pluginShort, kind, componentName string) (pluginCatalogComponent, string, bool) {
	for fullKey, entry := range loadPluginCatalog(home) {
		short, _, _ := splitPluginKey(fullKey)
		if short != pluginShort {
			continue
		}
		var pool []pluginCatalogComponent
		switch kind {
		case "skill":
			pool = entry.Components.Skills
		case "agent":
			pool = entry.Components.Agents
		case "command":
			pool = entry.Components.Commands
		}
		for _, c := range pool {
			if c.Name == componentName {
				return c, fullKey, true
			}
		}
	}
	return pluginCatalogComponent{}, "", false
}

// pluginCatalogTokenRatio returns the catalog's char→token ratio for the
// given plugin + model, derived from the published (plugin-level)
// always_on tokens vs the sum of its components' always_on chars. This is
// the same ratio CC's tokenizer produced when it built the cache, so
// applying it back to per-component chars yields tokens that match
// /context for that plugin/model.
//
// Falls back to 3.0 (our skill_listing_menu calibration) when the
// plugin lacks chars or tokens entries — keeps the math sane rather than
// returning 0 and silently scoring everything as zero.
func pluginCatalogTokenRatio(home, pluginShort, model string) float64 {
	for fullKey, entry := range loadPluginCatalog(home) {
		short, _, _ := splitPluginKey(fullKey)
		if short != pluginShort {
			continue
		}
		tok, ok := entry.Tokens[model]
		if !ok || tok.AlwaysOn == 0 {
			continue
		}
		charsSum := 0
		for _, c := range entry.Components.Skills {
			charsSum += c.Chars.AlwaysOn
		}
		for _, c := range entry.Components.Agents {
			charsSum += c.Chars.AlwaysOn
		}
		for _, c := range entry.Components.Commands {
			charsSum += c.Chars.AlwaysOn
		}
		if charsSum == 0 {
			continue
		}
		return float64(charsSum) / float64(tok.AlwaysOn)
	}
	return 3.0
}

// splitPluginKey splits an installed_plugins.json key ("name@marketplace")
// into (name, marketplace, ok). The same shape appears as keys in the
// catalog cache, which is why both readers share this helper.
func splitPluginKey(key string) (name, marketplace string, ok bool) {
	for i := 0; i < len(key); i++ {
		if key[i] == '@' {
			return key[:i], key[i+1:], true
		}
	}
	return key, "", false
}

// catalogComponentTokens converts a catalog component's chars.{always_on
// |on_invoke} to tokens for the given plugin+model. Uses the plugin's own
// chars→token ratio so the result matches /context's tokenizer output
// for that plugin (which is more precise than a global content-type
// calibration).
func catalogComponentTokens(home, pluginShort, model string, c pluginCatalogComponent) (alwaysOn, onInvoke int) {
	ratio := pluginCatalogTokenRatio(home, pluginShort, model)
	if ratio <= 0 {
		ratio = 3.0
	}
	alwaysOn = int(float64(c.Chars.AlwaysOn) / ratio)
	onInvoke = int(float64(c.Chars.OnInvoke) / ratio)
	return
}
