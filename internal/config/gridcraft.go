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

	// The embedded workbook editor runs foreign code on its own origin
	// (internal/editororigin). All of it is off unless EditorEnabled and
	// Enabled are both set; EditorOrigin alone only isolates that host.
	EditorEnabled             bool     `yaml:"editor_enabled"`
	EditorOrigin              string   `yaml:"editor_origin"`
	EditorDevelopmentSameSite bool     `yaml:"editor_development_same_site"` // Sibling subdomain of Studio; development only.
	EditorBundleDir           string   `yaml:"editor_bundle_dir"`
	EditorBundles             []string `yaml:"editor_bundles"` // sha256 of each qualified bundle's index.html.
}

func (c GridCraftConfig) validate() error {
	if !c.Enabled {
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
	if g.EditorEnabled && g.EditorOrigin == "" {
		return errors.New("gridcraft editor requires an editor origin")
	}
	if g.EditorOrigin != "" {
		var allowed []string
		for _, item := range strings.Split(allowedOrigins, ",") {
			if item = strings.TrimSpace(item); item != "" {
				allowed = append(allowed, item)
			}
		}
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
