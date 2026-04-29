package server

// Host-side file operations (list, download, save) for the per-crew
// output directory, plus the file-watcher initializer used by the WS
// realtime path. Extracted from routes.go for readability.

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
)

func (s *Server) handleFileList(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("id")
	agentSlug := r.URL.Query().Get("agent_slug")

	if s.storage == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": crewID, "files": []interface{}{}})
		return
	}

	// If agent_slug is provided, list agent's output namespace + root-level crew files
	dir := crewID
	if agentSlug != "" {
		clean := filepath.Base(agentSlug)
		if clean == "." || clean == ".." || strings.ContainsAny(clean, `/\`) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid agent_slug"})
			return
		}
		dir = filepath.Join(crewID, clean)
	}

	// Optional subdir parameter for lazy-loading subdirectories
	if subdir := r.URL.Query().Get("subdir"); subdir != "" {
		cleaned := filepath.Clean(subdir)
		if strings.HasPrefix(cleaned, "..") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid subdir"})
			return
		}
		dir = filepath.Join(dir, cleaned)
	}

	recursive := r.URL.Query().Get("recursive") == "true"

	var files []provider.FileInfo
	var err error
	if recursive {
		files, err = s.storage.ListRecursive(r.Context(), dir)
	} else {
		files, err = s.storage.List(r.Context(), dir)
	}
	if err != nil {
		s.logger.Error("file list failed", "crew_id", crewID, "agent_slug", agentSlug, "error", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": crewID, "files": []interface{}{}})
		return
	}

	// When listing an agent's namespace, also include root-level crew files
	// (files the agent saved to /output/ instead of /output/<agent-slug>/)
	if agentSlug != "" {
		var rootFiles []provider.FileInfo
		if recursive {
			rootFiles, err = s.storage.ListRecursive(r.Context(), crewID)
		} else {
			rootFiles, err = s.storage.List(r.Context(), crewID)
		}
		if err == nil {
			for _, f := range rootFiles {
				if !f.IsDir {
					files = append(files, f)
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": crewID, "files": files})
}

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("id")
	filePath := r.URL.Query().Get("path")

	if filePath == "" {
		http.Error(w, "path query parameter required", http.StatusBadRequest)
		return
	}

	// Sanitize path to prevent directory traversal
	cleanPath := filepath.Clean(filePath)
	if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	// Validate the path belongs to this crew (path from List is crew_id/agent/file)
	if !strings.HasPrefix(cleanPath, crewID+"/") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	if s.storage == nil {
		http.Error(w, "storage not configured", http.StatusServiceUnavailable)
		return
	}

	reader, err := s.storage.Read(r.Context(), cleanPath)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	defer reader.Close()

	filename := sanitizeDownloadFilename(filepath.Base(filePath))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, reader); err != nil {
		s.logger.Error("file download stream error", "path", filePath, "error", err)
	}
}

func (s *Server) handleFileSave(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("id")
	filePath := r.URL.Query().Get("path")

	if filePath == "" {
		http.Error(w, "path query parameter required", http.StatusBadRequest)
		return
	}

	cleanPath := filepath.Clean(filePath)
	if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(cleanPath, crewID+"/") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	if s.storage == nil {
		http.Error(w, "storage not configured", http.StatusServiceUnavailable)
		return
	}

	defer r.Body.Close()
	if err := s.storage.Write(r.Context(), cleanPath, r.Body); err != nil {
		s.logger.Error("file save failed", "path", filePath, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save file"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "saved", "path": filePath})
}

func sanitizeDownloadFilename(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if r < 0x20 || r == '"' || r == '\\' || r == 0x7f {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "download"
	}
	return b.String()
}

func (s *Server) ensureFileWatcher(crewID string) {
	if s.fileWatcher == nil {
		return
	}
	if _, loaded := s.watchedCrews.LoadOrStore(crewID, true); loaded {
		return
	}
	if err := s.fileWatcher.Watch(s.runCtx, crewID); err != nil {
		s.logger.Warn("failed to start file watcher", "crew_id", crewID, "error", err)
		s.watchedCrews.Delete(crewID)
	}
}

// sanitizeMetadata filters agent event metadata to a safe allowlist before
// broadcasting to workspace WebSocket clients, preventing leakage of tool
// inputs, error details, or MCP configuration.
// sanitizeMetadataAllowed lists the metadata keys that are safe to surface
// on the "agent.log" WS broadcast. Hoisted to package level so the per-event
// hot path doesn't rebuild the map literal on every AgentEvent.
