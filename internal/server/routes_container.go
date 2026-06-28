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
	// Stop sidecars on the same crew bridge. The provider's
	// SidecarProvider implementation is optional; providers without
	// it (Apple Containers today) silently skip. Sidecar stop is
	// best-effort — a failure here doesn't break the crew-stop
	// response, since the agent is already down and the only
	// remaining work is releasing some MB of Postgres memory.
	if sp, ok := s.container.(provider.SidecarProvider); ok && slug != "" {
		if err := sp.StopCrewServices(r.Context(), slug); err != nil {
			s.logger.Warn("sidecar stop failed", "crew_id", id, "error", err)
		}
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

// gitDiffScript computes "what this branch changed against its base" inside
// a crew container. Base = merge-base with the default branch (origin/HEAD,
// falling back to main/master), so it shows everything the work produced —
// committed or not — not just the dirty working tree. Markers delimit the
// three sections so the Go side parses one Exec instead of three round-trips.
const gitDiffScript = `
if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then echo "__NOTREPO__"; exit 0; fi
def=$(git symbolic-ref --quiet --short refs/remotes/origin/HEAD 2>/dev/null | sed 's@^origin/@@')
[ -z "$def" ] && git show-ref --verify --quiet refs/remotes/origin/main && def=main
[ -z "$def" ] && git show-ref --verify --quiet refs/remotes/origin/master && def=master
[ -z "$def" ] && def=main
base=$(git merge-base "origin/$def" HEAD 2>/dev/null || git merge-base "$def" HEAD 2>/dev/null || echo "")
echo "__STATUS__"
if [ -n "$base" ]; then git diff --name-status "$base" HEAD; else git diff --name-status; fi
echo "__NUMSTAT__"
if [ -n "$base" ]; then git diff --numstat "$base" HEAD; else git diff --numstat; fi
echo "__DIFF__"
if [ -n "$base" ]; then git diff "$base" HEAD; else git diff; fi
`

// gitDiffMaxBytes caps the patch we ship to the dashboard. A huge refactor
// shouldn't stream megabytes into a browser tab; past the cap we flag
// truncated:true and the UI says so.
const gitDiffMaxBytes = 256 * 1024

// handleContainerGitDiff runs `git diff <base>...HEAD` inside a crew's
// container and returns the changed-file summary + unified patch. Powers
// the dock's Changes tab.
func (s *Server) handleContainerGitDiff(w http.ResponseWriter, r *http.Request) {
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

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	result, err := s.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerName,
		Cmd:         []string{"sh", "-c", gitDiffScript},
		WorkingDir:  workDir,
		User:        "1001:1001",
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"is_repo": false, "error": "git not available"})
		return
	}
	defer result.Reader.Close()

	output, _ := io.ReadAll(io.LimitReader(result.Reader, gitDiffMaxBytes+1))
	writeJSON(w, http.StatusOK, parseGitDiff(string(output)))
}

// gitChangedFile is one row of the Changes tab's file summary.
type gitChangedFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// parseGitDiff turns the marker-delimited gitDiffScript output into the
// diffResponse shape the Changes tab consumes. Kept pure (no I/O) so it's
// unit-testable against canned container output.
func parseGitDiff(output string) map[string]interface{} {
	if strings.Contains(output, "__NOTREPO__") {
		return map[string]interface{}{"is_repo": false}
	}

	files := map[string]*gitChangedFile{}
	order := []string{}
	get := func(path string) *gitChangedFile {
		if f, ok := files[path]; ok {
			return f
		}
		f := &gitChangedFile{Path: path, Status: "modified"}
		files[path] = f
		order = append(order, path)
		return f
	}

	section := ""
	var diff strings.Builder
	for _, line := range strings.Split(output, "\n") {
		switch strings.TrimSpace(line) {
		case "__STATUS__":
			section = "status"
			continue
		case "__NUMSTAT__":
			section = "numstat"
			continue
		case "__DIFF__":
			section = "diff"
			continue
		}
		switch section {
		case "status":
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			path := fields[len(fields)-1]
			switch fields[0][0] {
			case 'A':
				get(path).Status = "added"
			case 'D':
				get(path).Status = "deleted"
			case 'R':
				get(path).Status = "renamed"
			default:
				get(path).Status = "modified"
			}
		case "numstat":
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			f := get(fields[2])
			fmt.Sscanf(fields[0], "%d", &f.Additions)
			fmt.Sscanf(fields[1], "%d", &f.Deletions)
		case "diff":
			diff.WriteString(line)
			diff.WriteString("\n")
		}
	}

	out := make([]gitChangedFile, 0, len(order))
	for _, p := range order {
		out = append(out, *files[p])
	}
	diffText := diff.String()
	truncated := len(diffText) > gitDiffMaxBytes
	if truncated {
		diffText = diffText[:gitDiffMaxBytes]
	}
	return map[string]interface{}{
		"is_repo":   true,
		"files":     out,
		"diff":      strings.TrimRight(diffText, "\n"),
		"truncated": truncated,
	}
}
