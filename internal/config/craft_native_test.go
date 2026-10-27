package config

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCraftNativeDefaultsOffAndExplicitEnv(t *testing.T) {
	cfg := Default()
	if cfg.CraftNative.Enabled {
		t.Fatal("native profile enabled by default")
	}
	t.Setenv("CREWSHIP_CRAFT_NATIVE_ENABLED", "true")
	applyEnvOverrides(cfg)
	if !cfg.CraftNative.Enabled || cfg.Mooring.Enabled || cfg.GridCraft.Enabled {
		t.Fatal("native flag must be independent of older profiles")
	}
	t.Setenv("CREWSHIP_CRAFT_NATIVE_ENABLED", "false")
	applyEnvOverrides(cfg)
	if cfg.CraftNative.Enabled {
		t.Fatal("explicit disable ignored")
	}
}

func TestCraftNativeBundleDirectoryUnionPreservesActiveLegacyPins(t *testing.T) {
	dir := t.TempDir()
	old := strings.Repeat("a", 64)
	native := strings.Repeat("b", 64)
	g := GridCraftConfig{Enabled: true, EditorEnabled: true, EditorBundleDir: dir, EditorBundles: []string{old}}
	c := CraftNativeConfig{Enabled: true, EditorBundleDir: dir, Profiles: map[string]CraftNativeDocumentProfile{"wordcraft": {Application: "wordcraft", EditorBundle: native, EditorURL: "/_editor/b/" + native + "/index.html"}}}
	resolved, pins, err := c.ResolveEditorBundles(g, true)
	if err != nil || resolved != dir || !reflect.DeepEqual(pins, []string{old, native}) {
		t.Fatalf("explicit same-dir union %q %v %v", resolved, pins, err)
	}
	if !reflect.DeepEqual(g.EditorBundles, []string{old}) {
		t.Fatal("native resolution mutated legacy allowlist")
	}
	c.EditorBundleDir = t.TempDir()
	if _, _, err = c.ResolveEditorBundles(g, true); err == nil {
		t.Fatal("silently replaced active legacy directory")
	}
	c.Enabled = false
	resolved, pins, err = c.ResolveEditorBundles(g, true)
	if err != nil || resolved != g.EditorBundleDir || !reflect.DeepEqual(pins, g.EditorBundles) {
		t.Fatal("native off changed legacy mount")
	}
	c.Enabled = true
	c.EditorBundleDir = dir
	_, pins, err = c.ResolveEditorBundles(g, false)
	if err != nil || !reflect.DeepEqual(pins, []string{native}) {
		t.Fatal("native-only mode enabled disabled legacy pins")
	}
}
func TestCraftNativeProfileYAMLPreservesOperatorOnlyBindings(t *testing.T) {
	var c CraftNativeConfig
	raw := []byte("enabled: true\neditor_bundle_dir: /srv/native-bundles\nprofiles:\n  wordcraft:\n    application: wordcraft\n    engine_revision: revision\n    cli_path: /srv/wordcraft\n    cli_sha256: digest\n    editor_bundle: bundle\n    editor_url: /_editor/b/bundle/index.html\n")
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	p := c.Profiles["wordcraft"]
	if c.EditorBundleDir != "/srv/native-bundles" || p.Application != "wordcraft" || p.EngineRevision != "revision" || p.CLIPath != "/srv/wordcraft" || p.CLISHA256 != "digest" || p.EditorBundle != "bundle" || p.EditorURL != "/_editor/b/bundle/index.html" {
		t.Fatal("trusted startup YAML lost native release binding")
	}
}

func TestCraftNativeWordExplicitEnvProfileOverridesPreserveOtherAppsAndDefaultOff(t *testing.T) {
	cfg := Default()
	cfg.CraftNative.Profiles = map[string]CraftNativeDocumentProfile{"wordcraft": {Application: "wordcraft", EngineRevision: "yaml-revision"}, "gridcraft": {Application: "gridcraft", EngineRevision: "grid-yaml"}}
	t.Setenv("CREWSHIP_CRAFT_NATIVE_EDITOR_BUNDLE_DIR", "/srv/native-bundles")
	t.Setenv("CREWSHIP_CRAFT_NATIVE_WORD_ENGINE_REVISION", strings.Repeat("a", 40))
	t.Setenv("CREWSHIP_CRAFT_NATIVE_WORD_CLI_PATH", "/srv/native/wordcraft")
	t.Setenv("CREWSHIP_CRAFT_NATIVE_WORD_CLI_SHA256", strings.Repeat("b", 64))
	t.Setenv("CREWSHIP_CRAFT_NATIVE_WORD_EDITOR_BUNDLE", strings.Repeat("c", 64))
	t.Setenv("CREWSHIP_CRAFT_NATIVE_WORD_EDITOR_URL", "/_editor/b/"+strings.Repeat("c", 64)+"/index.html")
	applyEnvOverrides(cfg)
	p := cfg.CraftNative.Profiles["wordcraft"]
	if cfg.CraftNative.Enabled || cfg.GridCraft.Enabled || cfg.Mooring.Enabled {
		t.Fatal("profile env implicitly enabled product or legacy profiles")
	}
	if cfg.CraftNative.EditorBundleDir != "/srv/native-bundles" || p.Application != "wordcraft" || p.EngineRevision != strings.Repeat("a", 40) || p.CLIPath != "/srv/native/wordcraft" || p.CLISHA256 != strings.Repeat("b", 64) || p.EditorBundle != strings.Repeat("c", 64) || p.EditorURL != "/_editor/b/"+strings.Repeat("c", 64)+"/index.html" {
		t.Fatal("native-only operator env lost release bindings")
	}
	if cfg.CraftNative.Profiles["gridcraft"].EngineRevision != "grid-yaml" {
		t.Fatal("Word env replaced other app profile")
	}
	t.Setenv("CREWSHIP_CRAFT_NATIVE_WORD_CLI_SHA256", "")
	applyEnvOverrides(cfg)
	if cfg.CraftNative.Profiles["wordcraft"].CLISHA256 != "" {
		t.Fatal("explicit empty operator override silently reused YAML qualification")
	}
}

func TestCraftNativeDedicatedOriginSharesOnlyExplicitDirectoryAndPins(t *testing.T) {
	dir := t.TempDir()
	old := strings.Repeat("a", 64)
	pin := strings.Repeat("b", 64)
	legacy := GridCraftConfig{Enabled: true, EditorEnabled: true, EditorOrigin: "https://editor.example.org", EditorBundleDir: dir, EditorBundles: []string{old}}
	native := CraftNativeConfig{Enabled: true, EditorBundleDir: dir, Profiles: map[string]CraftNativeDocumentProfile{"wordcraft": {Application: "wordcraft", EditorBundle: pin, EditorURL: legacy.EditorOrigin + "/b/" + pin + "/index.html"}}}
	got, pins, err := native.ResolveEditorBundles(legacy, true)
	if err != nil || got != dir || !reflect.DeepEqual(pins, []string{old, pin}) {
		t.Fatalf("dedicated existing mount must preserve legacy and native explicit pins: %s %v %v", got, pins, err)
	}
	profile := native.Profiles["wordcraft"]
	for _, url := range []string{"https://other.example.org/b/" + pin + "/index.html", "/_editor/b/" + pin + "/index.html"} {
		profile.EditorURL = url
		native.Profiles["wordcraft"] = profile
		if _, _, err = native.ResolveEditorBundles(legacy, true); err == nil {
			t.Error("native URL escaped configured dedicated origin")
		}
	}
}
