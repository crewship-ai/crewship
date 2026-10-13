package pagebuild

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const ArtifactRuntimePreparationPath = "/api/v1/pages/runtime/artifact-preparation"

//go:embed artifact_bootstrap.js
var artifactBootstrap string

// ServeArtifactRuntimePreparation serves only a constant program, never an
// artifact, identity or credential. Product admission remains a separate closed
// gate. No same-origin development exception applies to this candidate profile.
// Self-navigation remains a browser-bound residual risk, not a zero-egress claim.
func ServeArtifactRuntimePreparation(w http.ResponseWriter, r *http.Request, runtime, studio string) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet || r.URL.Path != ArtifactRuntimePreparationPath || r.URL.RawQuery != "" || !RuntimeMatchesHost(runtime, r.Host) {
		http.NotFound(w, r)
		return
	}
	runtimeURL, err := url.Parse(runtime)
	parent, parseErr := url.Parse(studio)
	if err != nil || parseErr != nil || runtimeURL.Scheme != "https" || parent.Scheme != "https" || ValidateRuntimeOrigin(runtime, studio) != nil {
		http.Error(w, "separate-site artifact preparation unavailable", http.StatusServiceUnavailable)
		return
	}
	// event.origin omits default ports and normalizes the hostname.
	parent.Host = strings.ToLower(parent.Host)
	if parent.Port() == "443" {
		parent.Host = parent.Hostname()
		if strings.Contains(parent.Host, ":") {
			parent.Host = "[" + parent.Host + "]"
		}
	}
	parent.Path = ""
	origin := parent.String()
	encoded, _ := json.Marshal(origin)
	w.Header().Del("X-Frame-Options")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Origin-Agent-Cluster", "?1")
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), usb=(), payment=()")
	w.Header().Set("Content-Security-Policy", fmt.Sprintf("sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; connect-src 'none'; frame-src 'none'; worker-src 'none'; object-src 'none'; media-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors %s", origin))
	_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><script>window.__CREWSHIP_ARTIFACT_PARENT_ORIGIN__=%s;%s</script>`, encoded, artifactBootstrap)
}
