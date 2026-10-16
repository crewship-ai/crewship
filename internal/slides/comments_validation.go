package slides

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RebuildCommentPageTx validates retained history and reconstructs projections
// from original anchors and immutable deck revisions. Callers must hold the
// restore transaction; imported status/provenance is never trusted.
func RebuildCommentPageTx(ctx context.Context, tx *sql.Tx, key Key) error {
	head, err := readState(ctx, tx, key, 0)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+commentThreadColumns+` FROM slides_comment_threads t LEFT JOIN slides_comment_events e ON e.workspace_id=t.workspace_id AND e.page_id=t.page_id AND e.thread_id=t.id AND e.version=1 WHERE t.workspace_id=? AND t.page_id=? ORDER BY t.thread_seq LIMIT ?`, key.WorkspaceID, key.PageID, MaxCommentThreads+1)
	if err != nil {
		return err
	}
	threads := []CommentThread{}
	for rows.Next() {
		thread, e := scanCommentThread(rows)
		if e != nil {
			rows.Close()
			return e
		}
		threads = append(threads, thread)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(threads) > MaxCommentThreads {
		return ErrQuota
	}
	if len(threads) == 0 {
		return nil
	}
	minRevision := head.Revision
	for _, thread := range threads {
		if !identifier.MatchString(thread.ID) || thread.PageID != key.PageID || ValidateCommentAnchor(thread.Anchor) != nil || thread.Anchor.Revision > head.Revision || thread.Version < 1 || thread.CommentCount < 1 {
			return errors.New("invalid retained Slides comment thread")
		}
		if _, e := time.Parse(time.RFC3339Nano, thread.CreatedAt); e != nil {
			return e
		}
		if _, e := time.Parse(time.RFC3339Nano, thread.UpdatedAt); e != nil {
			return e
		}
		if thread.Anchor.Revision < minRevision {
			minRevision = thread.Anchor.Revision
		}
	}
	if head.Revision-minRevision >= 1000 {
		return ErrQuota
	}
	// One membership index per revision; no repeated element scan per thread.
	for revision := minRevision; revision <= head.Revision; revision++ {
		state, e := readState(ctx, tx, key, revision)
		if e != nil {
			return fmt.Errorf("retained comment anchor revision: %w", e)
		}
		if revision == head.Revision && state.ContentHash != head.ContentHash {
			return errors.New("Slides head/revision mismatch")
		}
		var kind string
		if e = tx.QueryRowContext(ctx, `SELECT kind FROM slides_revisions WHERE workspace_id=? AND page_id=? AND rev=?`, key.WorkspaceID, key.PageID, revision).Scan(&kind); e != nil {
			return e
		}
		index := indexAnchors(state.Deck)
		for i := range threads {
			thread := &threads[i]
			if revision < thread.Anchor.Revision {
				continue
			}
			present := index.exists(thread.Anchor)
			if revision == thread.Anchor.Revision {
				if !present {
					return errors.New("original Slides comment anchor absent at pinned revision")
				}
				thread.Status = "attached"
				thread.LastMissingRevision = nil
				thread.RestoredAtRevision = nil
			} else if !present {
				if thread.Status != "detached" {
					value := revision
					thread.LastMissingRevision = &value
				}
				thread.Status = "detached"
			} else if thread.Status == "detached" {
				if kind != "restore" {
					return errors.New("comment identity reappeared outside restore")
				}
				thread.Status = "restored"
				value := revision
				thread.RestoredAtRevision = &value
			}
			thread.ProjectionRevision = revision
		}
	}
	var totalEvents, totalBytes int
	for _, thread := range threads {
		events, e := tx.QueryContext(ctx, `SELECT e.version,e.kind,e.body,e.actor,e.mutation_id,e.created_at,r.request_hash FROM slides_comment_events e LEFT JOIN slides_comment_receipts r ON r.workspace_id=e.workspace_id AND r.page_id=e.page_id AND r.thread_id=e.thread_id AND r.event_version=e.version WHERE e.workspace_id=? AND e.page_id=? AND e.thread_id=? ORDER BY e.version LIMIT ?`, key.WorkspaceID, key.PageID, thread.ID, MaxCommentEvents+1)
		if e != nil {
			return e
		}
		var version, comments int64
		resolved := false
		for events.Next() {
			var event CommentEvent
			var receiptHash sql.NullString
			if e = events.Scan(&event.Version, &event.Kind, &event.Body, &event.Actor, &event.MutationID, &event.CreatedAt, &receiptHash); e != nil {
				events.Close()
				return e
			}
			version++
			totalEvents++
			totalBytes += len(event.Body)
			if event.Version != version || validateCommentEvent(event) != nil {
				events.Close()
				return errors.New("invalid retained comment event sequence")
			}
			if _, e = time.Parse(time.RFC3339Nano, event.CreatedAt); e != nil { // tsformat:allow: validate retained timestamp syntax only; event ordering uses integer version, no timestamp is written or compared in SQL
				events.Close()
				return e
			}
			switch event.Kind {
			case "comment", "reply":
				if resolved {
					events.Close()
					return errors.New("reply to retained resolved thread")
				}
				comments++
			case "resolve":
				if resolved {
					events.Close()
					return errors.New("duplicate retained resolution")
				}
				resolved = true
			case "reopen":
				if !resolved {
					events.Close()
					return errors.New("retained reopen without resolution")
				}
				resolved = false
			}
			if receiptHash.Valid {
				hash, err := retainedCommentRequestHash(key, thread, event)
				if err != nil || hash != receiptHash.String {
					events.Close()
					return errors.New("Slides comment receipt hash mismatch")
				}
			}
			if totalEvents > MaxCommentEvents || totalBytes > MaxCommentHistoryBytes {
				events.Close()
				return ErrQuota
			}
		}
		e = events.Err()
		events.Close()
		if e != nil {
			return e
		}
		if version != thread.Version || comments != int64(thread.CommentCount) || resolved != thread.Resolved {
			return errors.New("Slides comment history/count/version mismatch")
		}
		if _, e = tx.ExecContext(ctx, `UPDATE slides_comment_threads SET status=?,projection_revision=?,last_missing_revision=?,restored_at_revision=? WHERE workspace_id=? AND page_id=? AND id=?`, thread.Status, thread.ProjectionRevision, thread.LastMissingRevision, thread.RestoredAtRevision, key.WorkspaceID, key.PageID, thread.ID); e != nil {
			return e
		}
	}
	var invalidReceipts int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM slides_comment_receipts r LEFT JOIN slides_comment_events e ON e.workspace_id=r.workspace_id AND e.page_id=r.page_id AND e.thread_id=r.thread_id AND e.version=r.event_version WHERE r.workspace_id=? AND r.page_id=? AND (e.version IS NULL OR r.actor<>e.actor OR r.mutation_id<>e.mutation_id OR r.kind<>CASE e.kind WHEN 'comment' THEN 'create' WHEN 'reply' THEN 'reply' ELSE 'resolve' END)`, key.WorkspaceID, key.PageID).Scan(&invalidReceipts)
	if err != nil {
		return err
	}
	if invalidReceipts != 0 {
		return errors.New("Slides comment receipt/event mismatch")
	}
	return nil
}

func retainedCommentRequestHash(key Key, thread CommentThread, event CommentEvent) (string, error) {
	switch event.Kind {
	case "comment":
		return requestHash(key, "comment/create", CreateCommentRequest{PageID: key.PageID, SchemaVersion: CommentProtocolVersion, BaseRev: thread.Anchor.Revision, MutationID: event.MutationID, Anchor: thread.Anchor, Body: event.Body})
	case "reply":
		return requestHash(key, "comment/reply/"+thread.ID, ReplyCommentRequest{PageID: key.PageID, SchemaVersion: CommentProtocolVersion, MutationID: event.MutationID, IfVersion: event.Version - 1, Body: event.Body})
	case "resolve", "reopen":
		return requestHash(key, "comment/resolve/"+thread.ID, ResolveCommentRequest{PageID: key.PageID, SchemaVersion: CommentProtocolVersion, MutationID: event.MutationID, IfVersion: event.Version - 1, Resolved: event.Kind == "resolve"})
	}
	return "", errors.New("unsupported retained comment event")
}
