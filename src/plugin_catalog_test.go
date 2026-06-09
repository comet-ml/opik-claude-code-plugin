package main

import (
	"path/filepath"
	"testing"
)

func TestPluginCatalogLookupAndRatio(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "plugins", "plugin-catalog-cache.json"), `{
		"catalog": {
			"plugins": {
				"demo-plugin@some-marketplace": {
					"tokens": {
						"claude-opus-4-7": {"always_on": 100, "on_invoke": 1000},
						"claude-sonnet-4-6": {"always_on": 80, "on_invoke": 800}
					},
					"components": {
						"skills": [
							{"name": "skill-alpha", "chars": {"always_on": 100, "on_invoke": 600}},
							{"name": "skill-beta",  "chars": {"always_on": 100, "on_invoke": 400}}
						],
						"agents": [
							{"name": "agent-x", "chars": {"always_on": 100, "on_invoke": 500}}
						]
					}
				}
			}
		}
	}`)

	// Force-isolate the cache from any other test's HOME by reaching into
	// the memo directly — same trick the production code does
	// implicitly when each subtest sets a different HOME.
	pluginCatalogMu.Lock()
	pluginCatalogMemo = nil
	pluginCatalogMu.Unlock()

	// Lookup a skill component by plugin short-name + leaf.
	c, fullKey, ok := pluginCatalogLookup(home, "demo-plugin", "skill", "skill-alpha")
	if !ok {
		t.Fatalf("expected lookup hit for demo-plugin/skill-alpha")
	}
	if fullKey != "demo-plugin@some-marketplace" {
		t.Errorf("fullKey = %q, want demo-plugin@some-marketplace", fullKey)
	}
	if c.Chars.OnInvoke != 600 {
		t.Errorf("OnInvoke chars = %d, want 600", c.Chars.OnInvoke)
	}

	// Wrong plugin → no hit.
	if _, _, ok := pluginCatalogLookup(home, "nope", "skill", "skill-alpha"); ok {
		t.Error("expected miss for unknown plugin")
	}
	// Wrong leaf → no hit.
	if _, _, ok := pluginCatalogLookup(home, "demo-plugin", "skill", "skill-omega"); ok {
		t.Error("expected miss for unknown skill")
	}

	// Ratio derivation: components always_on chars total = 300, plugin
	// always_on tokens for opus = 100 → ratio = 3.0.
	if ratio := pluginCatalogTokenRatio(home, "demo-plugin", "claude-opus-4-7"); ratio != 3.0 {
		t.Errorf("opus ratio = %v, want 3.0", ratio)
	}
	// Sonnet ratio: 300/80 = 3.75
	if ratio := pluginCatalogTokenRatio(home, "demo-plugin", "claude-sonnet-4-6"); ratio < 3.7 || ratio > 3.8 {
		t.Errorf("sonnet ratio = %v, want ~3.75", ratio)
	}

	// catalogComponentTokens applies the resolved ratio. Opus skill-alpha:
	// on_invoke chars 600 / ratio 3.0 = 200 tokens.
	_, onInvoke := catalogComponentTokens(home, "demo-plugin", "claude-opus-4-7", c)
	if onInvoke != 200 {
		t.Errorf("onInvoke tokens = %d, want 200", onInvoke)
	}

	// Unknown model → fallback ratio 3.0 from pluginCatalogTokenRatio
	// (no token entry matched).
	_, onInvokeUnknown := catalogComponentTokens(home, "demo-plugin", "no-such-model", c)
	if onInvokeUnknown != 200 {
		t.Errorf("unknown-model fallback onInvoke = %d, want 200", onInvokeUnknown)
	}
}

func TestSplitNamespacedSkillName(t *testing.T) {
	cases := []struct {
		in, plugin, leaf string
		ok               bool
	}{
		{"opik:opik", "opik", "opik", true},
		{"comet:create-jira-ticket", "comet", "create-jira-ticket", true},
		{"find-skills", "", "", false}, // un-namespaced
		{"", "", "", false},
	}
	for _, c := range cases {
		p, l, ok := splitNamespacedSkillName(c.in)
		if p != c.plugin || l != c.leaf || ok != c.ok {
			t.Errorf("split(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, p, l, ok, c.plugin, c.leaf, c.ok)
		}
	}
}
