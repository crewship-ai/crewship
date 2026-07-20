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

// --- Signup: losing the insert race for an existing address ---

// A concurrent signup for the same address can land between the
// existence probe and the INSERT. users.email is UNIQUE, so the INSERT
// fails — and that failure only ever happens for an address that
// exists, so it must answer with the same generic 202 as everything
// else rather than a distinguishing 500.
func TestCov2AuthSignup_UniqueRaceAnswersGeneric(t *testing.T) {
	t.Parallel()
	h, db, _ := covAuthHandler(t, true)
	// Stand in for the racing winner: the row appears after our probe
	// would have run. A BEFORE INSERT trigger that raises the driver's
	// UNIQUE message reproduces the loser's exact error.
	if _, err := db.Exec(`CREATE TRIGGER cov2_su_race BEFORE INSERT ON users
		BEGIN SELECT RAISE(ABORT, 'UNIQUE constraint failed: users.email'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	rec := httptest.NewRecorder()
	h.Signup(rec, cov2SignupReq(cov2ValidSignupBody))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (race loser must not 500), body=%s", rec.Code, rec.Body.String())
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
