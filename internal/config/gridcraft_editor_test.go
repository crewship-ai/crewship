package config

import (
	"strings"
	"testing"
)

func TestGridCraftEditorOriginIsDefaultOffAndOperatorOwned(t *testing.T) {
	c := Default()
	if c.GridCraft.EditorEnabled || c.GridCraft.EditorOrigin != "" || len(c.GridCraft.EditorBundles) != 0 {
		t.Fatal("editor origin on by default")
	}
	digest := strings.Repeat("a1", 32)
	t.Setenv("CREWSHIP_GRIDCRAFT_ENABLED", "true")
	t.Setenv("CREWSHIP_GRIDCRAFT_EDITOR_ENABLED", "true")
	t.Setenv("CREWSHIP_EDITOR_RUNTIME_ORIGIN", "https://editor.example.test")
	t.Setenv("CREWSHIP_EDITOR_RUNTIME_DEVELOPMENT_SAME_SITE", "true")
	t.Setenv("CREWSHIP_GRIDCRAFT_EDITOR_BUNDLE_DIR", "/srv/editor-bundles")
	t.Setenv("CREWSHIP_GRIDCRAFT_EDITOR_BUNDLES", " "+digest+" ,"+strings.Repeat("b2", 32))
	applyEnvOverrides(c)
	g := c.GridCraft
	if !g.EditorEnabled || g.EditorOrigin != "https://editor.example.test" || !g.EditorDevelopmentSameSite || g.EditorBundleDir != "/srv/editor-bundles" || len(g.EditorBundles) != 2 || g.EditorBundles[0] != digest {
		t.Fatalf("operator override lost: %+v", g)
	}
	t.Setenv("CREWSHIP_GRIDCRAFT_EDITOR_ENABLED", "yes")
	t.Setenv("CREWSHIP_EDITOR_RUNTIME_DEVELOPMENT_SAME_SITE", "1")
	applyEnvOverrides(c)
	if c.GridCraft.EditorEnabled || c.GridCraft.EditorDevelopmentSameSite {
		t.Fatal("misspelled flag opened the editor origin")
	}
}

func TestGridCraftEditorOriginValidation(t *testing.T) {
	digest := strings.Repeat("a1", 32)
	base := func() *Config {
		c := Default()
		c.Auth.NextjsURL = "https://studio.example.test"
		c.GridCraft = GridCraftConfig{Enabled: true, ServiceSocket: "/run/workbook/service.sock", ServiceTokenFile: "/run/secrets/worker-token",
			EditorEnabled: true, EditorOrigin: "https://editor.example.test", EditorDevelopmentSameSite: true,
			EditorBundleDir: "/srv/editor-bundles", EditorBundles: []string{digest}}
		return c
	}
	for _, tc := range []struct {
		name    string
		mutate  func(c *Config)
		allowed string
		ok      bool
	}{
		{"valid sibling with dev exception", func(c *Config) {}, "", true},
		{"disabled and unset", func(c *Config) { c.GridCraft = GridCraftConfig{} }, "", true},
		{"requires workbook engine", func(c *Config) { c.GridCraft.Enabled = false }, "", false},
		// Zero-config default: no dedicated origin serves the editor under a
		// sandboxed path prefix of the Studio origin.
		{"zero-config same host", func(c *Config) { c.GridCraft.EditorOrigin = ""; c.GridCraft.EditorDevelopmentSameSite = false }, "", true},
		// Out of the box nothing names the public URL: CREWSHIP_NEXTJS_URL
		// keeps its internal default and is not the browser origin. The
		// editor then follows each request's origin.
		{"zero-config with internal default Next.js URL", func(c *Config) {
			c.Auth.NextjsURL = Default().Auth.NextjsURL
			c.GridCraft.EditorOrigin, c.GridCraft.EditorDevelopmentSameSite = "", false
		}, "", true},
		{"zero-config ignores a cleartext Next.js URL", func(c *Config) {
			c.Auth.NextjsURL = "http://studio.example.test"
			c.GridCraft.EditorOrigin, c.GridCraft.EditorDevelopmentSameSite = "", false
		}, "", true},
		{"configured studio on loopback http", func(c *Config) {
			c.Storage.PageStudioOrigin = "http://localhost:3000"
			c.GridCraft.EditorOrigin, c.GridCraft.EditorDevelopmentSameSite = "", false
		}, "", true},
		{"configured cleartext studio", func(c *Config) {
			c.Storage.PageStudioOrigin = "http://studio.example.test"
			c.GridCraft.EditorOrigin, c.GridCraft.EditorDevelopmentSameSite = "", false
		}, "", false},
		{"same host with sibling exception set", func(c *Config) { c.GridCraft.EditorOrigin = "" }, "", false},
		// A same-origin Page runtime puts all of Studio into frame-src; a
		// self-navigated editor frame would then reach framable Studio documents.
		{"configured studio with same-origin page runtime", func(c *Config) {
			c.GridCraft.EditorOrigin, c.GridCraft.EditorDevelopmentSameSite = "", false
			c.Storage.PageStudioOrigin = "https://studio.example.test"
			c.Storage.PageRuntimeOrigin = "https://studio.example.test"
		}, "", false},
		// Without a configured Studio origin the Page runtime host is refused
		// per request (internal/editororigin), not at startup.
		{"zero-config with a page runtime", func(c *Config) {
			c.GridCraft.EditorOrigin, c.GridCraft.EditorDevelopmentSameSite = "", false
			c.Storage.PageRuntimeOrigin = "https://studio.example.test"
		}, "", true},
		{"same host with separate page runtime", func(c *Config) {
			c.GridCraft.EditorOrigin, c.GridCraft.EditorDevelopmentSameSite = "", false
			c.Storage.PageRuntimeOrigin = "https://apps.example.net"
		}, "", true},
		{"same host with opaque origin trusted", func(c *Config) {
			c.GridCraft.EditorOrigin, c.GridCraft.EditorDevelopmentSameSite = "", false
		}, "https://studio.example.test, null", false},
		{"dedicated with opaque origin trusted", func(c *Config) {}, "null", false},
		{"same host while disabled is not validated", func(c *Config) {
			c.GridCraft.EditorEnabled, c.GridCraft.EditorOrigin = false, ""
			c.Storage.PageRuntimeOrigin = "https://studio.example.test"
		}, "", true},
		{"sibling without dev exception", func(c *Config) { c.GridCraft.EditorDevelopmentSameSite = false }, "", false},
		{"equals studio", func(c *Config) { c.GridCraft.EditorOrigin = "https://studio.example.test" }, "", false},
		{"equals explicit studio origin", func(c *Config) {
			c.Storage.PageStudioOrigin = "https://app.example.test"
			c.GridCraft.EditorOrigin = "https://app.example.test"
		}, "", false},
		{"equals page runtime", func(c *Config) {
			c.Storage.PageRuntimeOrigin = "https://editor.example.test"
		}, "", false},
		{"listed as trusted origin", func(c *Config) {}, "https://studio.example.test, https://editor.example.test", false},
		{"origin set while disabled is still validated", func(c *Config) {
			c.GridCraft.EditorEnabled = false
			c.GridCraft.EditorOrigin = "https://studio.example.test"
		}, "", false},
		{"relative bundle dir", func(c *Config) { c.GridCraft.EditorBundleDir = "bundles" }, "", false},
		{"bundles without dir", func(c *Config) { c.GridCraft.EditorBundleDir = "" }, "", false},
		{"short digest", func(c *Config) { c.GridCraft.EditorBundles = []string{"abc"} }, "", false},
		{"uppercase digest", func(c *Config) { c.GridCraft.EditorBundles = []string{strings.ToUpper(digest)} }, "", false},
		{"prefixed digest", func(c *Config) { c.GridCraft.EditorBundles = []string{"sha256:" + digest} }, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mutate(c)
			if err := validateGridCraftEditor(c, tc.allowed); (err == nil) != tc.ok {
				t.Fatalf("err=%v want ok=%v", err, tc.ok)
			}
		})
	}
}
