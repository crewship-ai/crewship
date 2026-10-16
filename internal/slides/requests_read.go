package slides

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type SelectedPageRequest struct {
	Limit  int
	Cursor string
}

type SelectedRequestPage struct {
	Items      []SelectedRequestSummary `json:"items"`
	NextCursor string                   `json:"next_cursor"`
	Watermark  int64                    `json:"watermark"`
	HasMore    bool                     `json:"has_more"`
}
type selectedCursor struct {
	CursorSchemaVersion int    `json:"cursor_schema_version"`
	Kind                string `json:"kind"`
	WorkspaceID         string `json:"workspace_id"`
	PageID              string `json:"page_id"`
	After               int64  `json:"after"`
	Watermark           int64  `json:"watermark"`
}

func decodeSelectedCursor(key Key, page SelectedPageRequest) (selectedCursor, int, bool, error) {
	cursor := selectedCursor{CursorSchemaVersion: 1, Kind: "slides_requests", WorkspaceID: key.WorkspaceID, PageID: key.PageID}
	limit := page.Limit
	if limit == 0 {
		limit = 25
	}
	if limit < 1 || limit > 100 {
		return cursor, 0, false, fmt.Errorf("%w: request list limit must be1..100", ErrInvalid)
	}
	if page.Cursor == "" {
		return cursor, limit, true, nil
	}
	if len(page.Cursor) > 1024 {
		return cursor, 0, false, fmt.Errorf("%w: bounded selected cursor required", ErrInvalid)
	}
	raw, err := base64.RawURLEncoding.DecodeString(page.Cursor)
	if err != nil {
		return cursor, 0, false, fmt.Errorf("%w: invalid selected cursor", ErrInvalid)
	}
	err = strictRequest(raw, &cursor, nil, []string{"cursor_schema_version", "kind", "workspace_id", "page_id", "after", "watermark"}, nil)
	if err != nil || cursor.CursorSchemaVersion != 1 || cursor.Kind != "slides_requests" || cursor.WorkspaceID != key.WorkspaceID || cursor.PageID != key.PageID || cursor.After < 0 || cursor.Watermark < cursor.After {
		return cursor, 0, false, fmt.Errorf("%w: selected cursor namespace mismatch", ErrInvalid)
	}
	return cursor, limit, false, nil
}
func selectedHistory(ctx context.Context, tx *sql.Tx, key Key, record SelectedAuthorityState) ([]SelectedRequestEvent, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version,kind,actor,detail,created_at,COALESCE(mutation_id,'') FROM slides_request_events WHERE workspace_id=? AND page_id=? AND request_id=? ORDER BY version LIMIT ?`, key.WorkspaceID, key.PageID, record.Summary.ID, MaxSelectedRequestEvents+1)
	if err != nil {
		return nil, err
	}
	history := []SelectedRequestEvent{}
	status := ""
	bound, result := false, false
	for rows.Next() {
		var event SelectedRequestEvent
		if err = rows.Scan(&event.Version, &event.Kind, &event.Actor, &event.Detail, &event.CreatedAt, &event.MutationID); err != nil {
			rows.Close()
			return nil, err
		}
		if event.Version != int64(len(history))+1 || event.Actor == "" || len(event.Actor) > 512 || !validText(event.Detail, MaxSelectedRequestErrorBytes) {
			rows.Close()
			return nil, errors.New("invalid selected history event")
		}
		if _, err = time.Parse(time.RFC3339Nano, event.CreatedAt); err != nil { // tsformat:allow: validate retained event syntax only; ordering uses integer version, no timestamp is written or compared in SQL
			rows.Close()
			return nil, err
		}
		if (event.Kind == "cancelled" && !identifier.MatchString(event.MutationID)) || (event.Kind != "cancelled" && event.MutationID != "") {
			rows.Close()
			return nil, errors.New("invalid selected event mutation identity")
		}
		switch event.Kind {
		case "queued":
			if len(history) != 0 || event.Actor != record.Summary.HumanActor {
				rows.Close()
				return nil, errors.New("invalid selected queued event")
			}
			status = "queued"
		case "dispatched":
			if status != "queued" {
				rows.Close()
				return nil, errors.New("invalid selected dispatch sequence")
			}
			status = "running"
		case "bound":
			if status != "running" || bound || record.Summary.OriginalRunID == "" {
				rows.Close()
				return nil, errors.New("invalid selected run binding")
			}
			bound = true
		case "result":
			if status != "running" || !bound || result || record.Summary.ResultRevision == nil || event.Actor != record.Summary.AgentActor+":"+record.Summary.OriginalRunID {
				rows.Close()
				return nil, errors.New("invalid selected owned result event")
			}
			result = true
		case "succeeded":
			if status != "running" || !result {
				rows.Close()
				return nil, errors.New("selected success without result")
			}
			status = "succeeded"
		case "failed", "cancelled", "interrupted":
			if !selectedActive(status) {
				rows.Close()
				return nil, errors.New("invalid selected terminal sequence")
			}
			status = event.Kind
		default:
			rows.Close()
			return nil, errors.New("unsupported selected history event")
		}
		history = append(history, event)
		if len(history) > MaxSelectedRequestEvents {
			rows.Close()
			return nil, ErrQuota
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if int64(len(history)) != record.Summary.Version || status != record.Summary.Status || result != (record.Summary.ResultRevision != nil) || bound != (record.Summary.OriginalRunID != "") {
		return nil, errors.New("selected history/version/status mismatch")
	}
	return history, nil
}
func (s *Store) ReadSelectedRequest(ctx context.Context, key Key, id string, authorize Authorize) (result SelectedRequestDetail, err error) {
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, _ string) error {
		record, e := ReadSelectedAuthorityTx(ctx, tx, key, id)
		if e != nil {
			return e
		}
		history, e := selectedHistory(ctx, tx, key, record)
		if e != nil {
			return e
		}
		result = SelectedRequestDetail{Request: record.Summary, History: history}
		return nil
	})
	return
}
func (s *Store) ListSelectedRequests(ctx context.Context, key Key, page SelectedPageRequest, authorize Authorize) (result SelectedRequestPage, err error) {
	cursor, limit, fresh, err := decodeSelectedCursor(key, page)
	if err != nil {
		return result, err
	}
	result.Items = []SelectedRequestSummary{}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, _ string) error {
		if _, e := readState(ctx, tx, key, 0); e != nil {
			return e
		}
		var maximum int64
		if e := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(request_seq),0) FROM slides_requests WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID).Scan(&maximum); e != nil {
			return e
		}
		if fresh {
			cursor.Watermark = maximum
		} else if cursor.Watermark > maximum {
			return fmt.Errorf("%w: future selected cursor watermark", ErrInvalid)
		}
		result.Watermark = cursor.Watermark
		rows, e := tx.QueryContext(ctx, `SELECT `+selectedRequestColumns+` FROM slides_requests WHERE workspace_id=? AND page_id=? AND request_seq>? AND request_seq<=? ORDER BY request_seq LIMIT ?`, key.WorkspaceID, key.PageID, cursor.After, cursor.Watermark, limit+1)
		if e != nil {
			return e
		}
		records := []SelectedAuthorityState{}
		for rows.Next() {
			record, e := scanSelectedRequest(rows)
			if e != nil {
				rows.Close()
				return e
			}
			records = append(records, record)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(records) > limit {
			result.HasMore = true
			records = records[:limit]
		}
		for _, record := range records {
			if e = validateSelectedRecord(ctx, tx, key, record); e != nil {
				return e
			}
			if _, e = selectedHistory(ctx, tx, key, record); e != nil {
				return e
			}
			result.Items = append(result.Items, record.Summary)
			cursor.After = record.Sequence
		}
		if result.HasMore {
			raw, _ := json.Marshal(cursor)
			result.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
		}
		return nil
	})
	return
}
