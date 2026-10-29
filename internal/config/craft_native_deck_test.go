package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestCraftNativeDeckExplicitProfileStaysDefaultOff(t *testing.T) {
	cfg := Default()
	t.Setenv("CREWSHIP_CRAFT_NATIVE_DECK_ENGINE_REVISION", strings.Repeat("d", 40))
	t.Setenv("CREWSHIP_CRAFT_NATIVE_DECK_CLI_PATH", "/qualified/deckcraft-cli")
	t.Setenv("CREWSHIP_CRAFT_NATIVE_DECK_CLI_SHA256", strings.Repeat("a", 64))
	t.Setenv("CREWSHIP_CRAFT_NATIVE_DECK_EDITOR_BUNDLE", strings.Repeat("b", 64))
	t.Setenv("CREWSHIP_CRAFT_NATIVE_DECK_EDITOR_URL", "/_editor/b/"+strings.Repeat("b", 64)+"/index.html")
	applyEnvOverrides(cfg)
	p, ok := cfg.CraftNative.Profiles["deckcraft"]
	if !ok || p.Application != "deckcraft" || p.EngineRevision != strings.Repeat("d", 40) || p.CLIPath != "/qualified/deckcraft-cli" || p.CLISHA256 != strings.Repeat("a", 64) || p.EditorBundle != strings.Repeat("b", 64) || p.EditorURL != "/_editor/b/"+strings.Repeat("b", 64)+"/index.html" {
		t.Fatal("Deck closed startup profile missing")
	}
	if cfg.CraftNative.Enabled || cfg.CraftNative.AgentsEnabled || cfg.GridCraft.Enabled || cfg.Mooring.Enabled {
		t.Fatal("profile presence enabled native, agent or legacy engines")
	}
	// Presence, including empty values, must withdraw an old YAML binding.
	t.Setenv("CREWSHIP_CRAFT_NATIVE_DECK_CLI_SHA256", "")
	applyEnvOverrides(cfg)
	if cfg.CraftNative.Profiles["deckcraft"].CLISHA256 != "" {
		t.Fatal("empty operator pin did not replace old value")
	}
}
func TestCraftNativeDeckBundleUnionKeepsClosedApplicationSet(t *testing.T) {
	dir := t.TempDir()
	c := CraftNativeConfig{Enabled: true, EditorBundleDir: dir, Profiles: map[string]CraftNativeDocumentProfile{}}
	for i, app := range []string{"deckcraft", "gridcraft", "wordcraft"} {
		pin := strings.Repeat(string(rune('a'+i)), 64)
		c.Profiles[app] = CraftNativeDocumentProfile{Application: app, EditorBundle: pin, EditorURL: "/_editor/b/" + pin + "/index.html"}
	}
	_, pins, err := c.ResolveEditorBundles(GridCraftConfig{}, false)
	if err != nil || !reflect.DeepEqual(pins, []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)}) {
		t.Fatalf("qualified triple bundle union: %v %v", pins, err)
	}
	c.Profiles["othercraft"] = CraftNativeDocumentProfile{Application: "othercraft", EditorBundle: strings.Repeat("d", 64), EditorURL: "/_editor/b/" + strings.Repeat("d", 64) + "/index.html"}
	if _, _, err = c.ResolveEditorBundles(GridCraftConfig{}, false); err == nil {
		t.Fatal("unlisted native application admitted")
	}
}
