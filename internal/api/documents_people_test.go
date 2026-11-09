package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// documentPeopleFixture: an owner's Document, a member who can read it only
// through a grant, and people who must never be named to anyone.
func documentPeopleFixture(t *testing.T, private bool) (*PageHandler, string, string) {
	t.Helper()
	h, _, _, ws, owner := newPagesFixture(t)
	privateAudience := 0
	if private {
		privateAudience = 1
	}
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES('people-page',?,'people-doc','People',?,'{}',?)`, ws, owner, privateAudience)
	execOrFatal(t, h.db, `INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES(?,'people-page',5,X'01',CAST('{}' AS BLOB))`, ws)
	for _, person := range []struct{ id, name, role string }{
		{"bea", "Bea Member", "MEMBER"},
		{"cid", "Cid Without Access", "MEMBER"},
		{"sus", "Sus Suspended", "MEMBER"},
		{"res", "Res Restricted", "MEMBER"},
		{"zed", "Zed Outsider", ""},
	} {
		execOrFatal(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, person.id, person.id+"@example.invalid", person.name)
		if person.role != "" {
			execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,?)`, person.id+"-member", ws, person.id, person.role)
		}
	}
	for _, grantee := range []string{"bea", "sus", "res", "zed"} {
		execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('people-page','user',?,'read',?)`, grantee, owner)
	}
	execOrFatal(t, h.db, `UPDATE users SET suspended_at='2026-11-01T00:00:00Z' WHERE id='sus'`)
	execOrFatal(t, h.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id='res'`)
	return h, ws, owner
}

func documentPeopleCall(t *testing.T, h *PageHandler, ws, caller, role, query string) (int, []documentPerson) {
	t.Helper()
	w := documentPeopleServe(t, h, documentPeopleRequest(t, h, ws, caller, role, query))
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("people response must not be cached: %q", w.Header().Get("Cache-Control"))
	}
	if w.Code != 200 {
		return w.Code, nil
	}
	var body struct {
		People []documentPerson `json:"people"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.People == nil {
		t.Fatalf("people body %s: %v", w.Body, err)
	}
	return w.Code, body.People
}

// documentPeopleServe answers r with an engine that must never be called.
func documentPeopleServe(t *testing.T, h *PageHandler, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
		t.Fatal("people must never call the document engine")
		return nil, 0, nil
	}
	w := httptest.NewRecorder()
	h.DocumentPeople(w, r)
	return w
}

// documentPeopleRequest: a new current browser session for caller.
func documentPeopleRequest(t *testing.T, h *PageHandler, ws, caller, role, query string) *http.Request {
	t.Helper()
	return documentPeopleSessionRequest(t, h, ws, caller, role, query, documentPeopleSession(t, h, caller))
}

func documentPeopleSession(t *testing.T, h *PageHandler, caller string) string {
	t.Helper()
	session, err := sessions.NewDBStore(h.db).Create(t.Context(), caller, "document-people", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	return session.ID
}

// documentPeopleSessionRequest: a request on an existing session.
func documentPeopleSessionRequest(t *testing.T, h *PageHandler, ws, caller, role, query, sessionID string) *http.Request {
	t.Helper()
	r := pagesRequest(t, "GET", "/api/v1/pages/people-doc/document/people?"+query, ws, caller, role, "")
	ctx := context.WithValue(r.Context(), ctxAuthKind, AuthKindSession)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: caller, SessionID: sessionID})
	r = r.WithContext(ctx)
	r.SetPathValue("slug", "people-doc")
	return r
}

func names(people []documentPerson) []string {
	out := []string{}
	for _, person := range people {
		out = append(out, person.ID+"="+person.Name)
	}
	sort.Strings(out)
	return out
}

func TestDocumentPeopleNamesOnlyReadersAndOnlyToReaders(t *testing.T) {
	h, ws, owner := documentPeopleFixture(t, false)
	// The owner learns names only of people who can read this Document.
	status, people := documentPeopleCall(t, h, ws, owner, "OWNER", "ids="+url.QueryEscape("bea,cid,sus,res,zed,nobody,"+owner))
	if status != 200 || strings.Join(names(people), "|") != "bea=Bea Member|test-user-id=Test User" {
		t.Fatalf("named people = %d %v", status, names(people))
	}
	// An id for nobody and an id for a member without access get the same answer: nothing.
	_, nobody := documentPeopleCall(t, h, ws, owner, "OWNER", "ids=nobody")
	_, member := documentPeopleCall(t, h, ws, owner, "OWNER", "ids=cid")
	_, outsider := documentPeopleCall(t, h, ws, owner, "OWNER", "ids=zed")
	if len(nobody) != 0 || len(member) != 0 || len(outsider) != 0 {
		t.Fatalf("existence leaked: %v %v %v", nobody, member, outsider)
	}
	// Search lists only readers; email addresses never match or appear.
	_, all := documentPeopleCall(t, h, ws, owner, "OWNER", "q=")
	if strings.Join(names(all), "|") != "bea=Bea Member|test-user-id=Test User" {
		t.Fatalf("search = %v", names(all))
	}
	_, byName := documentPeopleCall(t, h, ws, owner, "OWNER", "q=bea")
	_, byEmail := documentPeopleCall(t, h, ws, owner, "OWNER", "q=example.invalid")
	_, hidden := documentPeopleCall(t, h, ws, owner, "OWNER", "q=Cid")
	if strings.Join(names(byName), "|") != "bea=Bea Member" || len(byEmail) != 0 || len(hidden) != 0 {
		t.Fatalf("search filters: %v %v %v", byName, byEmail, hidden)
	}
	// A reader through a grant may name co-readers; a member without access gets 404, like a missing page.
	if status, people := documentPeopleCall(t, h, ws, "bea", "MEMBER", "ids="+owner); status != 200 || len(people) != 1 {
		t.Fatalf("granted reader: %d %v", status, people)
	}
	for _, caller := range []string{"cid", "res", "sus"} {
		if status, _ := documentPeopleCall(t, h, ws, caller, "MEMBER", "ids="+owner); status != 404 {
			t.Fatalf("%s without read access got %d", caller, status)
		}
	}
	// Bounded, closed requests.
	many := make([]string, 101)
	for i := range many {
		many[i] = "u" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + strings.Repeat("y", i/26)
	}
	for _, query := range []string{"ids=bea&q=b", "ids=" + url.QueryEscape("bad id"), "ids=", "ids=" + url.QueryEscape("a,,b"), "ids=" + strings.Join(many, ","), "q=" + strings.Repeat("x", 101), "ids=bea&ids=cid", "q=a&q=b"} {
		if status, _ := documentPeopleCall(t, h, ws, owner, "OWNER", query); status != 400 {
			t.Fatalf("%s: status %d", query[:min(len(query), 30)], status)
		}
	}
}

func TestDocumentPeopleOnPrivateOutputFollowsThePrivateAudience(t *testing.T) {
	h, ws, owner := documentPeopleFixture(t, true)
	// A grant does not open private output; only its custodian (and the
	// audited workspace administrator read) can read, so only they are named.
	if status, people := documentPeopleCall(t, h, ws, owner, "OWNER", "ids=bea,"+owner); status != 200 || strings.Join(names(people), "|") != "test-user-id=Test User" {
		t.Fatalf("private names = %d %v", status, names(people))
	}
	if status, _ := documentPeopleCall(t, h, ws, "bea", "MEMBER", "ids="+owner); status != 404 {
		t.Fatalf("granted member read private people: %d", status)
	}
}

func TestDocumentPeopleRequiresADocumentPage(t *testing.T) {
	h, ws, owner := documentPeopleFixture(t, false)
	execOrFatal(t, h.db, `DELETE FROM documents WHERE page_id='people-page'`)
	if status, _ := documentPeopleCall(t, h, ws, owner, "OWNER", "ids="+owner); status != 404 {
		t.Fatalf("page without a document: %d", status)
	}
}

func TestDocumentPersonNameIsOneBoundedLine(t *testing.T) {
	for in, want := range map[string]string{"Eve\u202egnirts": "Evegnirts", "Ada\u2028Owner\u0085X": "Ada Owner X", "👩\u200d💻 Dev": "👩\u200d💻 Dev", "  Ada \n Owner\t": "Ada   Owner", "\x00\x1f": "", strings.Repeat("é", 250): strings.Repeat("é", 200)} {
		if got := documentPersonName(in); got != want {
			t.Fatalf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestDocumentPeopleRefusesAgentCallers(t *testing.T) {
	h, ws, owner := documentPeopleFixture(t, false)
	h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) { return nil, 500, nil }
	r := pagesRequest(t, "GET", "/api/v1/pages/people-doc/document/people?ids="+owner, ws, owner, "OWNER", "")
	r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindSession))
	r.SetPathValue("slug", "people-doc")
	r.Header.Set("X-Internal-Token", "agent-run")
	w := httptest.NewRecorder()
	h.DocumentPeople(w, r)
	if w.Code != 403 || strings.Contains(w.Body.String(), "Test User") {
		t.Fatalf("agent caller: %d %s", w.Code, w.Body)
	}
}

func TestDocumentPeopleRequiresTheCurrentCredentialAndReadScope(t *testing.T) {
	for _, change := range []string{"session-revoked", "cli-read", "cli-narrowed", "cli-revoked"} {
		t.Run(change, func(t *testing.T) {
			h, ws, owner := documentPeopleFixture(t, false)
			r := documentPeopleRequest(t, h, ws, owner, "OWNER", "q=")
			want := 401
			switch change {
			case "session-revoked":
				execOrFatal(t, h.db, `UPDATE user_sessions SET revoked_at='2026-01-01T00:00:00Z'`)
			default:
				const token = "crewship_cli_document_people_test"
				execOrFatal(t, h.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('people-cli',?,'people',?,'STANDARD','["pages:read"]')`, owner, hashStandard(token))
				r.Header.Set("Authorization", "Bearer "+token)
				r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindCLIToken))
				switch change {
				case "cli-read":
					want = 200
				case "cli-narrowed":
					execOrFatal(t, h.db, `UPDATE cli_tokens SET scopes='["crews:write"]' WHERE id='people-cli'`)
					want = 403
				case "cli-revoked":
					execOrFatal(t, h.db, `UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z' WHERE id='people-cli'`)
				}
			}
			h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
				t.Fatal("people must never call the document engine")
				return nil, 0, nil
			}
			w := httptest.NewRecorder()
			h.DocumentPeople(w, r)
			if w.Code != want || (want != 200 && strings.Contains(w.Body.String(), "Bea Member")) {
				t.Fatalf("%s: %d %s", change, w.Code, w.Body)
			}
		})
	}
}

func TestDocumentPeopleRefusesAPageTheDocumentReadRefuses(t *testing.T) {
	// The database already refuses a panel on a Document page; the shared check
	// is what the people endpoint and the document authority both apply.
	h, ws, owner := documentPeopleFixture(t, false)
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('mixed-page',?,'mixed','Mixed',?,'{}')`, ws, owner)
	execOrFatal(t, h.db, `INSERT INTO page_panels(id,page_id,panel_id,schema,owner_crew_id,producer_kind,producer_ref,sla_seconds) VALUES('mixed-panel','mixed-page','sensor','status.v1','crew-lookout','script','script/watch.sh',60)`)
	for page, want := range map[string]bool{"mixed-page": true, "people-page": false} {
		if other, err := documentPageHasOtherOutput(t.Context(), h.db, page); err != nil || other != want {
			t.Fatalf("%s: other output %v, %v", page, other, err)
		}
	}
}

func TestDocumentPeopleSearchMatchesInSQLAndTreatsWildcardsAsText(t *testing.T) {
	h, ws, owner := documentPeopleFixture(t, false)
	// More members than one scan used to read, all ahead of the reader alphabetically.
	for i := 0; i < 1100; i++ {
		id := fmt.Sprintf("aaa%04d", i)
		execOrFatal(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, id, id+"@example.invalid", "Aaron "+id)
		execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, id+"-m", ws, id)
	}
	execOrFatal(t, h.db, `INSERT INTO users(id,email,full_name) VALUES('zoe','zoe@example.invalid','Zoe 100% _Reader')`)
	execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('zoe-m',?,'zoe','MEMBER')`, ws)
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('people-page','user','zoe','read',?)`, owner)
	if _, found := documentPeopleCall(t, h, ws, owner, "OWNER", "q=zoe"); strings.Join(names(found), "|") != "zoe=Zoe 100% _Reader" {
		t.Fatalf("late member not found: %v", names(found))
	}
	if _, found := documentPeopleCall(t, h, ws, owner, "OWNER", "q="+url.QueryEscape("100% _")); len(found) != 1 {
		t.Fatalf("literal wildcard text: %v", names(found))
	}
	if _, found := documentPeopleCall(t, h, ws, owner, "OWNER", "q="+url.QueryEscape("%")); len(found) != 1 {
		t.Fatalf("%% must match only a literal percent sign: %v", names(found))
	}
}

func TestDocumentPeopleSearchFoldsUnicodeCase(t *testing.T) {
	h, ws, owner := documentPeopleFixture(t, false)
	execOrFatal(t, h.db, `INSERT INTO users(id,email,full_name) VALUES('sarka','sarka@example.invalid','Šárka Nováková')`)
	execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('sarka-m',?,'sarka','MEMBER')`, ws)
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('people-page','user','sarka','read',?)`, owner)
	for _, q := range []string{"šá", "ŠÁRKA", "nováková"} {
		if _, found := documentPeopleCall(t, h, ws, owner, "OWNER", "q="+url.QueryEscape(q)); strings.Join(names(found), "|") != "sarka=Šárka Nováková" {
			t.Fatalf("%q: %v", q, names(found))
		}
	}
}

func TestDocumentPeopleOnPrivateOutputSearchesOnlyItsPossibleReaders(t *testing.T) {
	h, ws, owner := documentPeopleFixture(t, true)
	// Many members sort ahead of the custodian; none of them can read private output.
	for i := 0; i < 250; i++ {
		id := fmt.Sprintf("aa%04d", i)
		execOrFatal(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, id, id+"@example.invalid", "Aa "+id)
		execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, id+"-m", ws, id)
	}
	execOrFatal(t, h.db, `INSERT INTO users(id,email,full_name) VALUES('adm','adm@example.invalid','Zed Admin')`)
	execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('adm-m',?,'adm','ADMIN')`, ws)
	if _, found := documentPeopleCall(t, h, ws, owner, "OWNER", "q="); strings.Join(names(found), "|") != "adm=Zed Admin|test-user-id=Test User" {
		t.Fatalf("private search: %v", names(found))
	}
}

func TestDocumentPeopleAdministratorReadIsAudited(t *testing.T) {
	for _, private := range []bool{true, false} {
		t.Run(fmt.Sprint("private=", private), func(t *testing.T) {
			h, ws, owner := documentPeopleFixture(t, private)
			execOrFatal(t, h.db, `INSERT INTO users(id,email,full_name) VALUES('adm','adm@example.invalid','Zed Admin')`)
			execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('adm-m',?,'adm','ADMIN')`, ws)
			action := auditActionWorkspaceAdminArtifactRead
			if private {
				action = auditActionPrivateArtifactRead
			}
			before := adminReadAudits(t, h, action, "adm", "people-page")
			if status, people := documentPeopleCall(t, h, ws, "adm", "ADMIN", "ids="+owner); status != 200 || len(people) != 1 {
				t.Fatalf("admin read: %d %v", status, people)
			}
			if after := adminReadAudits(t, h, action, "adm", "people-page"); after != before+1 {
				t.Fatalf("audit rows %d -> %d", before, after)
			}
			// The custodian's own read is not an administrator read.
			documentPeopleCall(t, h, ws, owner, "OWNER", "ids="+owner)
			if after := adminReadAudits(t, h, action, owner, "people-page"); after != 0 {
				t.Fatalf("owner read audited: %d", after)
			}
		})
	}
}

// Review round 4, nit 1: the access filter runs before the candidate limit, so
// a reader is found however many non-readers share the search text, through
// every way a person reaches a Document page: a user grant, a crew grant and
// the folder ACL (a Document page is always owned by a person).
func TestDocumentPeopleSearchFindsReadersBehindManyNonReaders(t *testing.T) {
	h, ws, owner := documentPeopleFixture(t, false)
	for i := 0; i < 300; i++ {
		id := fmt.Sprintf("al%04d", i)
		execOrFatal(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, id, id+"@example.invalid", "Alex "+id)
		execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, id+"-m", ws, id)
	}
	for _, person := range []string{"zgrant", "zcrew", "zfolder"} {
		execOrFatal(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, person, person+"@example.invalid", "Alex Z "+person)
		execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, person+"-m", ws, person)
	}
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('people-page','user','zgrant','read',?)`, owner)
	execOrFatal(t, h.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew-granted',?,'Granted','granted')`, ws)
	execOrFatal(t, h.db, `INSERT INTO crew_members(id,crew_id,user_id) VALUES('cm-zcrew','crew-granted','zcrew')`)
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('people-page','crew','crew-granted','read',?)`, owner)
	execOrFatal(t, h.db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at) VALUES('people-folder',?,'people-folder','Folder','crew-lookout',datetime('now'),datetime('now'))`, ws)
	execOrFatal(t, h.db, `INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_read,can_write,set_at) VALUES('people-folder','user','zfolder',1,0,datetime('now'))`)
	execOrFatal(t, h.db, `UPDATE pages SET folder_id='people-folder' WHERE id='people-page'`)
	_, found := documentPeopleCall(t, h, ws, owner, "OWNER", "q=alex")
	if strings.Join(names(found), "|") != "zcrew=Alex Z zcrew|zfolder=Alex Z zfolder|zgrant=Alex Z zgrant" {
		t.Fatalf("readers behind non-readers: %v", names(found))
	}
	// A retired grant to the whole workspace reaches nobody, so it must not
	// widen the candidates back to every member.
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('people-page','workspace','','comment',?)`, owner)
	execOrFatal(t, h.db, `UPDATE page_grants SET retired_at=datetime('now') WHERE page_id='people-page' AND subject_type='workspace'`)
	if _, found := documentPeopleCall(t, h, ws, owner, "OWNER", "q=alex"); len(found) != 3 {
		t.Fatalf("retired workspace grant widened the search: %v", names(found))
	}
	// A grant to the whole workspace makes every unrestricted member a reader.
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('people-page','workspace','',?,?)`, "read", owner)
	if _, found := documentPeopleCall(t, h, ws, owner, "OWNER", "q=alex"); len(found) != documentPeopleResultLimit {
		t.Fatalf("workspace grant: %d people", len(found))
	}
}

// Review round 4, nit 2: lookups are rate limited per person and workspace,
// with Retry-After; another person is unaffected.
func TestDocumentPeopleIsRateLimitedPerPerson(t *testing.T) {
	h, ws, owner := documentPeopleFixture(t, false)
	frozen := time.Now()
	h.documentPeople().now = func() time.Time { return frozen }
	limited := 0
	for i := 0; i < documentPeopleBurst+5; i++ {
		w := documentPeopleServe(t, h, documentPeopleRequest(t, h, ws, owner, "OWNER", "q=b"))
		if w.Code == http.StatusTooManyRequests {
			limited++
			if w.Header().Get("Retry-After") == "" {
				t.Fatal("429 without Retry-After")
			}
		} else if w.Code != 200 {
			t.Fatalf("call %d: %d", i, w.Code)
		}
	}
	if limited != 5 {
		t.Fatalf("limited %d of %d calls", limited, documentPeopleBurst+5)
	}
	if status, _ := documentPeopleCall(t, h, ws, "bea", "MEMBER", "q=b"); status != 200 {
		t.Fatalf("another reader limited: %d", status)
	}
	// The budget refills over time.
	frozen = frozen.Add(time.Second)
	if status, _ := documentPeopleCall(t, h, ws, owner, "OWNER", "q=b"); status != 200 {
		t.Fatalf("refilled budget refused: %d", status)
	}
}

// Review round 4, nit 3: an administrator's role-only lookups are audited once
// per Document and session, not once per keystroke; a new session is a new read.
func TestDocumentPeopleAdministratorAuditOncePerSession(t *testing.T) {
	for _, private := range []bool{true, false} {
		t.Run(fmt.Sprint("private=", private), func(t *testing.T) {
			h, ws, owner := documentPeopleFixture(t, private)
			execOrFatal(t, h.db, `INSERT INTO users(id,email,full_name) VALUES('adm','adm@example.invalid','Zed Admin')`)
			execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('adm-m',?,'adm','ADMIN')`, ws)
			action := auditActionWorkspaceAdminArtifactRead
			if private {
				action = auditActionPrivateArtifactRead
			}
			session := documentPeopleSession(t, h, "adm")
			for _, q := range []string{"q=t", "q=te", "q=tes", "ids=" + owner} {
				w := documentPeopleServe(t, h, documentPeopleSessionRequest(t, h, ws, "adm", "ADMIN", q, session))
				if w.Code != 200 {
					t.Fatalf("%s: %d", q, w.Code)
				}
			}
			if got := adminReadAudits(t, h, action, "adm", "people-page"); got != 1 {
				t.Fatalf("one session audited %d times", got)
			}
			w := documentPeopleServe(t, h, documentPeopleRequest(t, h, ws, "adm", "ADMIN", "q=t"))
			if w.Code != 200 || adminReadAudits(t, h, action, "adm", "people-page") != 2 {
				t.Fatalf("a new session must be audited again: %d", w.Code)
			}
			// The same session under a changed role is a new administrator read.
			execOrFatal(t, h.db, `UPDATE workspace_members SET role='OWNER' WHERE user_id='adm'`)
			w = documentPeopleServe(t, h, documentPeopleSessionRequest(t, h, ws, "adm", "OWNER", "q=t", session))
			if w.Code != 200 || adminReadAudits(t, h, action, "adm", "people-page") != 3 {
				t.Fatalf("a changed role must be audited again: %d", w.Code)
			}
			if private {
				return
			}
			// An administrator who also holds a grant reads without the role: no audit.
			execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('people-page','user','adm','read',?)`, owner)
			documentPeopleServe(t, h, documentPeopleRequest(t, h, ws, "adm", "OWNER", "q=t"))
			if got := adminReadAudits(t, h, action, "adm", "people-page"); got != 3 {
				t.Fatalf("granted administrator audited: %d", got)
			}
		})
	}
}
