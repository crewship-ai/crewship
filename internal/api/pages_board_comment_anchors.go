package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

var errInvalidBoardAnchor = errors.New("invalid board anchor")

type boardCommentSnapshot struct {
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
	Angle       float64 `json:"angle"`
	ElementType string  `json:"element_type"`
}
type boardCommentAnchor struct {
	suppliedSnapshot bool
	SchemaVersion    int                   `json:"schema_version"`
	Type             string                `json:"type"`
	BoardSeq         *int64                `json:"board_seq"`
	ElementID        string                `json:"element_id,omitempty"`
	ElementRev       *int64                `json:"element_rev,omitempty"`
	Snapshot         *boardCommentSnapshot `json:"snapshot,omitempty"`
}

func (a *boardCommentAnchor) UnmarshalJSON(raw []byte) error {
	if len(raw) > 2048 {
		return errInvalidBoardAnchor
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return errInvalidBoardAnchor
	}
	seen := map[string]bool{}
	allowed := map[string]bool{"schema_version": true, "type": true, "board_seq": true, "element_id": true, "element_rev": true, "snapshot": true}
	for d.More() {
		t, e = d.Token()
		if e != nil {
			return errInvalidBoardAnchor
		}
		k, ok := t.(string)
		if !ok || !allowed[k] || seen[k] {
			return errInvalidBoardAnchor
		}
		seen[k] = true
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return errInvalidBoardAnchor
		}
	}
	if _, e = d.Token(); e != nil || d.Decode(new(any)) != io.EOF {
		return errInvalidBoardAnchor
	}
	type plain boardCommentAnchor
	var v plain
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&v) != nil {
		return errInvalidBoardAnchor
	}
	*a = boardCommentAnchor(v)
	a.suppliedSnapshot = seen["snapshot"]
	// Null/extraneous element fields cannot alias a whole-board anchor.
	if a.Type == "board" && (seen["element_id"] || seen["element_rev"] || seen["snapshot"]) {
		return errInvalidBoardAnchor
	}
	return nil
}
func validateBoardCommentAnchor(a *boardCommentAnchor) error {
	if a == nil || a.SchemaVersion != 1 || a.BoardSeq == nil || *a.BoardSeq < 0 || a.Snapshot != nil || a.suppliedSnapshot {
		return errInvalidBoardAnchor
	}
	if a.Type == "board" && a.ElementID == "" && a.ElementRev == nil {
		return nil
	}
	if a.Type != "board-element" || len(a.ElementID) < 1 || len(a.ElementID) > 128 || a.ElementRev == nil || *a.ElementRev < 1 {
		return errInvalidBoardAnchor
	}
	return nil
}
func (h *PageHandler) captureBoardCommentAnchor(w http.ResponseWriter, r *http.Request, tx *sql.Tx, pageID string, a *boardCommentAnchor) bool {
	var seq int64
	if err := tx.QueryRowContext(r.Context(), `SELECT seq FROM page_boards WHERE page_id=?`, pageID).Scan(&seq); err != nil {
		replyInternalError(w, h.logger, "read comment board", err)
		return false
	}
	if seq != *a.BoardSeq {
		artifactCommentError(w, 409, "stale_board", "board changed; reload before anchoring a comment")
		return false
	}
	if a.Type == "board" {
		return true
	}
	var rev int64
	var raw sql.NullString
	err := tx.QueryRowContext(r.Context(), `SELECT element_rev,element_json FROM page_board_elements WHERE board_page_id=? AND element_id=?`, pageID, a.ElementID).Scan(&rev, &raw)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !raw.Valid {
		artifactCommentError(w, 409, "stale_element", "original element was deleted; review the board before choosing a new anchor")
		return false
	}
	if err != nil {
		replyInternalError(w, h.logger, "read comment element", err)
		return false
	}
	if rev != *a.ElementRev {
		artifactCommentError(w, 409, "stale_element", "element changed; reload before anchoring a comment")
		return false
	}
	var element struct {
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
		Angle  float64 `json:"angle"`
		Type   string  `json:"type"`
	}
	if err = json.Unmarshal([]byte(raw.String), &element); err != nil {
		replyInternalError(w, h.logger, "read comment element geometry", err)
		return false
	}
	a.Snapshot = &boardCommentSnapshot{element.X, element.Y, element.Width, element.Height, element.Angle, element.Type}
	return true
}

func checkBoardCommentRequestJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return errors.New("comment request must be an object")
	}
	seen := map[string]bool{}
	allowed := map[string]bool{"anchor": true, "body": true, "base_version": true, "resolved": true, "mutation_id": true}
	for d.More() {
		t, err = d.Token()
		if err != nil {
			return err
		}
		key, ok := t.(string)
		if !ok || !allowed[key] || seen[key] {
			return errors.New("unknown or duplicate comment field")
		}
		seen[key] = true
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return err
		}
	}
	if _, err = d.Token(); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("one comment object required")
	}
	return nil
}
