package api

import (
	"context"
	"database/sql"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

const pageMutationReplayWindow = 24 * time.Hour
const pageRequestHistoryWindow = 90 * 24 * time.Hour
const pageTerminalRequestHistory = 100
const workspaceTerminalBoardHistory = 1000

// Publication receipts for retained revisions remain useful for exact retries.
// Receipts alone never pin source bytes. Removed source has a 24-hour replay
// guarantee, independent of how many revisions a long-lived Page has published.
func compactArtifactReceipts(ctx context.Context, tx *sql.Tx, page string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM page_artifact_mutation_receipts WHERE page_id=? AND created_at<? AND NOT EXISTS(SELECT 1 FROM page_artifact_revisions r WHERE r.page_id=page_artifact_mutation_receipts.page_id AND r.revision=page_artifact_mutation_receipts.revision)`, page, tsformat.Format(now.Add(-pageMutationReplayWindow)))
	return err
}

// Discussion versions are monotonic, so an expired reply/edit/reaction retry
// still fails its original precondition after compaction. Keep thread-creation
// receipts for the thread lifetime: creation has no version precondition.
// They do not consume the transient mutation quota.
// This does not delete comments, original anchors or private body history.
func compactDiscussionReceipts(ctx context.Context, tx *sql.Tx, page string, board bool, now time.Time) error {
	table := "page_artifact_comment_mutations"
	if board {
		table = "page_board_comment_mutations"
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE page_id=? AND operation!='create' AND created_at<?`, page, tsformat.Format(now.Add(-pageMutationReplayWindow)))
	return err
}

// Terminal jobs have a documented history lifetime. Pending/running jobs and
// diagrams waiting for human insertion are never aged out. Current/recent-undo
// drawing provenance also retains its Mermaid source. Cascades release retired
// snapshots, previews and source/file pins; GC owns physical blob removal.
func compactPageRequestHistory(ctx context.Context, tx *sql.Tx, workspace string, now time.Time) error {
	cutoff := tsformat.Format(now.Add(-pageRequestHistoryWindow))
	// Keep at least the replay window, and otherwise the most recent terminal
	// jobs within resource/workspace bounds. A 90-day age limit alone could fill
	// the hard request quota in a busy day and block dispatch for months.
	replayCutoff := tsformat.Format(now.Add(-pageMutationReplayWindow))
	queries := []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM page_artifact_requests WHERE id IN (
   SELECT id FROM (SELECT r.id,r.updated_at,ROW_NUMBER() OVER(PARTITION BY r.page_id ORDER BY r.updated_at DESC,r.id DESC) history_rank
    FROM page_artifact_requests r JOIN pages p ON p.id=r.page_id WHERE p.workspace_id=? AND r.status IN('succeeded','failed','cancelled'))
   WHERE updated_at<? OR(history_rank>? AND updated_at<?) ORDER BY updated_at,id LIMIT 100)`, []any{workspace, cutoff, pageTerminalRequestHistory, replayCutoff}},
		{`DELETE FROM page_board_requests WHERE id IN (
   SELECT id FROM (SELECT r.id,r.updated_at,
    ROW_NUMBER() OVER(PARTITION BY COALESCE(r.board_page_id,r.original_board_id) ORDER BY r.updated_at DESC,r.id DESC) history_rank,
    ROW_NUMBER() OVER(ORDER BY r.updated_at DESC,r.id DESC) workspace_rank
    FROM page_board_requests r WHERE r.workspace_id=? AND r.status IN('inserted','succeeded','failed','cancelled')
    AND NOT EXISTS(SELECT 1 FROM page_board_elements e WHERE e.origin_request_id=r.id AND (e.element_json IS NOT NULL OR e.deleted_at>=?)))
   WHERE updated_at<? OR((history_rank>? OR workspace_rank>?) AND updated_at<?) ORDER BY updated_at,id LIMIT 100)`, []any{workspace, replayCutoff, cutoff, pageTerminalRequestHistory, workspaceTerminalBoardHistory, replayCutoff}},
	}
	for _, query := range queries {
		if _, err := tx.ExecContext(ctx, query.sql, query.args...); err != nil {
			return err
		}
	}

	return nil
}

func compactBoardFiles(ctx context.Context, tx *sql.Tx, board string, now time.Time) error {
	// Retain current drawings, all retained job snapshots and recent upload
	// receipts. last_used_at protects images removed/replaced within undo time.
	_, err := tx.ExecContext(ctx, `DELETE FROM page_board_files WHERE id IN (
 SELECT f.id FROM page_board_files f WHERE f.board_page_id=?
 AND COALESCE(NULLIF(f.last_used_at,''),f.created_at)<?
 AND NOT EXISTS(SELECT 1 FROM page_board_elements e WHERE e.board_page_id=f.board_page_id AND e.element_json IS NOT NULL AND json_extract(e.element_json,'$.fileId')=f.id)
 AND NOT EXISTS(SELECT 1 FROM page_board_request_files r WHERE r.file_id=f.id)
 AND NOT EXISTS(SELECT 1 FROM page_board_mutations m WHERE m.result_file_id=f.id AND m.created_at>=?)
 ORDER BY f.created_at,f.id LIMIT 100)`, board, tsformat.Format(now.Add(-boardHistoryWindow)), tsformat.Format(now.Add(-boardHistoryWindow)))
	return err
}

// Walk bounded batches with stable cursors, including workspaces whose source
// Pages were deleted. Writes also compact their own resource before quota checks.
func (h *PageHandler) cleanupPageRetention(ctx context.Context, pageCursor, workspaceCursor string, now time.Time) (string, string, error) {
	collect := func(query, cursor string) ([]string, error) {
		rows, err := h.db.QueryContext(ctx, query, cursor)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		return ids, rows.Err()
	}
	pages, err := collect(`SELECT id FROM pages WHERE id>? ORDER BY id LIMIT 10`, pageCursor)
	if err != nil {
		return pageCursor, workspaceCursor, err
	}
	if len(pages) == 0 {
		pageCursor = ""
	}
	for _, page := range pages {
		tx, err := h.db.BeginTx(ctx, nil)
		if err != nil {
			return pageCursor, workspaceCursor, err
		}
		for _, compact := range []func() error{
			func() error { return compactArtifactReceipts(ctx, tx, page, now) },
			func() error { return compactDiscussionReceipts(ctx, tx, page, false, now) },
			func() error { return compactDiscussionReceipts(ctx, tx, page, true, now) },
			func() error { return compactBoardFiles(ctx, tx, page, now) },
		} {
			if err = compact(); err != nil {
				break
			}
		}
		if err != nil {
			tx.Rollback()
			return pageCursor, workspaceCursor, err
		}
		if err = tx.Commit(); err != nil {
			return pageCursor, workspaceCursor, err
		}
		pageCursor = page
	}
	workspaces, err := collect(`SELECT id FROM workspaces WHERE id>? ORDER BY id LIMIT 10`, workspaceCursor)
	if err != nil {
		return pageCursor, workspaceCursor, err
	}
	if len(workspaces) == 0 {
		workspaceCursor = ""
	}
	for _, workspace := range workspaces {
		tx, err := h.db.BeginTx(ctx, nil)
		if err != nil {
			return pageCursor, workspaceCursor, err
		}
		if err = compactPageRequestHistory(ctx, tx, workspace, now); err != nil {
			tx.Rollback()
			return pageCursor, workspaceCursor, err
		}
		if err = tx.Commit(); err != nil {
			return pageCursor, workspaceCursor, err
		}
		workspaceCursor = workspace
	}
	return pageCursor, workspaceCursor, nil
}
