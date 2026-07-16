package terminal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/gorilla/websocket"
)

// validSlugRe matches safe slug values (alphanumeric, hyphens, underscores).
var validSlugRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// Handler manages WebSocket-based terminal sessions into crew containers.
type Handler struct {
	container provider.ContainerProvider
	logger    *slog.Logger
	validator *auth.JWTValidator
	db        *sql.DB
	// sessionStore backs revocation enforcement for browser tickets
	// (kind=ws tickets carrying a non-empty sid). It mirrors the chat
	// hub's sessions.Store so a force-logged-out / password-changed /
	// admin-revoked user cannot open a NEW container shell within the
	// ticket TTL, and a live shell is torn down when the row is revoked
	// mid-session. May be nil in dev / no-DB paths — the checks are then
	// skipped (same tolerance the slug lookup uses for a nil db).
	sessionStore sessions.Store
	// revokePollInterval is the cadence of the mid-session revocation
	// poll. Defaults to 30s (set in New, matching ws/client.go); tests
	// override it to a few ms to exercise teardown without waiting.
	revokePollInterval time.Duration
	upgrader           websocket.Upgrader
	sessions           sync.Map // sessionID -> *Session
	sessionID          atomic.Int64
}

// Session represents an active terminal connection.
type Session struct {
	id     string
	execID string
	conn   io.ReadWriteCloser // Docker hijacked / Apple pipe connection
	ws     *websocket.Conn
	cancel context.CancelFunc
}

// InitMessage is sent by the client as the first text message after connecting.
type InitMessage struct {
	Mode      string `json:"mode"`       // "shell"
	CrewID    string `json:"crew_id"`    // crew UUID
	CrewSlug  string `json:"crew_slug"`  // crew slug for container lookup
	AgentSlug string `json:"agent_slug"` // optional: agent-level shell
	Rows      uint16 `json:"rows"`
	Cols      uint16 `json:"cols"`
}

// resizeMessage is a control message for terminal resize.
type resizeMessage struct {
	Type string `json:"type"` // "resize"
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}

// New creates a new terminal Handler.
//
// sessionStore enforces session revocation for browser tickets (mirrors
// the chat hub, ws.NewHub). Unlike the hub — which panics on a nil store
// because production always has a DB-backed one — the terminal handler
// tolerates a nil store and skips the revocation checks. This matches
// the handler's existing dev-mode tolerance for a nil db (the crew-slug
// lookup falls back to the client-supplied slug when h.db == nil) and
// keeps the many handler-in-isolation tests that pass nil deps working.
// In production the server always passes a real store built from the
// same *sql.DB that backs the hub's store, so the checks are live.
func New(container provider.ContainerProvider, validator *auth.JWTValidator, db *sql.DB, logger *slog.Logger, sessionStore sessions.Store) *Handler {
	return &Handler{
		container:          container,
		logger:             logger,
		validator:          validator,
		db:                 db,
		sessionStore:       sessionStore,
		revokePollInterval: 30 * time.Second,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin != "" {
					// Browser case: Origin must match Host (same-origin
					// only). Cross-origin browser JS that crafts a WS
					// handshake will still carry the originating page's
					// scheme://host as Origin per the WS spec, so a strict
					// equality check defeats CSRF-style cross-site
					// upgrades.
					host := r.Host
					return origin == "http://"+host || origin == "https://"+host
				}
				// Non-browser clients (CLI / scripts) routinely omit
				// Origin. Pre-Patch-I this was an unconditional allow —
				// any caller that could reach /ws/terminal could pass
				// the CheckOrigin gate just by not setting the header.
				// Now require the explicit X-Crewship-Client header so
				// the caller has to know our protocol, not just stumble
				// into it. Bearer-token auth still happens in the post-
				// upgrade init message so this only adds a CSRF-style
				// gate; legitimate CLI clients (crewship cmd) set the
				// header in cmd_terminal.go.
				return r.Header.Get("X-Crewship-Client") != ""
			},
		},
	}
}

// ServeHTTP handles the WebSocket upgrade and terminal session lifecycle.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.validator == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}

	// Upgrade to WebSocket first (auth happens post-open to avoid token in URL).
	// Origin/CSRF gate is enforced via h.upgrader.CheckOrigin (defined in New()
	// above: same-origin equality for browsers, X-Crewship-Client header for CLI).
	ws, err := h.upgrader.Upgrade(w, r, nil) // nosemgrep: websocket-missing-origin-check — CheckOrigin set on h.upgrader in New()
	if err != nil {
		h.logger.Error("terminal ws upgrade failed", "error", err)
		return
	}
	defer ws.Close()

	// Limit read size to 1 MB to prevent unbounded memory allocation.
	ws.SetReadLimit(1 << 20)

	// Read auth message (first text message from client).
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, authMsg, err := ws.ReadMessage()
	if err != nil {
		h.logger.Warn("terminal auth read failed", "error", err)
		h.writeError(ws, "failed to read auth message")
		return
	}

	var authPayload struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(authMsg, &authPayload); err != nil || authPayload.Type != "auth" || authPayload.Token == "" {
		h.writeError(ws, "invalid auth message")
		return
	}

	// Terminal WS uses the same kind=ws ticket as the chat hub. CLI
	// tokens go through here too (their tickets carry empty sid).
	claims, err := h.validator.ValidateWS(authPayload.Token)
	if err != nil {
		h.logger.Warn("terminal auth failed", "error", err)
		h.writeError(ws, "invalid token")
		return
	}
	userID := claims.ID

	// Mirror the chat hub (ws/hub.go HandleUpgrade): browser tickets carry
	// a sid that joins to user_sessions; refuse the connection if the row
	// is gone or already revoked (force-logout, password change, admin
	// revoke) even though the 15-min ws ticket is still cryptographically
	// valid. This is a shell / code-exec surface, so a revoked user must
	// not be able to open a NEW container shell within the ticket TTL.
	// CLI-derived tickets have no sid — their CLI token is the auth
	// artifact, with its own revocation table — so they skip this check.
	// A nil sessionStore (dev / no-DB) also skips, per the New() contract.
	// Fail CLOSED at connect time (reject on any lookup error): unlike the
	// ongoing poll below, we can't establish a trustworthy session here.
	if claims.Sid != "" && h.sessionStore != nil {
		sess, sErr := h.sessionStore.Get(r.Context(), claims.Sid)
		if sErr != nil {
			if !errors.Is(sErr, sessions.ErrNotFound) {
				h.logger.Error("terminal ws session lookup", "error", sErr, "sid", claims.Sid)
			}
			h.writeError(ws, "session_revoked")
			return
		}
		if !sess.Active(time.Now()) {
			h.writeError(ws, "session_revoked")
			return
		}
	}

	// Read init message (second text message from client).
	_, msg, err := ws.ReadMessage()
	if err != nil {
		h.logger.Warn("terminal init read failed", "error", err)
		h.writeError(ws, "failed to read init message")
		return
	}
	ws.SetReadDeadline(time.Time{}) // clear deadline

	var init InitMessage
	if err := json.Unmarshal(msg, &init); err != nil {
		h.writeError(ws, "invalid init message")
		return
	}

	if init.CrewSlug == "" || init.CrewID == "" {
		h.writeError(ws, "crew_slug and crew_id are required")
		return
	}
	if init.Mode == "" {
		init.Mode = "shell"
	}
	if init.Rows == 0 {
		init.Rows = 24
	}
	if init.Cols == 0 {
		init.Cols = 80
	}

	// Verify user has access to this crew's workspace.
	if err := h.verifyAccess(r.Context(), userID, init.CrewID); err != nil {
		h.logger.Warn("terminal access denied", "user_id", userID, "crew_id", init.CrewID, "error", err)
		h.writeError(ws, "access denied")
		return
	}

	// Resolve actual crew slug from DB to prevent slug spoofing.
	// Fail closed: if DB is available but lookup fails, reject the request.
	var actualSlug string
	if h.db != nil {
		err := h.db.QueryRowContext(r.Context(), "SELECT slug FROM crews WHERE id = ?", init.CrewID).Scan(&actualSlug)
		if err != nil {
			h.logger.Error("terminal: crew slug lookup failed", "crew_id", init.CrewID, "error", err)
			h.writeError(ws, "failed to resolve crew")
			return
		}
	} else {
		actualSlug = init.CrewSlug // dev mode only
	}

	// Ensure container is running (start if needed).
	containerName := h.container.CrewContainerName(init.CrewID, actualSlug)
	status, err := h.container.ContainerStatus(r.Context(), containerName)
	if err != nil || status.State != "running" {
		h.logger.Info("terminal: starting container", "crew_slug", actualSlug)
		h.writeInfo(ws, "Starting container...")
		_, err := h.container.EnsureCrewRuntime(r.Context(), provider.CrewConfig{
			ID:   init.CrewID,
			Slug: actualSlug,
		})
		if err != nil {
			h.logger.Error("terminal: failed to start container", "error", err)
			h.writeError(ws, "failed to start container: "+err.Error())
			return
		}
		// Poll for container readiness with timeout.
		readyCtx, readyCancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer readyCancel()
		ready := false
		for {
			if st, stErr := h.container.ContainerStatus(readyCtx, containerName); stErr == nil && st.State == "running" {
				ready = true
				break
			}
			select {
			case <-readyCtx.Done():
			case <-time.After(200 * time.Millisecond):
				continue
			}
			break
		}
		if !ready {
			h.writeError(ws, "container did not become ready in time")
			return
		}
	}

	interactive, ok := h.container.(provider.InteractiveExecProvider)
	if !ok {
		h.writeError(ws, "terminal not supported by container provider")
		return
	}

	// Validate agent slug to prevent path traversal.
	if init.AgentSlug != "" && !validSlugRe.MatchString(init.AgentSlug) {
		h.writeError(ws, "invalid agent_slug")
		return
	}

	// Build exec config based on mode.
	var execCmd []string
	var execEnv []string
	var workingDir string

	switch init.Mode {
	case "attach":
		// Attach to a running agent's tmux session.
		if init.AgentSlug == "" {
			h.writeError(ws, "agent_slug is required for attach mode")
			return
		}
		tmuxSession := orchestrator.TmuxSessionName(init.AgentSlug)
		// Check if tmux session exists (agent is running).
		checkResult, err := h.container.Exec(r.Context(), provider.ExecConfig{
			ContainerID: containerName,
			Cmd:         []string{"tmux", "has-session", "-t", tmuxSession},
			User:        "1001:1001",
		})
		if err != nil {
			h.writeError(ws, "agent is not running")
			return
		}
		// Read and discard output, check exit code.
		if checkResult.Reader != nil {
			io.Copy(io.Discard, checkResult.Reader)
			checkResult.Reader.Close()
		}
		running, exitCode, inspectErr := h.container.ExecInspect(r.Context(), checkResult.ExecID)
		if inspectErr != nil {
			h.logger.Error("terminal: exec inspect failed", "exec_id", checkResult.ExecID, "error", inspectErr)
			h.writeError(ws, "failed to check agent session")
			return
		}
		if !running && exitCode != 0 {
			h.writeError(ws, "agent is not running (no active tmux session)")
			return
		}

		execCmd = []string{"tmux", "attach", "-t", tmuxSession}
		execEnv = []string{"TERM=xterm-256color"}
		workingDir = ""

	default: // "shell"
		workingDir = "/crew/shared"
		if init.AgentSlug != "" {
			workingDir = "/crew/agents/" + init.AgentSlug
			// Ensure agent directory exists (it's created on first agent run,
			// but user may open terminal before that).
			mkdirResult, err := h.container.Exec(r.Context(), provider.ExecConfig{
				ContainerID: containerName,
				Cmd:         []string{"mkdir", "-p", workingDir},
				User:        "1001:1001",
			})
			if err != nil {
				h.logger.Debug("terminal: mkdir failed", "dir", workingDir, "error", err)
			} else if mkdirResult.Reader != nil {
				io.Copy(io.Discard, mkdirResult.Reader)
				mkdirResult.Reader.Close()
			}
		}
		execCmd = []string{"/bin/bash", "--login"}
		execEnv = []string{
			"TERM=xterm-256color",
			"HOME=/home/agent",
			"PATH=/opt/crew-tools/bin:/home/agent/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		}
	}

	execResult, err := interactive.ExecInteractive(r.Context(), provider.InteractiveExecConfig{
		ContainerID: containerName,
		Cmd:         execCmd,
		Env:         execEnv,
		WorkingDir:  workingDir,
		User:        "1001:1001",
		Rows:        init.Rows,
		Cols:        init.Cols,
	})
	if err != nil {
		h.logger.Error("terminal exec failed", "error", err, "container", containerName)
		h.writeError(ws, "failed to start shell: "+err.Error())
		return
	}
	defer execResult.Conn.Close()

	sessionID := fmt.Sprintf("term-%d", h.sessionID.Add(1))
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	session := &Session{
		id:     sessionID,
		execID: execResult.ExecID,
		conn:   execResult.Conn,
		ws:     ws,
		cancel: cancel,
	}
	h.sessions.Store(sessionID, session)
	defer h.sessions.Delete(sessionID)

	// Ongoing revocation poll for browser sessions: mirror
	// ws/client.go watchSessionRevocation. Every 30s re-check the session
	// row and tear the live shell down (cancel the session ctx) if it has
	// been revoked mid-session, so a force-logout kills an already-open
	// shell rather than letting it run until the exec exits. Transient
	// (non-ErrNotFound) lookup errors are TOLERATED — a DB blip must not
	// kill an active shell. The goroutine exits on ctx.Done() (session /
	// connection teardown) so it never leaks. CLI (empty sid) and nil-store
	// paths don't start it.
	if claims.Sid != "" && h.sessionStore != nil {
		go h.watchSessionRevocation(ctx, cancel, claims.Sid)
	}

	h.logger.Info("terminal session started",
		"session_id", sessionID,
		"user_id", userID,
		"crew_slug", actualSlug,
		"agent_slug", init.AgentSlug,
		"mode", init.Mode,
	)

	// Bridge: two goroutines copying data between WebSocket and container exec.
	var wg sync.WaitGroup
	wg.Add(2)

	// container → ws (stdout)
	go func() {
		defer wg.Done()
		defer cancel()
		buf := make([]byte, 4096)
		for {
			n, err := execResult.Conn.Read(buf)
			if n > 0 {
				if writeErr := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// ws → container (stdin + control messages)
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			msgType, data, err := ws.ReadMessage()
			if err != nil {
				return
			}

			switch msgType {
			case websocket.TextMessage:
				// JSON control message (resize).
				var ctrl resizeMessage
				if json.Unmarshal(data, &ctrl) == nil && ctrl.Type == "resize" && ctrl.Rows > 0 && ctrl.Cols > 0 {
					_ = interactive.ExecResize(ctx, execResult.ExecID, ctrl.Rows, ctrl.Cols)
				}
			case websocket.BinaryMessage:
				// Raw terminal input.
				if _, writeErr := execResult.Conn.Write(data); writeErr != nil {
					return
				}
			}
		}
	}()

	// Wait for either goroutine to finish (connection closed, exec exited).
	<-ctx.Done()
	// Force close both sides to unblock goroutines.
	_ = execResult.Conn.Close()
	_ = ws.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second))
	_ = ws.Close() // unblock ws.ReadMessage() in reader goroutine
	wg.Wait()

	h.logger.Info("terminal session ended", "session_id", sessionID, "user_id", userID)
}

// verifyAccess checks that the user belongs to the workspace that owns the crew
// and has at least MEMBER role (VIEWER cannot use terminal).
//
// Fail-closed: a nil h.db is a configuration bug, not a "dev mode" shortcut.
// The pre-Patch-F branch returned nil on nil db so any valid JWT could open
// /ws/terminal against any crew — which would silently fire in production
// if server.New ever stops panicking on deps.DB == nil for any reason.
// The production constructor still panics on missing deps.DB, so this is
// strictly belt-and-braces.
func (h *Handler) verifyAccess(ctx context.Context, userID, crewID string) error {
	if h.db == nil {
		return fmt.Errorf("terminal: access verification not configured (db is nil)")
	}
	var role string
	err := h.db.QueryRowContext(ctx, `
		SELECT wm.role FROM workspace_members wm
		JOIN crews c ON c.workspace_id = wm.workspace_id
		WHERE wm.user_id = ? AND c.id = ?
	`, userID, crewID).Scan(&role)
	if err != nil {
		return fmt.Errorf("access check query: %w", err)
	}
	if role == "VIEWER" {
		return fmt.Errorf("user %s has insufficient role for terminal access", userID)
	}
	return nil
}

// watchSessionRevocation polls user_sessions every 30s for the given sid
// and cancels the terminal session (tearing down the live shell) once the
// row is definitively revoked — gone (ErrNotFound) or !Active. It mirrors
// ws/client.go watchSessionRevocation, including the crucial transient-
// error tolerance: a lookup error that is NOT ErrNotFound (DB timeout,
// momentary unavailability) is logged and skipped so a backend hiccup
// doesn't kill a working shell. The next tick retries; if the row really
// is revoked, ErrNotFound on a later tick closes it cleanly.
//
// The goroutine exits when ctx is done (the session/connection ended) so
// it does not leak. cancel() unblocks the main handler's <-ctx.Done(),
// which force-closes the exec conn and the websocket.
func (h *Handler) watchSessionRevocation(ctx context.Context, cancel context.CancelFunc, sid string) {
	interval := h.revokePollInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lookupCtx, lookupCancel := context.WithTimeout(ctx, 5*time.Second)
			sess, err := h.sessionStore.Get(lookupCtx, sid)
			lookupCancel()

			revoked := false
			switch {
			case errors.Is(err, sessions.ErrNotFound):
				revoked = true
			case err == nil && sess != nil && !sess.Active(time.Now()):
				revoked = true
			case err != nil:
				// Transient — skip this tick, keep the shell alive.
				h.logger.Debug("terminal session-revoke poll: transient error, retrying next tick",
					"error", err, "sid", sid)
				continue
			}

			if !revoked {
				continue
			}

			h.logger.Info("terminal session revoked mid-session, tearing down shell", "sid", sid)
			cancel()
			return
		}
	}
}

// writeError sends a JSON error message to the WebSocket client.
func (h *Handler) writeError(ws *websocket.Conn, msg string) {
	data, _ := json.Marshal(map[string]string{"type": "error", "message": msg})
	_ = ws.WriteMessage(websocket.TextMessage, data)
}

// writeInfo sends a JSON info message to the WebSocket client.
func (h *Handler) writeInfo(ws *websocket.Conn, msg string) {
	data, err := json.Marshal(map[string]string{"type": "info", "message": msg})
	if err != nil {
		return
	}
	_ = ws.WriteMessage(websocket.TextMessage, data)
}
