package server

import (
	"errors"
	"net/http"
	"os"
	"strings"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/craftassets"
	"github.com/crewship-ai/crewship/internal/editororigin"
)

func nativeCraftHTTPConfig(cfg *config.Config, craft *craftApps, editor *editororigin.Handler) (goapi.CraftNativeHTTPConfig, error) {
	return nativeCraftHTTPConfigWith(cfg, craft, editor, &sheetSessions{})
}

// nativeCraftHTTPConfigWith lets the server keep the session set it must close.
func nativeCraftHTTPConfigWith(cfg *config.Config, craft *craftApps, editor *editororigin.Handler, sessions *sheetSessions) (goapi.CraftNativeHTTPConfig, error) {
	craft = craftAppsOr(cfg, craft)
	out := goapi.CraftNativeHTTPConfig{Enabled: craft.enabled(), Profiles: make(map[string]goapi.CraftNativeDocumentProfile), Policy: craft.policy(), Components: make(map[string]craftapps.Component)}
	for app, c := range craft.components {
		out.Components[app] = c
	}
	// The agent engine is wired whenever documents are; the administrator's
	// agents setting (or the operator's explicit one) admits each task.
	out.AgentsEnabled = out.Enabled
	if out.AgentsEnabled {
		out.AgentApply, out.AgentInspect = nativeCraftAgentEngine(sessions)
		out.SheetComputed = sessions.computed
		out.SheetTrusted = sessions.trusted
	}
	for key, p := range craft.operator {
		if editor == nil || !editor.HasBundle(p.EditorBundle) || editor.IndexURL(p.EditorBundle) != p.EditorURL {
			return goapi.CraftNativeHTTPConfig{}, errors.New("qualified native editor bundle is not served at its exact pin")
		}
		out.Profiles[key] = goapi.CraftNativeDocumentProfile{Application: p.Application, EngineRevision: p.EngineRevision, CLIPath: p.CLIPath, CLISHA256: p.CLISHA256, EditorBundle: p.EditorBundle, EditorURL: p.EditorURL}
	}
	if err := sheetWorkbookRequired(os.Getenv("CREWSHIP_SHEET_WORKBOOK"), craft.installed); err != nil {
		return goapi.CraftNativeHTTPConfig{}, err
	}
	for app, inst := range craft.installed {
		p, c, err := craft.activate(editor, inst)
		if err != nil {
			craft.logger.Error("installed Craft application cannot run", "application", app, "error", err)
			// The application stays unavailable; the other one still works.
			out.Components[app] = craftapps.Component{Reason: craftapps.ReasonEditorIsolationFailed, Version: inst.Component.Version, Detail: err.Error()}
			continue
		}
		out.Profiles[app], out.Components[app] = p, c
	}
	if craft.installable() && editor != nil {
		out.Install = craft.installer(editor)
	}
	out.Enabled = len(out.Profiles) > 0 || out.Install != nil
	out.AgentsEnabled = out.Enabled
	if !out.Enabled {
		out.AgentApply, out.AgentInspect = nil, nil
	}
	return out, nil
}

// Only native bundle paths get this additional credentialless gate. Existing
// C1 pins and all other application routes retain their existing policy.
func nativeCraftCredentialGate(cfg *config.Config, craft *craftApps, editor *editororigin.Handler, next http.Handler) http.Handler {
	craft = craftAppsOr(cfg, craft)
	if !craft.enabled() {
		return next
	}
	dedicated := cfg.GridCraft.EditorOrigin != ""
	base := editororigin.SameHostPrefix
	if dedicated {
		base = ""
	}
	prefix := base + editororigin.BundlePrefix
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reuse the serving handler's host predicate, including its established
		// case/port/trailing-dot normalization. Every host spelling that handler
		// can serve gets the same native credential fence; Studio routes do not.
		if dedicated && (editor == nil || !editor.MatchesHost(r.Host)) {
			next.ServeHTTP(w, r)
			return
		}
		// The set grows when an application is installed while running.
		native := false
		if rest, ok := strings.CutPrefix(r.URL.EscapedPath(), prefix); ok {
			digest, _, _ := strings.Cut(rest, "/")
			native = craft.fenced(digest)
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

// sheetWorkbookRequired is the development and test switch CREWSHIP_SHEET_WORKBOOK=1: it asks for
// workbook Sheets, which only a Sheet package with document_format "workbook" provides (the
// package alone decides in a release). Asked without one, the server does not start.
func sheetWorkbookRequired(value string, installed map[string]*craftassets.Installed) error {
	switch value {
	case "":
		return nil
	case "1":
		if inst := installed["gridcraft"]; inst != nil && inst.Component.DocumentFormat == craftassets.DocumentFormatWorkbook {
			return nil
		}
		return errors.New(`CREWSHIP_SHEET_WORKBOOK=1 needs an installed Sheet package whose manifest names document_format "workbook"; the installed one keeps native documents`)
	}
	return errors.New("CREWSHIP_SHEET_WORKBOOK must be 1 or unset")
}
