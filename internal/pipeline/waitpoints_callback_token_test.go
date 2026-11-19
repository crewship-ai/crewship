package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// #1910: the waitpoint's identity is public, the callback capability is a
// separate secret that exists only as a digest at rest.

func createTestApproval(t *testing.T, store *SQLWaitpointStore, ws, run, step, form string) string {
	t.Helper()
	req := WaitpointApprovalRequest{WorkspaceID: ws, PipelineRunID: run, StepID: step, Prompt: "Ship?", TimeoutSec: 3600}
	if form != "" {
		req.DecisionForm = &DecisionForm{Fields: []InputSpec{}, Actions: []DecisionAction{{ID: "go", Label: "Go", Approved: true}}}
	}
	id, err := store.CreateApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}
	return id
}

func callbackDigest(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var d sql.NullString
	if err := db.QueryRow(`SELECT callback_token_hash FROM pipeline_waitpoints WHERE token = ?`, id).Scan(&d); err != nil {
		t.Fatalf("read digest: %v", err)
	}
	return d.String
}

func TestWaitpointCallback_IDIsNotACapability(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()
	ctx := context.Background()
	id := createTestApproval(t, store, "ws_a", "run_1", "gate", "")

	if !strings.HasPrefix(id, WaitpointIDPrefix) {
		t.Fatalf("CreateApproval returned %q, want a public %q id", id, WaitpointIDPrefix)
	}
	if d := callbackDigest(t, store.db, id); d != "" {
		t.Fatalf("a fresh waitpoint already has a callback digest %q; none may exist until one is minted", d)
	}
	// The id is what every member surface shows. It must not open the
	// public callback, and neither may anything derived from the row.
	for name, presented := range map[string]string{"public id": id, "digest of id": HashCapabilityToken(id), "empty": ""} {
		gotID, gotWS, err := store.ResolveCallbackToken(ctx, presented)
		if err != nil || gotID != "" || gotWS != "" {
			t.Fatalf("%s resolved to (%q,%q,%v); want nothing", name, gotID, gotWS, err)
		}
	}
}

func TestWaitpointCallback_MintStoresOnlyDigestAndRotates(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()
	ctx := context.Background()
	id := createTestApproval(t, store, "ws_a", "run_1", "gate", "")

	secret, rotated, err := store.MintCallbackToken(ctx, "ws_a", id)
	if err != nil || rotated {
		t.Fatalf("first mint: secret=%t rotated=%t err=%v", secret != "", rotated, err)
	}
	if secret == id || !strings.HasPrefix(secret, waitpointCallbackSecretPrefix) || len(secret) != len(waitpointCallbackSecretPrefix)+64 {
		t.Fatalf("minted secret has the wrong shape (len %d)", len(secret))
	}
	assertNoPlaintext(t, store.db, secret)
	if got := callbackDigest(t, store.db, id); got != HashCapabilityToken(secret) {
		t.Fatalf("stored digest does not match the minted secret")
	}

	gotID, gotWS, err := store.ResolveCallbackToken(ctx, secret)
	if err != nil || gotID != id || gotWS != "ws_a" {
		t.Fatalf("minted secret resolved to (%q,%q,%v); want (%q, ws_a)", gotID, gotWS, err, id)
	}
	// Replaying the stored digest is not presenting the secret.
	if gotID, _, _ := store.ResolveCallbackToken(ctx, callbackDigest(t, store.db, id)); gotID != "" {
		t.Fatalf("the stored digest resolved a waitpoint")
	}
	if gotID, _, _ := store.ResolveCallbackToken(ctx, secret+"x"); gotID != "" {
		t.Fatalf("a wrong secret resolved a waitpoint")
	}

	second, rotated, err := store.MintCallbackToken(ctx, "ws_a", id)
	if err != nil || !rotated || second == secret {
		t.Fatalf("re-mint: rotated=%t same=%t err=%v", rotated, second == secret, err)
	}
	if gotID, _, _ := store.ResolveCallbackToken(ctx, secret); gotID != "" {
		t.Fatalf("the rotated-out secret still resolves")
	}
	if gotID, _, _ := store.ResolveCallbackToken(ctx, second); gotID != id {
		t.Fatalf("the new secret does not resolve")
	}
}

func TestWaitpointCallback_MintRefusals(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()
	ctx := context.Background()
	plain := createTestApproval(t, store, "ws_a", "run_1", "gate", "")
	typed := createTestApproval(t, store, "ws_a", "run_2", "typed", "form")
	held := createTestApproval(t, store, "ws_a", "run_3", CrewshipApprovalStepKey("act", "binding"), "")
	decided := createTestApproval(t, store, "ws_a", "run_4", "gate", "")
	if err := store.CompleteApproval(ctx, "ws_a", decided, true, "user_1", ""); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, ws, id string
		want         error
	}{
		{"other workspace", "ws_b", plain, ErrWaitpointNotFound},
		{"unknown", "ws_a", "wp_missing", ErrWaitpointNotFound},
		{"typed decision", "ws_a", typed, ErrCallbackNotOffered},
		{"held action", "ws_a", held, ErrCallbackNotOffered},
		{"decided", "ws_a", decided, ErrAlreadyDecided},
	} {
		if _, _, err := store.MintCallbackToken(ctx, tc.ws, tc.id); !errors.Is(err, tc.want) {
			t.Errorf("%s: err=%v, want %v", tc.name, err, tc.want)
		}
	}
}

// A pre-#1910 database on the real migrated schema: the token column holds
// the secret, and the production writers have copied it into the inbox (row
// id included), a read marker, the notification outbox and the audit log.
func TestWaitpointCallback_LegacyRowsMigrate_NoTraceLeft(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	ctx := context.Background()
	const legacy = "0123456789abcdef0123456789abcdef"
	const decidedLegacy = "fedcba9876543210fedcba9876543210"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('ws_a','A','a')`,
		`INSERT INTO users(id,email,full_name) VALUES('u_1','u1@example.com','U')`,
		`INSERT INTO pipeline_waitpoints (token, workspace_id, pipeline_run_id, step_id, kind, timeout_at) VALUES ('` + legacy + `','ws_a','run_1','gate','approval','2999-01-01T00:00:00Z')`,
		`INSERT INTO pipeline_waitpoints (token, workspace_id, pipeline_run_id, step_id, kind, status, timeout_at) VALUES ('` + decidedLegacy + `','ws_a','run_2','gate','approval','approved','2999-01-01T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// The inbox card through the production writer, so its id has the real
	// "ibx_<ws>_waitpoint_<source_id>" shape.
	if err := inbox.Insert(ctx, db, slog.Default(), inbox.Item{WorkspaceID: "ws_a", Kind: inbox.KindWaitpoint, SourceID: legacy, Title: "Ship?", Blocking: true}); err != nil {
		t.Fatal(err)
	}
	var inboxID string
	if err := db.QueryRow(`SELECT id FROM inbox_items WHERE source_id = ?`, legacy).Scan(&inboxID); err != nil || !strings.Contains(inboxID, legacy) {
		t.Fatalf("fixture: writer-built inbox id %q (%v) does not embed the token", inboxID, err)
	}
	for _, q := range []string{
		`INSERT INTO inbox_item_reads(inbox_item_id,user_id,read_at) VALUES('` + inboxID + `','u_1','2026-11-19T00:00:00Z')`,
		`INSERT INTO notification_deliveries(id,workspace_id,channel_id,category,dedup_key,source_kind,source_id) VALUES('nd_1','ws_a','ch_1','approval','approval:` + legacy + `','waitpoint','` + legacy + `')`,
		`INSERT INTO audit_logs(id,workspace_id,action,entity_type,entity_id) VALUES('al_1','ws_a','waitpoint.decision_refused','waitpoint','` + legacy + `')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	store := NewSQLWaitpointStore(db)
	defer store.Close()

	var id string
	if err := db.QueryRow(`SELECT token FROM pipeline_waitpoints WHERE pipeline_run_id='run_1'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, WaitpointIDPrefix) {
		t.Fatalf("legacy row kept token %q", id)
	}
	// No trace of either old value anywhere in the database.
	assertNoPlaintext(t, db, legacy)
	assertNoPlaintext(t, db, decidedLegacy)
	// Old secrets are not carried forward: nothing opens the public callback
	// until a MANAGER mints a new URL.
	if d := callbackDigest(t, db, id); d != "" {
		t.Fatalf("legacy row kept a live callback digest")
	}
	if gotID, _, _ := store.ResolveCallbackToken(ctx, legacy); gotID != "" {
		t.Fatalf("the pre-upgrade secret still opens the callback")
	}
	// Every copy follows the new id, and the inbox card is still the card.
	var source, newInboxID, readRef, nd, dedup, audit string
	_ = db.QueryRow(`SELECT id, source_id FROM inbox_items WHERE kind='waitpoint'`).Scan(&newInboxID, &source)
	_ = db.QueryRow(`SELECT inbox_item_id FROM inbox_item_reads`).Scan(&readRef)
	_ = db.QueryRow(`SELECT source_id, dedup_key FROM notification_deliveries WHERE id='nd_1'`).Scan(&nd, &dedup)
	_ = db.QueryRow(`SELECT entity_id FROM audit_logs WHERE id='al_1'`).Scan(&audit)
	if want := strings.Replace(inboxID, legacy, id, 1); newInboxID != want || source != id || readRef != want {
		t.Fatalf("inbox not rewritten: id=%q source=%q read=%q want id %q source %q", newInboxID, source, readRef, want, id)
	}
	if nd != id || dedup != "approval:"+id || audit != id {
		t.Fatalf("refs not rewritten: delivery=%q dedup=%q audit=%q want %q", nd, dedup, audit, id)
	}
	var fkProblems int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&fkProblems); err != nil || fkProblems != 0 {
		t.Fatalf("foreign key check after migration: %d (%v)", fkProblems, err)
	}
	// The authed path keys on the new id, and a new URL can be minted.
	if _, _, err := store.MintCallbackToken(ctx, "ws_a", id); err != nil {
		t.Fatalf("cannot mint for a migrated waitpoint: %v", err)
	}
	if err := store.CompleteApproval(ctx, "ws_a", id, true, "u_1", ""); err != nil {
		t.Fatalf("migrated waitpoint cannot be decided by id: %v", err)
	}

	// Idempotent, and a legacy row restored into a running server is picked
	// up by the next sweep without a restart.
	store.sweepOnce()
	store.migrateLegacyTokens()
	var after string
	_ = db.QueryRow(`SELECT token FROM pipeline_waitpoints WHERE pipeline_run_id='run_1'`).Scan(&after)
	if after != id {
		t.Fatalf("second pass re-migrated the row: %q -> %q", id, after)
	}
	const restored = "aaaabbbbccccddddaaaabbbbccccdddd"
	if _, err := db.Exec(`INSERT INTO pipeline_waitpoints (token, workspace_id, pipeline_run_id, step_id, kind, timeout_at) VALUES ('` + restored + `','ws_a','run_3','gate','approval','2999-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	ch := make(chan waitDecision, 1)
	store.listeners[restored] = ch
	store.mu.Unlock()
	store.migrateLegacyTokens()
	assertNoPlaintext(t, db, restored)
	var restoredID string
	_ = db.QueryRow(`SELECT token FROM pipeline_waitpoints WHERE pipeline_run_id='run_3'`).Scan(&restoredID)
	// A waiter parked on the old value is woken by a decision on the new id.
	if err := store.CompleteApproval(ctx, "ws_a", restoredID, true, "u_1", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-ch:
		if !d.approved {
			t.Fatal("listener got a denial")
		}
	default:
		t.Fatal("the listener registered under the old value was not moved to the new id")
	}
}

// assertNoPlaintext fails if secret appears in any text column of any table,
// except at the "table:id" locations listed in allow (rows that never
// referenced the waitpoint and must not be touched).
func assertNoPlaintext(t *testing.T, db *sql.DB, secret string, allow ...string) {
	t.Helper()
	tables, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		_ = tables.Scan(&n)
		names = append(names, n)
	}
	tables.Close()
	allowed := map[string]bool{}
	for _, a := range allow {
		allowed[a] = true
	}
	for _, table := range names {
		rows, err := db.Query(`SELECT * FROM "` + table + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = rows.Scan(ptrs...)
			first := ""
			if len(vals) > 0 {
				first = toText(vals[0])
			}
			for i, v := range vals {
				if strings.Contains(toText(v), secret) && !allowed[table+":"+first] {
					rows.Close()
					t.Fatalf("plaintext secret found in %s.%s", table, cols[i])
				}
			}
		}
		rows.Close()
	}
}

func toText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return ""
}
