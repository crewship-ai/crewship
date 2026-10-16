package slides

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func selectedAdmissionFixture(t *testing.T) (*Store, Key, SelectedRequestInput, SelectedAdmission) {
	t.Helper()
	store, db, key, _, _ := storeFixture(t, Options{})
	state, request := selectedFixture(t)
	state.Deck.DeckID = key.PageID
	request.PageID = key.PageID
	request.BaseRev = 1
	request.ChatID = "selected_chat"
	initializeStore(t, store, key, state.Deck)
	for _, query := range []string{
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('selected_crew','slides-workspace','Selected','selected')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('selected_agent','slides-workspace','selected_crew','Selected agent','selected')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('selected_chat','slides-workspace','selected_agent','slides-owner','private')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	admission := SelectedAdmission{HumanUserID: "slides-owner", AgentID: "selected_agent", ChatID: "selected_chat", AudienceJSON: `{"trusted":"fixture"}`, CredentialKind: "session", CredentialID: "selected_session"}
	return store, key, request, admission
}
func TestSelectedRequestAdmissionReplayPaginationAndCancellation(t *testing.T) {
	store, key, input, admission := selectedAdmissionFixture(t)
	first, err := store.AdmitSelectedRequest(t.Context(), key, "request_one", input, admission, storeAuthority)
	if err != nil || first.ID != "request_one" || first.Version != 1 {
		t.Fatal(first, err)
	}
	retry, err := store.AdmitSelectedRequest(t.Context(), key, "unused_new_id", input, admission, storeAuthority)
	if err != nil || !retry.Duplicate || retry.ID != first.ID {
		t.Fatal(retry, err)
	}
	changed := input
	changed.Instruction = "Changed frozen instruction"
	if _, err = store.AdmitSelectedRequest(t.Context(), key, "changed", changed, admission, storeAuthority); !errors.Is(err, ErrConflict) {
		t.Fatal("changed submission accepted", err)
	}
	refreshed := admission
	refreshed.CredentialID = "different_session"
	if _, err = store.AdmitSelectedRequest(t.Context(), key, "different_credential", input, refreshed, storeAuthority); !errors.Is(err, ErrConflict) {
		t.Fatal("active admission credential refreshed", err)
	}
	second := input
	second.SubmissionID = "second"
	if _, err = store.AdmitSelectedRequest(t.Context(), key, "request_two", second, admission, storeAuthority); err != nil {
		t.Fatal(err)
	}
	third := input
	third.SubmissionID = "third"
	if _, err = store.AdmitSelectedRequest(t.Context(), key, "request_three", third, admission, storeAuthority); err != nil {
		t.Fatal(err)
	}
	fourth := input
	fourth.SubmissionID = "fourth"
	if _, err = store.AdmitSelectedRequest(t.Context(), key, "request_four", fourth, admission, storeAuthority); !errors.Is(err, ErrQuota) {
		t.Fatal("active request quota bypassed", err)
	}
	page, err := store.ListSelectedRequests(t.Context(), key, SelectedPageRequest{Limit: 1}, storeAuthority)
	if err != nil || !page.HasMore || page.Watermark != 3 || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	cancel := CancelSelectedRequestInput{PageID: key.PageID, SchemaVersion: 1, MutationID: "cancel_one", IfVersion: 1}
	ack, err := store.CancelSelectedRequest(t.Context(), key, first.ID, cancel, storeAuthority)
	if err != nil || ack.Version != 2 || ack.Status != "cancelled" {
		t.Fatal(ack, err)
	}
	ack, err = store.CancelSelectedRequest(t.Context(), key, first.ID, cancel, storeAuthority)
	if err != nil || !ack.Duplicate {
		t.Fatal(ack, err)
	}
	retry, err = store.AdmitSelectedRequest(t.Context(), key, "still_unused", input, admission, storeAuthority)
	if err != nil || !retry.Duplicate || retry.Status != "cancelled" {
		t.Fatal("terminal admission retry restarted work", retry, err)
	}
	if _, err = store.AdmitSelectedRequest(t.Context(), key, "request_four", fourth, admission, storeAuthority); err != nil {
		t.Fatal(err)
	}
	detail, err := store.ReadSelectedRequest(t.Context(), key, first.ID, storeAuthority)
	if err != nil || len(detail.History) != 2 || detail.History[1].Kind != "cancelled" || detail.Request.Selection != input.Selection {
		t.Fatal(detail, err)
	}
	next, err := store.ListSelectedRequests(t.Context(), key, SelectedPageRequest{Limit: 100, Cursor: page.NextCursor}, storeAuthority)
	if err != nil || next.HasMore || next.Watermark != 3 || len(next.Items) != 2 || next.Items[0].ID != "request_two" || next.Items[1].ID != "request_three" {
		t.Fatal("membership watermark widened", next, err)
	}
	deny := func(context.Context, *sql.Tx) (string, error) { return "", errors.New("Page grant revoked") }
	if _, err = store.ListSelectedRequests(t.Context(), key, SelectedPageRequest{Cursor: page.NextCursor}, deny); err == nil {
		t.Fatal("revoked page-two request succeeded")
	}
	if _, err = store.CancelSelectedRequest(t.Context(), key, first.ID, cancel, deny); err == nil {
		t.Fatal("revoked cancellation replay succeeded")
	}
	if state := currentState(t, store, key); state.Revision != 1 {
		t.Fatal("admission/cancellation mutated deck", state.Revision)
	}
}
func TestSelectedRequestRejectsCorruptedPersistedOriginalHashAndPageCascade(t *testing.T) {
	store, key, input, admission := selectedAdmissionFixture(t)
	if _, err := store.AdmitSelectedRequest(t.Context(), key, "request_one", input, admission, storeAuthority); err != nil {
		t.Fatal(err)
	}
	record, err := store.ReadSelectedRequest(t.Context(), key, "request_one", storeAuthority)
	if err != nil || record.Request.BaseRevision != 1 {
		t.Fatal(record, err)
	}
	var raw string
	if err = store.db.QueryRow(`SELECT verified_json FROM slides_requests WHERE id='request_one'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var verified VerifiedTextSelection
	if err = json.Unmarshal([]byte(raw), &verified); err != nil {
		t.Fatal(err)
	}
	verified.ContentHash = strings.Repeat("f", 64)
	corrupted, _ := canonicalValue(verified)
	// Simulate corrupt/imported bytes, not a runtime capability: production
	// immutable guards would reject this UPDATE before the reader is reached.
	if _, err = store.db.Exec(`DROP TRIGGER slides_request_original_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE slides_requests SET verified_json=?,base_content_hash=? WHERE id='request_one'`, string(corrupted), verified.ContentHash); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReadSelectedRequest(t.Context(), key, "request_one", storeAuthority); err == nil {
		t.Fatal("corrupt persisted verified hash admitted")
	}
	cancel := CancelSelectedRequestInput{PageID: key.PageID, SchemaVersion: 1, MutationID: "cancel_corrupt", IfVersion: 1}
	if _, err = store.CancelSelectedRequest(t.Context(), key, "request_one", cancel, storeAuthority); err == nil {
		t.Fatal("corrupt original request mutation committed")
	}
	if state := currentState(t, store, key); state.Revision != 1 {
		t.Fatal("corrupt request changed content")
	}
	var version int
	if err = store.db.QueryRow(`SELECT version FROM slides_requests WHERE id='request_one'`).Scan(&version); err != nil || version != 1 {
		t.Fatal("corrupt request changed history", version, err)
	}
	if _, err = store.db.Exec(`DELETE FROM pages WHERE id=?`, key.PageID); err != nil {
		t.Fatal("whole-Page cascade refused", err)
	}
	for _, table := range []string{"slides_heads", "slides_revisions", "slides_requests", "slides_request_events", "slides_request_receipts"} {
		var count int
		if err = store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("request graph survived Page deletion", table, count, err)
		}
	}
}
