package pagebuild

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const BoardConverterPath = "/api/v1/pages/runtime/board-converter"
const BoardConverterScriptPath = "/board-converter/runner.js"

// ServeBoardConverter serves only the trusted, versioned conversion program.
// User input arrives once through a transferred MessagePort; this document has
// no session, platform capability or ability to fetch private page contents.
func ServeBoardConverter(w http.ResponseWriter, runtime, studio string) {
	// Deliberately no same-origin development exemption: a parser loop must
	// not share Studio's browser site, including in a preview installation.
	if err := ValidateRuntimeOrigin(runtime, studio); err != nil {
		http.Error(w, "separate-site Board converter unavailable", http.StatusServiceUnavailable)
		return
	}
	parent, _ := url.Parse(studio)
	if parent.Port() == "443" && parent.Scheme == "https" || parent.Port() == "80" && parent.Scheme == "http" {
		parent.Host = parent.Hostname()
		if strings.Contains(parent.Host, ":") {
			parent.Host = "[" + parent.Host + "]"
		}
	}
	parent.Host = strings.ToLower(parent.Host)
	parent.Path = ""
	parentJSON, _ := json.Marshal(parent.String())
	nonce := rand.Text()
	w.Header().Del("X-Frame-Options")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Origin-Agent-Cluster", "?1")
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), usb=(), payment=()")
	w.Header().Set("Content-Security-Policy", fmt.Sprintf("sandbox allow-scripts; default-src 'none'; script-src 'nonce-%s'; style-src 'unsafe-inline'; img-src data:; font-src data:; connect-src 'none'; frame-src 'none'; worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors %s", nonce, parent.String()))
	// The external classic IIFE is part of the same pinned frontend build.
	// A nonce permits this one program without permitting arbitrary imports.
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"></head><body><script nonce="%s">window.__CREWSHIP_BOARD_CONVERTER_PARENT__=%s;</script><script crossorigin="anonymous" nonce="%s" src="%s"></script></body></html>`, nonce, parentJSON, nonce, BoardConverterScriptPath)
}
