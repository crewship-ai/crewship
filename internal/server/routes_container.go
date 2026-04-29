package server

// Container lifecycle + in-container introspection (file listing,
// git log). Extracted from routes.go for readability.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func (s *Server) handleContainerStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if s.container == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": id, "status": "not_configured"})
		return
	}

	status, err := s.container.ContainerStatus(r.Context(), id)
	if err != nil {
		s.logger.Error("container status failed", "crew_id", id, "error", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": id, "status": "unknown"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"crew_id": id,
		"status":  status.State,
		"uptime":  status.Uptime,
	})
}

func (s *Server) handleContainerStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.logger.Info("container start request", "crew_id", id)

	if s.container == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "container provider not configured"})
		return
	}

	containerID, err := s.container.EnsureCrewRuntime(r.Context(), provider.CrewConfig{
		ID:       id,
		MemoryMB: s.cfg.Container.DefaultMemoryMB,
		CPUs:     s.cfg.Container.DefaultCPUs,
	})
	if err != nil {
		s.logger.Error("container start failed", "crew_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "container start failed"})
		return
	}

	s.ensureFileWatcher(id)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"crew_id":      id,
		"container_id": containerID,
		"status":       "running",
	})
}

func (s *Server) handleContainerStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.logger.Info("container stop request", "crew_id", id)

	if s.container == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "container provider not configured"})
		return
	}

	// Resolve crew slug from DB so we can build the container name via provider.
	var slug string
	if s.db != nil {
		_ = s.db.QueryRowContext(r.Context(), "SELECT slug FROM crews WHERE id = ?", id).Scan(&slug)
	}
	containerName := id // fallback: use raw id (works for Docker container hashes)
	if slug != "" {
		containerName = s.container.CrewContainerName(slug)
	}

	if err := s.container.StopCrewRuntime(r.Context(), containerName); err != nil {
		s.logger.Error("container stop failed", "crew_id", id, "container", containerName, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "container stop failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": id, "status": "stopped"})
}

func (s *Server) handleContainerFileList(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("id")
	subdir := r.URL.Query().Get("subdir")

	if s.container == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "container provider not configured"})
		return
	}

	var slug string
	if s.db != nil {
		_ = s.db.QueryRowContext(r.Context(), "SELECT slug FROM crews WHERE id = ?", crewID).Scan(&slug)
	}
	if slug == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "crew not found"})
		return
	}

	containerName := s.container.CrewContainerName(slug)
	targetDir := "/home"
	if subdir != "" {
		cleaned := filepath.Clean(subdir)
		if strings.HasPrefix(cleaned, "..") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid subdir"})
			return
		}
		targetDir = filepath.Join("/home", cleaned)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	result, err := s.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerName,
		Cmd:         []string{"find", targetDir, "-maxdepth", "3", "-printf", "%y %s %T@ %p\\n"},
		User:        "1001:1001",
	})
	if err != nil {
		s.logger.Error("container file list exec failed", "crew_id", crewID, "error", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": crewID, "files": []interface{}{}})
		return
	}
	defer result.Reader.Close()

	output, _ := io.ReadAll(io.LimitReader(result.Reader, 512*1024)) // 512KB cap

	type fileEntry struct {
		Path  string `json:"path"`
		Name  string `json:"name"`
		Size  int64  `json:"size"`
		IsDir bool   `json:"is_dir"`
	}
	var files []fileEntry
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 4)
		if len(parts) < 4 {
			continue
		}
		ftype := parts[0]
		var size int64
		fmt.Sscanf(parts[1], "%d", &size)
		fpath := parts[3]
		name := filepath.Base(fpath)
		if name == "." || name == ".." {
			continue
		}
		files = append(files, fileEntry{
			Path:  fpath,
			Name:  name,
			Size:  size,
			IsDir: ftype == "d",
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": crewID, "files": files})
}

// handleContainerGitLog runs `git log` inside a crew's container and returns recent commits.

func (s *Server) handleContainerGitLog(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("id")
	agentSlug := r.URL.Query().Get("agent_slug")

	if s.container == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "container provider not configured"})
		return
	}

	var slug string
	if s.db != nil {
		_ = s.db.QueryRowContext(r.Context(), "SELECT slug FROM crews WHERE id = ?", crewID).Scan(&slug)
	}
	if slug == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "crew not found"})
		return
	}

	containerName := s.container.CrewContainerName(slug)
	workDir := "/home"
	if agentSlug != "" {
		clean := filepath.Base(agentSlug)
		if clean != "." && clean != ".." && !strings.ContainsAny(clean, `/\`) {
			workDir = filepath.Join("/output", clean)
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	result, err := s.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerName,
		Cmd:         []string{"git", "log", "--oneline", "-20", "--format=%H|%s|%an|%aI"},
		WorkingDir:  workDir,
		User:        "1001:1001",
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": crewID, "commits": []interface{}{}, "error": "git not available"})
		return
	}
	defer result.Reader.Close()

	output, _ := io.ReadAll(io.LimitReader(result.Reader, 64*1024))

	type gitCommit struct {
		Hash    string `json:"hash"`
		Message string `json:"message"`
		Author  string `json:"author"`
		Date    string `json:"date"`
	}
	var commits []gitCommit
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		commits = append(commits, gitCommit{
			Hash:    parts[0],
			Message: parts[1],
			Author:  parts[2],
			Date:    parts[3],
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"crew_id": crewID, "commits": commits})
}
