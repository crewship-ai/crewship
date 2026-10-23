package api

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
)

// These tests drive the real router and auth middleware: web access tokens
// issued for a recorded sign-in session, and the CLI token the fixture mints.
// They pin that an editor session never outlives the sign-in or token behind
// it, whatever revokes it.

func signInRouterFixture(t *testing.T) (*instanceFixture, string) {
	t.Helper()
	f := gridcraftInstanceFixture(t)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true, Editor: true}, gridcraftCellsEngine{})
	setGridCraftEditorFlag(t, f, "ws-old", true)
	return f, liveWorkbook(t, f, f.boss, "boss", "signin-book")
}

// signIn records a web sign-in of boss and returns its id and an access token.
func (f *instanceFixture) signIn(t *testing.T) (string, string) {
	t.Helper()
	s, err := f.r.sessionsStore.Create(t.Context(), "boss", "editor-test", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return s.ID, f.accessToken(t, s.ID)
}

func (f *instanceFixture) accessToken(t *testing.T, sid string) string {
	t.Helper()
	tok, err := f.r.authMw.validator.IssueAccessToken("boss", sid, "Boss", "boss@ex.com")
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func signInHead(t *testing.T, db *sql.DB) int {
	t.Helper()
	var rev int
	if err := db.QueryRow(`SELECT head_rev FROM gridcraft_heads WHERE page_id='signin-book'`).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	return rev
}

func heartbeat(session string) string { return fmt.Sprintf(`{"session_id":%q}`, session) }

// Logout, a password change (every sign-in of the user), and removal from the
// workspace each end editing through the middleware, before any session
// check; a refreshed access token of the same sign-in keeps the session, and
// another sign-in or the CLI token of the same user must reopen.
func TestGridCraftEditorSessionFollowsSignInThroughRouter(t *testing.T) {
	f, base := signInRouterFixture(t)
	q := "?workspace_id=ws-old"
	sidA, tokA := f.signIn(t)
	s := openEditorSession(t, f, tokA, base)
	wantCode(t, f.do(tokA, "POST", base+"/edits"+q, cellEdit(s.SessionID, 1, 1, "B2", "a1")), 200, "edit, sign-in A")
	refreshed := f.accessToken(t, sidA)
	if refreshed == tokA {
		t.Fatal("refreshed access token equals the first one")
	}
	wantCode(t, f.do(refreshed, "POST", base+"/edits"+q, cellEdit(s.SessionID, 2, 2, "B2", "a2")), 200, "edit, refreshed access token of A")

	_, tokB := f.signIn(t)
	wantError(t, f.do(tokB, "POST", base+"/edits"+q, cellEdit(s.SessionID, 3, 3, "B2", "b")), 409, "editor_session_expired", "edit of A's session from sign-in B")
	wantError(t, f.do(tokB, "POST", base+"/sessions"+q, heartbeat(s.SessionID)), 409, "editor_session_expired", "heartbeat of A's session from sign-in B")
	wantError(t, f.do(f.boss, "POST", base+"/edits"+q, cellEdit(s.SessionID, 3, 3, "B2", "cli")), 409, "editor_session_expired", "edit of A's session with the CLI token")

	head := signInHead(t, f.db)
	if err := f.r.sessionsStore.Revoke(t.Context(), sidA, "logout"); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(refreshed, "POST", base+"/edits"+q, cellEdit(s.SessionID, 3, head, "B2", "after logout")), 401, "session_revoked", "edit after logout")
	wantError(t, f.do(refreshed, "POST", base+"/sessions"+q, heartbeat(s.SessionID)), 401, "session_revoked", "heartbeat after logout")
	wantError(t, f.do(refreshed, "GET", base+"/commits"+q+"&since=0", ""), 401, "session_revoked", "commits after logout")
	wantError(t, f.do(refreshed, "POST", base+"/sessions"+q, ""), 401, "session_revoked", "open after logout")

	sB := openEditorSession(t, f, tokB, base)
	if _, err := f.r.sessionsStore.RevokeAllForUser(t.Context(), "boss", "password_change"); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(tokB, "POST", base+"/edits"+q, cellEdit(sB.SessionID, 1, head, "B2", "after password change")), 401, "session_revoked", "edit after a password change")
	wantError(t, f.do(tokB, "POST", base+"/sessions"+q, heartbeat(sB.SessionID)), 401, "session_revoked", "heartbeat after a password change")
	if got := signInHead(t, f.db); got != head {
		t.Fatalf("head moved to %d after revocation, want %d", got, head)
	}

	_, tokC := f.signIn(t)
	sC := openEditorSession(t, f, tokC, base)
	wantCode(t, f.do(tokC, "POST", base+"/edits"+q, cellEdit(sC.SessionID, 1, head, "B2", "c1")), 200, "edit after signing in again")
	head = signInHead(t, f.db)
	mustExec(t, f.db, `DELETE FROM workspace_members WHERE workspace_id='ws-old' AND user_id='boss'`)
	for name, w := range map[string]func() int{
		"edit after removal": func() int {
			return f.do(tokC, "POST", base+"/edits"+q, cellEdit(sC.SessionID, 2, head, "B2", "removed")).Code
		},
		"heartbeat after removal": func() int { return f.do(tokC, "POST", base+"/sessions"+q, heartbeat(sC.SessionID)).Code },
		"commits after removal":   func() int { return f.do(tokC, "GET", base+"/commits"+q+"&since=0", "").Code },
		"CLI edit after removal": func() int {
			return f.do(f.boss, "POST", base+"/edits"+q, cellEdit(sC.SessionID, 2, head, "B2", "cli")).Code
		},
	} {
		if code := w(); code != 403 {
			t.Errorf("%s: %d, want 403", name, code)
		}
	}
	if got := signInHead(t, f.db); got != head {
		t.Fatalf("head moved to %d after removal, want %d", got, head)
	}
}

// A CLI-token session ends with its token: an expired or revoked token is
// refused by the middleware and changes nothing.
func TestGridCraftCLISessionFollowsTokenThroughRouter(t *testing.T) {
	for _, tc := range []struct {
		name, end string
	}{
		{"expired", `UPDATE cli_tokens SET expires_at=strftime('%Y-%m-%dT%H:%M:%SZ','now','-1 minute') WHERE id='clt-boss'`},
		{"revoked", `UPDATE cli_tokens SET revoked_at=datetime('now') WHERE id='clt-boss'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, base := signInRouterFixture(t)
			q := "?workspace_id=ws-old"
			s := openEditorSession(t, f, f.boss, base)
			wantCode(t, f.do(f.boss, "POST", base+"/edits"+q, cellEdit(s.SessionID, 1, 1, "B2", "x")), 200, "edit")
			mustExec(t, f.db, tc.end)
			wantError(t, f.do(f.boss, "POST", base+"/edits"+q, cellEdit(s.SessionID, 2, 2, "B2", "y")), 401, "session_invalid", "edit")
			wantError(t, f.do(f.boss, "POST", base+"/sessions"+q, heartbeat(s.SessionID)), 401, "session_invalid", "heartbeat")
			if got := signInHead(t, f.db); got != 2 {
				t.Fatalf("head %d, want 2", got)
			}
		})
	}
}

// Another user, even a workspace OWNER who may read the workbook, cannot edit
// with, renew or close the owner's session: it does not exist for them.
func TestGridCraftSessionOfAnotherUserIsNotFoundThroughRouter(t *testing.T) {
	f, base := signInRouterFixture(t)
	q := "?workspace_id=ws-old"
	s := openEditorSession(t, f, f.boss, base)
	other := seedRoleMemberToken(t, f.db, "ws-old", "mallory", "OWNER", "signinmallory000000000000")
	wantCode(t, f.do(other, "POST", base+"/edits"+q, cellEdit(s.SessionID, 1, 1, "B2", "m")), 404, "edit with another user's session")
	wantCode(t, f.do(other, "POST", base+"/sessions"+q, heartbeat(s.SessionID)), 404, "heartbeat of another user's session")
	wantCode(t, f.do(other, "DELETE", base+"/sessions/"+s.SessionID+q, ""), 404, "close another user's session")
	wantCode(t, f.do(f.boss, "POST", base+"/edits"+q, cellEdit(s.SessionID, 1, 1, "B2", "ok")), 200, "owner still edits")
}

// holdWriteLock takes the database-wide write lock on a second connection
// until the test ends.
func holdWriteLock(t *testing.T, db *sql.DB) {
	t.Helper()
	db.SetMaxOpenConns(2)
	holder, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Rollback() })
	if _, err = holder.Exec(`UPDATE gridcraft_writer_fence SET seq=seq WHERE id=1`); err != nil {
		t.Fatal(err)
	}
}

// within1s fails when call needed the write lock (it would wait for it). The
// context passed to call derives from parent.
func within1s(t *testing.T, parent context.Context, name string, call func(ctx context.Context)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)
	defer cancel()
	start := time.Now()
	call(ctx)
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("%s waited %v for the write lock", name, waited)
	}
}

// A heartbeat while plenty of the idle expiry remains writes nothing and takes
// no write lock, like an edit's renewal; it still reports the current expiry.
// Once the renew step has passed it slides the expiry again.
func TestGridCraftHeartbeatWithPlentyLeftDoesNotWrite(t *testing.T) {
	x := newLiveRig(t)
	x.imported()
	x.editorFlag(true)
	s := x.opened(x.serve(x.h.openSession, x.cli("POST", "", time.Time{})), 201, "open")
	stored := time.Now().Add(liveIdle - 2*time.Minute).Truncate(time.Millisecond)
	mustExec(t, x.db, `UPDATE gridcraft_sessions SET expires_at=? WHERE session_id=?`, stored.UnixMilli(), s.SessionID)

	holdWriteLock(t, x.db)
	r := x.cli("POST", heartbeat(s.SessionID), time.Time{})
	within1s(t, r.Context(), "heartbeat", func(ctx context.Context) {
		beat := x.opened(x.serve(x.h.openSession, r.WithContext(ctx)), 200, "heartbeat with plenty left")
		if at, err := time.Parse(time.RFC3339Nano, beat.ExpiresAt); err != nil || !at.Equal(stored) {
			t.Fatalf("heartbeat reports %s, want the stored %s: %v", beat.ExpiresAt, stored.UTC(), err)
		}
	})
}

// An edit while more than idle minus the renew step remains does not write a
// renewal; the stored expiry stays exactly as it was.
func TestGridCraftEditWithPlentyLeftDoesNotRenew(t *testing.T) {
	x := newLiveRig(t)
	x.imported()
	x.editorFlag(true)
	s := x.opened(x.serve(x.h.openSession, x.cli("POST", "", time.Time{})), 201, "open")
	stored := time.Now().Add(liveIdle - 2*time.Minute).Truncate(time.Millisecond)
	mustExec(t, x.db, `UPDATE gridcraft_sessions SET expires_at=? WHERE session_id=?`, stored.UnixMilli(), s.SessionID)
	wantCode(t, x.serve(x.h.edit, x.cli("POST", cellEdit(s.SessionID, 1, 1, "B2", "x"), time.Time{})), 200, "edit")
	if got := x.expiry(s.SessionID); !got.Equal(stored) {
		t.Fatalf("edit with plenty left renewed the session to %s, want %s", got.UTC(), stored.UTC())
	}
}

// The outbox sweeper also prunes editor sessions that expired more than the
// linger period ago, on every Page; a sweep with nothing to prune takes no
// write lock.
func TestGridCraftSweeperPrunesExpiredSessions(t *testing.T) {
	x := newLiveRig(t)
	x.imported()
	x.editorFlag(true)
	ids := map[string]editorSession{}
	for _, name := range []string{"old", "recent", "live", "closed"} {
		ids[name] = x.opened(x.serve(x.h.openSession, x.cli("POST", "", time.Time{})), 201, "open "+name)
	}
	now := time.Now()
	mustExec(t, x.db, `UPDATE gridcraft_sessions SET created_at=?, expires_at=? WHERE session_id=?`, now.Add(-30*time.Hour).UnixMilli(), now.Add(-25*time.Hour).UnixMilli(), ids["old"].SessionID)
	mustExec(t, x.db, `UPDATE gridcraft_sessions SET expires_at=created_at+1 WHERE session_id=?`, ids["recent"].SessionID)
	mustExec(t, x.db, `UPDATE gridcraft_sessions SET closed_at=? WHERE session_id=?`, now.UnixMilli(), ids["closed"].SessionID)

	if n, err := x.h.pruneSessions(t.Context(), time.Now()); err != nil || n != 1 {
		t.Fatalf("prune: %d %v, want 1", n, err)
	}
	left := map[string]bool{}
	rows, err := x.db.Query(`SELECT session_id FROM gridcraft_sessions`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left[id] = true
	}
	_ = rows.Close()
	for name, s := range ids {
		if want := name != "old"; left[s.SessionID] != want {
			t.Errorf("session %s kept=%v, want %v", name, left[s.SessionID], want)
		}
	}
	// A late edit with the recently expired session still learns why.
	wantError(t, x.serve(x.h.edit, x.cli("POST", cellEdit(ids["recent"].SessionID, 1, 1, "B2", "late"), time.Time{})), 409, "editor_session_expired", "late edit")

	holdWriteLock(t, x.db)
	within1s(t, t.Context(), "idle prune", func(ctx context.Context) {
		if n, err := x.h.pruneSessions(ctx, time.Now()); err != nil || n != 0 {
			t.Fatalf("idle prune: %d %v", n, err)
		}
	})
}

// The sweeper prunes on its first sweep and then at most once per interval.
func TestGridCraftSweeperPrunesSessionsPeriodically(t *testing.T) {
	x := newLiveRig(t)
	x.imported()
	x.editorFlag(true)
	s := x.opened(x.serve(x.h.openSession, x.cli("POST", "", time.Time{})), 201, "open")
	expire := func() {
		mustExec(t, x.db, `UPDATE gridcraft_sessions SET created_at=?, expires_at=? WHERE session_id=?`, time.Now().Add(-30*time.Hour).UnixMilli(), time.Now().Add(-25*time.Hour).UnixMilli(), s.SessionID)
	}
	count := func() int {
		var n int
		if err := x.db.QueryRow(`SELECT count(*) FROM gridcraft_sessions`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	expire()
	w := &gridcraftOutboxWorker{h: x.h}
	w.sweep(t.Context())
	if n := count(); n != 0 {
		t.Fatalf("first sweep left %d sessions", n)
	}
	s = x.opened(x.serve(x.h.openSession, x.cli("POST", "", time.Time{})), 201, "open again")
	expire()
	w.sweep(t.Context())
	if n := count(); n != 1 {
		t.Fatalf("sweep within the interval pruned: %d left", n)
	}
	w.pruned = time.Now().Add(-gridcraftSessionPruneInterval)
	w.sweep(t.Context())
	if n := count(); n != 0 {
		t.Fatalf("sweep after the interval left %d sessions", n)
	}
}
