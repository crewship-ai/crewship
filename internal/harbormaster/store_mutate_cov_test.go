package harbormaster

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	_ "modernc.org/sqlite"
)

// --- Enqueue validation + failure paths ---

func TestEnqueue_Validation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	cases := []struct {
		name    string
		mutate  func(r *Request)
		wantSub string
	}{
		{"missing workspace", func(r *Request) { r.WorkspaceID = "" }, "workspace_id required"},
		{"missing requested_by", func(r *Request) { r.RequestedBy = "" }, "requested_by required"},
		{"missing kind", func(r *Request) { r.Kind = "" }, "kind required"},
		{"missing reason", func(r *Request) { r.Reason = "" }, "reason required"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			req := newReq()
			tc.mutate(&req)
			_, err := Enqueue(ctx, db, nil, req)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q should contain %q", err, tc.wantSub)
			}
		})
	}
}

func TestEnqueue_PayloadMarshalError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	req := newReq()
	req.Payload = map[string]any{"bad": make(chan int)} // unmarshalable
	_, err := Enqueue(context.Background(), db, nil, req)
	if err == nil {
		t.Fatal("expected marshal error")
	}
	if !strings.Contains(err.Error(), "marshal payload") {
		t.Errorf("error %q should wrap marshal payload", err)
	}
}

func TestEnqueue_InsertErrorOnClosedDB(t *testing.T) {
	db := openTestDB(t)
	db.Close()
	_, err := Enqueue(context.Background(), db, nil, newReq())
	if err == nil {
		t.Fatal("expected insert error")
	}
	if !strings.Contains(err.Error(), "insert approval") {
		t.Errorf("error %q should wrap insert approval", err)
	}
}

// Enqueue must force pending even when the caller pre-sets a terminal status.
func TestEnqueue_ForcesPendingStatus(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()
	req := newReq()
	req.Status = StatusApproved // caller tries to smuggle a resolved row
	id, err := Enqueue(ctx, db, nil, req)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	row, err := Get(ctx, db, "ws_test", id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != StatusPending {
		t.Errorf("status = %q, want pending", row.Status)
	}
}

// --- Decide ---

func TestDecide_InputValidation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := Decide(ctx, db, nil, "ws_test", "some-id", StatusPending, "alice", ""); !errors.Is(err, ErrBadStatus) {
		t.Errorf("pending status: err = %v, want ErrBadStatus", err)
	}
	if err := Decide(ctx, db, nil, "ws_test", "", StatusApproved, "alice", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty id: err = %v, want ErrNotFound", err)
	}
	if err := Decide(ctx, db, nil, "", "some-id", StatusApproved, "alice", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty workspace: err = %v, want ErrNotFound", err)
	}
}

func TestDecide_UpdateErrorOnClosedDB(t *testing.T) {
	db := openTestDB(t)
	db.Close()
	err := Decide(context.Background(), db, nil, "ws_test", "some-id", StatusApproved, "alice", "")
	if err == nil {
		t.Fatal("expected exec error")
	}
	if !strings.Contains(err.Error(), "update decision") {
		t.Errorf("error %q should wrap update decision", err)
	}
}

// Denying emits approval.denied and records a denied reward outcome.
func TestDecide_DeniedEmitsAndRecordsReward(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		CREATE TABLE gate_reward_history (
			id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL,
			tool_name TEXT NOT NULL, args_hash TEXT NOT NULL,
			outcome TEXT NOT NULL, decided_by TEXT,
			decided_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			request_id TEXT);`); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	rec := &recorderEmitter{}
	id, err := Enqueue(ctx, db, rec, newReq()) // payload carries tool=deploy_prod
	if err != nil {
		t.Fatal(err)
	}

	if err := Decide(ctx, db, rec, "ws_test", id, StatusDenied, "bob", "too risky"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if !rec.hasType(journal.EntryApprovalDenied) {
		t.Errorf("expected approval.denied entry, got %v", rec.typesEmitted())
	}

	var outcome string
	if err := db.QueryRow(`SELECT outcome FROM gate_reward_history WHERE request_id = ?`, id).Scan(&outcome); err != nil {
		t.Fatalf("reward row: %v", err)
	}
	if outcome != string(OutcomeDenied) {
		t.Errorf("reward outcome = %q, want denied", outcome)
	}
}

// A payload without a tool key skips reward recording but the decision lands.
func TestDecide_MissingToolSkipsReward(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	req := newReq()
	req.Payload = map[string]any{"note": "no tool here"}
	id, err := Enqueue(ctx, db, nil, req)
	if err != nil {
		t.Fatal(err)
	}
	// No gate_reward_history table exists in this DB — if the reward path
	// ran it would warn; the decision itself must still succeed.
	if err := Decide(ctx, db, nil, "ws_test", id, StatusApproved, "alice", ""); err != nil {
		t.Fatalf("decide: %v", err)
	}
	row, err := Get(ctx, db, "ws_test", id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != StatusApproved {
		t.Errorf("status = %q, want approved", row.Status)
	}
}

// --- extractToolArgs ---

func TestExtractToolArgs(t *testing.T) {
	if tool, args := extractToolArgs(nil); tool != "" || args != nil {
		t.Errorf("nil payload: got %q %v", tool, args)
	}
	tool, args := extractToolArgs(map[string]any{
		"tool": "deploy_prod",
		"args": map[string]any{"target": "prod"},
	})
	if tool != "deploy_prod" {
		t.Errorf("tool = %q", tool)
	}
	if args["target"] != "prod" {
		t.Errorf("args = %v", args)
	}
	// Wrong-typed fields degrade to zero values, not panics.
	if tool, args := extractToolArgs(map[string]any{"tool": 42, "args": "nope"}); tool != "" || args != nil {
		t.Errorf("mistyped payload: got %q %v", tool, args)
	}
}

// --- Cancel ---

func TestCancel_InputValidation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if err := Cancel(ctx, db, nil, "ws_test", "", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty id: err = %v, want ErrNotFound", err)
	}
	if err := Cancel(ctx, db, nil, "", "some-id", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty workspace: err = %v, want ErrNotFound", err)
	}
}

func TestCancel_UnknownIDIsNotFound(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if err := Cancel(context.Background(), db, nil, "ws_test", "ghost", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestCancel_AlreadyDecidedIsNotPending(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()
	id, err := Enqueue(ctx, db, nil, newReq())
	if err != nil {
		t.Fatal(err)
	}
	if err := Decide(ctx, db, nil, "ws_test", id, StatusApproved, "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := Cancel(ctx, db, nil, "ws_test", id, "too late"); !errors.Is(err, ErrNotPending) {
		t.Errorf("err = %v, want ErrNotPending", err)
	}
}

func TestCancel_ExecErrorOnClosedDB(t *testing.T) {
	db := openTestDB(t)
	db.Close()
	err := Cancel(context.Background(), db, nil, "ws_test", "some-id", "x")
	if err == nil {
		t.Fatal("expected exec error")
	}
	if !strings.Contains(err.Error(), "cancel") {
		t.Errorf("error %q should wrap cancel", err)
	}
}
