package api

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// RetainPageProjects keeps 64 recent revisions/builds and 32 publications per
// Page within shared workspace history budgets, plus live/draft/running roots.
// Counters never rewind. Workspace caps leave headroom below storage admission.
// The exclusive lease covers SQL root selection through the last file deletion.
func (h *PageHandler) RetainPageProjects(ctx context.Context, workspace string) error {
	return h.retainPageProjects(ctx, workspace, false)
}

func (h *PageHandler) retainPageProjects(ctx context.Context, workspace string, pressure bool) error {
	if h.projectStore == nil || h.pageArtifacts == nil {
		return nil
	}
	release, err := h.projectStore.Lease(ctx, workspace, true)
	if err != nil {
		return err
	}
	defer release()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	queries := []string{
		`DELETE FROM page_project_publications AS old WHERE page_id IN (SELECT id FROM pages WHERE workspace_id=?) AND version NOT IN (SELECT version FROM page_project_live WHERE page_id=old.page_id) AND version NOT IN (SELECT version FROM page_project_publications WHERE page_id=old.page_id ORDER BY version DESC LIMIT 32)`,
		`DELETE FROM page_project_publications AS old WHERE page_id IN (SELECT id FROM pages WHERE workspace_id=?) AND NOT EXISTS (SELECT 1 FROM page_project_live WHERE page_id=old.page_id AND version=old.version) AND (page_id,version) NOT IN (SELECT v.page_id,v.version FROM page_project_publications v JOIN pages p ON p.id=v.page_id WHERE p.workspace_id=? ORDER BY v.created_at DESC,v.page_id,v.version DESC LIMIT 64)`,
		`DELETE FROM page_project_builds AS old WHERE page_id IN (SELECT id FROM pages WHERE workspace_id=?) AND state!='running' AND NOT EXISTS (SELECT 1 FROM page_project_publications WHERE build_id=old.id) AND id NOT IN (SELECT id FROM page_project_builds WHERE page_id=old.page_id AND state!='running' ORDER BY created_at DESC,id DESC LIMIT 64)`,
		`DELETE FROM page_project_builds AS old WHERE page_id IN (SELECT id FROM pages WHERE workspace_id=?) AND state!='running' AND NOT EXISTS (SELECT 1 FROM page_project_publications WHERE build_id=old.id) AND id NOT IN (SELECT b.id FROM page_project_builds b JOIN pages p ON p.id=b.page_id WHERE p.workspace_id=? AND b.state!='running' ORDER BY b.created_at DESC,b.id DESC LIMIT 128)`,
		`DELETE FROM page_project_revisions AS old WHERE page_id IN (SELECT id FROM pages WHERE workspace_id=?) AND revision NOT IN (SELECT revision FROM page_project_revisions WHERE page_id=old.page_id ORDER BY revision DESC LIMIT 64) AND NOT EXISTS (SELECT 1 FROM page_project_drafts WHERE page_id=old.page_id AND revision=old.revision) AND NOT EXISTS (SELECT 1 FROM page_project_builds WHERE page_id=old.page_id AND source_revision=old.revision) AND NOT EXISTS (SELECT 1 FROM page_project_publications WHERE page_id=old.page_id AND source_revision=old.revision)`,
		`DELETE FROM page_project_revisions AS old WHERE page_id IN (SELECT id FROM pages WHERE workspace_id=?) AND NOT EXISTS (SELECT 1 FROM page_project_drafts WHERE page_id=old.page_id AND revision=old.revision) AND NOT EXISTS (SELECT 1 FROM page_project_builds WHERE page_id=old.page_id AND source_revision=old.revision) AND NOT EXISTS (SELECT 1 FROM page_project_publications WHERE page_id=old.page_id AND source_revision=old.revision) AND (page_id,revision) NOT IN (SELECT v.page_id,v.revision FROM page_project_revisions v JOIN pages p ON p.id=v.page_id WHERE p.workspace_id=? ORDER BY v.created_at DESC,v.page_id,v.revision DESC LIMIT 128)`,
	}
	for _, query := range queries {
		if pressure {
			query = strings.ReplaceAll(query, "LIMIT 64)", "LIMIT 0)")
			query = strings.ReplaceAll(query, "LIMIT 128)", "LIMIT 0)")
			query = strings.ReplaceAll(query, "LIMIT 32)", "LIMIT 0)")
		}
		args := make([]any, strings.Count(query, "?"))
		for i := range args {
			args[i] = workspace
		}
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	sources := map[string]bool{}
	artifacts := map[string]bool{}
	checkpoints := map[string][]string{}
	// Read all roots from this transaction, including builds which outlive a draft.
	rows, err := tx.QueryContext(ctx, `SELECT d.page_id,d.source_digest,'',d.git_commit FROM page_project_drafts d JOIN pages p ON p.id=d.page_id WHERE p.workspace_id=? UNION SELECT r.page_id,r.source_digest,'',r.git_commit FROM page_project_revisions r JOIN pages p ON p.id=r.page_id WHERE p.workspace_id=? UNION SELECT b.page_id,b.source_digest,COALESCE(b.artifact_digest,''),'' FROM page_project_builds b JOIN pages p ON p.id=b.page_id WHERE p.workspace_id=? UNION SELECT v.page_id,v.source_digest,v.artifact_digest,v.git_commit FROM page_project_publications v JOIN pages p ON p.id=v.page_id WHERE p.workspace_id=?`, workspace, workspace, workspace, workspace)
	if err != nil {
		return err
	}
	for rows.Next() {
		var page, source, artifact, commit string
		if err := rows.Scan(&page, &source, &artifact, &commit); err != nil {
			rows.Close()
			return err
		}
		if source != "" {
			sources[source] = true
		}
		if artifact != "" {
			artifacts[artifact] = true
		}
		if commit != "" {
			checkpoints[page] = append(checkpoints[page], commit)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := h.projectStore.Prune(ctx, workspace, sources, checkpoints); err != nil {
		return err
	}
	if err := h.pageArtifacts.Prune(ctx, workspace, artifacts); err != nil {
		return err
	}
	release() // The expensive Git pass must not hold the readers' exclusive lease.
	return h.projectStore.Compact(ctx, workspace)
}

// StartProjectRetention runs one bounded maintenance pass per hour. It does not
// compete with startup/recovery, spawn a worker per workspace or poll per viewer.
func (h *PageHandler) StartProjectRetention(ctx context.Context) {
	if h.projectStore == nil || h.pageArtifacts == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rows, err := h.db.QueryContext(ctx, `SELECT id FROM workspaces ORDER BY id`)
				if err != nil {
					h.logger.Warn("pages: retention workspace lookup failed", "error", err)
					continue
				}
				var workspaces []string
				for rows.Next() {
					var id string
					if err = rows.Scan(&id); err != nil {
						break
					}
					workspaces = append(workspaces, id)
				}
				if err == nil {
					err = rows.Err()
				}
				rows.Close()
				if err != nil {
					h.logger.Warn("pages: retention workspace lookup failed", "error", err)
					continue
				}
				for _, id := range workspaces {
					if ctx.Err() != nil {
						return
					}
					bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
					err := h.RetainPageProjects(bounded, id)
					cancel()
					if err != nil {
						h.logger.Warn("pages: retention failed", "workspace_id", id, "error", err)
					}
				}
			}
		}
	}()
}

// Reclaim optional history before admission instead of trapping a workspace at
// the quota until a timer fires. Nested MCP/restore calls use their outer lease.
func (h *PageHandler) prepareProjectWrite(w http.ResponseWriter, r *http.Request) bool {
	if h.projectStore == nil || r.Context().Value(pageLeaseKey{}) == h.projectStore {
		return true
	}
	ws := WorkspaceIDFromContext(r.Context())
	release, err := h.projectStore.Lease(r.Context(), ws, false)
	if err != nil {
		h.logger.Warn("acquire Page storage lease", "error", err)
		replyError(w, 503, "Page storage is busy; try again shortly")
		return false
	}
	pressure, err := h.projectStore.StoragePressure(r.Context(), ws)
	if err == nil && h.pageArtifacts != nil {
		artifactPressure, artifactErr := h.pageArtifacts.StoragePressure(r.Context(), ws)
		pressure = pressure || artifactPressure
		err = artifactErr
	}
	release()
	if err != nil {
		replyInternalError(w, h.logger, "check Page storage capacity", err)
		return false
	}
	var builds, revisions int
	err = h.db.QueryRowContext(r.Context(), `SELECT (SELECT count(*) FROM page_project_builds b JOIN pages p ON p.id=b.page_id WHERE p.workspace_id=?),(SELECT count(*) FROM page_project_revisions v JOIN pages p ON p.id=v.page_id WHERE p.workspace_id=?)`, ws, ws).Scan(&builds, &revisions)
	if err != nil {
		replyInternalError(w, h.logger, "check Page history capacity", err)
		return false
	}
	if pressure || builds >= 192 || revisions >= 192 {
		if err := h.retainPageProjects(r.Context(), ws, pressure); err != nil {
			replyInternalError(w, h.logger, "reclaim Page history capacity", err)
			return false
		}
	}
	return true
}
