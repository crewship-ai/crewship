package server

import (
	"log/slog"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/editororigin"
)

// newEditorOrigin returns the handler for the embedded workbook editor host,
// or nil when no editor origin is configured. A configured origin is always
// isolated from the main application; bundles are loaded only while both the
// workbook engine and the editor are enabled.
func newEditorOrigin(cfg *config.Config, logger *slog.Logger) *editororigin.Handler {
	g := cfg.GridCraft
	if g.EditorOrigin == "" {
		return nil
	}
	o := editororigin.Options{Origin: g.EditorOrigin, StudioOrigin: cfg.PageStudioOrigin()}
	if g.Enabled && g.EditorEnabled {
		o.BundleDir, o.Bundles = g.EditorBundleDir, g.EditorBundles
	}
	h, err := editororigin.New(o)
	if h == nil {
		// Config validation refuses this; if it is ever bypassed, fail
		// closed: an unmatchable editor host must not fall into the app.
		logger.Error("gridcraft editor origin unusable; refusing every request", "error", err)
		return editororigin.Deny()
	}
	if err != nil {
		// Unverified bundles are not served; the host stays isolated.
		logger.Error("gridcraft editor bundle refused", "error", err)
	}
	return h
}
