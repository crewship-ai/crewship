package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// #1910: the waitpoint's identity is public, the callback capability is a
// separate secret that exists only as a digest at rest.

func createTestApproval(t *testing.T, store *SQLWaitpointStore, ws, run, step, form string) string {
	t.Helper()
	req := WaitpointApprovalRequest{WorkspaceID: ws, PipelineRunID: run, StepID: step, Prompt: "Ship?", TimeoutSec: 3600}
	if form != "" {
		req.DecisionForm = &DecisionForm{Actions: []DecisionAction{{ID: "go", Label: "Go", Approved: true}}}
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

// A pre-#1910 database: the token column holds the secret, and copies of it
// sit in the inbox, the notification outbox and the audit log.
func TestWaitpointCallback_LegacyRowsMigrateInPlace(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	ctx := context.Background()
	for _, ddl := range []string{
		`CREATE TABLE pipeline_waitpoints (
    token TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, pipeline_run_id TEXT NOT NULL,
    step_id TEXT NOT NULL, kind TEXT NOT NULL, prompt TEXT, invoking_crew_id TEXT,
    status TEXT NOT NULL DEFAULT 'pending', decision_payload TEXT, decided_by_user_id TEXT,
    timeout_at TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    decided_at TEXT, decision_form_json TEXT NOT NULL DEFAULT '', routine_version INTEGER)`,
		`CREATE TABLE inbox_items (id TEXT PRIMARY KEY, workspace_id TEXT, kind TEXT, source_id TEXT)`,
		`CREATE TABLE notification_deliveries (id TEXT PRIMARY KEY, workspace_id TEXT, dedup_key TEXT, source_kind TEXT, source_id TEXT)`,
		`CREATE TABLE audit_logs (id TEXT PRIMARY KEY, workspace_id TEXT, entity_type TEXT, entity_id TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	const legacy = "0123456789abcdef0123456789abcdef"
	const decidedLegacy = "fedcba9876543210fedcba9876543210"
	for _, q := range []string{
		`INSERT INTO pipeline_waitpoints (token, workspace_id, pipeline_run_id, step_id, kind, timeout_at) VALUES ('` + legacy + `','ws_a','run_1','gate','approval','2999-01-01T00:00:00Z')`,
		`INSERT INTO pipeline_waitpoints (token, workspace_id, pipeline_run_id, step_id, kind, status, timeout_at) VALUES ('` + decidedLegacy + `','ws_a','run_2','gate','approval','approved','2999-01-01T00:00:00Z')`,
		`INSERT INTO inbox_items VALUES ('in_1','ws_a','waitpoint','` + legacy + `')`,
		`INSERT INTO inbox_items VALUES ('in_2','ws_a','escalation','` + legacy + `')`, // not a waitpoint ref
		`INSERT INTO notification_deliveries VALUES ('nd_1','ws_a','approval:` + legacy + `','waitpoint','` + legacy + `')`,
		`INSERT INTO audit_logs VALUES ('al_1','ws_a','waitpoint','` + legacy + `')`,
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
	for _, secret := range []string{legacy, decidedLegacy} {
		assertNoPlaintext(t, db, secret, "inbox_items:in_2")
	}
	// In place: the external holder of the old callback URL keeps working.
	gotID, gotWS, err := store.ResolveCallbackToken(ctx, legacy)
	if err != nil || gotID != id || gotWS != "ws_a" {
		t.Fatalf("legacy secret resolved to (%q,%q,%v); want (%q, ws_a)", gotID, gotWS, err, id)
	}
	// Every copy follows the new id.
	var inbox, nd, dedup, audit string
	_ = db.QueryRow(`SELECT source_id FROM inbox_items WHERE id='in_1'`).Scan(&inbox)
	_ = db.QueryRow(`SELECT source_id, dedup_key FROM notification_deliveries WHERE id='nd_1'`).Scan(&nd, &dedup)
	_ = db.QueryRow(`SELECT entity_id FROM audit_logs WHERE id='al_1'`).Scan(&audit)
	if inbox != id || nd != id || dedup != "approval:"+id || audit != id {
		t.Fatalf("refs not rewritten: inbox=%q delivery=%q dedup=%q audit=%q want %q", inbox, nd, dedup, audit, id)
	}
	// The authed route still accepts a token a script captured pre-upgrade.
	if got := store.ResolveWaitpointRef(ctx, "ws_a", legacy); got != id {
		t.Fatalf("ResolveWaitpointRef(legacy) = %q, want %q", got, id)
	}
	if got := store.ResolveWaitpointRef(ctx, "ws_b", legacy); got == id {
		t.Fatalf("ResolveWaitpointRef resolved across workspaces")
	}
	if err := store.CompleteApproval(ctx, "ws_a", id, true, "user_1", ""); err != nil {
		t.Fatalf("migrated waitpoint cannot be decided by id: %v", err)
	}

	// Idempotent: a second construction finds nothing to do.
	again := NewSQLWaitpointStore(db)
	again.Close()
	var after string
	_ = db.QueryRow(`SELECT token FROM pipeline_waitpoints WHERE pipeline_run_id='run_1'`).Scan(&after)
	if after != id {
		t.Fatalf("second construction re-migrated the row: %q -> %q", id, after)
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
