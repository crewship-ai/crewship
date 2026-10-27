package config

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/editororigin"
)

// CraftNativeConfig controls the explicit whole-native single-editor profile.
// Existing document formats remain unchanged when enabled.
type CraftNativeConfig struct {
	Enabled         bool                                  `yaml:"enabled"`
	Profiles        map[string]CraftNativeDocumentProfile `yaml:"profiles"`
	EditorBundleDir string                                `yaml:"editor_bundle_dir"`
}

// CraftNativeDocumentProfile names an operator-qualified native release.
// Request bodies cannot override these executable or compatibility bindings.
type CraftNativeDocumentProfile struct {
	Application    string `yaml:"application"`
	EngineRevision string `yaml:"engine_revision"`
	CLIPath        string `yaml:"cli_path"`
	CLISHA256      string `yaml:"cli_sha256"`
	EditorBundle   string `yaml:"editor_bundle"`
	EditorURL      string `yaml:"editor_url"`
}

// ResolveEditorBundles preserves an active legacy mount and adds only explicit
// native pins. Native-only mode must not activate a disabled legacy allowlist.
func (c CraftNativeConfig) ResolveEditorBundles(g GridCraftConfig, legacyEnabled bool) (string, []string, error) {
	if !c.Enabled || len(c.Profiles) == 0 {
		return g.EditorBundleDir, append([]string{}, g.EditorBundles...), nil
	}
	if c.EditorBundleDir == "" || !filepath.IsAbs(c.EditorBundleDir) || filepath.Clean(c.EditorBundleDir) != c.EditorBundleDir || strings.ContainsRune(c.EditorBundleDir, 0) {
		return "", nil, errors.New("craft_native.editor_bundle_dir must name a clean absolute qualified bundle directory")
	}
	editorBase := editororigin.SameHostPrefix
	if g.EditorOrigin != "" {
		editorBase = editororigin.CanonicalOrigin(g.EditorOrigin)
		if editorBase == "" || !strings.HasPrefix(editorBase, "https://") {
			return "", nil, errors.New("native editor requires a valid qualified HTTPS editor origin")
		}
	}
	if legacyEnabled && g.EditorBundleDir != c.EditorBundleDir {
		return "", nil, errors.New("native and active legacy editor bundle directories must match explicitly")
	}
	var bundles []string
	if legacyEnabled {
		bundles = append(bundles, g.EditorBundles...)
	}
	seen := map[string]bool{}
	for _, pin := range bundles {
		seen[pin] = true
	}
	keys := make([]string, 0, len(c.Profiles))
	for key := range c.Profiles {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		p := c.Profiles[key]
		if (key != "wordcraft" && key != "gridcraft") || key != p.Application || !editororigin.IsDigest(p.EditorBundle) || p.EditorURL != editorBase+editororigin.BundlePrefix+p.EditorBundle+"/index.html" {
			return "", nil, errors.New("native application bundle pin or server-owned URL is invalid")
		}
		if !seen[p.EditorBundle] {
			bundles = append(bundles, p.EditorBundle)
			seen[p.EditorBundle] = true
		}
	}
	if len(bundles) > 4 {
		return "", nil, errors.New("combined native and legacy editor bundle allowlist exceeds four pins")
	}
	return c.EditorBundleDir, bundles, nil
}

// Only operator startup environment may choose these release bindings. Presence,
// including an empty value, overrides YAML; qualification remains fail-closed.
func applyCraftNativeEnv(c *CraftNativeConfig) {
	if value, ok := os.LookupEnv("CREWSHIP_CRAFT_NATIVE_EDITOR_BUNDLE_DIR"); ok {
		c.EditorBundleDir = value
	}
	for _, app := range []struct{ application, prefix string }{{"wordcraft", "WORD"}, {"gridcraft", "GRID"}} {
		profile := c.Profiles[app.application]
		changed := false
		for _, field := range []struct {
			env    string
			target *string
		}{
			{"ENGINE_REVISION", &profile.EngineRevision},
			{"CLI_PATH", &profile.CLIPath},
			{"CLI_SHA256", &profile.CLISHA256},
			{"EDITOR_BUNDLE", &profile.EditorBundle},
			{"EDITOR_URL", &profile.EditorURL},
		} {
			if value, ok := os.LookupEnv("CREWSHIP_CRAFT_NATIVE_" + app.prefix + "_" + field.env); ok {
				*field.target = value
				changed = true
			}
		}
		if changed {
			profile.Application = app.application
			if c.Profiles == nil {
				c.Profiles = make(map[string]CraftNativeDocumentProfile)
			}
			c.Profiles[app.application] = profile
		}
	}
}
