package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/editororigin"
)

type GridCraftConfig struct {
	Enabled             bool   `yaml:"enabled"`
	ServiceSocket       string `yaml:"service_socket"`
	ServiceTokenFile    string `yaml:"service_token_file"`
	MaxWorkers          int    `yaml:"max_workers"`
	MaxWorkspaceWorkers int    `yaml:"max_workspace_workers"`

	// EditorEnabled is the operator switch of live workbook editing (editor
	// sessions, cell edits, the commits feed) and of the embedded editor; the
	// workspace flag gridcraft_editor must be on as well. The editor runs
	// foreign code in a sandboxed frame (internal/editororigin). All of it is
	// off unless EditorEnabled and Enabled are both set. Without EditorOrigin
	// the editor is served under a path prefix of the Studio origin each
	// request reached, or only of Storage.PageStudioOrigin when that is set
	// (zero-config default); EditorOrigin
	// moves it to a dedicated host (optional hardening), and alone only
	// isolates that host.
	EditorEnabled             bool     `yaml:"editor_enabled"`
	EditorOrigin              string   `yaml:"editor_origin"`
	EditorDevelopmentSameSite bool     `yaml:"editor_development_same_site"` // Sibling subdomain of Studio; development only.
	EditorBundleDir           string   `yaml:"editor_bundle_dir"`
	EditorBundles             []string `yaml:"editor_bundles"` // sha256 of each qualified bundle's index.html.
}

func (c GridCraftConfig) validate() error {
	if !c.Enabled {
		if c.EditorEnabled {
			return errors.New("gridcraft editor requires the gridcraft workbook API to be enabled")
		}
		return nil
	}
	if _, err := appengine.NewWorkerClient(c.ServiceSocket, c.ServiceTokenFile); err != nil {
		return errors.New("gridcraft worker requires distinct clean absolute private service paths <=100 bytes")
	}
	if c.MaxWorkers < 0 || c.MaxWorkers > 16 || c.MaxWorkspaceWorkers < 0 || c.MaxWorkspaceWorkers > 16 {
		return errors.New("gridcraft worker limits must be between 0 (default) and 16")
	}
	return nil
}

// validateGridCraftEditor checks the editor origin against the other browser
// origins. allowedOrigins is the raw CREWSHIP_ALLOWED_ORIGINS value.
func validateGridCraftEditor(c *Config, allowedOrigins string) error {
	g := c.GridCraft
	if g.EditorEnabled && !g.Enabled {
		return errors.New("gridcraft editor requires the gridcraft workbook engine")
	}
	var allowed []string
	for _, item := range strings.Split(allowedOrigins, ",") {
		if item = strings.TrimSpace(item); item != "" {
			allowed = append(allowed, item)
		}
	}
	if (g.EditorEnabled || c.Mooring.Enabled) && g.EditorOrigin == "" {
		if g.EditorDevelopmentSameSite {
			return errors.New("gridcraft editor: the sibling-subdomain exception applies only to a dedicated editor origin")
		}
		// Only an explicitly configured public Studio origin pins the editor;
		// otherwise it follows each request's origin (zero-config default).
		if err := editororigin.ValidateSameHost(c.Storage.PageStudioOrigin, c.Storage.PageRuntimeOrigin, allowed); err != nil {
			return fmt.Errorf("gridcraft editor: %w", err)
		}
	}
	if g.EditorOrigin != "" {
		if err := editororigin.ValidateOrigin(g.EditorOrigin, c.PageStudioOrigin(), c.Storage.PageRuntimeOrigin, g.EditorDevelopmentSameSite, allowed); err != nil {
			return fmt.Errorf("gridcraft editor: %w", err)
		}
	}
	if len(g.EditorBundles) > 0 && g.EditorBundleDir == "" {
		return errors.New("gridcraft editor bundles require a bundle directory")
	}
	if d := g.EditorBundleDir; d != "" && (!filepath.IsAbs(d) || filepath.Clean(d) != d || strings.ContainsRune(d, 0)) {
		return errors.New("gridcraft editor bundle directory must be a clean absolute path")
	}
	for _, digest := range g.EditorBundles {
		if !editororigin.IsDigest(digest) {
			return errors.New("gridcraft editor bundles must be lowercase hex sha256 digests")
		}
	}
	return nil
}

func applyGridCraftEditorEnv(g *GridCraftConfig) {
	if v, ok := os.LookupEnv("CREWSHIP_GRIDCRAFT_EDITOR_ENABLED"); ok {
		g.EditorEnabled = v == "true" || v == "1"
	}
	if v, ok := os.LookupEnv("CREWSHIP_EDITOR_RUNTIME_ORIGIN"); ok {
		g.EditorOrigin = strings.TrimSpace(v)
	}
	if v, ok := os.LookupEnv("CREWSHIP_EDITOR_RUNTIME_DEVELOPMENT_SAME_SITE"); ok {
		g.EditorDevelopmentSameSite = v == "true"
	}
	if v, ok := os.LookupEnv("CREWSHIP_GRIDCRAFT_EDITOR_BUNDLE_DIR"); ok {
		g.EditorBundleDir = strings.TrimSpace(v)
	}
	if v, ok := os.LookupEnv("CREWSHIP_GRIDCRAFT_EDITOR_BUNDLES"); ok {
		g.EditorBundles = nil
		for _, item := range strings.Split(v, ",") {
			if item = strings.TrimSpace(item); item != "" {
				g.EditorBundles = append(g.EditorBundles, item)
			}
		}
	}
}
