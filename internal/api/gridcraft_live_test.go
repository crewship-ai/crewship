package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
)

func setGridCraftEditorFlag(t *testing.T, f *instanceFixture, workspace string, on bool) {
	t.Helper()
	mustExec(t, f.db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('ffo-gridcraft-'||?,'ffl_gridcraft_editor',?,?) ON CONFLICT(flag_id,workspace_id) DO UPDATE SET enabled=excluded.enabled`, workspace, workspace, on)
}

// liveWorkbook seeds an imported personal private workbook Page owned by owner.
func liveWorkbook(t *testing.T, f *instanceFixture, token, owner, id string) string {
	t.Helper()
	wmSeedPage(t, f.db, "ws-old", id, id, "Workbook "+id, owner, "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id=?`, id)
	base := "/api/v1/pages/" + id + "/workbook"
	wantCode(t, f.do(token, "POST", base+"/operations?workspace_id=ws-old", `{"mutation_id":"import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`), 200, "import "+id)
	return base
}

type editorSession struct {
	SessionID  string `json:"session_id"`
	PageID     string `json:"page_id"`
	ActorLabel string `json:"actor_label"`
	Rev        int64  `json:"rev"`
	Generation int64  `json:"generation"`
	Protocol   int    `json:"protocol"`
	ExpiresAt  string `json:"expires_at"`
}

func openEditorSession(t *testing.T, f *instanceFixture, token, base string) editorSession {
	t.Helper()
	w := f.do(token, "POST", base+"/sessions?workspace_id=ws-old", "")
	wantCode(t, w, 201, "open editor session")
	var s editorSession
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil || s.SessionID == "" {
		t.Fatalf("session %s: %v", w.Body.String(), err)
	}
	return s
}

func cellEdit(session string, seq, base int, cell, input string) string {
	return fmt.Sprintf(`{"session_id":%q,"mutation_id":"%s:%d","base_rev":%d,"generation":1,"ops":[{"kind":"cell.set","sheet":"Sheet1","cell":%q,"input":%q}]}`, session, session, seq, base, cell, input)
}

func wantError(t *testing.T, w *httptest.ResponseRecorder, code int, message, name string) {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if w.Code != code || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error != message {
		t.Fatalf("%s: %d %s, want %d %s", name, w.Code, w.Body.String(), code, message)
	}
}

// The live editing surface needs both the operator switch and the workspace
// flag gridcraft_editor; either one off answers 503, while authorization still
// comes first.
func TestGridCraftEditorFlagGatesLiveEditing(t *testing.T) {
	for _, tc := range []struct {
		name               string
		operator, flag, ok bool
	}{
		{name: "operator off", flag: true},
		{name: "workspace flag off by default", operator: true},
		{name: "both on", operator: true, flag: true, ok: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := gridcraftInstanceFixture(t)
			f.r.RegisterGridCraft(appengine.Options{Enabled: true, Editor: tc.operator}, gridcraftCellsEngine{})
			if tc.flag {
				setGridCraftEditorFlag(t, f, "ws-old", true)
			}
			base := liveWorkbook(t, f, f.boss, "boss", "flag-book")
			q := "?workspace_id=ws-old"
			wantCode(t, f.do(f.wsAdmin, "POST", base+"/sessions"+q, ""), 404, "not the owner, whatever the flag")
			if !tc.ok {
				wantError(t, f.do(f.boss, "POST", base+"/sessions"+q, ""), 503, "gridcraft_editor_disabled", "sessions")
				wantError(t, f.do(f.boss, "POST", base+"/edits"+q, cellEdit("ws_x", 1, 1, "B2", "1")), 503, "gridcraft_editor_disabled", "edits")
				wantError(t, f.do(f.boss, "GET", base+"/commits"+q+"&since=0", ""), 503, "gridcraft_editor_disabled", "commits")
				wantError(t, f.do(f.boss, "DELETE", base+"/sessions/ws_x"+q, ""), 503, "gridcraft_editor_disabled", "close")
				return
			}
			s := openEditorSession(t, f, f.boss, base)
			wantCode(t, f.do(f.boss, "POST", base+"/edits"+q, cellEdit(s.SessionID, 1, 1, "B2", "1")), 200, "edit while on")
			// Switching the workspace flag off takes effect without a restart,
			// also for an already open session.
			setGridCraftEditorFlag(t, f, "ws-old", false)
			wantError(t, f.do(f.boss, "POST", base+"/edits"+q, cellEdit(s.SessionID, 2, 2, "B3", "1")), 503, "gridcraft_editor_disabled", "edit after flag off")
			wantError(t, f.do(f.boss, "GET", base+"/commits"+q+"&since=0", ""), 503, "gridcraft_editor_disabled", "commits after flag off")
			var head int
			if err := f.db.QueryRow(`SELECT head_rev FROM gridcraft_heads WHERE page_id='flag-book'`).Scan(&head); err != nil || head != 2 {
				t.Fatalf("head=%d %v", head, err)
			}
		})
	}
}

// An edit must name a live session of the same user, credential and Page.
// Someone else's or another Page's session is not found (404); an expired or
// closed session must be replaced (401 session_expired).
func TestGridCraftEditsRequireLiveOwnSession(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true, Editor: true}, gridcraftCellsEngine{})
	setGridCraftEditorFlag(t, f, "ws-old", true)
	mustExec(t, f.db, `UPDATE users SET full_name='Boss Person' WHERE id='boss'`)
	base := liveWorkbook(t, f, f.boss, "boss", "session-book")
	second := liveWorkbook(t, f, f.boss, "boss", "second-book")
	foreign := liveWorkbook(t, f, f.wsAdmin, "wsadmin", "admin-book")
	q := "?workspace_id=ws-old"
	edit := func(token, session string, seq, rev int) *httptest.ResponseRecorder {
		return f.do(token, "POST", base+"/edits"+q, cellEdit(session, seq, rev, "B2", fmt.Sprintf("v%d", seq)))
	}
	wantCode(t, edit(f.boss, "ws_neveropened", 1, 1), 404, "session never opened")

	before := time.Now()
	opened := f.do(f.boss, "POST", base+"/sessions"+q, "")
	wantCode(t, opened, 201, "open")
	var a editorSession
	if err := json.Unmarshal(opened.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	expires, err := time.Parse(time.RFC3339Nano, a.ExpiresAt)
	if err != nil || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(a.SessionID) || len(a.SessionID) < 22 ||
		a.PageID != "session-book" || a.Rev != 1 || a.Generation != 1 || a.Protocol != 1 || a.ActorLabel != "Boss Person" ||
		!expires.After(before) || expires.After(before.Add(12*time.Hour+time.Minute)) || opened.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("session %s: %v", opened.Body.String(), err)
	}
	if strings.Contains(opened.Body.String(), "user:boss") || strings.Contains(opened.Body.String(), "token") {
		t.Fatalf("session leaks internal identity: %s", opened.Body.String())
	}
	if other := openEditorSession(t, f, f.boss, base); other.SessionID == a.SessionID {
		t.Fatal("session IDs repeat")
	}
	wantCode(t, edit(f.boss, a.SessionID, 1, 1), 200, "own live session")

	b := openEditorSession(t, f, f.boss, second)
	wantCode(t, edit(f.boss, b.SessionID, 1, 2), 404, "session of another Page")
	c := openEditorSession(t, f, f.wsAdmin, foreign)
	wantCode(t, edit(f.boss, c.SessionID, 1, 2), 404, "another user's session")
	wantCode(t, f.do(f.wsAdmin, "POST", base+"/sessions"+q, ""), 404, "only the owner opens a session")
	// The session is bound to the credential that opened it.
	const otherToken = "crewship_cli_instfixbosssecond000000000"
	mustExec(t, f.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('clt-boss-second','boss','t2',?,datetime('now'))`, sha256Hex(otherToken))
	wantCode(t, edit(otherToken, a.SessionID, 2, 2), 404, "another credential of the same user")

	// A heartbeat renews the same session; the writer stays the same.
	renewed := f.do(f.boss, "POST", base+"/sessions"+q, fmt.Sprintf(`{"session_id":%q}`, a.SessionID))
	wantCode(t, renewed, 200, "renew")
	var r editorSession
	if json.Unmarshal(renewed.Body.Bytes(), &r) != nil || r.SessionID != a.SessionID || r.Rev != 2 {
		t.Fatalf("renewed %s", renewed.Body.String())
	}
	wantCode(t, f.do(f.boss, "POST", base+"/sessions"+q, fmt.Sprintf(`{"session_id":%q}`, c.SessionID)), 404, "renew another user's session")
	wantCode(t, f.do(f.boss, "POST", base+"/sessions"+q, `{"session_id":"ws#1"}`), 400, "renew with an invalid session")
	wantCode(t, f.do(f.boss, "POST", base+"/sessions"+q, `{"page":"x"}`), 400, "unknown field")
	wantCode(t, f.do(f.boss, "POST", base+"/sessions"+q, `{"protocol":2}`), 400, "unsupported bridge protocol")
	withProtocol := f.do(f.boss, "POST", base+"/sessions"+q, `{"protocol":1}`)
	wantCode(t, withProtocol, 201, "bridge protocol 1")
	// No editor origin is configured here: the host gets no bundle to frame.
	if !strings.Contains(withProtocol.Body.String(), `"bundle_digest":""`) || strings.Contains(withProtocol.Body.String(), "editor_url") {
		t.Fatalf("bundle without an editor origin: %s", withProtocol.Body.String())
	}

	mustExec(t, f.db, `UPDATE gridcraft_sessions SET expires_at=created_at+1 WHERE session_id=?`, a.SessionID)
	wantError(t, edit(f.boss, a.SessionID, 2, 2), 401, "session_expired", "expired session")
	wantError(t, f.do(f.boss, "POST", base+"/sessions"+q, fmt.Sprintf(`{"session_id":%q}`, a.SessionID)), 401, "session_expired", "renew expired")

	d := openEditorSession(t, f, f.boss, base)
	wantCode(t, f.do(f.boss, "DELETE", base+"/sessions/"+d.SessionID+q, ""), 200, "close")
	wantCode(t, f.do(f.boss, "DELETE", base+"/sessions/"+d.SessionID+q, ""), 200, "close is idempotent")
	wantError(t, edit(f.boss, d.SessionID, 1, 2), 401, "session_expired", "closed session")
	wantCode(t, f.do(f.boss, "DELETE", base+"/sessions/"+c.SessionID+q, ""), 404, "close another user's session")
	wantCode(t, f.do(f.wsAdmin, "DELETE", foreign+"/sessions/"+c.SessionID+q, ""), 200, "owner closes own session")

	// A session never outlives the credential that opened it.
	const shortToken = "crewship_cli_instfixbossshort0000000000"
	limit := time.Now().Add(20 * time.Minute).UTC()
	mustExec(t, f.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at,expires_at) VALUES('clt-boss-short','boss','t3',?,datetime('now'),?)`, sha256Hex(shortToken), limit.Format(time.RFC3339Nano))
	short := openEditorSession(t, f, shortToken, base)
	if at, err := time.Parse(time.RFC3339Nano, short.ExpiresAt); err != nil || at.After(limit) {
		t.Fatalf("session expires %s after credential %s: %v", short.ExpiresAt, limit, err)
	}
	var head int
	if err := f.db.QueryRow(`SELECT head_rev FROM gridcraft_heads WHERE page_id='session-book'`).Scan(&head); err != nil || head != 2 {
		t.Fatalf("refused edits changed head: %d %v", head, err)
	}
	// A session belongs to one workbook generation.
	e := openEditorSession(t, f, f.boss, base)
	mustExec(t, f.db, `UPDATE gridcraft_heads SET generation=2 WHERE page_id='session-book'`)
	wantError(t, edit(f.boss, e.SessionID, 1, 2), 401, "session_expired", "session of an older generation")
}

// The commits feed returns confirmed revisions after since under the workbook
// read authorization, including the audited workspace OWNER. Session and
// mutation IDs appear only on the caller's own revisions.
func TestGridCraftCommitsFeedUnderReadAccess(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true, Editor: true}, gridcraftCellsEngine{})
	setGridCraftEditorFlag(t, f, "ws-old", true)
	mustExec(t, f.db, `UPDATE users SET full_name='Boss Person' WHERE id='boss'`)
	mustExec(t, f.db, `UPDATE users SET full_name='Ada Admin' WHERE id='wsadmin'`)
	base := liveWorkbook(t, f, f.boss, "boss", "feed-book")
	q := "?workspace_id=ws-old"
	a := openEditorSession(t, f, f.boss, base)
	b := openEditorSession(t, f, f.boss, base)
	wantCode(t, f.do(f.boss, "POST", base+"/edits"+q, cellEdit(a.SessionID, 1, 1, "B2", "seven")), 200, "edit A")
	wantCode(t, f.do(f.boss, "POST", base+"/edits"+q, cellEdit(b.SessionID, 1, 1, "C3", "nine")), 200, "edit B")
	type op struct{ Sheet, Cell, Input string }
	type commit struct {
		Rev        int64  `json:"rev"`
		Kind       string `json:"kind"`
		ActorLabel string `json:"actor_label"`
		Own        bool   `json:"own"`
		SessionID  string `json:"session_id"`
		MutationID string `json:"mutation_id"`
		Ops        []op   `json:"ops"`
	}
	type feed struct {
		HeadRev    int64    `json:"head_rev"`
		Generation int64    `json:"generation"`
		MinSince   int64    `json:"min_since"`
		Commits    []commit `json:"commits"`
	}
	read := func(token, query string) (feed, *httptest.ResponseRecorder) {
		t.Helper()
		w := f.do(token, "GET", base+"/commits"+q+query, "")
		var out feed
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out, w
	}
	got, w := read(f.boss, "&since=0")
	wantCode(t, w, 200, "owner feed")
	want := feed{HeadRev: 3, Generation: 1, Commits: []commit{
		{Rev: 1, Kind: "snapshot", ActorLabel: "Boss Person", Own: true, MutationID: "import"},
		{Rev: 2, Kind: "cells", ActorLabel: "Boss Person", Own: true, SessionID: a.SessionID, MutationID: a.SessionID + ":1", Ops: []op{{"Sheet1", "B2", "seven"}}},
		{Rev: 3, Kind: "cells", ActorLabel: "Boss Person", Own: true, SessionID: b.SessionID, MutationID: b.SessionID + ":1", Ops: []op{{"Sheet1", "C3", "nine"}}},
	}}
	if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", want) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("feed\n got %+v\nwant %+v", got, want)
	}
	if strings.Contains(w.Body.String(), "user:boss") {
		t.Fatalf("internal authority leaked: %s", w.Body.String())
	}
	if page, w := read(f.boss, "&since=1&limit=1"); w.Code != 200 || len(page.Commits) != 1 || page.Commits[0].Rev != 2 || page.HeadRev != 3 {
		t.Fatalf("page %d %s", w.Code, w.Body.String())
	}
	for _, bad := range []string{"", "&since=-1", "&since=x", "&since=0&limit=0", "&since=0&limit=201", "&since=0&limit=x"} {
		wantCode(t, f.do(f.boss, "GET", base+"/commits"+q+bad, ""), 400, "bad query "+bad)
	}
	wantCode(t, f.do(f.wsAdmin, "GET", base+"/commits"+q+"&since=0", ""), 404, "ADMIN without access")

	// History that was pruned cannot be replayed: 410 says from where it can.
	mustExec(t, f.db, `UPDATE gridcraft_audit SET kind='snapshot',command=NULL WHERE page_id='feed-book' AND rev<=2`)
	for _, since := range []string{"0", "1", "4"} {
		gone := f.do(f.boss, "GET", base+"/commits"+q+"&since="+since, "")
		var body struct {
			Error      string `json:"error"`
			HeadRev    int64  `json:"head_rev"`
			Generation int64  `json:"generation"`
			MinSince   int64  `json:"min_since"`
		}
		if gone.Code != 410 || json.Unmarshal(gone.Body.Bytes(), &body) != nil || body.Error != "snapshot_required" || body.HeadRev != 3 || body.Generation != 1 || body.MinSince != 2 {
			t.Fatalf("since %s: %d %s", since, gone.Code, gone.Body.String())
		}
	}
	if kept, w := read(f.boss, "&since=2"); w.Code != 200 || kept.MinSince != 2 || len(kept.Commits) != 1 || kept.Commits[0].Rev != 3 {
		t.Fatalf("retained %d %s", w.Code, w.Body.String())
	}

	// The workspace OWNER reads another member's private workbook feed only
	// with an audit record, and never sees that member's sessions.
	other := liveWorkbook(t, f, f.wsAdmin, "wsadmin", "admin-feed")
	s := openEditorSession(t, f, f.wsAdmin, other)
	wantCode(t, f.do(f.wsAdmin, "POST", other+"/edits"+q, cellEdit(s.SessionID, 1, 1, "D4", "private")), 200, "member edit")
	owner := f.do(f.boss, "GET", other+"/commits"+q+"&since=1", "")
	wantCode(t, owner, 200, "audited OWNER feed")
	if strings.Contains(owner.Body.String(), s.SessionID) || strings.Contains(owner.Body.String(), "mutation_id") || !strings.Contains(owner.Body.String(), `"actor_label":"Ada Admin","own":false`) {
		t.Fatalf("OWNER feed %s", owner.Body.String())
	}
	var audits int
	if err := f.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND entity_id='admin-feed'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit=%d %v", audits, err)
	}
	mustExec(t, f.db, `CREATE TRIGGER deny_feed_audit BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if denied := f.do(f.boss, "GET", other+"/commits"+q+"&since=1", ""); denied.Code == 200 || strings.Contains(denied.Body.String(), "private") {
		t.Fatalf("feed released without audit: %d %s", denied.Code, denied.Body.String())
	}
}
