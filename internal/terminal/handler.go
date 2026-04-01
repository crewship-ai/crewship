package terminal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
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
	upgrader  websocket.Upgrader
	sessions  sync.Map // sessionID -> *Session
	sessionID atomic.Int64
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
func New(container provider.ContainerProvider, validator *auth.JWTValidator, db *sql.DB, logger *slog.Logger) *Handler {
	return &Handler{
		container: container,
		logger:    logger,
		validator: validator,
		db:        db,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true // non-browser clients
			}
			host := r.Host
			return origin == "http://"+host || origin == "https://"+host
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
	ws, err := h.upgrader.Upgrade(w, r, nil)
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

	claims, err := h.validator.Validate(authPayload.Token)
	if err != nil {
		h.logger.Warn("terminal auth failed", "error", err)
		h.writeError(ws, "invalid token")
		return
	}
	userID := claims.ID

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
	// The client supplies crew_slug for convenience, but we must use the slug
	// that corresponds to the verified crew_id.
	actualSlug := init.CrewSlug
	if h.db != nil {
		var dbSlug string
		err := h.db.QueryRowContext(r.Context(), "SELECT slug FROM crews WHERE id = ?", init.CrewID).Scan(&dbSlug)
		if err == nil && dbSlug != "" {
			actualSlug = dbSlug
		}
	}

	// Ensure container is running (start if needed).
	containerName := h.container.CrewContainerName(actualSlug)
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
		// Give the container a moment to fully initialize.
		h.logger.Debug("waiting 1s for container init", "crew_slug", actualSlug)
		time.Sleep(time.Second)
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
		running, exitCode, _ := h.container.ExecInspect(r.Context(), checkResult.ExecID)
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
			if err == nil && mkdirResult.Reader != nil {
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
		Rows:       init.Rows,
		Cols:       init.Cols,
	})
	if err != nil {
		h.logger.Error("terminal exec failed", "error", err, "container", containerName)
		h.writeError(ws, "failed to start shell: "+err.Error())
		return
	}
	defer execResult.Conn.Close()

	sessionID := fmt.Sprintf("term-%d", h.sessionID.Add(1))
	ctx, cancel := context.WithCancel(context.Background())
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
	execResult.Conn.Close()
	ws.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second))
	wg.Wait()

	h.logger.Info("terminal session ended", "session_id", sessionID, "user_id", userID)
}

// verifyAccess checks that the user belongs to the workspace that owns the crew.
func (h *Handler) verifyAccess(ctx context.Context, userID, crewID string) error {
	if h.db == nil {
		return nil // no DB = no auth check (dev mode)
	}
	var count int
	err := h.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM workspace_members wm
		JOIN crews c ON c.workspace_id = wm.workspace_id
		WHERE wm.user_id = ? AND c.id = ?
	`, userID, crewID).Scan(&count)
	if err != nil {
		return fmt.Errorf("access check query: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("user %s has no access to crew %s", userID, crewID)
	}
	return nil
}

// writeError sends a JSON error message to the WebSocket client.
func (h *Handler) writeError(ws *websocket.Conn, msg string) {
	data, _ := json.Marshal(map[string]string{"type": "error", "message": msg})
	_ = ws.WriteMessage(websocket.TextMessage, data)
}

// writeInfo sends a JSON info message to the WebSocket client.
func (h *Handler) writeInfo(ws *websocket.Conn, msg string) {
	data, _ := json.Marshal(map[string]string{"type": "info", "message": msg})
	_ = ws.WriteMessage(websocket.TextMessage, data)
}
