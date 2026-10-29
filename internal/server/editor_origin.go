package server

import (
	"io/fs"
	"log/slog"
	"net"
	"strings"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/editororigin"
	"github.com/crewship-ai/crewship/internal/mooringassets"
)

// newEditorOrigin returns the handler for embedded application editors, or nil
// when nothing is served.
//
// With a dedicated editor origin configured, that host is always isolated
// from the main application; bundles are loaded only while both the workbook
// engine and the editor are enabled, or Mooring is enabled. Without one (the zero-config default),
// an enabled editor is served under editororigin.SameHostPrefix on the Studio
// origin, and a disabled editor changes nothing. The Studio origin is the one
// each request reached (Host, and the scheme by Crewship's trusted-proxy
// rule), unless CREWSHIP_PAGE_STUDIO_ORIGIN names it, in which case the
// editor answers only there.
func newEditorOrigin(cfg *config.Config, logger *slog.Logger) *editororigin.Handler {
	g := cfg.GridCraft
	legacyEnabled := (g.Enabled && g.EditorEnabled) || cfg.Mooring.Enabled
	dir, bundles, resolveErr := cfg.CraftNative.ResolveEditorBundles(g, legacyEnabled)
	if resolveErr != nil {
		logger.Error("native editor bundle configuration refused", "error", resolveErr)
		return editororigin.Deny()
	}
	g.EditorBundleDir, g.EditorBundles = dir, bundles
	enabled := legacyEnabled || nativeCraftEditorEnabled(cfg)
	var applicationPackages fs.FS
	if cfg.Mooring.Enabled {
		var err error
		applicationPackages, err = mooringassets.EditorPackages()
		if err != nil {
			// configureMooringHTTP refuses startup without the matching assets.
			logger.Error("Mooring editor release unavailable", "error", err)
		}
	}
	if g.EditorOrigin == "" {
		if !enabled {
			return nil
		}
		h, err := editororigin.New(editororigin.Options{SameHost: true, StudioOrigin: cfg.Storage.PageStudioOrigin,
			PageRuntimeOrigin: cfg.Storage.PageRuntimeOrigin, Scheme: goapi.RequestScheme, LoopbackServer: loopbackBind(cfg.Server.Host),
			BundleDir: g.EditorBundleDir, Bundles: g.EditorBundles, ApplicationPackages: applicationPackages})
		if err != nil {
			// The handler still owns the prefix; refused bundles are not served.
			logger.Error("gridcraft editor (same host) refused", "error", err)
		}
		return h
	}
	o := editororigin.Options{Origin: g.EditorOrigin, StudioOrigin: cfg.PageStudioOrigin(), ApplicationPackages: applicationPackages}
	if enabled {
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

// loopbackBind reports whether the server listens on loopback only
// (development); "" and 0.0.0.0 listen on every interface.
func loopbackBind(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// editorHeaderOptions adds the editor to Studio's frame-src only while the
// editor is enabled: the dedicated origin, or the bundle path on the
// request's Studio origin, never the bare Studio origin.
func editorHeaderOptions(cfg *config.Config, editor *editororigin.Handler) securityHeaderOptions {
	o := securityHeaderOptions{pageRuntimeOrigin: cfg.Storage.PageRuntimeOrigin}
	if ((cfg.GridCraft.Enabled && cfg.GridCraft.EditorEnabled) || cfg.Mooring.Enabled || nativeCraftEditorEnabled(cfg)) && editor != nil {
		o.editorFrameSrc = editor.FrameSourceFor
		o.editorPerRequest = cfg.GridCraft.EditorOrigin == ""
	}
	return o
}
