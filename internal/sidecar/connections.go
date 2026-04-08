package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// handleConnectionsList handles GET /connections — returns the list of connected crews
// for this agent's crew (from the pre-loaded IPC config connections).
func (s *Server) handleConnectionsList(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	type connEntry struct {
		CrewSlug  string `json:"crew_slug"`
		Direction string `json:"direction"`
	}

	// Build connections from crew_connections via crewshipd (live query).
	// Filter to only connections involving this crew (not the entire workspace graph).
	s.proxyToAPI(w, r, http.MethodGet, "/api/v1/internal/crew-connections?workspace_id="+
		url.QueryEscape(s.ipc.WorkspaceID)+"&crew_id="+url.QueryEscape(s.ipc.CrewID))
}

// handleConnectionSendMessage handles POST /connections/{crew-slug}/message
// Validates the target crew slug, injects IPC identity fields, and forwards to crewshipd.
func (s *Server) handleConnectionSendMessage(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	// Extract target crew slug from path: /connections/{slug}/message
	slug := extractConnectionSlug(r.URL.Path)
	if slug == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "missing crew slug in path"})
		return
	}

	// Parse the agent's request body (capped at 1MB).
	var agentReq struct {
		Content  json.RawMessage `json:"content"`
		Metadata json.RawMessage `json:"metadata,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&agentReq); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if len(agentReq.Content) == 0 {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "content is required"})
		return
	}

	// Resolve target crew ID from slug via crewshipd.
	targetCrewID, err := s.resolveCrewIDBySlug(r.Context(), slug)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "failed to resolve target crew"})
		return
	}
	if targetCrewID == "" {
		writeJSONResponse(w, http.StatusNotFound, map[string]string{"error": "target crew not found"})
		return
	}

	// Build the crewshipd request with injected identity (agent cannot override).
	contentStr := string(agentReq.Content)
	// If content is a JSON string (quoted), unquote it.
	if len(contentStr) > 1 && contentStr[0] == '"' {
		json.Unmarshal(agentReq.Content, &contentStr)
	}

	body := map[string]interface{}{
		"from_crew_id":  s.ipc.CrewID,
		"to_crew_id":    targetCrewID,
		"from_agent_id": s.ipc.AgentID,
		"workspace_id":  s.ipc.WorkspaceID,
		"content":       contentStr,
	}
	if agentReq.Metadata != nil {
		body["metadata"] = agentReq.Metadata
	}

	bodyJSON, _ := json.Marshal(body)

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.ipc.BaseURL+"/api/v1/internal/crew-messages", bytes.NewReader(bodyJSON))
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to create request"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(req)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "crewshipd request failed"})
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// handleConnectionListMessages handles GET /connections/{crew-slug}/messages
// Returns messages between this crew and the target crew.
func (s *Server) handleConnectionListMessages(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	slug := extractConnectionSlug(r.URL.Path)
	if slug == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "missing crew slug in path"})
		return
	}

	targetCrewID, err := s.resolveCrewIDBySlug(r.Context(), slug)
	if err != nil || targetCrewID == "" {
		writeJSONResponse(w, http.StatusNotFound, map[string]string{"error": "target crew not found"})
		return
	}

	apiURL := fmt.Sprintf("/api/v1/internal/crew-messages?crew_id=%s&peer_crew_id=%s&direction=all",
		url.QueryEscape(s.ipc.CrewID), url.QueryEscape(targetCrewID))
	if since := r.URL.Query().Get("since"); since != "" {
		apiURL += "&since=" + url.QueryEscape(since)
	}
	if limit := r.URL.Query().Get("limit"); limit != "" {
		apiURL += "&limit=" + url.QueryEscape(limit)
	}

	s.proxyToAPI(w, r, http.MethodGet, apiURL)
}

// handleConnectionReadFiles handles GET /connections/{crew-slug}/files
// Reads files from the target crew's shared directory.
func (s *Server) handleConnectionReadFiles(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	slug := extractConnectionSlug(r.URL.Path)
	if slug == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "missing crew slug in path"})
		return
	}

	targetCrewID, err := s.resolveCrewIDBySlug(r.Context(), slug)
	if err != nil || targetCrewID == "" {
		writeJSONResponse(w, http.StatusNotFound, map[string]string{"error": "target crew not found"})
		return
	}

	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		filePath = "."
	}

	fileURL := fmt.Sprintf("/api/v1/internal/crew-files/%s?path=%s&requester_crew_id=%s",
		url.PathEscape(targetCrewID), url.QueryEscape(filePath), url.QueryEscape(s.ipc.CrewID))

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.ipc.BaseURL+fileURL, nil)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to create request"})
		return
	}
	req.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(req)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "crewshipd request failed"})
		return
	}
	defer resp.Body.Close()

	// Forward response headers and body.
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// handleConnectionWriteFiles handles POST /connections/{crew-slug}/files
// Uploads a file to the target crew's shared incoming directory.
func (s *Server) handleConnectionWriteFiles(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	slug := extractConnectionSlug(r.URL.Path)
	if slug == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "missing crew slug in path"})
		return
	}

	targetCrewID, err := s.resolveCrewIDBySlug(r.Context(), slug)
	if err != nil || targetCrewID == "" {
		writeJSONResponse(w, http.StatusNotFound, map[string]string{"error": "target crew not found"})
		return
	}

	// Limit upload to 10MB.
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid multipart form or file too large"})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "file field is required"})
		return
	}
	defer file.Close()

	destPath := r.FormValue("path")
	if destPath == "" {
		destPath = header.Filename
	}

	// Build multipart request to crewshipd.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("requester_crew_id", s.ipc.CrewID)
	mw.WriteField("path", destPath)

	fw, err := mw.CreateFormFile("file", header.Filename)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to build request"})
		return
	}
	if _, err := io.Copy(fw, file); err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to read file"})
		return
	}
	mw.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	writeURL := fmt.Sprintf("%s/api/v1/internal/crew-files/%s", s.ipc.BaseURL, url.PathEscape(targetCrewID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, writeURL, &buf)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to create request"})
		return
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(req)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "crewshipd request failed"})
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// --- Helpers ---

// extractConnectionSlug extracts the crew slug from paths like /connections/{slug}/message
func extractConnectionSlug(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] != "connections" {
		return ""
	}
	return parts[1]
}

// resolveCrewIDBySlug calls the crewshipd internal API to find a crew ID by slug.
func (s *Server) resolveCrewIDBySlug(ctx context.Context, slug string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	reqURL := s.ipc.BaseURL + "/api/v1/internal/crews?workspace_id=" + url.QueryEscape(s.ipc.WorkspaceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var crews []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&crews); err != nil {
		return "", err
	}

	for _, c := range crews {
		if c.Slug == slug {
			return c.ID, nil
		}
	}
	return "", nil
}
