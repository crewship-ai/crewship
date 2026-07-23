package server

// Host-side file operations (list, download, save) for the per-crew
// output directory, plus the file-watcher initializer used by the WS
// realtime path. Extracted from routes.go for readability.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// resolveCrewFileKey maps a client-supplied crew file path to a storage
// key, reporting whether it is valid.
//
// Paths under "shared/" (or the bare "shared") route to the crew's
// /crew/shared bind source (storage key "crews/<id>/shared/..."), so a
// bundled file — a Crew manifest `files:` entry with dest "shared/..." —
// lands exactly where EnsureCrewRuntime mounts /crew. That is what makes
// bundled scripts reach the container even for an agentless crew whose
// container is provisioned lazily (the file already sits on the bind
// source when the mount comes up). Other paths use the legacy /output
// tree ("<id>/..."), where agent-generated output files live. Traversal
// and absolute paths are rejected.
// safeCrewID reports whether a crew id from the request path is a single
// clean path component safe to join into a storage key — no slash, no
// backslash, no "." / ".." / empty, and unchanged by filepath.Clean (so an
// encoded-slash value can't collapse a key out of its intended subtree).
func safeCrewID(crewID string) bool {
	if crewID == "" || crewID == "." || crewID == ".." || strings.ContainsAny(crewID, `/\`) {
		return false
	}
	return filepath.Clean(crewID) == crewID
}

func resolveCrewFileKey(crewID, path string) (string, bool) {
	// crewID comes from r.PathValue("id") and is joined into the storage
	// key below (see safeCrewID) — reject anything that isn't a single
	// clean path component so filepath.Join can't escape the crews/ prefix.
	if !safeCrewID(crewID) {
		return "", false
	}
	clean := filepath.Clean(path)
	if clean == "" || clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", false
	}
	if clean == "shared" || strings.HasPrefix(clean, "shared/") {
		return filepath.Join("crews", crewID, clean), true
	}
	if clean == crewID || strings.HasPrefix(clean, crewID+"/") {
		return clean, true
	}
	return "", false
}

func (s *Server) handleFileList(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("id")
	agentSlug := r.URL.Query().Get("agent_slug")

	// Same crew-id join hazard as resolveCrewFileKey: dir is built from
	// crewID below, so reject an unsafe id before it reaches filepath.Join.
	if !safeCrewID(crewID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid crew id"})
		return
	}

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
		// A "shared/..." subdir lists the crew's /crew/shared bind tree
		// (crews/<id>/shared/...) — where bundled `files:` live — rather
		// than the /output tree. Keeps list consistent with save/download.
		if agentSlug == "" && (cleaned == "shared" || strings.HasPrefix(cleaned, "shared/")) {
			dir = filepath.Join("crews", crewID, cleaned)
		} else {
			dir = filepath.Join(dir, cleaned)
		}
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
		writeEmptyOK(w, s.logger, "file list failed", err,
			map[string]interface{}{"crew_id": crewID, "files": []interface{}{}},
			"crew_id", crewID, "agent_slug", agentSlug)
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

	// Route + sanitize: "shared/..." → crew bind tree, "<id>/..." → output
	// tree; traversal/absolute rejected. (Path from List is crew_id/agent/file.)
	storageKey, ok := resolveCrewFileKey(crewID, filePath)
	if !ok {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	if s.storage == nil {
		http.Error(w, "storage not configured", http.StatusServiceUnavailable)
		return
	}

	reader, err := s.storage.Read(r.Context(), storageKey)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	defer reader.Close()

	filename := sanitizeDownloadFilename(filepath.Base(filePath))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, reader); err != nil {
		s.logger.Error("file download stream error", "path", sanitizeLogPath(filePath), "error", err)
	}
}

func (s *Server) handleFileSave(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("id")
	filePath := r.URL.Query().Get("path")

	if filePath == "" {
		http.Error(w, "path query parameter required", http.StatusBadRequest)
		return
	}

	storageKey, ok := resolveCrewFileKey(crewID, filePath)
	if !ok {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	if s.storage == nil {
		http.Error(w, "storage not configured", http.StatusServiceUnavailable)
		return
	}

	defer r.Body.Close()

	// Only shared-tree keys can hit the #922 ownership-handoff overwrite path
	// (the entrypoint chowns /crew to UID 1001 after provisioning), so only
	// they need to be buffered for a possible container replay. Agent /output
	// writes stream straight to storage exactly as before — no size cap, no
	// second copy in memory.
	cpath, isShared := crewSharedContainerPath(crewID, storageKey)
	if !isShared {
		if err := s.storage.Write(r.Context(), storageKey, r.Body); err != nil {
			s.logger.Error("file save failed", "path", sanitizeLogPath(filePath), "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save file"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "saved", "path": filePath})
		return
	}

	// Shared tree: buffer (capped) so an EACCES overwrite can be replayed
	// through the container as UID 1001 (the reader is single-use).
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCrewFileSaveBytes+1))
	if err != nil {
		s.logger.Error("file save read failed", "path", sanitizeLogPath(filePath), "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read request body"})
		return
	}
	if int64(len(body)) > maxCrewFileSaveBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge,
			map[string]string{"error": fmt.Sprintf("file exceeds %d byte limit", maxCrewFileSaveBytes)})
		return
	}

	// #931 no-op short-circuit: if the existing file is byte-identical, skip the
	// write entirely. Reading a shared file is allowed even when it's owned by
	// the crew's UID 1001 (the tree is world-readable), so this lets an
	// UNCHANGED apply/redelivery succeed even on a STOPPED crew — instead of
	// 409ing on the container route it would otherwise need. It also saves an
	// exec on every steady-state apply.
	//
	// The existing file is STREAMED against the already-buffered body (32 KiB
	// chunks, early-exit on the first mismatch) — no second full load, so peak
	// memory is one body buffer, not two. The body itself is unavoidably
	// buffered: the request stream is single-use and a diverging write still
	// needs the full bytes for the host/container replay below. (Skipping the
	// body buffer on a match would mean reconstructing the matched prefix from
	// the existing file on divergence — not worth the complexity for the
	// KB-scale scripts this path carries, capped at 32 MiB regardless.)
	if existing, rerr := s.storage.Read(r.Context(), storageKey); rerr == nil {
		equal, cmpErr := readerEqualsBytes(existing, body)
		existing.Close()
		if cmpErr == nil && equal {
			writeJSON(w, http.StatusOK, map[string]string{"status": "unchanged", "path": filePath})
			return
		}
	}

	werr := s.storage.Write(r.Context(), storageKey, bytes.NewReader(body))
	if werr != nil {
		// #922: after a crew is provisioned, the entrypoint chowns /crew (the
		// bind source of "crews/<id>/shared/...") to the agent UID 1001, so a
		// host-side overwrite by the server UID fails with EACCES. Re-route the
		// write through the container as 1001 — the tree owner — mirroring the
		// exec-as-1001 pattern the credential materializer uses.
		if s.container != nil && errors.Is(werr, fs.ErrPermission) {
			if cerr := s.writeCrewSharedFileViaContainer(r.Context(), crewID, cpath, body); cerr != nil {
				s.logger.Error("file save via container failed", "path", sanitizeLogPath(filePath), "error", cerr)
				status, msg := containerSaveErrorResponse(cerr)
				writeJSON(w, status, map[string]string{"error": msg})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "saved", "path": filePath})
			return
		}
		s.logger.Error("file save failed", "path", sanitizeLogPath(filePath), "error", werr)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save file"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "saved", "path": filePath})
}

// handleFileDelete removes a single file from a crew's shared/output tree.
// Path routing and traversal rejection are identical to save/download
// (resolveCrewFileKey). Delete is idempotent — localfs RemoveAll treats a
// missing key as success — so a repeat delete still returns 200.
func (s *Server) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("id")
	filePath := r.URL.Query().Get("path")

	if filePath == "" {
		http.Error(w, "path query parameter required", http.StatusBadRequest)
		return
	}

	storageKey, ok := resolveCrewFileKey(crewID, filePath)
	if !ok {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	if s.storage == nil {
		http.Error(w, "storage not configured", http.StatusServiceUnavailable)
		return
	}

	err := s.storage.Delete(r.Context(), storageKey)
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "path": filePath})
		return
	}

	// #922 ownership handoff: after a crew is provisioned the entrypoint
	// chowns /crew (the bind source of "crews/<id>/shared/...") to the agent
	// UID 1001, so a host-side unlink by the server UID fails with EACCES —
	// removing a file needs write on its parent directory, which 1001 now
	// owns. Re-route the removal through the container as 1001, mirroring the
	// exec-as-1001 fallback handleFileSave uses for the same reason.
	cpath, isShared := crewSharedContainerPath(crewID, storageKey)
	if isShared && s.container != nil && errors.Is(err, fs.ErrPermission) {
		if cerr := s.deleteCrewSharedFileViaContainer(r.Context(), crewID, cpath); cerr != nil {
			s.logger.Error("file delete via container failed", "path", sanitizeLogPath(filePath), "error", cerr)
			status, msg := containerSaveErrorResponse(cerr)
			writeJSON(w, status, map[string]string{"error": msg})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "path": filePath})
		return
	}

	s.logger.Error("file delete failed", "path", sanitizeLogPath(filePath), "error", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete file"})
}

// deleteCrewSharedFileViaContainer removes containerPath inside the crew
// container as UID 1001 — the owner of the provisioned /crew tree — so a
// host-side EACCES unlink (#922) still lands. The parent directory's realpath
// is checked INSIDE the container (defence-in-depth on top of the host-side
// resolveCrewFileKey fence) so a symlinked path component can't redirect the
// removal outside /crew/shared. Paths pass via env so a crafted destination
// can't break out of the shell command.
func (s *Server) deleteCrewSharedFileViaContainer(ctx context.Context, crewID, containerPath string) error {
	containerName, _, ok := s.resolveCrewContainer(ctx, crewID, false)
	if !ok {
		return errCrewNotFound
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	const script = `set -eu; d=$(dirname "$DEST"); ` +
		`rp=$(realpath "$d"); case "$rp" in /crew/shared|/crew/shared/*) ;; ` +
		`*) echo "refuse: destination escapes /crew/shared" >&2; exit 3 ;; esac; ` +
		`rm -f "$DEST"`
	result, err := s.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerName,
		Cmd:         []string{"sh", "-c", script},
		Env:         []string{"DEST=" + containerPath},
		User:        "1001:1001",
	})
	if err != nil {
		return fmt.Errorf("%w: %v", errCrewContainerUnavailable, err)
	}
	defer result.Reader.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(result.Reader, 64*1024))

	_, code, ierr := s.container.ExecInspect(ctx, result.ExecID)
	if ierr != nil {
		return fmt.Errorf("inspect container delete: %w", ierr)
	}
	if code != 0 {
		return fmt.Errorf("container delete exited %d", code)
	}
	return nil
}

// maxCrewFileSaveBytes bounds a single crew-file save. Crew scripts/config are
// small; the cap only exists so a buffered body can't exhaust server memory.
const maxCrewFileSaveBytes int64 = 32 << 20 // 32 MiB

var (
	errCrewNotFound             = errors.New("crew not found")
	errCrewContainerUnavailable = errors.New("crew container unavailable")
)

// crewSharedContainerPath maps a "crews/<id>/shared/..." storage key to the
// absolute path inside the crew container, where <OutputBasePath>/crews/<id>
// is bind-mounted at /crew (docker provider buildMounts). Reports false for
// keys outside that crew's shared subtree — the /output tree stays host-side.
func crewSharedContainerPath(crewID, storageKey string) (string, bool) {
	prefix := "crews/" + crewID + "/"
	if !strings.HasPrefix(storageKey, prefix) {
		return "", false
	}
	rel := strings.TrimPrefix(storageKey, prefix)
	if rel != "shared" && !strings.HasPrefix(rel, "shared/") {
		return "", false
	}
	return "/crew/" + rel, true
}

// writeCrewSharedFileViaContainer writes content to containerPath inside the
// crew container as UID 1001 — the owner of the provisioned /crew tree — so an
// overwrite the server UID can't do host-side (#922) still lands. The write is
// atomic (temp file in the destination dir, then mv -f), and paths pass via env
// so a crafted destination can't break out of the shell command.
func (s *Server) writeCrewSharedFileViaContainer(ctx context.Context, crewID, containerPath string, content []byte) error {
	containerName, _, ok := s.resolveCrewContainer(ctx, crewID, false)
	if !ok {
		return errCrewNotFound
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// Atomic write as UID 1001, fenced to /crew/shared. The realpath check
	// runs INSIDE the container (defence-in-depth on top of the host-side
	// resolveCrewFileKey fence): even if the agent planted a symlink inside
	// the shared tree that redirects the resolved destination dir outside
	// /crew/shared, the write is refused before any bytes land. Paths pass via
	// env so a crafted destination can't break out of the shell command.
	const script = `set -eu; d=$(dirname "$DEST"); mkdir -p "$d"; ` +
		`rp=$(realpath "$d"); case "$rp" in /crew/shared|/crew/shared/*) ;; ` +
		`*) echo "refuse: destination escapes /crew/shared" >&2; exit 3 ;; esac; ` +
		`tmp=$(mktemp "$d/.crewship-save.XXXXXX"); cat > "$tmp"; ` +
		`chmod 0664 "$tmp"; mv -f "$tmp" "$DEST"`
	result, err := s.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerName,
		Cmd:         []string{"sh", "-c", script},
		Env:         []string{"DEST=" + containerPath},
		User:        "1001:1001",
		Stdin:       bytes.NewReader(content),
	})
	if err != nil {
		// The crew container isn't running (or doesn't exist) — nothing to
		// exec into. Callers surface this as a 409 with an actionable message.
		return fmt.Errorf("%w: %v", errCrewContainerUnavailable, err)
	}
	defer result.Reader.Close()
	// Drain stdout/stderr to EOF so the exec has finished before we inspect
	// its exit code.
	_, _ = io.Copy(io.Discard, io.LimitReader(result.Reader, 64*1024))

	// Only the exit code decides success. We deliberately do NOT gate on the
	// ExecInspect "running" flag: after draining the attached stream to EOF the
	// process has finished, but the daemon can still momentarily report
	// running=true before it finalizes the exit code — treating that as a
	// failure produced spurious errors on a write that actually succeeded.
	_, code, ierr := s.container.ExecInspect(ctx, result.ExecID)
	if ierr != nil {
		return fmt.Errorf("inspect container write: %w", ierr)
	}
	if code != 0 {
		return fmt.Errorf("container write exited %d", code)
	}
	return nil
}

// containerSaveErrorResponse maps a container-write failure to an HTTP status
// and a message the CLI can relay.
func containerSaveErrorResponse(err error) (int, string) {
	switch {
	case errors.Is(err, errCrewNotFound):
		return http.StatusNotFound, "crew not found"
	case errors.Is(err, errCrewContainerUnavailable):
		return http.StatusConflict,
			"file is owned by the crew runtime; it can only be overwritten while the crew container is running — start the crew and retry"
	default:
		return http.StatusInternalServerError, "failed to save file"
	}
}

// readerEqualsBytes reports whether the stream r is byte-for-byte equal to
// want, reading r in fixed chunks so a large existing file is never fully
// buffered (only the incoming body — already in memory — is held). A read error
// is surfaced so the caller falls back to a normal write rather than a false
// "unchanged".
func readerEqualsBytes(r io.Reader, want []byte) (bool, error) {
	buf := make([]byte, 32*1024)
	off := 0
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if off+n > len(want) || !bytes.Equal(buf[:n], want[off:off+n]) {
				return false, nil
			}
			off += n
		}
		if err == io.EOF {
			return off == len(want), nil
		}
		if err != nil {
			return false, err
		}
	}
}

// sanitizeLogPath strips CR/LF and other control characters from a
// user-supplied path before it enters a log record, defusing log-forging
// (CodeQL "log entries created from user input"). slog escapes these in its
// JSON handler, but sanitizing at the source also protects a text handler and
// satisfies the static check.
func sanitizeLogPath(p string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, p)
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
