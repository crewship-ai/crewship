package slides

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

type CommentPageRequest struct {
	Limit  int
	Cursor string
}
type commentCursor struct {
	CursorSchemaVersion int    `json:"cursor_schema_version"`
	WorkspaceID         string `json:"workspace_id"`
	PageID              string `json:"page_id"`
	ThreadID            string `json:"thread_id,omitempty"`
	After               int64  `json:"after"`
	Watermark           int64  `json:"watermark"`
}

func decodeCommentCursor(key Key, thread string, input CommentPageRequest) (commentCursor, int, bool, error) {
	limit := input.Limit
	if limit == 0 {
		limit = 25
	}
	if limit < 1 || limit > MaxCommentPageSize {
		return commentCursor{}, 0, false, fmt.Errorf("%w: comment page limit must be 1..100", ErrInvalid)
	}
	cursor := commentCursor{CursorSchemaVersion: 1, WorkspaceID: key.WorkspaceID, PageID: key.PageID, ThreadID: thread}
	if input.Cursor == "" {
		return cursor, limit, true, nil
	}
	if len(input.Cursor) > 1024 {
		return cursor, 0, false, fmt.Errorf("%w: bounded comment cursor required", ErrInvalid)
	}
	raw, err := base64.RawURLEncoding.DecodeString(input.Cursor)
	if err != nil {
		return cursor, 0, false, fmt.Errorf("%w: invalid comment cursor", ErrInvalid)
	}
	fields := []string{"cursor_schema_version", "workspace_id", "page_id", "after", "watermark"}
	if thread != "" {
		fields = append(fields, "thread_id")
	}
	if err = strictRequest(raw, &cursor, nil, fields, nil); err != nil {
		return cursor, 0, false, fmt.Errorf("%w: invalid comment cursor", ErrInvalid)
	}
	if cursor.CursorSchemaVersion != 1 || cursor.WorkspaceID != key.WorkspaceID || cursor.PageID != key.PageID || cursor.ThreadID != thread || cursor.After < 0 || cursor.Watermark < cursor.After {
		return cursor, 0, false, fmt.Errorf("%w: comment cursor identity/watermark mismatch", ErrInvalid)
	}
	return cursor, limit, false, nil
}
func encodeCommentCursor(cursor commentCursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

const commentThreadColumns = `t.id,t.page_id,t.anchor_schema_version,t.anchor_kind,t.slide_id,COALESCE(t.element_id,''),t.anchor_revision,t.version,t.resolved,t.status,t.projection_revision,t.last_missing_revision,t.restored_at_revision,t.created_at,t.updated_at,t.comment_count,e.version,e.kind,e.body,e.actor,e.mutation_id,e.created_at`

type commentScanner interface{ Scan(...any) error }

func scanCommentThread(row commentScanner) (CommentThread, error) {
	var thread CommentThread
	var missing, restored sql.NullInt64
	err := row.Scan(&thread.ID, &thread.PageID, &thread.Anchor.AnchorSchemaVersion, &thread.Anchor.Kind, &thread.Anchor.SlideID, &thread.Anchor.ElementID, &thread.Anchor.Revision, &thread.Version, &thread.Resolved, &thread.Status, &thread.ProjectionRevision, &missing, &restored, &thread.CreatedAt, &thread.UpdatedAt, &thread.CommentCount, &thread.FirstComment.Version, &thread.FirstComment.Kind, &thread.FirstComment.Body, &thread.FirstComment.Actor, &thread.FirstComment.MutationID, &thread.FirstComment.CreatedAt)
	if missing.Valid {
		value := missing.Int64
		thread.LastMissingRevision = &value
	}
	if restored.Valid {
		value := restored.Int64
		thread.RestoredAtRevision = &value
	}
	if errors.Is(err, sql.ErrNoRows) {
		return thread, ErrNotFound
	}
	if err == nil {
		err = validateCommentEvent(thread.FirstComment)
		if err == nil && thread.ID != thread.FirstComment.MutationID {
			err = errors.New("Slides comment thread identity differs from original create mutation")
		}
	}
	return thread, err
}
func readCommentThread(ctx context.Context, tx *sql.Tx, key Key, id string, state State) (CommentThread, error) {
	thread, err := scanCommentThread(tx.QueryRowContext(ctx, `SELECT `+commentThreadColumns+` FROM slides_comment_threads t LEFT JOIN slides_comment_events e ON e.workspace_id=t.workspace_id AND e.page_id=t.page_id AND e.thread_id=t.id AND e.version=1 WHERE t.workspace_id=? AND t.page_id=? AND t.id=?`, key.WorkspaceID, key.PageID, id))
	if err != nil {
		return thread, err
	}
	if err = ValidateCommentProjection(thread, state); err != nil {
		return thread, err
	}
	return thread, nil
}
func (s *Store) CommentThreads(ctx context.Context, key Key, page CommentPageRequest, authorize Authorize) (result CommentThreadPage, err error) {
	result.Items = []CommentThread{}
	cursor, limit, initial, err := decodeCommentCursor(key, "", page)
	if err != nil {
		return result, err
	}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, _ string) error {
		state, err := readState(ctx, tx, key, 0)
		if err != nil {
			return err
		}
		var currentWatermark int64
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(thread_seq),0) FROM slides_comment_threads WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID).Scan(&currentWatermark); err != nil {
			return err
		}
		if initial {
			cursor.Watermark = currentWatermark
		} else if cursor.Watermark > currentWatermark {
			return fmt.Errorf("%w: comment cursor watermark is unavailable", ErrInvalid)
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+commentThreadColumns+`,t.thread_seq FROM slides_comment_threads t LEFT JOIN slides_comment_events e ON e.workspace_id=t.workspace_id AND e.page_id=t.page_id AND e.thread_id=t.id AND e.version=1 WHERE t.workspace_id=? AND t.page_id=? AND t.thread_seq>? AND t.thread_seq<=? ORDER BY t.thread_seq LIMIT ?`, key.WorkspaceID, key.PageID, cursor.After, cursor.Watermark, limit+1)
		if err != nil {
			return err
		}
		// Scan the monotonic sequence with the same explicit field set. No private
		// history bodies are loaded beyond the first comment for each visible row.
		sequences := []int64{}
		for rows.Next() {
			var sequence int64
			thread, err := scanCommentThread(commentSequenceScanner{rows, &sequence})
			if err != nil {
				rows.Close()
				return err
			}
			result.Items = append(result.Items, thread)
			sequences = append(sequences, sequence)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if err = ValidateCommentProjections(result.Items, state); err != nil {
			return err
		}
		result.Watermark = cursor.Watermark
		result.HasMore = len(result.Items) > limit
		if result.HasMore {
			result.Items = result.Items[:limit]
			cursor.After = sequences[limit-1]
			result.NextCursor = encodeCommentCursor(cursor)
		}
		return nil
	})
	return
}

type commentSequenceScanner struct {
	row      commentScanner
	sequence *int64
}

func (scanner commentSequenceScanner) Scan(values ...any) error {
	return scanner.row.Scan(append(values, scanner.sequence)...)
}
func (s *Store) CommentThread(ctx context.Context, key Key, id string, authorize Authorize) (result CommentThread, err error) {
	if !identifier.MatchString(id) {
		return result, fmt.Errorf("%w: invalid thread ID", ErrInvalid)
	}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, _ string) error {
		state, e := readState(ctx, tx, key, 0)
		if e != nil {
			return e
		}
		result, e = readCommentThread(ctx, tx, key, id, state)
		return e
	})
	return
}
func (s *Store) CommentHistory(ctx context.Context, key Key, id string, page CommentPageRequest, authorize Authorize) (result CommentHistoryPage, err error) {
	result.Items = []CommentEvent{}
	if !identifier.MatchString(id) {
		return result, fmt.Errorf("%w: invalid thread ID", ErrInvalid)
	}
	cursor, limit, initial, err := decodeCommentCursor(key, id, page)
	if err != nil {
		return result, err
	}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, _ string) error {
		state, err := readState(ctx, tx, key, 0)
		if err != nil {
			return err
		}
		thread, err := readCommentThread(ctx, tx, key, id, state)
		if err != nil {
			return err
		}
		if initial {
			cursor.Watermark = thread.Version
		} else if cursor.Watermark > thread.Version {
			return fmt.Errorf("%w: comment history watermark is unavailable", ErrInvalid)
		}
		rows, err := tx.QueryContext(ctx, `SELECT version,kind,body,actor,mutation_id,created_at FROM slides_comment_events WHERE workspace_id=? AND page_id=? AND thread_id=? AND version>? AND version<=? ORDER BY version LIMIT ?`, key.WorkspaceID, key.PageID, id, cursor.After, cursor.Watermark, limit+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			var event CommentEvent
			if err = rows.Scan(&event.Version, &event.Kind, &event.Body, &event.Actor, &event.MutationID, &event.CreatedAt); err != nil {
				rows.Close()
				return err
			}
			if event.Version != cursor.After+int64(len(result.Items))+1 || validateCommentEvent(event) != nil {
				rows.Close()
				return errors.New("invalid or incomplete Slides comment history")
			}
			result.Items = append(result.Items, event)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		result.Watermark = cursor.Watermark
		result.HasMore = len(result.Items) > limit
		if !result.HasMore && (len(result.Items) == 0 && cursor.After != cursor.Watermark || len(result.Items) > 0 && result.Items[len(result.Items)-1].Version != cursor.Watermark) {
			return errors.New("incomplete Slides comment history watermark")
		}
		if result.HasMore {
			result.Items = result.Items[:limit]
			cursor.After = result.Items[limit-1].Version
			result.NextCursor = encodeCommentCursor(cursor)
		}
		return nil
	})
	return
}
func commentReceipt(ctx context.Context, tx *sql.Tx, key Key, id, hash, actor, kind string) (CommentReceipt, bool, error) {
	var result CommentReceipt
	var recordedHash, recordedActor, recordedKind string
	err := tx.QueryRowContext(ctx, `SELECT mutation_id,thread_id,event_version,request_hash,actor,kind FROM slides_comment_receipts WHERE workspace_id=? AND page_id=? AND mutation_id=?`, key.WorkspaceID, key.PageID, id).Scan(&result.MutationID, &result.ThreadID, &result.Version, &recordedHash, &recordedActor, &recordedKind)
	if errors.Is(err, sql.ErrNoRows) {
		var historic bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM slides_comment_events WHERE workspace_id=? AND page_id=? AND mutation_id=?)`, key.WorkspaceID, key.PageID, id).Scan(&historic); err != nil {
			return result, false, err
		}
		if historic {
			return result, false, ErrDuplicate
		}
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	if recordedHash != hash || recordedActor != actor || recordedKind != kind {
		return result, false, ErrDuplicate
	}
	result.Duplicate = true
	return result, true, nil
}
func commentQuota(ctx context.Context, tx *sql.Tx, key Key, body string, create bool) error {
	var count int
	var historyBytes int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(body AS BLOB))),0) FROM slides_comment_events WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID).Scan(&count, &historyBytes); err != nil {
		return err
	}
	if count >= MaxCommentEvents || historyBytes+int64(len(body)) > MaxCommentHistoryBytes {
		return ErrQuota
	}
	if create {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM slides_comment_threads WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID).Scan(&count); err != nil {
			return err
		}
		if count >= MaxCommentThreads {
			return ErrQuota
		}
	}
	return nil
}
func writeCommentEvent(ctx context.Context, tx *sql.Tx, key Key, thread, id, actor, kind, body, requestHash, requestKind string, version int64) (CommentReceipt, error) {
	result := CommentReceipt{MutationID: id, ThreadID: thread, Version: version}
	if _, err := tx.ExecContext(ctx, `INSERT INTO slides_comment_events(workspace_id,page_id,thread_id,version,kind,body,actor,mutation_id) VALUES(?,?,?,?,?,?,?,?)`, key.WorkspaceID, key.PageID, thread, version, kind, body, actor, id); err != nil {
		return result, err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO slides_comment_receipts(workspace_id,page_id,thread_id,event_version,mutation_id,request_hash,actor,kind) VALUES(?,?,?,?,?,?,?,?)`, key.WorkspaceID, key.PageID, thread, version, id, requestHash, actor, requestKind)
	return result, err
}
func (s *Store) CreateComment(ctx context.Context, key Key, request CreateCommentRequest, authorize Authorize) (result CommentReceipt, err error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	request, err = DecodeCreateComment(raw)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	hash, err := requestHash(key, "comment/create", request)
	if err != nil {
		return result, err
	}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, actor string) error {
		if request.PageID != key.PageID {
			return ErrConflict
		}
		previous, found, err := commentReceipt(ctx, tx, key, request.MutationID, hash, actor, "create")
		if err != nil {
			return err
		}
		if found {
			result = previous
			return nil
		}
		state, err := readState(ctx, tx, key, 0)
		if err != nil {
			return err
		}
		if state.Revision != request.BaseRev || !anchorExists(state.Deck, request.Anchor) {
			return ErrConflict
		}
		if err = commentQuota(ctx, tx, key, request.Body, true); err != nil {
			return err
		}
		var sequence int64
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(thread_seq),0)+1 FROM slides_comment_threads WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID).Scan(&sequence); err != nil {
			return err
		}
		var element any
		if request.Anchor.Kind == "element" {
			element = request.Anchor.ElementID
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO slides_comment_threads(workspace_id,page_id,id,thread_seq,anchor_schema_version,anchor_kind,slide_id,element_id,anchor_revision,version,resolved,comment_count,status,projection_revision) VALUES(?,?,?,?,?,?,?,?,?,1,0,1,'attached',?)`, key.WorkspaceID, key.PageID, request.MutationID, sequence, request.Anchor.AnchorSchemaVersion, request.Anchor.Kind, request.Anchor.SlideID, element, request.Anchor.Revision, state.Revision); err != nil {
			return err
		}
		result, err = writeCommentEvent(ctx, tx, key, request.MutationID, request.MutationID, actor, "comment", request.Body, hash, "create", 1)
		return err
	})
	return
}
func (s *Store) ReplyComment(ctx context.Context, key Key, threadID string, request ReplyCommentRequest, authorize Authorize) (result CommentReceipt, err error) {
	if !identifier.MatchString(threadID) {
		return result, fmt.Errorf("%w: invalid thread ID", ErrInvalid)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	request, err = DecodeReplyComment(raw)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	hash, err := requestHash(key, "comment/reply/"+threadID, request)
	if err != nil {
		return result, err
	}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, actor string) error {
		if request.PageID != key.PageID {
			return ErrConflict
		}
		previous, found, err := commentReceipt(ctx, tx, key, request.MutationID, hash, actor, "reply")
		if err != nil {
			return err
		}
		if found {
			result = previous
			return nil
		}
		state, err := readState(ctx, tx, key, 0)
		if err != nil {
			return err
		}
		thread, err := readCommentThread(ctx, tx, key, threadID, state)
		if err != nil {
			return err
		}
		if thread.Version != request.IfVersion || thread.Resolved {
			return ErrConflict
		}
		if err = commentQuota(ctx, tx, key, request.Body, false); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE slides_comment_threads SET version=version+1,comment_count=comment_count+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE workspace_id=? AND page_id=? AND id=?`, key.WorkspaceID, key.PageID, threadID); err != nil {
			return err
		}
		result, err = writeCommentEvent(ctx, tx, key, threadID, request.MutationID, actor, "reply", request.Body, hash, "reply", thread.Version+1)
		return err
	})
	return
}
func (s *Store) ResolveComment(ctx context.Context, key Key, threadID string, request ResolveCommentRequest, authorize Authorize) (result CommentReceipt, err error) {
	if !identifier.MatchString(threadID) {
		return result, fmt.Errorf("%w: invalid thread ID", ErrInvalid)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	request, err = DecodeResolveComment(raw)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	hash, err := requestHash(key, "comment/resolve/"+threadID, request)
	if err != nil {
		return result, err
	}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, actor string) error {
		if request.PageID != key.PageID {
			return ErrConflict
		}
		previous, found, err := commentReceipt(ctx, tx, key, request.MutationID, hash, actor, "resolve")
		if err != nil {
			return err
		}
		if found {
			result = previous
			return nil
		}
		state, err := readState(ctx, tx, key, 0)
		if err != nil {
			return err
		}
		thread, err := readCommentThread(ctx, tx, key, threadID, state)
		if err != nil {
			return err
		}
		if thread.Version != request.IfVersion || thread.Resolved == request.Resolved {
			return ErrConflict
		}
		if err = commentQuota(ctx, tx, key, "", false); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE slides_comment_threads SET version=version+1,resolved=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE workspace_id=? AND page_id=? AND id=?`, request.Resolved, key.WorkspaceID, key.PageID, threadID); err != nil {
			return err
		}
		kind := "resolve"
		if !request.Resolved {
			kind = "reopen"
		}
		result, err = writeCommentEvent(ctx, tx, key, threadID, request.MutationID, actor, kind, "", hash, "resolve", thread.Version+1)
		return err
	})
	return
}

func validateCommentEvent(event CommentEvent) error {
	if event.Version < 1 || event.Actor == "" || !identifier.MatchString(event.MutationID) {
		return errors.New("invalid Slides comment event identity")
	}
	switch event.Kind {
	case "comment":
		if event.Version != 1 || !commentBodyValid(event.Body) {
			return errors.New("invalid first Slides comment")
		}
	case "reply":
		if event.Version == 1 || !commentBodyValid(event.Body) {
			return errors.New("invalid Slides reply")
		}
	case "resolve", "reopen":
		if event.Version == 1 || event.Body != "" {
			return errors.New("invalid Slides comment resolution")
		}
	default:
		return errors.New("unsupported Slides comment event")
	}
	return nil
}
