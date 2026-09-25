package sidecar

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
)

// A background collector has no agent identity. Its one-panel webhook token
// remains the authority, exactly as on the public webhook endpoint. Relay it
// through the existing local IPC transport so a self-hosted installation does
// not need public DNS or private-network egress just to publish its own data.
func (s *Server) handlePageWebhook(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/page-webhooks/")
	suffix := strings.TrimPrefix(token, "pgw_")
	if suffix == token || len(suffix) != 64 {
		writeJSONResponse(w, 400, map[string]string{"error": "invalid Page webhook token"})
		return
	}
	if _, err := hex.DecodeString(suffix); err != nil {
		writeJSONResponse(w, 400, map[string]string{"error": "invalid Page webhook token"})
		return
	}
	if s.ipc == nil {
		writeJSONResponse(w, 503, map[string]string{"error": "Crewship connection unavailable"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONResponse(w, 413, map[string]string{"error": "Page payload too large"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), pagePushTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.ipc.BaseURL+"/api/v1/page-webhooks/"+token, bytes.NewReader(body))
	if err != nil {
		writeJSONResponse(w, 502, map[string]string{"error": "Page webhook connection unavailable"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	// No agent/internal authorization: the public capability validates its own
	// scope, revocation and rate limits. Never log a transport error containing URL.
	resp, err := ipcClient.Do(req)
	if err != nil {
		writeJSONResponse(w, 502, map[string]string{"error": "Page webhook connection unavailable"})
		return
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		writeJSONResponse(w, 502, map[string]string{"error": "Invalid Page webhook response"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if retry := resp.Header.Get("Retry-After"); retry != "" {
		w.Header().Set("Retry-After", retry)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(raw)
}
