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

// execProbeTimeout bounds waiting for a crew-file exec to report its status.
const execProbeTimeout = 30 * time.Second

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

	filename := sanitizeDownloadFilename(filepath.Base(filePath))
	setDownloadHeaders := func() {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		w.Header().Set("Content-Type", "application/octet-stream")
	}

	reader, err := s.storage.Read(r.Context(), storageKey)
	if err == nil {
		defer reader.Close()
		setDownloadHeaders()
		if _, cerr := io.Copy(w, reader); cerr != nil {
			s.logger.Error("file download stream error", "path", sanitizeLogPath(filePath), "error", cerr)
		}
		return
	}

	// A host-side read can fail for a reason that has nothing to do with the
	// file being absent.
	//
	// Every tree here is chowned to the agent UID 1001 when the crew is
	// provisioned (#922), and the agent writes into it at that UID, so
	// crewshipd — running as the host user — takes EACCES on anything the
	// container created without group-read. `List` still succeeds, because it
	// only needs the DIRECTORY, which is 0755. The result was a Files panel
	// that listed a full tree and answered "file not found" for every single
	// entry in it, which is a lie with the worst possible failure mode: it
	// sends you looking for a missing file that is right there.
	//
	// The save path has replayed through the container on exactly this error
	// since #922. The read path never did, which is the whole asymmetry — so
	// it does now, with the same fence, and the same UID.
	if !errors.Is(err, fs.ErrPermission) {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	cpath, fence, replayable := crewContainerPath(crewID, storageKey)
	if !replayable {
		s.logger.Warn("file download permission denied and not replayable",
			"path", sanitizeLogPath(filePath), "error", err)
		http.Error(w, "file not readable", http.StatusForbidden)
		return
	}

	content, rerr := s.readCrewFileViaContainer(r.Context(), crewID, cpath, fence)
	if rerr != nil {
		if errors.Is(rerr, errCrewFileNotFound) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		// Do NOT collapse this into 404. The bytes exist and we could not get
		// at them; saying "not found" is what made the original defect take a
		// day to see.
		s.logger.Error("container file read replay failed",
			"path", sanitizeLogPath(filePath), "error", rerr)
		http.Error(w, "file not readable — the crew container is unavailable", http.StatusConflict)
		return
	}

	setDownloadHeaders()
	if _, werr := w.Write(content); werr != nil {
		s.logger.Error("file download stream error", "path", sanitizeLogPath(filePath), "error", werr)
	}
}

/**
 * The read half of the #922 container replay.
 *
 * No `echo` anywhere in the script, and that is deliberate rather than terse:
 * the docker provider demuxes stdout and stderr into ONE pipe
 * (stdcopy.StdCopy(pw, pw, …)), so any diagnostic the script printed would be
 * spliced into the middle of the file the caller asked for. Every failure is
 * therefore a bare exit code, and the bytes are only trusted once the exit
 * code is 0.
 *
 * That is also why this buffers rather than streaming: the exit code is only
 * knowable after the stream ends, so streaming straight to the client would
 * mean committing to bytes we cannot yet vouch for.
 */
const crewFileReadScript = `set -eu; f=$(realpath "$FENCE"); rp=$(realpath "$SRC") || exit 4; ` +
	`case "$rp" in "$f"|"$f"/*) ;; *) exit 3 ;; esac; ` +
	`[ -f "$rp" ] || exit 4; exec cat "$rp"`

// maxCrewFileReadBytes caps the replay. An /output write is uncapped by design
// — an agent artefact can be arbitrarily large — but this path buffers, so it
// needs a ceiling that is generous for anything a person opens in the Files
// panel and still bounded.
const maxCrewFileReadBytes = 32 << 20 // 32 MiB

// crewFileReadSem bounds concurrent read replays, the way gitDiffSem bounds
// `git diff` execs and for a sharper reason: this path BUFFERS, so the ceiling
// above is per-request, not per-server. Unbounded, N simultaneous downloads of
// large artefacts reserve N × 32 MiB of heap while also each holding a docker
// exec slot — a workspace member with read access could starve agent work by
// opening the Files panel in a loop. Four in flight caps that at 128 MiB.
var crewFileReadSem = make(chan struct{}, 4)

var errCrewFileNotFound = errors.New("crew file not found")

func (s *Server) readCrewFileViaContainer(ctx context.Context, crewID, containerPath, fence string) ([]byte, error) {
	// resolveCrewContainer dereferences s.container (its doc comment says
	// callers must have checked), and a nil provider is a supported state —
	// handleContainerStatus reports it as "not_configured". Without this the
	// replay panics instead of reporting an unavailable container.
	if s.container == nil {
		return nil, errCrewContainerUnavailable
	}

	containerName, _, ok := s.resolveCrewContainer(ctx, crewID, false)
	if !ok {
		return nil, errCrewNotFound
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Wait for a slot, or give up if the client goes away — the same shape
	// handleContainerGitDiff uses.
	select {
	case crewFileReadSem <- struct{}{}:
		defer func() { <-crewFileReadSem }()
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %v", errCrewContainerUnavailable, ctx.Err())
	}

	result, err := s.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerName,
		Cmd:         []string{"sh", "-c", crewFileReadScript},
		Env:         []string{"SRC=" + containerPath, "FENCE=" + fence},
		User:        "1001:1001",
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errCrewContainerUnavailable, err)
	}
	defer result.Reader.Close()

	content, rerr := io.ReadAll(io.LimitReader(result.Reader, maxCrewFileReadBytes+1))
	if rerr != nil {
		return nil, fmt.Errorf("read container file stream: %w", rerr)
	}
	if len(content) > maxCrewFileReadBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", maxCrewFileReadBytes)
	}

	code, ierr := provider.WaitExecExit(ctx, s.container, result.ExecID, execProbeTimeout)
	if ierr != nil {
		return nil, fmt.Errorf("inspect container read: %w", ierr)
	}
	switch code {
	case 0:
		return content, nil
	case 4:
		return nil, errCrewFileNotFound
	default:
		return nil, fmt.Errorf("container read exited %d", code)
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

	// Both crew trees are chowned to the agent UID 1001 when the crew is
	// provisioned (#922) — the shared tree by the container entrypoint, the
	// /output tree by prepareCrewDirs — so a host-side write by the server UID
	// can be refused with EACCES in either. Both therefore need the bytes a
	// second time, to replay the write through the container as 1001.
	//
	// They get them differently, because their size contracts differ. The
	// shared tree is buffered up front (KB-scale manifest files, hard 413 past
	// the cap) so the no-op comparison below can read it twice. An /output
	// write is uncapped by design — an agent file can be arbitrarily large —
	// so it streams straight to storage while a bounded capture rides along;
	// past the cap the capture is dropped and only the replay is lost, never
	// the write.
	cpath, fence, replayable := crewContainerPath(crewID, storageKey)
	if fence != containerCrewSharedRoot {
		capture := &captureReader{r: r.Body, limit: maxCrewFileSaveBytes}
		werr := s.storage.Write(r.Context(), storageKey, capture)
		if werr == nil {
			writeJSON(w, http.StatusOK, map[string]string{"status": "saved", "path": filePath})
			return
		}
		if !replayable || !errors.Is(werr, fs.ErrPermission) {
			s.logger.Error("file save failed", "path", sanitizeLogPath(filePath), "error", werr)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save file"})
			return
		}
		s.saveViaContainer(r.Context(), w, crewID, filePath, cpath, fence, capture.replay)
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
		if replayable && errors.Is(werr, fs.ErrPermission) {
			s.saveViaContainer(r.Context(), w, crewID, filePath, cpath, fence,
				func() ([]byte, bool) { return body, true })
			return
		}
		s.logger.Error("file save failed", "path", sanitizeLogPath(filePath), "error", werr)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save file"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "saved", "path": filePath})
}

// saveViaContainer replays a write the host refused with EACCES (#922) through
// the crew container as UID 1001 — the owner of the provisioned tree — and
// turns each way that can fail into an answer the caller can act on rather
// than a bare "failed to save file".
//
// content is a thunk because the two trees produce the bytes differently: a
// shared-tree save already holds the whole body, an /output save may still
// need to drain the unread remainder of the request. It reports false when the
// bytes cannot be produced at all (larger than the replay buffer, or the
// remainder could not be read) — the one failure here that starting the crew
// would not fix, so it gets its own status and its own sentence.
func (s *Server) saveViaContainer(ctx context.Context, w http.ResponseWriter,
	crewID, filePath, containerPath, fence string, content func() ([]byte, bool)) {
	if s.container == nil {
		s.logger.Error("file save failed: destination is owned by the crew runtime and no container runtime is configured",
			"path", sanitizeLogPath(filePath))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": errNoContainerRuntimeMsg})
		return
	}
	body, ok := content()
	if !ok {
		s.logger.Error("file save failed: too large to replay through the crew container",
			"path", sanitizeLogPath(filePath), "limit_bytes", maxCrewFileSaveBytes)
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
			"error": fmt.Sprintf("this file is owned by the crew runtime, so it has to be written through the "+
				"crew container, which accepts at most %d bytes — the upload is larger", maxCrewFileSaveBytes)})
		return
	}
	if cerr := s.writeCrewFileViaContainer(ctx, crewID, containerPath, fence, body); cerr != nil {
		s.logger.Error("file save via container failed", "path", sanitizeLogPath(filePath), "error", cerr)
		status, msg := containerSaveErrorResponse(cerr, fence)
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved", "path": filePath})
}

// errNoContainerRuntimeMsg is what a caller sees when the destination belongs
// to the crew runtime and this deployment has no container provider at all —
// distinct from "the crew is stopped", because no retry will help.
const errNoContainerRuntimeMsg = "the destination is owned by the crew runtime and this server has no " +
	"container runtime configured, so it cannot be written — configure a container provider and retry"

// captureReader streams r through to its consumer while keeping a copy of the
// bytes it passed on, up to limit, so a failed write can be replayed.
//
// A request body is single-use, so the container replay needs the bytes twice.
// The shared tree solves that by buffering up front and rejecting anything past
// the cap with a 413; /output cannot, because an agent file write is uncapped
// by design and streams straight to storage. Capturing keeps that property: the
// write still streams, at most `limit` bytes are held, and a body that outgrows
// the limit loses only its replay — never its write, and never silently (the
// caller answers 413 explaining that the container route is the constrained
// one).
type captureReader struct {
	r     io.Reader
	buf   bytes.Buffer
	limit int64
	over  bool
}

func (c *captureReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 && !c.over {
		if int64(c.buf.Len()+n) > c.limit {
			// Past the limit the capture can never be a whole body, and a
			// partial one must never be replayed — drop it and stop holding
			// memory for a replay that cannot happen.
			c.over = true
			c.buf = bytes.Buffer{}
		} else {
			c.buf.Write(p[:n])
		}
	}
	return n, err
}

// replay returns the complete body, draining whatever the consumer never read
// — a write that fails at mkdirat/openat never touches the reader at all, so
// on the EACCES path the capture is typically still empty. It reports false if
// the body is larger than the capture limit or the remainder cannot be read.
func (c *captureReader) replay() ([]byte, bool) {
	buf := make([]byte, 32*1024)
	for !c.over {
		_, err := c.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false
		}
	}
	if c.over {
		return nil, false
	}
	return c.buf.Bytes(), true
}

// handleFileDelete removes one key from a crew's shared/output tree.
// Path routing and traversal rejection are identical to save/download
// (resolveCrewFileKey). Delete is idempotent — localfs RemoveAll treats a
// missing key as success — so a repeat delete still returns 200.
//
// A key naming a DIRECTORY removes it and what is under it, host-side and
// through the container replay alike (crewFileDeleteScript). Chat attachments
// depend on that: one attachment is one directory.
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

	// #922 ownership handoff: after a crew is provisioned both crew trees are
	// owned by the agent UID 1001, so a host-side unlink by the server UID
	// fails with EACCES — removing a file needs write on its parent directory,
	// which 1001 now owns. Re-route the removal through the container as 1001,
	// mirroring the exec-as-1001 fallback handleFileSave uses for the same
	// reason. Applies to the /output tree too: a file the host cannot write is
	// a file the host cannot unlink either.
	cpath, fence, replayable := crewContainerPath(crewID, storageKey)
	if replayable && s.container != nil && errors.Is(err, fs.ErrPermission) {
		if cerr := s.deleteCrewFileViaContainer(r.Context(), crewID, cpath, fence); cerr != nil {
			s.logger.Error("file delete via container failed", "path", sanitizeLogPath(filePath), "error", cerr)
			status, msg := containerDeleteErrorResponse(cerr, fence)
			writeJSON(w, status, map[string]string{"error": msg})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "path": filePath})
		return
	}

	s.logger.Error("file delete failed", "path", sanitizeLogPath(filePath), "error", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete file"})
}

// deleteCrewFileViaContainer removes containerPath inside the crew container
// as UID 1001 — the owner of the provisioned crew trees — so a host-side
// EACCES unlink (#922) still lands. The parent directory's realpath is checked
// INSIDE the container (defence-in-depth on top of the host-side
// resolveCrewFileKey fence) so a symlinked path component can't redirect the
// removal outside fence. Paths pass via env so a crafted destination can't
// break out of the shell command.
func (s *Server) deleteCrewFileViaContainer(ctx context.Context, crewID, containerPath, fence string) error {
	containerName, _, ok := s.resolveCrewContainer(ctx, crewID, false)
	if !ok {
		return errCrewNotFound
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	result, err := s.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerName,
		Cmd:         []string{"sh", "-c", crewFileDeleteScript},
		Env:         []string{"DEST=" + containerPath, "FENCE=" + fence},
		User:        "1001:1001",
	})
	if err != nil {
		return fmt.Errorf("%w: %v", errCrewContainerUnavailable, err)
	}
	defer result.Reader.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(result.Reader, 64*1024))

	code, ierr := provider.WaitExecExit(ctx, s.container, result.ExecID, execProbeTimeout)
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

// The two crew trees, as the container sees them. buildMounts binds
// <OutputBasePath>/crews/<id> at /crew and <OutputBasePath>/<id> at /output;
// each is also the fence a container-side write must resolve inside.
const (
	containerCrewSharedRoot = "/crew/shared"
	containerOutputRoot     = "/output"
)

// crewContainerPath maps a crew-file storage key to its absolute path inside
// the crew container, together with the tree root that write must stay under:
//
//	crews/<id>/shared/...  →  /crew/shared/...   fenced to /crew/shared
//	<id>/<agent>/...       →  /output/<agent>/…  fenced to /output
//
// Both trees end up owned by the agent UID 1001 once the crew is provisioned,
// so both need the container replay when the host-side write is refused
// (#922). This used to answer for the shared tree only — which is why chat
// attachments, which live in the /output tree, could never reach the fallback:
// handleFileSave returned on the "not shared" branch before it.
//
// Reports false for anything outside those two trees, for a crew id that is not
// a single clean path component, and for the tree roots themselves (a directory
// is not a file this can write).
func crewContainerPath(crewID, storageKey string) (containerPath, fence string, ok bool) {
	if !safeCrewID(crewID) {
		return "", "", false
	}
	if prefix := "crews/" + crewID + "/"; strings.HasPrefix(storageKey, prefix) {
		rel := strings.TrimPrefix(storageKey, prefix)
		if rel != "shared" && !strings.HasPrefix(rel, "shared/") {
			return "", "", false
		}
		return "/crew/" + rel, containerCrewSharedRoot, true
	}
	if prefix := crewID + "/"; strings.HasPrefix(storageKey, prefix) {
		if rel := strings.TrimPrefix(storageKey, prefix); rel != "" {
			return containerOutputRoot + "/" + rel, containerOutputRoot, true
		}
	}
	return "", "", false
}

// writeCrewFileViaContainer writes content to containerPath inside the crew
// container as UID 1001 — the owner of the provisioned crew trees — so a write
// the server UID can't do host-side (#922) still lands. fence is the tree root
// the resolved destination must sit under (/crew/shared or /output). The write
// is atomic (temp file in the destination dir, then mv -f), and paths pass via
// env so a crafted destination can't break out of the shell command.
func (s *Server) writeCrewFileViaContainer(ctx context.Context, crewID, containerPath, fence string, content []byte) error {
	containerName, _, ok := s.resolveCrewContainer(ctx, crewID, false)
	if !ok {
		return errCrewNotFound
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// Atomic write as UID 1001, fenced to $FENCE. The realpath check runs
	// INSIDE the container (defence-in-depth on top of the host-side
	// resolveCrewFileKey fence): even if the agent planted a symlink inside
	// the tree that redirects the resolved destination dir outside the fence,
	// the write is refused before any bytes land. Both paths pass via env —
	// the fence included, so a crafted destination can't break out of the
	// shell command and the pattern stays a literal (quoted expansions are
	// not globbed by `case`).
	//
	// mkdir -p is load-bearing on the /output tree, not just tidiness: a chat
	// attachment is the FIRST thing to touch <agent>/attachments/<chatId>/,
	// and the host cannot create it — that missing mkdir is the whole bug.
	result, err := s.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerName,
		Cmd:         []string{"sh", "-c", crewFileWriteScript},
		Env:         []string{"DEST=" + containerPath, "FENCE=" + fence},
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
	code, ierr := provider.WaitExecExit(ctx, s.container, result.ExecID, execProbeTimeout)
	if ierr != nil {
		return fmt.Errorf("inspect container write: %w", ierr)
	}
	if code != 0 {
		return fmt.Errorf("container write exited %d", code)
	}
	return nil
}

// crewFileWriteScript is the in-container half of the #922 replay: create the
// destination dir, refuse anything whose realpath leaves $FENCE, then write
// atomically. Shared by both trees; $FENCE is what tells them apart.
const crewFileWriteScript = `set -eu; d=$(dirname "$DEST"); mkdir -p "$d"; ` +
	`rp=$(realpath "$d"); case "$rp" in "$FENCE"|"$FENCE"/*) ;; ` +
	`*) echo "refuse: destination escapes $FENCE" >&2; exit 3 ;; esac; ` +
	`tmp=$(mktemp "$d/.crewship-save.XXXXXX"); cat > "$tmp"; ` +
	`chmod 0664 "$tmp"; mv -f "$tmp" "$DEST"`

// crewFileDeleteScript is the same fence in front of a removal.
//
// It removes RECURSIVELY, because the host half it replays does: storage.Delete
// is localfs's RemoveAll, so a key naming a directory takes the directory. The
// script used to run `rm -f`, which exits non-zero on a directory and — under
// `set -eu` — failed the whole exec. That made the replay strictly less capable
// than the operation it stands in for, so one request succeeded or 5xx'd purely
// on whether the crew was provisioned. A chat attachment is exactly a directory
// key (attachments/<chatId>/<attachmentId>/<filename>, and the delete removes
// the <attachmentId> directory so no empty directory is left in a tree the agent
// reads), which made every attachment on a provisioned crew undeletable.
//
// The one thing the host half will do and this deliberately will not is remove a
// tree ROOT: /output and /crew/shared are the mount points themselves, nothing
// addresses them as a file to delete, and `rm -rf` aimed at one would take a
// crew's entire shared tree or an agent's whole output namespace. `rm -rf` on a
// missing path is still a success — deletion is idempotent on both halves.
// The fence is resolved before it is compared, because the destination's
// parent is: realpath on one side and the caller's spelling on the other are
// only equal when no component of the fence is a symlink, and the moment one
// is, every removal inside the tree is refused for escaping it. macOS CI found
// this — /var is a symlink to /private/var — but a bind mount reached through
// a link would do the same to a real install.
//
// Resolving it costs nothing in containment: FENCE is ours, not the caller's,
// and DEST is still judged by where its parent actually lands. DEST itself is
// never resolved — it is allowed not to exist, and `set -eu` would turn that
// into an error rather than the success "already gone" has to be.
const crewFileDeleteScript = `set -eu; f=$(realpath "$FENCE"); d=$(dirname "$DEST"); ` +
	`rp=$(realpath "$d"); case "$rp" in "$f"|"$f"/*) ;; ` +
	`*) echo "refuse: destination escapes $FENCE" >&2; exit 3 ;; esac; ` +
	`case "$DEST" in "$FENCE"|"$FENCE"/|"$f"|"$f"/) echo "refuse: will not remove the tree root $FENCE" >&2; exit 4 ;; esac; ` +
	`rm -rf "$DEST"`

// containerSaveErrorResponse maps a container-write failure to an HTTP status
// and a message the CLI (and the chat composer, which forwards it) can relay.
// The 409 names the tree, because "start the crew and retry" is only actionable
// if the reader knows which file the crew runtime owns.
func containerSaveErrorResponse(err error, fence string) (int, string) {
	switch {
	case errors.Is(err, errCrewNotFound):
		return http.StatusNotFound, "crew not found"
	case errors.Is(err, errCrewContainerUnavailable):
		if fence == containerOutputRoot {
			return http.StatusConflict,
				"the agent's output directory is owned by the crew runtime; files can only be written there while the crew container is running — start the crew and retry"
		}
		// The shared tree is reached from `crewship crew files save`, so
		// its reader has a shell. Name the command: "start the crew"
		// reads as `crewship crew provision`, which builds an image and
		// — on a cache hit — reports success while the container stays
		// stopped, so the obvious next move reproduced this same 409.
		return http.StatusConflict,
			"file is owned by the crew runtime; it can only be overwritten while the crew container is running. " +
				"Start it with `crewship crew start <crew>` and retry — note that `crewship crew provision` " +
				"only builds the image and does not start anything."
	default:
		return http.StatusInternalServerError, "failed to save file"
	}
}

// containerDeleteErrorResponse is the same mapping for the removal half. The
// crew-ownership diagnoses are identical — the tree that cannot be written
// cannot be unlinked either, and "start the crew and retry" is the remedy for
// both — but the fallback message names the operation that actually failed. The
// API layer prefixes this text with "failed to delete attachment: " and shows it
// to a user (attachmentDeleteErrorMessage), so a delete reporting "failed to
// save file" is a sentence that describes something nobody asked for.
func containerDeleteErrorResponse(err error, fence string) (int, string) {
	status, msg := containerSaveErrorResponse(err, fence)
	if status == http.StatusInternalServerError {
		return status, "failed to delete file"
	}
	return status, msg
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
