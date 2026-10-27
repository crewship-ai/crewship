package server

import (
	"errors"
	"net/http"
	"strings"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/editororigin"
)

func nativeCraftHTTPConfig(cfg *config.Config, editor *editororigin.Handler) (goapi.CraftNativeHTTPConfig, error) {
	out := goapi.CraftNativeHTTPConfig{Enabled: cfg.CraftNative.Enabled, Profiles: make(map[string]goapi.CraftNativeDocumentProfile, len(cfg.CraftNative.Profiles))}
	for key, p := range cfg.CraftNative.Profiles {
		if cfg.CraftNative.Enabled && (editor == nil || !editor.HasBundle(p.EditorBundle) || editor.IndexURL(p.EditorBundle) != p.EditorURL) {
			return goapi.CraftNativeHTTPConfig{}, errors.New("qualified native editor bundle is not served at its exact pin")
		}
		out.Profiles[key] = goapi.CraftNativeDocumentProfile{Application: p.Application, EngineRevision: p.EngineRevision, CLIPath: p.CLIPath, CLISHA256: p.CLISHA256, EditorBundle: p.EditorBundle, EditorURL: p.EditorURL}
	}
	return out, nil
}

func nativeCraftEditorEnabled(cfg *config.Config) bool {
	return cfg.CraftNative.Enabled && len(cfg.CraftNative.Profiles) > 0
}

// Only native bundle paths get this additional credentialless gate. Existing
// C1 pins and all other application routes retain their existing policy.
func nativeCraftCredentialGate(cfg *config.Config, editor *editororigin.Handler, next http.Handler) http.Handler {
	if !nativeCraftEditorEnabled(cfg) {
		return next
	}
	dedicated := cfg.GridCraft.EditorOrigin != ""
	base := editororigin.SameHostPrefix
	if dedicated {
		base = ""
	}
	prefixes := make([]string, 0, len(cfg.CraftNative.Profiles))
	for _, p := range cfg.CraftNative.Profiles {
		prefixes = append(prefixes, base+editororigin.BundlePrefix+p.EditorBundle+"/")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reuse the serving handler's host predicate, including its established
		// case/port/trailing-dot normalization. Every host spelling that handler
		// can serve gets the same native credential fence; Studio routes do not.
		if dedicated && (editor == nil || !editor.MatchesHost(r.Host)) {
			next.ServeHTTP(w, r)
			return
		}
		native := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(r.URL.EscapedPath(), prefix) {
				native = true
				break
			}
		}
		if native && (len(r.Header.Values("Authorization")) > 0 || len(r.Header.Values("X-Craft-Session")) > 0 || len(r.Header.Values("X-Internal-Token")) > 0 || len(r.Header.Values("Cookie")) > 0) {
			w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
			w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			http.Error(w, "native editor requires a credentialless sandboxed frame", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
