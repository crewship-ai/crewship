package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CrewMessagingHandler handles cross-crew messaging and file sharing.
// All requests come from sidecar (internal auth via X-Internal-Token).
type CrewMessagingHandler struct {
	db          *sql.DB
	storagePath string // base path for crew file storage
	logger      *slog.Logger
}

func NewCrewMessagingHandler(db *sql.DB, storagePath string, logger *slog.Logger) *CrewMessagingHandler {
	return &CrewMessagingHandler{db: db, storagePath: storagePath, logger: logger}
}

// --- Messages ---

type sendMessageRequest struct {
	FromCrewID  string          `json:"from_crew_id"`
	ToCrewID    string          `json:"to_crew_id"`
	FromAgentID string          `json:"from_agent_id"`
	WorkspaceID string          `json:"workspace_id"`
	Content     string          `json:"content"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

type messageResponse struct {
	ID          string           `json:"id"`
	FromCrewID  string           `json:"from_crew_id"`
	ToCrewID    string           `json:"to_crew_id"`
	FromAgentID string           `json:"from_agent_id,omitempty"`
	Content     string           `json:"content"`
	Metadata    *json.RawMessage `json:"metadata,omitempty"`
	DeliveredAt *string          `json:"delivered_at,omitempty"`
	CreatedAt   string           `json:"created_at"`
}

// SendMessage handles POST /api/v1/internal/crew-messages
func (h *CrewMessagingHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	var req sendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if req.FromCrewID == "" || req.ToCrewID == "" || req.Content == "" || req.WorkspaceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from_crew_id, to_crew_id, workspace_id, and content are required"})
		return
	}

	if len(req.Content) > 1<<20 { // 1MB max message size
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content too large (max 1MB)"})
		return
	}

	// Validate connection exists and direction permits this message.
	allowed, err := h.canCommunicate(r, req.FromCrewID, req.ToCrewID)
	if err != nil {
		h.logger.Error("check crew connection", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if !allowed {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no active connection from source to target crew"})
		return
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	var metadataStr *string
	if req.Metadata != nil {
		s := string(req.Metadata)
		metadataStr = &s
	}

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO crew_messages (id, workspace_id, from_crew_id, to_crew_id, from_agent_id, content, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, req.WorkspaceID, req.FromCrewID, req.ToCrewID, req.FromAgentID, req.Content, metadataStr, now)
	if err != nil {
		h.logger.Error("insert crew message", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to store message"})
		return
	}

	// Audit log
	h.logAudit(r, req.WorkspaceID, "message_sent", req.FromCrewID, req.ToCrewID, req.FromAgentID, map[string]string{
		"message_id":     id,
		"content_length": fmt.Sprintf("%d", len(req.Content)),
	})

	writeJSON(w, http.StatusCreated, messageResponse{
		ID:          id,
		FromCrewID:  req.FromCrewID,
		ToCrewID:    req.ToCrewID,
		FromAgentID: req.FromAgentID,
		Content:     req.Content,
		Metadata:    ptrRawJSON(req.Metadata),
		CreatedAt:   now,
	})
}

// ListMessages handles GET /api/v1/internal/crew-messages
// Query params: crew_id (required), direction=incoming|outgoing|all, limit, since
func (h *CrewMessagingHandler) ListMessages(w http.ResponseWriter, r *http.Request) {
	crewID := r.URL.Query().Get("crew_id")
	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crew_id is required"})
		return
	}

	direction := r.URL.Query().Get("direction")
	if direction == "" {
		direction = "incoming"
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}

	since := r.URL.Query().Get("since")     // RFC3339 timestamp
	peerCrewID := r.URL.Query().Get("peer_crew_id") // optional: filter to specific peer

	var query string
	var args []interface{}

	cols := `id, workspace_id, from_crew_id, to_crew_id, from_agent_id, content, metadata, delivered_at, created_at`

	switch direction {
	case "outgoing":
		query = `SELECT ` + cols + ` FROM crew_messages WHERE from_crew_id = ?`
		args = []interface{}{crewID}
	case "all":
		query = `SELECT ` + cols + ` FROM crew_messages WHERE (from_crew_id = ? OR to_crew_id = ?)`
		args = []interface{}{crewID, crewID}
	default: // "incoming"
		query = `SELECT ` + cols + ` FROM crew_messages WHERE to_crew_id = ?`
		args = []interface{}{crewID}
	}

	// Filter to a specific peer crew (scopes messages to one connection).
	if peerCrewID != "" {
		query += " AND (from_crew_id = ? OR to_crew_id = ?)"
		args = append(args, peerCrewID, peerCrewID)
	}

	if since != "" {
		query += " AND created_at > ?"
		args = append(args, since)
	}

	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT %d", limit)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("list crew messages", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	defer rows.Close()

	var messages []messageResponse
	for rows.Next() {
		var m messageResponse
		var wsID string
		var metadata, deliveredAt, fromAgent sql.NullString
		if err := rows.Scan(&m.ID, &wsID, &m.FromCrewID, &m.ToCrewID, &fromAgent, &m.Content, &metadata, &deliveredAt, &m.CreatedAt); err != nil {
			h.logger.Error("scan crew message", "error", err)
			continue
		}
		if fromAgent.Valid {
			m.FromAgentID = fromAgent.String
		}
		if metadata.Valid {
			raw := json.RawMessage(metadata.String)
			m.Metadata = &raw
		}
		if deliveredAt.Valid {
			m.DeliveredAt = &deliveredAt.String
		}
		messages = append(messages, m)
	}
	if messages == nil {
		messages = []messageResponse{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"data": messages})
}

// --- Files ---

// ReadFile handles GET /api/v1/internal/crew-files/{crewId}
// Query params: path (required), requester_crew_id (required)
func (h *CrewMessagingHandler) ReadFile(w http.ResponseWriter, r *http.Request) {
	targetCrewID := r.PathValue("crewId")
	filePath := r.URL.Query().Get("path")
	requesterCrewID := r.URL.Query().Get("requester_crew_id")

	if targetCrewID == "" || filePath == "" || requesterCrewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crewId, path, and requester_crew_id are required"})
		return
	}

	// Validate connection: requester must be able to communicate with target.
	allowed, err := h.canCommunicate(r, requesterCrewID, targetCrewID)
	if err != nil {
		h.logger.Error("check crew connection for file read", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if !allowed {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no active connection to target crew"})
		return
	}

	// Sanitize path: must be within /crew/shared/ and cannot contain ..
	cleanPath := filepath.Clean(filePath)
	if strings.Contains(cleanPath, "..") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}

	// Resolve to host filesystem: {storagePath}/crews/{crewId}/shared/{path}
	absPath := filepath.Join(h.storagePath, "crews", targetCrewID, "shared", cleanPath)

	// Verify the resolved path is still within the expected directory.
	crewSharedDir := filepath.Join(h.storagePath, "crews", targetCrewID, "shared")
	if !strings.HasPrefix(absPath, crewSharedDir) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path traversal not allowed"})
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found"})
			return
		}
		h.logger.Error("stat crew file", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	if info.IsDir() {
		// List directory contents
		entries, err := os.ReadDir(absPath)
		if err != nil {
			h.logger.Error("read crew directory", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		type fileEntry struct {
			Name  string `json:"name"`
			IsDir bool   `json:"is_dir"`
			Size  int64  `json:"size"`
		}
		var files []fileEntry
		for _, e := range entries {
			fi, _ := e.Info()
			size := int64(0)
			if fi != nil {
				size = fi.Size()
			}
			files = append(files, fileEntry{Name: e.Name(), IsDir: e.IsDir(), Size: size})
		}
		if files == nil {
			files = []fileEntry{}
		}

		h.logAudit(r, "", "file_list", requesterCrewID, targetCrewID, "", map[string]string{"path": filePath})
		writeJSON(w, http.StatusOK, map[string]interface{}{"entries": files})
		return
	}

	// Limit file size to 10MB for streaming
	if info.Size() > 10<<20 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file too large (max 10MB)"})
		return
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		h.logger.Error("read crew file", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	h.logAudit(r, "", "file_read", requesterCrewID, targetCrewID, "", map[string]string{
		"path": filePath,
		"size": fmt.Sprintf("%d", info.Size()),
	})

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-File-Name", filepath.Base(absPath))
	w.Header().Set("X-File-Size", fmt.Sprintf("%d", info.Size()))
	w.Write(data)
}

// WriteFile handles POST /api/v1/internal/crew-files/{crewId}
// Body: multipart form with "file" field and "requester_crew_id", "path" fields.
func (h *CrewMessagingHandler) WriteFile(w http.ResponseWriter, r *http.Request) {
	targetCrewID := r.PathValue("crewId")

	// Limit upload to 10MB
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid multipart form or file too large (max 10MB)"})
		return
	}

	requesterCrewID := r.FormValue("requester_crew_id")
	destPath := r.FormValue("path")

	if targetCrewID == "" || requesterCrewID == "" || destPath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crewId, requester_crew_id, and path are required"})
		return
	}

	// Validate connection: requester must be able to write to target.
	// For unidirectional connections, only the source can write.
	allowed, err := h.canCommunicate(r, requesterCrewID, targetCrewID)
	if err != nil {
		h.logger.Error("check crew connection for file write", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if !allowed {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no active connection to target crew"})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file field is required"})
		return
	}
	defer file.Close()

	// Sanitize destination path
	cleanPath := filepath.Clean(destPath)
	if strings.Contains(cleanPath, "..") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}

	// Write to: {storagePath}/crews/{crewId}/shared/incoming/{requester-crew-id}/{path}
	absDir := filepath.Join(h.storagePath, "crews", targetCrewID, "shared", "incoming", requesterCrewID, filepath.Dir(cleanPath))
	crewSharedDir := filepath.Join(h.storagePath, "crews", targetCrewID, "shared")
	if !strings.HasPrefix(absDir, crewSharedDir) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path traversal not allowed"})
		return
	}

	if err := os.MkdirAll(absDir, 0755); err != nil {
		h.logger.Error("create incoming dir", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	absPath := filepath.Join(absDir, filepath.Base(cleanPath))
	dst, err := os.Create(absPath)
	if err != nil {
		h.logger.Error("create file", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	defer dst.Close()

	written, err := io.Copy(dst, file)
	if err != nil {
		h.logger.Error("write file", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "write failed"})
		return
	}

	// Chown to agent user (1001:1001) so container can read it.
	_ = os.Chown(absPath, 1001, 1001)

	h.logAudit(r, "", "file_written", requesterCrewID, targetCrewID, "", map[string]string{
		"path":          destPath,
		"original_name": header.Filename,
		"size":          fmt.Sprintf("%d", written),
	})

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"path":    destPath,
		"size":    written,
		"crew_id": targetCrewID,
	})
}

// --- Helpers ---

// canCommunicate checks if fromCrewID is allowed to send to toCrewID.
func (h *CrewMessagingHandler) canCommunicate(r *http.Request, fromCrewID, toCrewID string) (bool, error) {
	var exists bool
	err := h.db.QueryRowContext(r.Context(), `
		SELECT 1 FROM crew_connections
		WHERE status = 'active' AND (
			(from_crew_id = ? AND to_crew_id = ?)
			OR (from_crew_id = ? AND to_crew_id = ? AND direction = 'bidirectional')
		)`, fromCrewID, toCrewID, toCrewID, fromCrewID).Scan(&exists)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (h *CrewMessagingHandler) resolveWorkspaceID(ctx context.Context, crewID string) string {
	if crewID == "" {
		return ""
	}
	var wsID string
	h.db.QueryRowContext(ctx, "SELECT workspace_id FROM crews WHERE id = ?", crewID).Scan(&wsID)
	return wsID
}

func (h *CrewMessagingHandler) logAudit(r *http.Request, workspaceID, action, fromCrewID, toCrewID, agentID string, details map[string]string) {
	if workspaceID == "" {
		workspaceID = h.resolveWorkspaceID(r.Context(), fromCrewID)
	}
	detailsJSON, _ := json.Marshal(details)
	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO crew_audit_log (id, workspace_id, action, from_crew_id, to_crew_id, agent_id, details, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		generateCUID(), workspaceID, action, fromCrewID, toCrewID, agentID, string(detailsJSON), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		h.logger.Warn("failed to log crew audit", "action", action, "error", err)
	}
}

func ptrRawJSON(data json.RawMessage) *json.RawMessage {
	if data == nil {
		return nil
	}
	return &data
}
