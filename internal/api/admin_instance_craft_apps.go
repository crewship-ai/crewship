package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/editororigin"
)

// CraftAppsAdminHandler is Admin › Artifacts › Applications (#5315): the
// instance administrator turns Word, Sheet and agents in them on or off, and
// runs a self-test of an application's engine. Turning off never deletes or
// changes a document.
type CraftAppsAdminHandler struct {
	r *Router
}

type craftAppView struct {
	ID         string               `json:"id"`
	Label      string               `json:"label"`
	Desired    craftapps.Desired    `json:"desired"`
	Source     craftapps.Source     `json:"source"`
	Locked     bool                 `json:"locked"`
	State      craftapps.State      `json:"state"`
	Reason     craftapps.Reason     `json:"reason,omitempty"`
	Message    string               `json:"message,omitempty"`
	DrainUntil *time.Time           `json:"drain_until,omitempty"`
	UpdatedBy  string               `json:"updated_by,omitempty"`
	UpdatedAt  *time.Time           `json:"updated_at,omitempty"`
	Component  *craftapps.Component `json:"component,omitempty"`
	Documents  *int                 `json:"documents,omitempty"`
}

type craftAppsView struct {
	Applications []craftAppView `json:"applications"`
	Agents       craftAppView   `json:"agents"`
	// Editor is the editor origin's isolation check ("ok", "pending",
	// "failed", "disabled" or "off").
	Editor string `json:"editor"`
}

func (h *CraftAppsAdminHandler) policy() *craftapps.Policy {
	if p := h.r.craftNativeHTTPConfig.Policy; p != nil {
		return p
	}
	return &craftapps.Policy{}
}

func (h *CraftAppsAdminHandler) component(app string) craftapps.Component {
	h.r.craftComponentsMu.Lock()
	c, ok := h.r.craftNativeHTTPConfig.Components[app]
	h.r.craftComponentsMu.Unlock()
	if !ok {
		return craftapps.Component{Reason: craftapps.ReasonComponentMissing}
	}
	if c.Available {
		if _, live := h.r.craftNativeDocuments.profile(app); !live || h.r.craftNativeStartupErr != nil {
			c.Available, c.Reason = false, craftapps.ReasonEngineStartFailed
		}
	}
	return c
}

func (h *CraftAppsAdminHandler) setComponent(app string, c craftapps.Component) {
	h.r.craftComponentsMu.Lock()
	defer h.r.craftComponentsMu.Unlock()
	if h.r.craftNativeHTTPConfig.Components == nil {
		h.r.craftNativeHTTPConfig.Components = map[string]craftapps.Component{}
	}
	h.r.craftNativeHTTPConfig.Components[app] = c
}

// installable reports whether the application can be installed now: the
// server can install packages and the application does not run yet.
func (h *CraftAppsAdminHandler) installable(app string) bool {
	if h.r.craftNativeHTTPConfig.Install == nil || h.r.craftNativeDocuments == nil || h.r.craftNativeStartupErr != nil {
		return false
	}
	switch h.component(app).Reason {
	case craftapps.ReasonNotInstalled, craftapps.ReasonInstallFailed, craftapps.ReasonComponentIntegrity:
		return true
	}
	return false
}

var (
	errCraftInstallRunning     = errors.New("the application is already being installed")
	errCraftInstallUnsupported = errors.New("this server cannot install the application: it already runs, or the build trusts no package of it")
)

// beginInstall marks the application as installing; at most one
// installation per application runs at a time.
func (h *CraftAppsAdminHandler) beginInstall(app string) (craftapps.Component, error) {
	if !h.installable(app) {
		return craftapps.Component{}, errCraftInstallUnsupported
	}
	h.r.craftComponentsMu.Lock()
	defer h.r.craftComponentsMu.Unlock()
	if h.r.craftInstalling == nil {
		h.r.craftInstalling = map[string]bool{}
	}
	if h.r.craftInstalling[app] {
		return craftapps.Component{}, errCraftInstallRunning
	}
	// Checked again under the lock: another installation may have finished
	// since installable() above.
	switch h.r.craftNativeHTTPConfig.Components[app].Reason {
	case craftapps.ReasonNotInstalled, craftapps.ReasonInstallFailed, craftapps.ReasonComponentIntegrity:
	default:
		return craftapps.Component{}, errCraftInstallUnsupported
	}
	h.r.craftInstalling[app] = true
	if h.r.craftNativeHTTPConfig.Components == nil {
		h.r.craftNativeHTTPConfig.Components = map[string]craftapps.Component{}
	}
	previous := h.r.craftNativeHTTPConfig.Components[app]
	h.r.craftNativeHTTPConfig.Components[app] = craftapps.Component{Reason: craftapps.ReasonInstalling}
	return previous, nil
}

// finishInstall installs and activates the application; archive nil
// downloads it. On failure Admin shows component_install_failed and why.
func (h *CraftAppsAdminHandler) finishInstall(ctx context.Context, app string, previous craftapps.Component, archive io.Reader) error {
	defer func() {
		h.r.craftComponentsMu.Lock()
		delete(h.r.craftInstalling, app)
		h.r.craftComponentsMu.Unlock()
	}()
	profile, c, err := h.r.craftNativeHTTPConfig.Install(ctx, app, archive)
	if err == nil {
		err = h.r.craftNativeDocuments.activate(profile)
		sha := profile.CLISHA256
		if profile.Engine != nil {
			sha = profile.Engine.SHA256()
		}
		if running, ok := h.r.craftNativeDocuments.profile(app); errors.Is(err, errCraftAppRunning) && ok && running.CLISHA256 == sha && running.EditorBundle == profile.EditorBundle {
			err = nil // the same package already runs
		}
	}
	if err != nil {
		h.r.logger.Warn("Craft application install failed", "application", app, "error", err)
		previous.Available, previous.Reason, previous.Detail = false, craftapps.ReasonInstallFailed, err.Error()
		h.setComponent(app, previous)
		return err
	}
	h.r.logger.Info("Craft application installed", "application", app, "version", c.Version, "engine_sha256", c.EngineSHA256)
	h.setComponent(app, c)
	return nil
}

// startInstall downloads the application in the background; Admin shows
// component_installing until it finishes.
func (h *CraftAppsAdminHandler) startInstall(app string) error {
	previous, err := h.beginInstall(app)
	if err != nil {
		return err
	}
	done := beginBackgroundWork()
	go func() {
		defer done()
		ctx, cancel := context.WithTimeout(h.r.craftInstallContext(), 30*time.Minute)
		defer cancel()
		_ = h.finishInstall(ctx, app, previous, nil)
	}()
	return nil
}

func (h *CraftAppsAdminHandler) editorState() string {
	if h.r.workbookEditorDetail == nil {
		return "off"
	}
	if d, ok := h.r.workbookEditorDetail().(editororigin.Detail); ok {
		if d.Status == "" {
			return "ok"
		}
		return d.Status
	}
	return "ok"
}

func (h *CraftAppsAdminHandler) view(ctx context.Context) (craftAppsView, error) {
	p := h.policy()
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	out := craftAppsView{Editor: h.editorState()}
	anyOpen := false
	for _, app := range craftapps.Applications {
		s, err := p.Get(ctx, h.r.db, app)
		if err != nil {
			return out, err
		}
		c := h.component(app)
		state, reason := craftapps.Evaluate(s, c, now)
		if state == craftapps.StateReady && (out.Editor == "failed" || out.Editor == "disabled") {
			state, reason = craftapps.StateUnavailable, craftapps.ReasonEditorIsolationFailed
		}
		anyOpen = anyOpen || state == craftapps.StateReady
		var n int
		if err = h.r.db.QueryRowContext(ctx, `SELECT count(*) FROM craft_native_documents WHERE application_id=?`, app).Scan(&n); err != nil {
			return out, err
		}
		component := c
		out.Applications = append(out.Applications, craftAppView{ID: app, Label: craftapps.Label(app), Desired: s.Desired, Source: s.Source,
			Locked: s.Source == craftapps.SourceServerConfig, State: state, Reason: reason, Message: reason.Message(),
			DrainUntil: s.DrainUntil, UpdatedBy: s.UpdatedBy, UpdatedAt: s.UpdatedAt, Component: &component, Documents: &n})
	}
	s, err := p.Get(ctx, h.r.db, craftapps.Agents)
	if err != nil {
		return out, err
	}
	agents := craftAppView{ID: craftapps.Agents, Label: craftapps.Label(craftapps.Agents), Desired: s.Desired, Source: s.Source,
		Locked: s.Source == craftapps.SourceServerConfig, UpdatedBy: s.UpdatedBy, UpdatedAt: s.UpdatedAt}
	switch {
	case s.Mode(now) != craftapps.ModeOpen:
		agents.State, agents.Reason = craftapps.StateDisabled, craftapps.ReasonAppDisabled
	case !anyOpen:
		agents.State, agents.Reason = craftapps.StateUnavailable, craftapps.ReasonAgentsNeedApplication
	default:
		agents.State = craftapps.StateReady
	}
	agents.Message = agents.Reason.Message()
	out.Agents = agents
	return out, nil
}

// List is GET /api/v1/admin/instance/artifacts/applications.
func (h *CraftAppsAdminHandler) List(w http.ResponseWriter, req *http.Request) {
	v, err := h.view(req.Context())
	if err != nil {
		internalError(w, req, h.r.logger, "load Craft applications", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, v)
}

// Patch is PATCH /api/v1/admin/instance/artifacts/applications/{app}:
//
//	{ "desired": "disabled", "drain_seconds": 900 }
//
// {app} is wordcraft, gridcraft or agents. Turning an application off with a
// drain lets editors that are already open finish and save for that long;
// without one it closes at once. Documents are never changed.
func (h *CraftAppsAdminHandler) Patch(w http.ResponseWriter, req *http.Request) {
	subject := req.PathValue("app")
	if !craftapps.IsSubject(subject) {
		replyError(w, http.StatusNotFound, craftapps.ErrSubject.Error())
		return
	}
	var body struct {
		Desired      craftapps.Desired `json:"desired"`
		DrainSeconds *int              `json:"drain_seconds"`
	}
	dec := json.NewDecoder(io.LimitReader(req.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	drain := time.Duration(0)
	if body.DrainSeconds != nil {
		if *body.DrainSeconds < 0 || *body.DrainSeconds > int(craftapps.MaxDrain/time.Second) {
			replyError(w, http.StatusBadRequest, craftapps.ErrDrain.Error())
			return
		}
		drain = time.Duration(*body.DrainSeconds) * time.Second
	} else if body.Desired == craftapps.Disabled && subject != craftapps.Agents {
		drain = craftapps.DefaultDrain
	}
	actor := ""
	if u := UserFromContext(req.Context()); u != nil {
		actor = u.Email
		if actor == "" {
			actor = u.ID
		}
	}
	ctx := req.Context()
	tx, err := h.r.db.BeginTx(ctx, nil)
	if err != nil {
		internalError(w, req, h.r.logger, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	before, after, err := h.policy().Set(ctx, tx, subject, body.Desired, actor, drain)
	switch {
	case errors.Is(err, craftapps.ErrDesired), errors.Is(err, craftapps.ErrDrain):
		replyError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, craftapps.ErrLocked):
		replyError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		internalError(w, req, h.r.logger, "set Craft application", err)
		return
	}
	if before != after {
		meta := map[string]any{"subject": subject, "desired_from": before.Desired, "desired_to": after.Desired}
		if after.DrainUntil != nil {
			meta["drain_until"] = after.DrainUntil.UTC().Format(time.RFC3339)
		}
		if err = auditInstance(ctx, req, tx, "instance.craft_application_set", "craft_application", subject, "", meta); err != nil {
			internalError(w, req, h.r.logger, "audit", err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		internalError(w, req, h.r.logger, "commit", err)
		return
	}
	// Turning an application on that is not installed yet downloads it.
	if after.Desired == craftapps.Enabled && craftapps.IsApplication(subject) && h.installable(subject) {
		_ = h.startInstall(subject)
	}
	h.List(w, req)
}

// Install is POST /api/v1/admin/instance/artifacts/applications/{app}/install.
// Without a body it downloads the package this release trusts and returns 202
// at once; Admin shows component_installing until it is done. With an
// application/octet-stream body (a .craftpkg file, offline) it installs that
// package, which must be the same trusted one, and answers when it is done.
// The application starts running without a restart; whether people may use
// it is still the setting above.
func (h *CraftAppsAdminHandler) Install(w http.ResponseWriter, req *http.Request) {
	app := req.PathValue("app")
	if !craftapps.IsApplication(app) {
		replyError(w, http.StatusNotFound, craftapps.ErrSubject.Error())
		return
	}
	if !h.installable(app) {
		replyError(w, http.StatusConflict, errCraftInstallUnsupported.Error())
		return
	}
	file := strings.HasPrefix(req.Header.Get("Content-Type"), "application/octet-stream") && req.ContentLength != 0
	audit := func(result string, detail map[string]any) bool {
		meta := map[string]any{"source": "download", "result": result}
		if file {
			meta["source"] = "file"
		}
		for k, v := range detail {
			meta[k] = v
		}
		if err := auditInstance(req.Context(), req, h.r.db, "instance.craft_application_install", "craft_application", app, "", meta); err != nil {
			internalError(w, req, h.r.logger, "audit", err)
			return false
		}
		return true
	}
	if !file {
		if err := h.startInstall(app); err != nil {
			replyError(w, http.StatusConflict, err.Error())
			return
		}
		if !audit("started", nil) {
			return
		}
		v, err := h.view(req.Context())
		if err != nil {
			internalError(w, req, h.r.logger, "load Craft applications", err)
			return
		}
		writeJSON(w, http.StatusAccepted, v)
		return
	}
	previous, err := h.beginInstall(app)
	if err != nil {
		replyError(w, http.StatusConflict, err.Error())
		return
	}
	// The route is exempt from the API body cap and the whole-request read
	// timeout; this limit and a deadline that moves with progress apply.
	body := progressDeadlineReader{reader: http.MaxBytesReader(w, req.Body, craftPackageLimit), controller: http.NewResponseController(w), idle: uploadIdleTimeout}
	if err = h.finishInstall(context.WithoutCancel(req.Context()), app, previous, body); err != nil {
		if !audit("refused", map[string]any{"error": err.Error()}) {
			return
		}
		replyError(w, http.StatusUnprocessableEntity, "the package was refused: "+err.Error())
		return
	}
	c := h.component(app)
	if !audit("installed", map[string]any{"version": c.Version, "engine_sha256": c.EngineSHA256, "editor_bundle": c.EditorBundle}) {
		return
	}
	h.List(w, req)
}

// craftPackageLimit bounds an uploaded package (craftassets.MaxPackageBytes).
const craftPackageLimit = 512 << 20

const craftApplicationsPath = "/api/v1/admin/instance/artifacts/applications"

// craftPackageUploadPath matches the install route literal in router_admin.go
// for an application, the one route here exempt from the API body cap.
func craftPackageUploadPath(p string) bool {
	return p == craftApplicationsPath+"/"+craftapps.Word+"/install" || p == craftApplicationsPath+"/"+craftapps.Sheet+"/install"
}

type craftAppDiagnostics struct {
	ID         string           `json:"id"`
	Passed     bool             `json:"passed"`
	Reason     craftapps.Reason `json:"reason,omitempty"`
	Message    string           `json:"message,omitempty"`
	Runtime    string           `json:"runtime,omitempty"`
	EngineSHA  string           `json:"engine_sha256,omitempty"`
	Version    string           `json:"version,omitempty"`
	DurationMS int64            `json:"duration_ms"`
	Editor     string           `json:"editor"`
}

// Diagnose is POST /api/v1/admin/instance/artifacts/applications/{app}/diagnostics.
// It starts the application's engine (compiling it on first use) and asks it
// for its version. It reads and writes no document.
func (h *CraftAppsAdminHandler) Diagnose(w http.ResponseWriter, req *http.Request) {
	app := req.PathValue("app")
	if !craftapps.IsApplication(app) {
		replyError(w, http.StatusNotFound, craftapps.ErrSubject.Error())
		return
	}
	out := craftAppDiagnostics{ID: app, Editor: h.editorState()}
	c := h.component(app)
	profile, ok := h.r.craftNativeDocuments.profile(app)
	if !c.Available || !ok || profile.Engine == nil {
		out.Reason = c.Reason
		if out.Reason == craftapps.ReasonNone {
			out.Reason = craftapps.ReasonComponentMissing
		}
		out.Message = out.Reason.Message()
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Runtime, out.EngineSHA = profile.Engine.Runtime(), profile.Engine.SHA256()
	started := time.Now()
	dir, err := os.MkdirTemp("", "craft-selftest-")
	if err != nil {
		internalError(w, req, h.r.logger, "self-test directory", err)
		return
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 2*time.Minute)
	defer cancel()
	var stdout, stderr bytes.Buffer
	err = profile.Engine.Run(ctx, dir, []string{"--version"}, &limitedBuffer{b: &stdout, n: 4096}, &limitedBuffer{b: &stderr, n: 4096})
	out.DurationMS = time.Since(started).Milliseconds()
	version := strings.TrimSpace(stdout.String())
	if err != nil || !strings.Contains(strings.ToLower(version), app) {
		h.r.logger.Warn("Craft engine self-test failed", "application", app, "error", err, "stderr", strings.TrimSpace(stderr.String()))
		out.Reason = craftapps.ReasonEngineSelftestFailed
		out.Message = out.Reason.Message()
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Passed, out.Version = true, version
	if out.Editor == "failed" || out.Editor == "disabled" {
		out.Passed, out.Reason = false, craftapps.ReasonEditorIsolationFailed
		out.Message = out.Reason.Message()
	}
	writeJSON(w, http.StatusOK, out)
}

type limitedBuffer struct {
	b *bytes.Buffer
	n int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.b.Len()+len(p) > l.n {
		return 0, errors.New("output limit")
	}
	return l.b.Write(p)
}

// craftInstallContext is cancelled when the router shuts down, so a download
// in progress stops and leaves only staging files the store sweeps at start.
func (r *Router) craftInstallContext() context.Context {
	r.craftComponentsMu.Lock()
	defer r.craftComponentsMu.Unlock()
	if r.craftInstallCtx == nil {
		r.craftInstallCtx, r.craftInstallCancel = context.WithCancel(context.Background())
	}
	return r.craftInstallCtx
}
