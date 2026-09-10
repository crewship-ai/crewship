package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type declaredRunArtifact struct {
	Kind    string          `json:"kind"`
	Label   string          `json:"label"`
	Path    string          `json:"path"`
	Content json.RawMessage `json:"content"`
	URL     string          `json:"url"`
}

// NewRoutineArtifactPublisher shares immutable blobs, locks and file checks with
// Issue attachments. Only explicit artifacts in JSON or the handoff are captured.
func NewRoutineArtifactPublisher(db *sql.DB, storageRoot string) pipeline.ArtifactPublisher {
	return pipeline.ArtifactPublishFunc(func(ctx context.Context, workspaceID, crewID, runID, executionID, output, state string) error {
		var envelope struct {
			Artifacts []declaredRunArtifact `json:"artifacts"`
		}
		_ = json.Unmarshal([]byte(output), &envelope)
		if handoff := orchestrator.ParseHandoff(output); handoff.Parsed && handoff.Artifacts != "" {
			for _, path := range strings.FieldsFunc(handoff.Artifacts, func(r rune) bool { return r == ',' || r == '\n' || r == ';' }) {
				path = strings.Trim(strings.TrimSpace(path), "`\" ")
				if strings.HasPrefix(path, "/crew/shared/") {
					envelope.Artifacts = append(envelope.Artifacts, declaredRunArtifact{Kind: "file", Path: path})
				}
			}
		}
		if len(envelope.Artifacts) == 0 {
			return nil
		}
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		// Scope is derived again from the durable run; an adapter cannot choose
		// another workspace's blob namespace by supplying a different argument.
		var actualWorkspace string
		if err := db.QueryRowContext(persistCtx, `SELECT workspace_id FROM pipeline_runs WHERE id=?`, runID).Scan(&actualWorkspace); err != nil {
			return err
		}
		if actualWorkspace != workspaceID {
			return errors.New("artifact workspace mismatch")
		}
		for i, artifact := range envelope.Artifacts {
			if i >= 20 {
				break
			}
			kind := artifact.Kind
			if kind == "" && artifact.Path != "" {
				kind = "file"
			}
			switch kind {
			case "file", "text", "json", "external_record", "action_receipt":
			default:
				continue
			}
			label := artifact.Label
			if label == "" {
				label = filepath.Base(artifact.Path)
			}
			if label == "" || label == "." {
				label = kind
			}
			artifactState, reason, sha, contentType := state, "", "", ""
			source := artifact.URL
			if kind == "file" {
				source = artifact.Path
			}
			content := string(artifact.Content)
			unlock := func() {}
			if kind == "file" {
				data, err := readRoutineArtifact(storageRoot, crewID, artifact.Path)
				if err == nil {
					contentType, err = resolveAttachmentType(artifact.Path)
				}
				if err != nil {
					artifactState = "unavailable"
					reason = "Declared file could not be snapshotted: missing, unsupported or outside shared storage."
				} else {
					sha = attachmentDigest(data)
					unlock = lockAttachmentBlob(workspaceID, sha)
					if _, _, err = storeAttachmentBlob(storageRoot, workspaceID, data); err != nil {
						unlock()
						return err
					}
				}
			}
			// The executor publishes an agent step's declaration twice: as a
			// draft from the nested invocation that produced it, then as
			// available from the step that accepted it. That is one
			// deliverable, so the enclosing step promotes the row its own
			// subtree already owns instead of adding a second one. Two
			// UNRELATED steps naming the same file still keep separate rows.
			promoted, err := db.ExecContext(persistCtx, `UPDATE pipeline_run_artifacts SET state=?,content_type=?,sha256=?,content=?,error=?
              WHERE run_id=? AND kind=? AND label=? AND source=? AND state='draft' AND step_execution_id IN (
                SELECT child.id FROM pipeline_step_executions child JOIN pipeline_step_executions parent ON parent.id=?
                WHERE child.run_id=parent.run_id
                  AND child.parent_execution_id=parent.id
                  AND child.kind='agent_attempt' AND child.status='completed'
                ORDER BY child.attempt DESC,child.started_at DESC LIMIT 1)`,
				artifactState, contentType, sha, content, reason, runID, kind, label, source, executionID)
			if err != nil {
				unlock()
				return err
			}
			if replaced, _ := promoted.RowsAffected(); replaced > 0 {
				unlock()
				continue
			}
			_, err = db.ExecContext(persistCtx, `INSERT INTO pipeline_run_artifacts(id,run_id,step_execution_id,kind,label,state,content_type,sha256,content,source,error,created_at)
              VALUES (?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(step_execution_id,kind,label,source) DO NOTHING`, "art_"+generateCUID(), runID, executionID, kind, label, artifactState, contentType, sha, content, source, reason, tsformat.Format(time.Now()))
			unlock()
			if err != nil {
				return err
			}
		}
		return nil
	})
}
func readRoutineArtifact(storageRoot, crewID, path string) ([]byte, error) {
	if storageRoot == "" || !safeIDPattern.MatchString(crewID) || !strings.HasPrefix(path, "/crew/shared/") {
		return nil, errors.New("invalid shared file")
	}
	relative := strings.TrimPrefix(path, "/crew/shared/")
	if !filepath.IsLocal(relative) || strings.HasPrefix(relative, ".memory/") {
		return nil, errors.New("invalid shared file")
	}
	root, err := os.OpenRoot(filepath.Join(storageRoot, "crews", crewID, "shared"))
	if err != nil {
		return nil, err
	}
	defer root.Close()
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
		return nil, errors.New("unsupported file")
	}
	data, err := io.ReadAll(io.LimitReader(file, agentAttachmentUploadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > agentAttachmentUploadBytes {
		return nil, errors.New("file too large")
	}
	return data, nil
}
func (h *PipelineHandler) RunArtifacts(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "read") {
		replyError(w, 403, "Forbidden")
		return
	}
	ws, runID := WorkspaceIDFromContext(r.Context()), r.PathValue("runId")
	var exists int
	err := h.db.QueryRowContext(r.Context(), `SELECT 1 FROM pipeline_runs WHERE id=? AND workspace_id=?`, runID, ws).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "run not found")
		return
	}
	if err != nil {
		replyError(w, 500, "load run")
		return
	}
	if id := r.URL.Query().Get("download"); id != "" {
		var sha, label, contentType string
		err := h.db.QueryRowContext(r.Context(), `SELECT sha256,label,content_type FROM pipeline_run_artifacts WHERE id=? AND run_id=? AND kind='file'`, id, runID).Scan(&sha, &label, &contentType)
		if err != nil || sha == "" {
			replyError(w, 404, "artifact unavailable")
			return
		}
		data, err := readAttachmentBlob(h.storagePath, ws, sha)
		if err != nil {
			replyError(w, 404, "artifact content unavailable")
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": label}))
		w.Header().Set("Cache-Control", "private, no-store")
		w.Write(data)
		return
	}
	if id := r.URL.Query().Get("artifact_id"); id != "" {
		var content string
		err := h.db.QueryRowContext(r.Context(), `SELECT content FROM pipeline_run_artifacts WHERE id=? AND run_id=?`, id, runID).Scan(&content)
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, 404, "artifact not found")
			return
		}
		if err != nil {
			replyError(w, 500, "load artifact content")
			return
		}
		writeJSON(w, 200, pipelineRunArtifactContent{ID: id, Content: content})
		return
	}
	var after int64
	if raw := r.URL.Query().Get("after"); raw != "" {
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 {
			replyError(w, 400, "invalid cursor")
			return
		}
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT a.rowid,a.id,a.step_execution_id,a.kind,a.label,a.state,a.content_type,a.sha256,length(a.content),a.source,a.error,a.created_at,e.execution_path,e.attempt FROM pipeline_run_artifacts a JOIN pipeline_step_executions e ON e.id=a.step_execution_id WHERE a.run_id=? AND a.rowid>? ORDER BY a.rowid LIMIT 51`, runID, after)
	if err != nil {
		replyError(w, 500, "load artifacts")
		return
	}
	defer rows.Close()
	artifacts := make([]pipelineRunArtifact, 0)
	truncated := false
	var next int64
	for rows.Next() {
		var id, execution, kind, label, state, media, sha, source, reason, at, path string
		var attempt, contentSize int
		var seq int64
		if err := rows.Scan(&seq, &id, &execution, &kind, &label, &state, &media, &sha, &contentSize, &source, &reason, &at, &path, &attempt); err != nil {
			replyError(w, 500, "read artifacts")
			return
		}
		if len(artifacts) == 50 {
			truncated = true
			break
		}
		next = seq
		artifacts = append(artifacts, pipelineRunArtifact{
			ID: id, StepExecutionID: execution, Kind: kind, Label: label, State: state,
			MediaType: media, SHA256: sha, ContentBytes: contentSize, Source: source,
			Error: reason, CreatedAt: at, ExecutionPath: path, Attempt: attempt,
		})
	}
	if err := rows.Err(); err != nil {
		replyError(w, 500, fmt.Sprint("read artifacts: ", err))
		return
	}
	var cursor *string
	if truncated {
		encoded := strconv.FormatInt(next, 10)
		cursor = &encoded
	}
	writeJSON(w, 200, pipelineRunArtifactList{Artifacts: artifacts, Truncated: truncated, NextCursor: cursor})
}
