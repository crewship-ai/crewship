package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// liveRig drives the workbook handlers directly for one private workbook Page
// whose owner is the human of an agent run, with both credential kinds.
type liveRig struct {
	t                    *testing.T
	h                    *gridcraftHTTP
	db                   *sql.DB
	pushed               *[]gridcraftCommitted
	ws, run, user        string
	cliToken, agentToken string
	f                    *artifactPrincipalFixture
}

const liveRigPage = "grid-life-page"

// liveRigEngine accepts human multi-cell edits and agent single-cell writes,
// each changing exactly the cells it writes.
type liveRigEngine struct{ gridcraftCellsEngine }

func (e liveRigEngine) Prepare(ctx context.Context, b appengine.Binding, s appengine.State, c appengine.Command) (appengine.Candidate, error) {
	if c.Kind == "cell.set" {
		return appengine.NewCandidate(b, s.Snapshot, []string{c.Sheet + "!" + c.Cell}), nil
	}
	return e.gridcraftCellsEngine.Prepare(ctx, b, s, c)
}

func newLiveRig(t *testing.T) *liveRig {
	t.Helper()
	rig := pagesTransferSetupWithDB(t, gridcraftFullDB(t))
	f, _ := documentCreationAdmissionWithPrincipal(t, newArtifactPrincipalFixtureWithRig(t, rig))
	f.rig.db.SetMaxOpenConns(1)
	wmSeedPage(t, f.rig.db, f.a.WorkspaceID, liveRigPage, "grid-life", "Workbook", f.a.HumanUserID, "")
	mustExec(t, f.rig.db, `UPDATE pages SET private_audience=1 WHERE id=?`, liveRigPage)
	f.pages.artifactMasterToken = "synthetic-workbook-master"
	store := appengine.New(f.rig.db, appengine.Options{Enabled: true})
	engine := liveRigEngine{}
	x := &liveRig{t: t, db: f.rig.db, f: f, ws: f.a.WorkspaceID, run: f.a.RunID, user: f.a.HumanUserID}
	x.h = &gridcraftHTTP{pages: f.pages, store: store, engine: engine, operations: artifacts.NewGridCraftAdapter(store, engine, true), editor: true}
	var pushed []gridcraftCommitted
	x.pushed = &pushed
	x.h.broadcast = func(prefix, id, event string, payload any) { pushed = append(pushed, payload.(gridcraftCommitted)) }
	x.cliToken = mintTokenFor(t, f.rig.db, f.a.HumanUserID, "workbook-life-owner")
	x.agentToken = internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	return x
}

func (x *liveRig) request(method, body string, user *AuthUser, kind string) *http.Request {
	r := httptest.NewRequest(method, "/", strings.NewReader(body))
	if kind == AuthKindCLIToken {
		r.Header.Set("Authorization", "Bearer "+x.cliToken)
	}
	ctx := context.WithValue(r.Context(), ctxUser, user)
	ctx = context.WithValue(ctx, ctxWorkspaceID, x.ws)
	ctx = context.WithValue(ctx, ctxAuthKind, kind)
	r = r.WithContext(ctx)
	r.SetPathValue("slug", "@"+liveRigPage)
	return r
}

// cli is a request with the owner's CLI token, expiring at exp (zero: never).
func (x *liveRig) cli(method, body string, exp time.Time) *http.Request {
	return x.request(method, body, &AuthUser{ID: x.user, TokenID: "clt-" + x.user, CredentialExpiresAt: exp}, AuthKindCLIToken)
}

// web is a request of the owner's web sign-in session sid whose current
// access token expires at access.
func (x *liveRig) web(sid string, access time.Time, method, body string) *http.Request {
	return x.request(method, body, &AuthUser{ID: x.user, SessionID: sid, CredentialExpiresAt: access}, AuthKindSession)
}

// signIn records a web sign-in session of the owner valid until until.
func (x *liveRig) signIn(sid string, until time.Time) {
	mustExec(x.t, x.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET expires_at=excluded.expires_at`, sid, x.user, until.UTC().Format(time.RFC3339Nano))
}

func (x *liveRig) serve(handler http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func (x *liveRig) agent(body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	x.h.agent(w, documentDraftRequest(x.f, x.agentToken, "POST", body))
	return w
}

func (x *liveRig) imported() {
	x.t.Helper()
	wantCode(x.t, x.serve(x.h.importFile, x.cli("POST", `{"mutation_id":"import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`, time.Time{})), 200, "import")
}

func (x *liveRig) editorFlag(on bool) {
	mustExec(x.t, x.db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('ffo-life','ffl_gridcraft_editor',?,?) ON CONFLICT(flag_id,workspace_id) DO UPDATE SET enabled=excluded.enabled`, x.ws, on)
}

func (x *liveRig) opened(w *httptest.ResponseRecorder, code int, name string) editorSession {
	x.t.Helper()
	wantCode(x.t, w, code, name)
	var s editorSession
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil || s.SessionID == "" {
		x.t.Fatalf("%s: %s %v", name, w.Body.String(), err)
	}
	return s
}

func (x *liveRig) expiry(session string) time.Time {
	x.t.Helper()
	var at int64
	if err := x.db.QueryRow(`SELECT expires_at FROM gridcraft_sessions WHERE session_id=?`, session).Scan(&at); err != nil {
		x.t.Fatal(err)
	}
	return time.UnixMilli(at)
}

func within(t *testing.T, name string, got, from, to time.Time) {
	t.Helper()
	if got.Before(from) || got.After(to) {
		t.Fatalf("%s: %s not within [%s, %s]", name, got.UTC(), from.UTC(), to.UTC())
	}
}

const liveIdle = 15 * time.Minute

// An agent's commits feed starts at its mandate's base revision: history the
// mandate never covered, such as an overwritten input, is not readable through
// it. After base_rev the agent sees every changed cell, as workbook_read shows
// it the whole current workbook.
func TestGridCraftAgentFeedStartsAtMandateBase(t *testing.T) {
	x := newLiveRig(t)
	x.imported()
	x.editorFlag(true)
	s := x.opened(x.serve(x.h.openSession, x.cli("POST", "", time.Time{})), 201, "open")
	for i, v := range []string{"secret-old", "public-new"} {
		wantCode(t, x.serve(x.h.edit, x.cli("POST", cellEdit(s.SessionID, i+1, i+1, "C3", v), time.Time{})), 200, "edit "+v)
	}
	wantCode(t, x.serve(x.h.mandate, x.cli("POST", fmt.Sprintf(`{"mandate_id":"floor-mandate","run_id":%q,"base_rev":3,"generation":1,"targets":["Sheet1!B2"]}`, x.run), time.Time{})), 200, "mandate")
	commits := func(since int) *httptest.ResponseRecorder {
		return x.agent(fmt.Sprintf(`{"action":"commits","artifact_id":%q,"mandate_id":"floor-mandate","since":%d}`, liveRigPage, since))
	}
	for _, since := range []int{0, 2} {
		got := commits(since)
		var body struct {
			Error    string `json:"error"`
			HeadRev  int64  `json:"head_rev"`
			MinSince int64  `json:"min_since"`
		}
		if got.Code != 410 || json.Unmarshal(got.Body.Bytes(), &body) != nil || body.Error != "snapshot_required" || body.HeadRev != 3 || body.MinSince != 3 || strings.Contains(got.Body.String(), "secret-old") {
			t.Fatalf("since %d before the mandate: %d %s", since, got.Code, got.Body.String())
		}
	}
	wantCode(t, x.agent(fmt.Sprintf(`{"action":"apply","artifact_id":%q,"mandate_id":"floor-mandate","request":{"mutation_id":"agent-1","mandate_id":"floor-mandate","base_rev":3,"generation":1,"command":{"kind":"cell.set","sheet":"Sheet1","cell":"B2","input":"42"}}}`, liveRigPage)), 200, "agent apply")
	wantCode(t, x.serve(x.h.edit, x.cli("POST", cellEdit(s.SessionID, 3, 4, "C3", "later"), time.Time{})), 200, "owner edit after the mandate")
	got := commits(3)
	var feed struct {
		MinSince int64 `json:"min_since"`
		Commits  []struct {
			Rev int64 `json:"rev"`
			Own bool  `json:"own"`
			Ops []struct {
				Cell  string `json:"cell"`
				Input string `json:"input"`
			} `json:"ops"`
		} `json:"commits"`
	}
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &feed) != nil || feed.MinSince != 3 || len(feed.Commits) != 2 ||
		feed.Commits[0].Rev != 4 || !feed.Commits[0].Own || feed.Commits[1].Rev != 5 || feed.Commits[1].Own || feed.Commits[1].Ops[0].Input != "later" ||
		strings.Contains(got.Body.String(), s.SessionID) || strings.Contains(got.Body.String(), "user:") ||
		strings.Contains(got.Body.String(), "secret-old") || strings.Contains(got.Body.String(), "public-new") {
		t.Fatalf("feed from base_rev: %d %s", got.Code, got.Body.String())
	}
}

// A web editor session follows the sign-in session, not the 15-minute access
// token: a refreshed access token of the same sign-in keeps it, use and
// heartbeats slide an idle expiry, and it never outlives 12 hours from opening
// or the sign-in session. A different sign-in of the same user must reopen.
func TestGridCraftEditorSessionFollowsSignIn(t *testing.T) {
	x := newLiveRig(t)
	x.imported()
	x.editorFlag(true)
	x.signIn("sid-a", time.Now().Add(30*24*time.Hour))
	start := time.Now()
	s := x.opened(x.serve(x.h.openSession, x.web("sid-a", start.Add(2*time.Second), "POST", "")), 201, "open with an access token about to expire")
	within(t, "opened", x.expiry(s.SessionID), start.Add(liveIdle-time.Minute), time.Now().Add(liveIdle+time.Second))
	refreshed := time.Now().Add(15 * time.Minute)
	wantCode(t, x.serve(x.h.edit, x.web("sid-a", refreshed, "POST", cellEdit(s.SessionID, 1, 1, "B2", "one"))), 200, "edit with a refreshed access token")

	// An edit slides the idle expiry.
	mustExec(t, x.db, `UPDATE gridcraft_sessions SET expires_at=? WHERE session_id=?`, time.Now().Add(time.Minute).UnixMilli(), s.SessionID)
	before := time.Now()
	wantCode(t, x.serve(x.h.edit, x.web("sid-a", refreshed, "POST", cellEdit(s.SessionID, 2, 2, "B2", "two"))), 200, "edit near idle expiry")
	within(t, "after edit", x.expiry(s.SessionID), before.Add(liveIdle-time.Second), time.Now().Add(liveIdle+time.Second))
	// So does a heartbeat.
	mustExec(t, x.db, `UPDATE gridcraft_sessions SET expires_at=? WHERE session_id=?`, time.Now().Add(time.Minute).UnixMilli(), s.SessionID)
	before = time.Now()
	beat := x.opened(x.serve(x.h.openSession, x.web("sid-a", refreshed, "POST", fmt.Sprintf(`{"session_id":%q}`, s.SessionID))), 200, "heartbeat")
	within(t, "after heartbeat", x.expiry(s.SessionID), before.Add(liveIdle-time.Second), time.Now().Add(liveIdle+time.Second))
	if at, err := time.Parse(time.RFC3339Nano, beat.ExpiresAt); err != nil || at.Sub(x.expiry(s.SessionID)).Abs() > time.Millisecond {
		t.Fatalf("heartbeat reports %s: %v", beat.ExpiresAt, err)
	}

	// Never beyond the sign-in session.
	signOut := time.Now().Add(5 * time.Minute)
	x.signIn("sid-a", signOut)
	x.opened(x.serve(x.h.openSession, x.web("sid-a", refreshed, "POST", fmt.Sprintf(`{"session_id":%q}`, s.SessionID))), 200, "heartbeat near sign-in expiry")
	within(t, "capped by sign-in", x.expiry(s.SessionID), signOut.Add(-time.Second), signOut.Add(time.Millisecond))
	x.signIn("sid-a", time.Now().Add(30*24*time.Hour))

	// Never beyond 12 hours from opening.
	opened := time.Now().Add(-12*time.Hour + 2*time.Minute)
	mustExec(t, x.db, `UPDATE gridcraft_sessions SET created_at=?, expires_at=? WHERE session_id=?`, opened.UnixMilli(), time.Now().Add(time.Second).UnixMilli(), s.SessionID)
	x.opened(x.serve(x.h.openSession, x.web("sid-a", refreshed, "POST", fmt.Sprintf(`{"session_id":%q}`, s.SessionID))), 200, "heartbeat near the absolute cap")
	within(t, "absolute cap", x.expiry(s.SessionID), opened.Add(12*time.Hour-time.Second), opened.Add(12*time.Hour+time.Millisecond))
	mustExec(t, x.db, `UPDATE gridcraft_sessions SET created_at=?, expires_at=? WHERE session_id=?`, time.Now().Add(-13*time.Hour).UnixMilli(), time.Now().Add(time.Hour).UnixMilli(), s.SessionID)
	wantError(t, x.serve(x.h.edit, x.web("sid-a", refreshed, "POST", cellEdit(s.SessionID, 3, 3, "B2", "three"))), 409, "editor_session_expired", "edit after the absolute cap")

	// Another sign-in of the same user reopens rather than losing access; the
	// expired code is never a 401 the app's auth handling would act on.
	t2 := x.opened(x.serve(x.h.openSession, x.web("sid-a", refreshed, "POST", "")), 201, "open again")
	x.signIn("sid-b", time.Now().Add(time.Hour))
	wantError(t, x.serve(x.h.edit, x.web("sid-b", refreshed, "POST", cellEdit(t2.SessionID, 1, 3, "B2", "other sign-in"))), 409, "editor_session_expired", "edit from another sign-in")
	wantError(t, x.serve(x.h.openSession, x.web("sid-b", refreshed, "POST", fmt.Sprintf(`{"session_id":%q}`, t2.SessionID))), 409, "editor_session_expired", "heartbeat from another sign-in")
	wantError(t, x.serve(x.h.edit, x.cli("POST", cellEdit(t2.SessionID, 2, 3, "B2", "cli"), time.Time{})), 409, "editor_session_expired", "edit with a CLI token of the same user")
	wantCode(t, x.serve(x.h.edit, x.web("sid-a", refreshed, "POST", cellEdit(t2.SessionID, 3, 3, "B2", "same sign-in"))), 200, "same sign-in still edits")
}

// A CLI session follows its token's expiry and the same idle and absolute
// limits. Probe: a token-bound session opened close to an expiry stays valid
// when it is used before then.
func TestGridCraftCLISessionSlidesWithinTokenExpiry(t *testing.T) {
	x := newLiveRig(t)
	x.imported()
	x.editorFlag(true)
	short := time.Now().Add(5 * time.Minute)
	s := x.opened(x.serve(x.h.openSession, x.cli("POST", "", short)), 201, "open")
	within(t, "capped by token", x.expiry(s.SessionID), short.Add(-time.Second), short.Add(time.Millisecond))
	long := time.Now().Add(24 * time.Hour)
	before := time.Now()
	wantCode(t, x.serve(x.h.edit, x.cli("POST", cellEdit(s.SessionID, 1, 1, "B2", "x"), long)), 200, "edit")
	within(t, "slid", x.expiry(s.SessionID), before.Add(liveIdle-time.Second), time.Now().Add(liveIdle+time.Second))
	mustExec(t, x.db, `UPDATE gridcraft_sessions SET expires_at=created_at+1 WHERE session_id=?`, s.SessionID)
	wantError(t, x.serve(x.h.edit, x.cli("POST", cellEdit(s.SessionID, 2, 2, "B2", "y"), long)), 409, "editor_session_expired", "expired")
	wantError(t, x.serve(x.h.openSession, x.cli("POST", fmt.Sprintf(`{"session_id":%q}`, s.SessionID), long)), 409, "editor_session_expired", "heartbeat of an expired session")
}

// One user holds at most 32 live sessions per Page. Sessions nobody closes
// expire when idle, so they free the cap; a refusal says when to retry.
func TestGridCraftSessionCapAndIdleExpiry(t *testing.T) {
	x := newLiveRig(t)
	x.imported()
	x.editorFlag(true)
	var first editorSession
	for i := 0; i < 32; i++ {
		s := x.opened(x.serve(x.h.openSession, x.cli("POST", "", time.Time{})), 201, fmt.Sprintf("open %d", i+1))
		if i == 0 {
			first = s
		}
	}
	full := x.serve(x.h.openSession, x.cli("POST", "", time.Time{}))
	wantError(t, full, 429, "too_many_editor_sessions", "33rd session")
	retry, err := strconv.Atoi(full.Header().Get("Retry-After"))
	if err != nil || retry < 1 || retry > int(liveIdle/time.Second)+1 {
		t.Fatalf("Retry-After %q", full.Header().Get("Retry-After"))
	}
	mustExec(t, x.db, `UPDATE gridcraft_sessions SET expires_at=created_at+1 WHERE session_id=?`, first.SessionID)
	x.opened(x.serve(x.h.openSession, x.cli("POST", "", time.Time{})), 201, "after one idled out")
}

// No workbook.committed push goes out while the live editor is off for the
// workspace or the server; the revision is still marked delivered so it is not
// pushed late when the editor is turned on.
func TestGridCraftNoPushWhileEditorOff(t *testing.T) {
	x := newLiveRig(t)
	x.imported()
	if len(*x.pushed) != 0 {
		t.Fatalf("pushed with the workspace flag off: %+v", *x.pushed)
	}
	x.editorFlag(true)
	s := x.opened(x.serve(x.h.openSession, x.cli("POST", "", time.Time{})), 201, "open")
	wantCode(t, x.serve(x.h.edit, x.cli("POST", cellEdit(s.SessionID, 1, 1, "B2", "on"), time.Time{})), 200, "edit")
	if len(*x.pushed) != 1 || (*x.pushed)[0].Rev != 2 {
		t.Fatalf("push with the editor on: %+v", *x.pushed)
	}
	wantCode(t, x.serve(x.h.mandate, x.cli("POST", fmt.Sprintf(`{"mandate_id":"off-mandate","run_id":%q,"base_rev":2,"generation":1,"targets":["Sheet1!C3"]}`, x.run), time.Time{})), 200, "mandate")
	x.editorFlag(false)
	wantCode(t, x.agent(fmt.Sprintf(`{"action":"apply","artifact_id":%q,"mandate_id":"off-mandate","request":{"mutation_id":"agent-off","mandate_id":"off-mandate","base_rev":2,"generation":1,"command":{"kind":"cell.set","sheet":"Sheet1","cell":"C3","input":"1"}}}`, liveRigPage)), 200, "agent apply with the flag off")
	x.editorFlag(true)
	x.h.editor = false
	wantCode(t, x.serve(x.h.importFile, x.cli("POST", `{"mutation_id":"import-2","mandate_id":"human-import","base_rev":3,"generation":1,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`, time.Time{})), 200, "import with the operator switch off")
	x.h.editor = true
	x.h.publish(t.Context(), "")
	if len(*x.pushed) != 1 {
		t.Fatalf("pushed while off: %+v", *x.pushed)
	}
	var pending int
	if err := x.db.QueryRow(`SELECT count(*) FROM gridcraft_outbox WHERE state='pending'`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("pending=%d %v", pending, err)
	}
}

// Delivery is at most once: a revision is marked sent when it is claimed, so a
// push lost after the claim is not repeated. Clients recover it from the
// commits feed after a reconnect or a revision gap.
func TestGridCraftPushIsAtMostOnce(t *testing.T) {
	x := newLiveRig(t)
	x.editorFlag(true)
	lost := x.h.broadcast
	x.h.broadcast = func(string, string, string, any) {}
	x.imported()
	x.h.broadcast = lost
	x.h.publish(t.Context(), "")
	if len(*x.pushed) != 0 {
		t.Fatalf("lost push repeated: %+v", *x.pushed)
	}
	status := x.serve(x.h.status, func() *http.Request {
		r := x.cli("GET", "", time.Time{})
		r.SetPathValue("mutation", "import")
		return r
	}())
	if !strings.Contains(status.Body.String(), `"delivery":"sent"`) {
		t.Fatalf("status %d %s", status.Code, status.Body.String())
	}
	feed := x.serve(x.h.commits, func() *http.Request {
		r := x.cli("GET", "", time.Time{})
		r.URL.RawQuery = "since=0"
		return r
	}())
	if feed.Code != 200 || !strings.Contains(feed.Body.String(), `"rev":1`) {
		t.Fatalf("feed recovers the revision: %d %s", feed.Code, feed.Body.String())
	}
}

// With the sweeper running, the push is claimed and sent by the sweeper, not on
// the response path: a slow push never delays an edit's response. Shutdown
// stops the sweeper; what is then pending waits for the next start.
func TestGridCraftOutboxSweeperOwnsThePush(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true, Editor: true}, gridcraftCellsEngine{})
	setGridCraftEditorFlag(t, f, "ws-old", true)
	release := make(chan struct{})
	frames := make(chan gridcraftCommitted, 16)
	f.r.gridcraftOutbox.h.broadcast = func(_, _, _ string, payload any) {
		<-release
		frames <- payload.(gridcraftCommitted)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.r.StartGridCraftOutbox(ctx)
	stopped := false
	defer func() {
		if !stopped {
			close(release)
			f.r.Shutdown()
		}
	}()
	timed := func(name string, call func() *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		t.Helper()
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- call() }()
		select {
		case w := <-done:
			return w
		case <-time.After(3 * time.Second):
			t.Fatalf("%s waited for the push", name)
		}
		return nil
	}
	base := "/api/v1/pages/sweep-book/workbook"
	wmSeedPage(t, f.db, "ws-old", "sweep-book", "sweep-book", "Workbook", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='sweep-book'`)
	wantCode(t, timed("import", func() *httptest.ResponseRecorder {
		return f.do(f.boss, "POST", base+"/operations?workspace_id=ws-old", `{"mutation_id":"import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`)
	}), 200, "import")
	s := openEditorSession(t, f, f.boss, base)
	wantCode(t, timed("edit", func() *httptest.ResponseRecorder {
		return f.do(f.boss, "POST", base+"/edits?workspace_id=ws-old", cellEdit(s.SessionID, 1, 1, "B2", "1"))
	}), 200, "edit while a push is blocked")
	close(release)
	for want := int64(1); want <= 2; want++ {
		select {
		case p := <-frames:
			if p.Rev != want {
				t.Fatalf("push %+v, want rev %d", p, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no push for rev %d", want)
		}
	}
	stopped = true
	f.r.Shutdown()
	wantCode(t, f.do(f.boss, "POST", base+"/edits?workspace_id=ws-old", cellEdit(s.SessionID, 2, 2, "B2", "2")), 200, "edit after shutdown")
	var pending int
	if err := f.db.QueryRow(`SELECT count(*) FROM gridcraft_outbox WHERE state='pending' AND page_id='sweep-book'`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("pending after shutdown=%d %v", pending, err)
	}
}
