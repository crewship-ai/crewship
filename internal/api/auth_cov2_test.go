package api

// auth.go coverage top-up #2. Targets the DB-failure branches of Signup
// and Bootstrap that the first cov file left behind. Failure injection
// uses SQLite triggers (RAISE(ABORT, ...)) so the exact statement we
// want to break fails while the rest of the schema stays intact — no
// production code changes, no driver mocking.
//
// All tests are prefixed TestCov2Auth so `go test -run TestCov2Auth`
// selects exactly this file.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// cov2AuthAbortTrigger installs a trigger that aborts the given
// operation ("INSERT"/"UPDATE"/"DELETE") on the given table.
func cov2AuthAbortTrigger(t *testing.T, db *sql.DB, name, op, table string) {
	t.Helper()
	stmt := `CREATE TRIGGER ` + name + ` BEFORE ` + op + ` ON ` + table + `
		BEGIN SELECT RAISE(ABORT, 'cov2 injected failure'); END`
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("create trigger %s: %v", name, err)
	}
}

func cov2SignupReq(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/api/v1/auth/signup", strings.NewReader(body))
}

const cov2ValidSignupBody = `{"full_name":"Cov Two","email":"cov2@example.com","password":"longenough1"}`

// --- Signup: bcrypt failure (password longer than 72 bytes) → 500 ---

func TestCov2AuthSignup_PasswordTooLongBcryptError(t *testing.T) {
	t.Parallel()
	h, _, _ := covAuthHandler(t, true)
	long := strings.Repeat("p", 80) // bcrypt errors at >72 bytes
	body := `{"full_name":"Cov Two","email":"cov2-long@example.com","password":"` + long + `"}`
	rec := httptest.NewRecorder()
	h.Signup(rec, cov2SignupReq(body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (bcrypt >72 bytes), body=%s", rec.Code, rec.Body.String())
	}
}

// --- Signup: each tx INSERT failing → 500 ---

func TestCov2AuthSignup_TxInsertFailures(t *testing.T) {
	cases := []struct {
		name  string
		table string
	}{
		{"users insert fails", "users"},
		{"workspaces insert fails", "workspaces"},
		{"workspace_members insert fails", "workspace_members"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, db, _ := covAuthHandler(t, true)
			cov2AuthAbortTrigger(t, db, "cov2_su_"+tc.table, "INSERT", tc.table)
			rec := httptest.NewRecorder()
			h.Signup(rec, cov2SignupReq(cov2ValidSignupBody))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
			}
			// The tx rolled back — no orphan user row.
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = 'cov2@example.com'`).Scan(&n); err != nil {
				t.Fatalf("count users: %v", err)
			}
			if n != 0 {
				t.Errorf("users rows = %d, want 0 after rollback", n)
			}
		})
	}
}

// --- Signup: session-create failure → orphan cleanup runs ---

// With a nil sessions store the commit succeeds but setSessionCookies
// fails, so Signup must delete the just-created user + workspace and
// answer 500.
func TestCov2AuthSignup_SessionFailureCleansUpOrphan(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	h := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), nil /* no sessions store */, true)
	rec := httptest.NewRecorder()
	h.Signup(rec, cov2SignupReq(cov2ValidSignupBody))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (session store missing), body=%s", rec.Code, rec.Body.String())
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = 'cov2@example.com'`).Scan(&n); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if n != 0 {
		t.Errorf("orphan user rows = %d, want 0 (cleanupOrphanedSignup must delete)", n)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspaces`).Scan(&n); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if n != 0 {
		t.Errorf("orphan workspace rows = %d, want 0", n)
	}
}

// Same path, but with DELETE triggers installed so both best-effort
// deletes inside cleanupOrphanedSignup fail — the handler still
// answers 500 and does not panic.
func TestCov2AuthSignup_CleanupDeleteFailuresAreLoggedNotFatal(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	h := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), nil, true)
	cov2AuthAbortTrigger(t, db, "cov2_cl_users", "DELETE", "users")
	cov2AuthAbortTrigger(t, db, "cov2_cl_ws", "DELETE", "workspaces")
	rec := httptest.NewRecorder()
	h.Signup(rec, cov2SignupReq(cov2ValidSignupBody))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
	// Cleanup was blocked by the triggers, so the orphan user remains —
	// proving the error branches ran rather than the deletes silently
	// succeeding.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = 'cov2@example.com'`).Scan(&n); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if n != 1 {
		t.Errorf("user rows = %d, want 1 (delete was trigger-blocked)", n)
	}
}

// --- setSessionCookies: sessions.Create DB failure (via Signup) ---

func TestCov2AuthSignup_SessionsTableGone500(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	h := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), sessions.NewDBStore(db), true)
	if _, err := db.Exec(`DROP TABLE user_sessions`); err != nil {
		t.Fatalf("drop user_sessions: %v", err)
	}
	rec := httptest.NewRecorder()
	h.Signup(rec, cov2SignupReq(cov2ValidSignupBody))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (sessions.Create failed), body=%s", rec.Code, rec.Body.String())
	}
}

// --- Bootstrap: bcrypt failure → 500 ---

func TestCov2AuthBootstrap_PasswordTooLongBcryptError(t *testing.T) {
	t.Parallel()
	h, _, _ := covAuthHandler(t, false)
	long := strings.Repeat("p", 80)
	body := `{"full_name":"Boot Strap","email":"boot@example.com","password":"` + long + `"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/bootstrap", strings.NewReader(body))
	h.Bootstrap(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (bcrypt >72 bytes), body=%s", rec.Code, rec.Body.String())
	}
}

// --- Bootstrap: each tx INSERT failing → 500, nothing persisted ---

func TestCov2AuthBootstrap_TxInsertFailures(t *testing.T) {
	cases := []struct {
		name  string
		table string
	}{
		{"users insert fails", "users"},
		{"workspaces insert fails", "workspaces"},
		{"workspace_members insert fails", "workspace_members"},
		{"cli_tokens insert fails", "cli_tokens"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, db, _ := covAuthHandler(t, false)
			cov2AuthAbortTrigger(t, db, "cov2_bs_"+tc.table, "INSERT", tc.table)
			body := `{"full_name":"Boot Strap","email":"boot@example.com","password":"longenough1"}`
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/bootstrap", strings.NewReader(body))
			h.Bootstrap(rec, req)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
				t.Fatalf("count users: %v", err)
			}
			if n != 0 {
				t.Errorf("users rows = %d, want 0 after rollback", n)
			}
		})
	}
}
