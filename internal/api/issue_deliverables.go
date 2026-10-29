package api

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// Publish only explicitly declared shared deliverables. OpenRoot confines all
// reads, including symlinks, to this issue's crew; arbitrary host paths are never
// read. Existing attachment validation, size limits and digest dedupe still apply.
//
// The declared paths are agent-written text (possibly under prompt injection),
// so anything wrong with a path — missing, escaping the root, not a regular
// file, too large, an untypeable name — is a rejected deliverable: logged and
// skipped. Only host faults that a later projection pass can cure (I/O errors,
// descriptor or memory exhaustion, storage or database failures) are returned,
// because a returned error makes the owned projection retry.
func (h *AssignmentHandler) publishIssueDeliverables(ctx context.Context, assignmentID, result string) error {
	if h.attachments == nil || h.attachments.storagePath == "" {
		return nil
	}
	handoff := orchestrator.ParseHandoff(result)
	if !handoff.Parsed || handoff.Artifacts == "" {
		return nil
	}
	var mission, workspace, crew, agent string
	err := h.db.QueryRowContext(ctx, `SELECT m.id,m.workspace_id,m.crew_id,a.assigned_to_id FROM assignments a JOIN missions m ON m.id=a.mission_id WHERE a.id=? AND a.workspace_id=m.workspace_id`, assignmentID).Scan(&mission, &workspace, &crew, &agent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !safeIDPattern.MatchString(crew) {
		return nil
	}
	reject := func(path, reason string, cause error) {
		h.logger.Warn("issue deliverable rejected", "assignment_id", assignmentID, "mission_id", mission, "path", path, "reason", reason, "error", cause)
	}
	root, err := os.OpenRoot(filepath.Join(h.attachments.storagePath, "crews", crew, "shared"))
	if err != nil {
		if retryableDeliverableError(ctx, err) {
			return err
		}
		reject("/crew/shared", "crew shared directory unavailable", err)
		return nil
	}
	defer root.Close()
	for i, raw := range strings.FieldsFunc(handoff.Artifacts, func(r rune) bool { return r == ',' || r == '\n' || r == ';' }) {
		if i >= 10 {
			break
		}
		path := strings.Trim(strings.TrimSpace(raw), "`\" ")
		if !strings.HasPrefix(path, "/crew/shared/") {
			continue
		}
		// Clean first: "./.memory/notes" and ".memory//notes" are local paths
		// that reach the same files while sliding past a literal prefix test.
		relative := filepath.Clean(strings.TrimPrefix(path, "/crew/shared/"))
		if relative == ".memory" || strings.HasPrefix(relative, ".memory/") || !filepath.IsLocal(relative) {
			continue
		}
		// Name and type are validated before any read so an untypeable name is
		// a rejection here rather than an attachBytes failure below.
		name, err := sanitizeAttachmentFilename(filepath.Base(relative))
		if err == nil {
			_, err = resolveAttachmentType(name)
		}
		if err != nil {
			reject(path, "attachment name or type not allowed", err)
			continue
		}
		data, err := readDeclaredDeliverable(root, relative)
		if err != nil {
			if retryableDeliverableError(ctx, err) {
				return err
			}
			// Includes a symlink escaping the crew root: os.Root refuses it and
			// it stays refused; it is simply not a projection failure.
			reject(path, "declared file cannot be published", err)
			continue
		}
		if data == nil {
			reject(path, "not a regular file or over the size limit", nil)
			continue
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
		_, created, err := h.attachments.attachBytes(req, workspace, mission, name, data, "", agent)
		if err != nil {
			return err
		}
		if created {
			h.attachments.publishAttachmentMutation(mission, workspace)
		}
	}
	return nil
}

// readDeclaredDeliverable returns nil data for a file that exists but may not
// be published (not regular, too large).
func readDeclaredDeliverable(root *os.Root, relative string) ([]byte, error) {
	file, err := root.Open(relative)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > agentAttachmentUploadBytes {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(file, agentAttachmentUploadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > agentAttachmentUploadBytes {
		return nil, nil
	}
	return data, nil
}

// retryableDeliverableError reports host faults a later pass can cure. Every
// other filesystem error on an agent-declared path (ENOENT, ENOTDIR, ELOOP,
// EACCES, EISDIR, a path escaping the root, ...) is a property of the path the
// agent named and would fail identically forever.
func retryableDeliverableError(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	for _, errno := range []syscall.Errno{syscall.EIO, syscall.EMFILE, syscall.ENFILE, syscall.ENOMEM, syscall.EAGAIN, syscall.EINTR, syscall.EBUSY} {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}
