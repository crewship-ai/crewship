package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

type libraryFixtureT struct {
	h       *PageHandler
	ws      string
	owner   string
	tokens  map[string]string
	t       *testing.T
	libID   string
	deckID  string
	members map[string]string // user -> workspace
}

// call routes one request the way the router does, with the caller's own CLI
// token and workspace.
func (f *libraryFixtureT) call(user, method, path, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, "/api/v1/pages/"+path, strings.NewReader(body))
	tok := f.tokens[user]
	if tok == "" {
		tok = mintTokenFor(f.t, f.h.db, user, "library-"+user)
		f.tokens[user] = tok
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	ws := f.members[user]
	if ws == "" {
		ws = f.ws
	}
	ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, "MEMBER")
	ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
	req = req.WithContext(ctx)
	slug, rest, _ := strings.Cut(path, "/")
	req.SetPathValue("slug", slug)
	w := httptest.NewRecorder()
	switch {
	case path == "artifacts":
		f.h.CreateArtifactProject(w, req)
	case rest == "artifact/versions":
		f.h.PublishArtifactProject(w, req)
	case rest == "artifact/share":
		f.h.ShareArtifactProject(w, req)
	case rest == "artifact/restore":
		f.h.RestoreArtifactProject(w, req)
	case rest == "artifact/library/publish":
		f.h.PublishArtifactLibrary(w, req)
	case rest == "artifact/library":
		f.h.GetArtifactLibrary(w, req)
	case rest == "artifact/library-pin" && method == "PUT":
		f.h.PutArtifactLibraryPin(w, req)
	case rest == "artifact/library-pin":
		f.h.GetArtifactLibraryPin(w, req)
	case rest == "artifact/library-pin/update":
		f.h.GetArtifactLibraryPinUpdate(w, req)
	default:
		f.t.Fatalf("unrouted %s %s", method, path)
	}
	return w
}

func (f *libraryFixtureT) must(code int, user, method, path, body string) map[string]any {
	f.t.Helper()
	w := f.call(user, method, path, body)
	if w.Code != code {
		f.t.Fatalf("%s %s %s: %d %s", user, method, path, w.Code, w.Body)
	}
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out
}

func libraryProjectBody(base int, tokens string) string {
	return fmt.Sprintf(`{"base_version":%d,"entrypoint":"index.html","files":[{"path":"index.html","media_type":"text/html","content":"<main>Library</main>"},{"path":"design-system.json","media_type":"application/json","content":%q}]}`, base, tokens)
}

func (f *libraryFixtureT) member(user, role string) {
	f.t.Helper()
	mustExec(f.t, f.h.db, `INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, user, user+"@example.com", user)
	mustExec(f.t, f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,?)`, generateCUID(), f.ws, user, role)
}

func (f *libraryFixtureT) grant(page, user, level string) {
	f.t.Helper()
	mustExec(f.t, f.h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,?,?,datetime('now'))`, page, user, level, f.owner)
}

// newLibraryFixture: owner's "lib" (v1 shared and published) and "deck"
// (consumer); editor holds Edit on both, reader holds Read on lib and Edit on
// deck, admin is a workspace ADMIN, outsider belongs to another workspace.
func newLibraryFixture(t *testing.T) *libraryFixtureT {
	h, ws, owner, _ := artifactProjectFixture(t)
	f := &libraryFixtureT{h: h, ws: ws, owner: owner, tokens: map[string]string{owner: "crewship_cli_artifact-project-owner"}, t: t, members: map[string]string{}}
	f.must(201, owner, "POST", "artifacts", `{"slug":"lib","name":"Brand library",`+strings.TrimPrefix(libraryProjectBody(0, `{"colors":{"primary":"#111"}}`), "{"))
	f.must(201, owner, "POST", "artifacts", `{"slug":"deck","name":"Deck",`+strings.TrimPrefix(artifactProjectBody(0, "Deck"), "{"))
	f.must(200, owner, "POST", "lib/artifact/share", `{"base_version":1,"version":1}`)
	f.must(200, owner, "POST", "deck/artifact/share", `{"base_version":1,"version":1}`)
	_ = h.db.QueryRow(`SELECT id FROM pages WHERE slug='lib'`).Scan(&f.libID)
	_ = h.db.QueryRow(`SELECT id FROM pages WHERE slug='deck'`).Scan(&f.deckID)
	f.member("lib-editor", "MEMBER")
	f.member("lib-reader", "MEMBER")
	f.member("lib-admin", "ADMIN")
	f.grant(f.libID, "lib-editor", "write")
	f.grant(f.deckID, "lib-editor", "write")
	f.grant(f.libID, "lib-reader", "read")
	f.grant(f.deckID, "lib-reader", "write")
	mustExec(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('ws-other','Other','other')`)
	mustExec(t, h.db, `INSERT INTO users(id,email,full_name) VALUES('outsider','outsider@example.com','Outsider')`)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,'ws-other','outsider','OWNER')`, generateCUID())
	f.members["outsider"] = "ws-other"
	return f
}

func (f *libraryFixtureT) published() int {
	var v int
	_ = f.h.db.QueryRow(`SELECT coalesce((SELECT version FROM artifact_library_publications WHERE page_id=?),0)`, f.libID).Scan(&v)
	return v
}

func TestArtifactLibraryPublishIsOwnerOnly(t *testing.T) {
	f := newLibraryFixture(t)
	for _, c := range []struct {
		user string
		code int
	}{
		{"lib-editor", 403},
		{"lib-reader", 403},
		{"lib-admin", 403},
		{"outsider", 404},
	} {
		t.Run(c.user, func(t *testing.T) {
			if w := f.call(c.user, "POST", "lib/artifact/library/publish", `{"version":1}`); w.Code != c.code {
				t.Fatalf("publish %d %s", w.Code, w.Body)
			}
			if f.published() != 0 {
				t.Fatal("non-owner moved the publication")
			}
			if c.user != "outsider" {
				if got := f.must(200, c.user, "GET", "lib/artifact/library", ""); got["can_publish"] != false {
					t.Fatalf("can_publish %v", got)
				}
			}
		})
	}
	got := f.must(200, f.owner, "POST", "lib/artifact/library/publish", `{"version":1}`)
	if got["published_version"] != float64(1) || f.published() != 1 {
		t.Fatalf("owner publish %v", got)
	}
	if got = f.must(200, f.owner, "GET", "lib/artifact/library", ""); got["can_publish"] != true {
		t.Fatalf("owner can_publish %v", got)
	}
	var audits, entries int
	_ = f.h.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='artifact.library_published'`).Scan(&audits)
	_ = f.h.db.QueryRow(`SELECT count(*) FROM journal_entries WHERE entry_type='artifact.library_published'`).Scan(&entries)
	if audits != 1 || entries != 1 {
		t.Fatalf("publish audit %d journal %d", audits, entries)
	}
	// Editors still edit drafts; publishing stays where the owner left it.
	f.must(200, "lib-editor", "POST", "lib/artifact/versions", libraryProjectBody(1, `{"colors":{"primary":"#999"}}`))
	if f.published() != 1 {
		t.Fatal("an edit moved the publication")
	}
}

func TestArtifactLibraryPinCopyUpdateAndRevocation(t *testing.T) {
	f := newLibraryFixture(t)
	unpublished := f.call("lib-editor", "PUT", "deck/artifact/library-pin", `{"library":"lib","version":1,"if_pinned_version":0}`)
	missing := f.call("lib-editor", "PUT", "deck/artifact/library-pin", `{"library":"no-such-library","version":1,"if_pinned_version":0}`)
	if unpublished.Code != 404 || missing.Code != 404 || unpublished.Body.String() != missing.Body.String() {
		t.Fatalf("unpublished and absent libraries differ: %d %s / %d %s", unpublished.Code, unpublished.Body, missing.Code, missing.Body)
	}
	f.must(200, f.owner, "POST", "lib/artifact/library/publish", `{"version":1}`)
	if w := f.call("lib-editor", "PUT", "deck/artifact/library-pin", `{"library":"lib"}`); w.Code != 400 {
		t.Fatalf("missing if_pinned_version %d", w.Code)
	}
	pin := f.must(200, "lib-editor", "PUT", "deck/artifact/library-pin", `{"library":"lib","version":1,"if_pinned_version":0}`)
	if pin["library_version"] != float64(1) || pin["changed"] != true || pin["library_name"] != "Brand library" || !strings.Contains(fmt.Sprint(pin["tokens"]), "#111") {
		t.Fatalf("pin %v", pin)
	}
	again := f.must(200, "lib-editor", "PUT", "deck/artifact/library-pin", `{"library":"lib","version":1,"if_pinned_version":0}`)
	if again["changed"] != false || again["copied_at"] != pin["copied_at"] {
		t.Fatalf("pin is not idempotent %v", again)
	}

	// The owner edits v2, publishes it, then restores v1 as v3 without
	// republishing: neither restore nor publication touches the pin.
	f.must(200, f.owner, "POST", "lib/artifact/versions", libraryProjectBody(1, `{"colors":{"primary":"#222","accent":"#f00"}}`))
	f.must(200, f.owner, "POST", "lib/artifact/library/publish", `{"version":2}`)
	f.must(200, f.owner, "POST", "lib/artifact/restore", `{"base_version":2,"version":1}`)
	if got := f.must(200, "lib-editor", "GET", "deck/artifact/library-pin", ""); got["library_version"] != float64(1) || got["sha256"] != pin["sha256"] {
		t.Fatalf("restore changed the pin %v", got)
	}
	if f.published() != 2 {
		t.Fatal("restore moved the publication")
	}

	// The editor covers only the latest library version (3), so v2 is not
	// readable to them and the review says the library is unavailable.
	if up := f.must(200, "lib-editor", "GET", "deck/artifact/library-pin/update", ""); up["library_available"] != false || up["update_available"] != false {
		t.Fatalf("editor coverage %v", up)
	}
	// The reader covers only the shared version; after the owner shares v2
	// the reader sees the update, reviews it and applies it.
	if w := f.call("lib-reader", "PUT", "deck/artifact/library-pin", `{"library":"lib","version":2,"if_pinned_library":"lib","if_pinned_version":1}`); w.Code != 404 || w.Body.String() != missing.Body.String() {
		t.Fatalf("reader pinned an unshared version %d %s", w.Code, w.Body)
	}
	f.must(200, f.owner, "POST", "lib/artifact/share", `{"base_version":3,"version":2}`)
	up := f.must(200, "lib-reader", "GET", "deck/artifact/library-pin/update", "")
	changes, _ := up["changes"].(map[string]any)
	if up["update_available"] != true || up["published_version"] != float64(2) || fmt.Sprint(changes["added"]) != "[colors.accent]" || fmt.Sprint(changes["changed"]) != "[colors.primary]" {
		t.Fatalf("review %v", up)
	}
	if w := f.call("lib-reader", "PUT", "deck/artifact/library-pin", `{"library":"lib","version":2,"if_pinned_version":0}`); w.Code != 409 {
		t.Fatalf("stale apply %d %s", w.Code, w.Body)
	}
	if got := f.must(200, "lib-reader", "PUT", "deck/artifact/library-pin", `{"library":"lib","version":2,"if_pinned_library":"lib","if_pinned_version":1}`); got["library_version"] != float64(2) || got["changed"] != true {
		t.Fatalf("apply %v", got)
	}

	// Revoking the reader's library grant keeps the consumer rendering from
	// its copy but blocks the next update.
	mustExec(t, f.h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id='lib-reader'`, f.libID)
	f.must(200, f.owner, "POST", "lib/artifact/versions", libraryProjectBody(3, `{"colors":{"primary":"#444"}}`))
	f.must(200, f.owner, "POST", "lib/artifact/library/publish", `{"version":4}`)
	f.must(200, f.owner, "POST", "lib/artifact/share", `{"base_version":4,"version":4}`)
	if got := f.must(200, "lib-reader", "GET", "deck/artifact/library-pin", ""); got["library_version"] != float64(2) || !strings.Contains(fmt.Sprint(got["tokens"]), "#222") {
		t.Fatalf("revoked reader lost the copy %v", got)
	}
	if up = f.must(200, "lib-reader", "GET", "deck/artifact/library-pin/update", ""); up["library_available"] != false || up["published_version"] != float64(0) {
		t.Fatalf("revoked review leaks %v", up)
	}
	if w := f.call("lib-reader", "PUT", "deck/artifact/library-pin", `{"library":"lib","version":4,"if_pinned_library":"lib","if_pinned_version":2}`); w.Code != 404 {
		t.Fatalf("revoked update %d %s", w.Code, w.Body)
	}
	// Consumer access is still the consumer's own: the outsider sees nothing.
	if w := f.call("outsider", "GET", "deck/artifact/library-pin", ""); w.Code != 404 {
		t.Fatalf("outsider read pin %d", w.Code)
	}
}

func TestArtifactLibraryPinDeniesOtherWorkspaceAndAdminWrites(t *testing.T) {
	f := newLibraryFixture(t)
	f.must(200, f.owner, "POST", "lib/artifact/library/publish", `{"version":1}`)
	// The outsider's own consumer in another workspace cannot name this library.
	f.must(201, "outsider", "POST", "artifacts", `{"slug":"their-deck","name":"Theirs",`+strings.TrimPrefix(artifactProjectBody(0, "Theirs"), "{"))
	for _, ref := range []string{"lib", "@" + f.libID} {
		if w := f.call("outsider", "PUT", "their-deck/artifact/library-pin", `{"library":"`+ref+`","version":1,"if_pinned_version":0}`); w.Code != 404 {
			t.Fatalf("cross-workspace pin %s: %d %s", ref, w.Code, w.Body)
		}
	}
	// A workspace admin reads with audit but holds no write on the consumer.
	if w := f.call("lib-admin", "PUT", "deck/artifact/library-pin", `{"library":"lib","version":1,"if_pinned_version":0}`); w.Code != 403 && w.Code != 404 {
		t.Fatalf("admin pinned %d %s", w.Code, w.Body)
	}
	var pins int
	_ = f.h.db.QueryRow(`SELECT count(*) FROM artifact_library_pins`).Scan(&pins)
	if pins != 0 {
		t.Fatal("denied pin persisted", pins)
	}
	// A project cannot pin itself.
	if w := f.call(f.owner, "PUT", "lib/artifact/library-pin", `{"library":"lib","version":1,"if_pinned_version":0}`); w.Code != 400 {
		t.Fatalf("self pin %d %s", w.Code, w.Body)
	}
}

func TestArtifactLibraryPinChecksAuthorityBeforeResolvingReferences(t *testing.T) {
	f := newLibraryFixture(t)
	f.must(200, f.owner, "POST", "lib/artifact/library/publish", `{"version":1}`)
	for _, ref := range []string{"lib", "@" + f.libID, "no-such-library"} {
		w := f.call("lib-admin", "PUT", "deck/artifact/library-pin", fmt.Sprintf(`{"library":%q,"version":1,"if_pinned_version":0}`, ref))
		if w.Code != 403 {
			t.Errorf("without consumer write, ref %q returned %d: %s", ref, w.Code, w.Body)
		}
	}
	// Consumer write alone must not distinguish an unreadable publication from a missing one,
	// including when the reviewed pin reference is stale.
	f.must(201, f.owner, "POST", "artifacts", `{"slug":"secret","name":"Secret",`+strings.TrimPrefix(libraryProjectBody(0, `{"colors":{"primary":"#abc"}}`), "{"))
	f.must(200, f.owner, "POST", "secret/artifact/library/publish", `{"version":1}`)
	for _, ref := range []string{"secret", "no-such-library"} {
		w := f.call("lib-reader", "PUT", "deck/artifact/library-pin", fmt.Sprintf(`{"library":%q,"version":1,"if_pinned_library":"missing-old","if_pinned_version":1}`, ref))
		if w.Code != 404 {
			t.Errorf("unreadable publication ref %q returned %d: %s", ref, w.Code, w.Body)
		}
	}
}

// A workspace admin reads a private library with audit, but that read must
// never become a durable copy in the admin's own project, where its readers
// would see the private tokens.
func TestArtifactLibraryAdminCannotPinAPrivateLibrary(t *testing.T) {
	f := newLibraryFixture(t)
	f.must(201, f.owner, "POST", "artifacts", `{"slug":"secret","name":"Secret library",`+strings.TrimPrefix(libraryProjectBody(0, `{"colors":{"primary":"#abc"}}`), "{"))
	f.must(200, f.owner, "POST", "secret/artifact/library/publish", `{"version":1}`)
	f.must(201, "lib-admin", "POST", "artifacts", `{"slug":"admin-deck","name":"Admin deck",`+strings.TrimPrefix(artifactProjectBody(0, "Admin"), "{"))
	var before int
	_ = f.h.db.QueryRow(`SELECT count(*) FROM audit_logs`).Scan(&before)
	missing := f.call("lib-admin", "PUT", "admin-deck/artifact/library-pin", `{"library":"no-such-library","version":1,"if_pinned_version":0}`)
	for _, ref := range []string{"secret", "lib"} {
		// "lib" is shared but the admin holds no grant on it: role-only reach is
		// the audited admin path and is excluded too.
		w := f.call("lib-admin", "PUT", "admin-deck/artifact/library-pin", `{"library":"`+ref+`","version":1,"if_pinned_version":0}`)
		if w.Code != 404 || w.Body.String() != missing.Body.String() {
			t.Fatalf("admin pinned %s: %d %s", ref, w.Code, w.Body)
		}
	}
	var after, pins int
	_ = f.h.db.QueryRow(`SELECT count(*) FROM audit_logs`).Scan(&after)
	_ = f.h.db.QueryRow(`SELECT count(*) FROM artifact_library_pins`).Scan(&pins)
	if after != before || pins != 0 {
		t.Fatalf("refused admin pin left audit %d->%d, pins %d", before, after, pins)
	}
	// The owner may pin their own private library into their own project.
	if got := f.must(200, f.owner, "PUT", "deck/artifact/library-pin", `{"library":"secret","version":1,"if_pinned_version":0}`); got["library_version"] != float64(1) {
		t.Fatalf("owner pin %v", got)
	}
	// A reader with Edit on the consumer but no access to the private library cannot.
	if w := f.call("lib-reader", "PUT", "deck/artifact/library-pin", `{"library":"secret","version":1,"if_pinned_library":"secret","if_pinned_version":1}`); w.Code != 404 {
		t.Fatalf("reader switch to unshared library %d %s", w.Code, w.Body)
	}
	if w := f.call("lib-editor", "GET", "deck/artifact/library-pin/update", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"library_available":false`) {
		t.Fatalf("editor review of private library %d %s", w.Code, w.Body)
	}
}

func TestArtifactLibraryApplyRequiresTheReviewedPublication(t *testing.T) {
	f := newLibraryFixture(t)
	f.must(200, f.owner, "POST", "lib/artifact/library/publish", `{"version":1}`)
	f.must(200, f.owner, "PUT", "deck/artifact/library-pin", `{"library":"lib","version":1,"if_pinned_version":0}`)
	f.must(200, f.owner, "POST", "lib/artifact/versions", libraryProjectBody(1, `{"colors":{"primary":"#222"}}`))
	f.must(200, f.owner, "POST", "lib/artifact/library/publish", `{"version":2}`)
	// Reviewed v2; meanwhile the owner publishes v3.
	f.must(200, f.owner, "POST", "lib/artifact/versions", libraryProjectBody(2, `{"colors":{"primary":"#333"}}`))
	f.must(200, f.owner, "POST", "lib/artifact/library/publish", `{"version":3}`)
	if w := f.call(f.owner, "PUT", "deck/artifact/library-pin", `{"library":"lib","version":2,"if_pinned_library":"lib","if_pinned_version":1}`); w.Code != 409 {
		t.Fatalf("applied an unreviewed publication %d %s", w.Code, w.Body)
	}
	// A wrong pinned library with the right version number is stale too.
	f.must(201, f.owner, "POST", "artifacts", `{"slug":"lib-two","name":"Second library",`+strings.TrimPrefix(libraryProjectBody(0, `{"colors":{"primary":"#999"}}`), "{"))
	if w := f.call(f.owner, "PUT", "deck/artifact/library-pin", `{"library":"lib","version":3,"if_pinned_library":"lib-two","if_pinned_version":1}`); w.Code != 409 {
		t.Fatalf("wrong pinned library accepted %d %s", w.Code, w.Body)
	}
	if got := f.must(200, f.owner, "GET", "deck/artifact/library-pin", ""); got["library_version"] != float64(1) {
		t.Fatalf("pin moved %v", got)
	}
	if got := f.must(200, f.owner, "PUT", "deck/artifact/library-pin", `{"library":"lib","version":3,"if_pinned_library":"@`+f.libID+`","if_pinned_version":1}`); got["library_version"] != float64(3) {
		t.Fatalf("reviewed apply %v", got)
	}
}

func TestArtifactLibraryPublishFollowsOwnershipTransfer(t *testing.T) {
	f := newLibraryFixture(t)
	mustExec(t, f.h.db, `UPDATE pages SET owner_user_id='lib-editor' WHERE id=?`, f.libID)
	if w := f.call(f.owner, "POST", "lib/artifact/library/publish", `{"version":1}`); w.Code == 200 {
		t.Fatalf("former owner published: %s", w.Body)
	}
	if f.published() != 0 {
		t.Fatal("former owner moved the publication")
	}
	if got := f.must(200, "lib-editor", "POST", "lib/artifact/library/publish", `{"version":1}`); got["published_version"] != float64(1) {
		t.Fatalf("new owner publish %v", got)
	}
}

func TestArtifactLibraryAgentRunsCannotPublishOrPin(t *testing.T) {
	f := newLibraryFixture(t)
	f.must(200, f.owner, "POST", "lib/artifact/library/publish", `{"version":1}`)
	run := &artifactRunAuthority{WorkspaceID: f.ws, AgentID: "lib-agent", CrewID: "crew-lookout", RunID: "lib-run", HumanUserID: f.owner}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "lib/artifact/library/publish", `{"version":1}`},
		{"PUT", "deck/artifact/library-pin", `{"library":"lib","version":1,"if_pinned_version":0}`},
	} {
		req := httptest.NewRequest(c.method, "/api/v1/pages/"+c.path, strings.NewReader(c.body))
		req.Header.Set("Authorization", "Bearer "+f.tokens[f.owner])
		ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: f.owner}), f.ws, "OWNER")
		ctx = context.WithValue(context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken), artifactRunKey{}, run)
		req = req.WithContext(ctx)
		slug, _, _ := strings.Cut(c.path, "/")
		req.SetPathValue("slug", slug)
		w := httptest.NewRecorder()
		if c.method == "POST" {
			f.h.PublishArtifactLibrary(w, req)
		} else {
			f.h.PutArtifactLibraryPin(w, req)
		}
		if w.Code != 403 && w.Code != 404 {
			t.Fatalf("agent run %s %s: %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	var pins, entries int
	_ = f.h.db.QueryRow(`SELECT count(*) FROM artifact_library_pins`).Scan(&pins)
	_ = f.h.db.QueryRow(`SELECT count(*) FROM journal_entries WHERE entry_type='artifact.library_published'`).Scan(&entries)
	if pins != 0 || entries != 1 {
		t.Fatalf("agent run left pins %d, publish entries %d", pins, entries)
	}
}

// The guard itself: whatever admits the run, an "agent:" actor never publishes
// or pins.
func TestRejectArtifactAgentActor(t *testing.T) {
	for _, c := range []struct {
		actor string
		ok    bool
	}{{"user-1", true}, {"agent:a:run", false}} {
		got, err := rejectArtifactAgentActor(func(context.Context, *sql.Tx) (string, error) { return c.actor, nil })(t.Context(), nil)
		if (err == nil) != c.ok || (c.ok && got != c.actor) {
			t.Fatalf("%s: %q %v", c.actor, got, err)
		}
	}
}

// A pin whose stored bytes no longer match their sha256 is refused with a
// specific conflict rather than served or reported as a generic failure.
func TestArtifactLibraryCorruptPinIsAConflict(t *testing.T) {
	f := newLibraryFixture(t)
	f.must(200, f.owner, "POST", "lib/artifact/library/publish", `{"version":1}`)
	f.must(200, "lib-editor", "PUT", "deck/artifact/library-pin", `{"library":"lib","version":1,"if_pinned_version":0}`)
	mustExec(t, f.h.db, `UPDATE artifact_library_pins SET content=CAST('{"colors":{"primary":"#bad"}}' AS BLOB) WHERE page_id=?`, f.deckID)
	for _, path := range []string{"deck/artifact/library-pin", "deck/artifact/library-pin/update"} {
		w := f.call("lib-editor", "GET", path, "")
		if w.Code != 409 || !strings.Contains(w.Body.String(), "The pinned copy is damaged; pin the library again.") || !strings.Contains(w.Body.String(), `"code":"artifact_library_pin_corrupt"`) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "#bad") {
			t.Fatalf("%s served corrupt bytes", path)
		}
	}
}

func TestArtifactLibraryPinCanRepairCorruptCopyWithReviewedState(t *testing.T) {
	f := newLibraryFixture(t)
	f.must(200, f.owner, "POST", "lib/artifact/library/publish", `{"version":1}`)
	f.must(200, "lib-editor", "PUT", "deck/artifact/library-pin", `{"library":"lib","version":1,"if_pinned_version":0}`)
	mustExec(t, f.h.db, `UPDATE artifact_library_pins SET content=CAST('{"colors":{"primary":"#bad"}}' AS BLOB) WHERE page_id=?`, f.deckID)
	f.must(409, "lib-editor", "PUT", "deck/artifact/library-pin", `{"library":"lib","version":1,"if_pinned_version":0}`)
	got := f.must(200, "lib-editor", "PUT", "deck/artifact/library-pin", `{"library":"lib","version":1,"if_pinned_library":"lib","if_pinned_version":1}`)
	if got["changed"] != true || !strings.Contains(fmt.Sprint(got["tokens"]), "#111") {
		t.Fatalf("repair did not replace the corrupt copy: %v", got)
	}
	retry := f.must(200, "lib-editor", "PUT", "deck/artifact/library-pin", `{"library":"lib","version":1,"if_pinned_library":"lib","if_pinned_version":1}`)
	if retry["changed"] != false {
		t.Fatalf("repair retry mutated an intact copy: %v", retry)
	}
}

// Library coverage follows every human grant source, not only direct user
// grants: a crew (group) Edit grant covers the latest library version, a
// folder ACL Read covers only the shared version, and a folder Read on the
// consumer is not consumer write authority.
func TestArtifactLibraryPinHonoursGroupAndFolderGrants(t *testing.T) {
	f := newLibraryFixture(t)
	db := f.h.db
	f.must(200, f.owner, "POST", "lib/artifact/versions", libraryProjectBody(1, `{"colors":{"primary":"#222"}}`))
	f.must(200, f.owner, "POST", "lib/artifact/library/publish", `{"version":2}`)
	for _, slug := range []string{"crew-deck", "folder-deck", "folder-only-deck"} {
		f.must(201, f.owner, "POST", "artifacts", `{"slug":"`+slug+`","name":"Consumer",`+strings.TrimPrefix(artifactProjectBody(0, "Consumer"), "{"))
		f.must(200, f.owner, "POST", slug+"/artifact/share", `{"base_version":1,"version":1}`)
	}
	page := func(slug string) string {
		var id string
		if err := db.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug=?`, f.ws, slug).Scan(&id); err != nil {
			t.Fatal(slug, err)
		}
		return id
	}
	f.member("crew-editor", "MEMBER")
	f.member("folder-reader", "MEMBER")
	mustExec(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('library-crew',?,'Library crew','library-crew')`, f.ws)
	mustExec(t, db, `INSERT INTO crew_members(id,crew_id,user_id) VALUES(?,'library-crew','crew-editor')`, generateCUID())
	for _, id := range []string{f.libID, page("crew-deck")} {
		mustExec(t, db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'crew','library-crew','write',?,datetime('now'))`, id, f.owner)
	}
	mustExec(t, db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at) VALUES('library-folder',?,'library-folder','Folder','library-crew',datetime('now'),datetime('now'))`, f.ws)
	mustExec(t, db, `UPDATE pages SET folder_id='library-folder' WHERE id IN (?,?)`, f.libID, page("folder-only-deck"))
	mustExec(t, db, `INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_read,can_write,set_at) VALUES('library-folder','user','folder-reader',1,0,datetime('now'))`)
	f.grant(page("folder-deck"), "folder-reader", "write")

	// Crew Edit covers the latest (unshared) published version.
	if got := f.must(200, "crew-editor", "PUT", "crew-deck/artifact/library-pin", `{"library":"lib","version":2,"if_pinned_version":0}`); got["library_version"] != float64(2) {
		t.Fatalf("crew editor pin %v", got)
	}
	// Folder Read covers only the shared version (1), so v2 is refused …
	missing := f.call("folder-reader", "PUT", "folder-deck/artifact/library-pin", `{"library":"no-such-library","version":2,"if_pinned_version":0}`)
	if w := f.call("folder-reader", "PUT", "folder-deck/artifact/library-pin", `{"library":"lib","version":2,"if_pinned_version":0}`); w.Code != 404 || w.Body.String() != missing.Body.String() {
		t.Fatalf("folder reader pinned an unshared version %d %s", w.Code, w.Body)
	}
	// … until the owner shares it.
	f.must(200, f.owner, "POST", "lib/artifact/share", `{"base_version":2,"version":2}`)
	if got := f.must(200, "folder-reader", "PUT", "folder-deck/artifact/library-pin", `{"library":"lib","version":2,"if_pinned_version":0}`); got["library_version"] != float64(2) {
		t.Fatalf("folder reader pin %v", got)
	}
	// Folder Read on a consumer is not write authority on it.
	if w := f.call("folder-reader", "PUT", "folder-only-deck/artifact/library-pin", `{"library":"lib","version":2,"if_pinned_version":0}`); w.Code != 403 && w.Code != 404 {
		t.Fatalf("folder reader wrote a read-only consumer %d %s", w.Code, w.Body)
	}
	var pins int
	_ = db.QueryRow(`SELECT count(*) FROM artifact_library_pins WHERE page_id=?`, page("folder-only-deck")).Scan(&pins)
	if pins != 0 {
		t.Fatal("read-only consumer gained a pin")
	}
	// Leaving the crew removes library coverage; the existing copy stays.
	mustExec(t, db, `DELETE FROM crew_members WHERE crew_id='library-crew' AND user_id='crew-editor'`)
	mustExec(t, db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user','crew-editor','write',?,datetime('now'))`, page("crew-deck"), f.owner)
	if up := f.must(200, "crew-editor", "GET", "crew-deck/artifact/library-pin/update", ""); up["library_available"] != false {
		t.Fatalf("former crew member still covers the library %v", up)
	}
	if got := f.must(200, "crew-editor", "GET", "crew-deck/artifact/library-pin", ""); got["library_version"] != float64(2) {
		t.Fatalf("copy lost %v", got)
	}
}

// An if_pinned_library slug that no longer resolves (renamed or deleted) is a
// stale review, not a missing Library: 409, and the pin is unchanged.
func TestArtifactLibraryUnresolvablePinnedLibraryIsAConflict(t *testing.T) {
	f := newLibraryFixture(t)
	f.must(200, f.owner, "POST", "lib/artifact/library/publish", `{"version":1}`)
	f.must(200, "lib-editor", "PUT", "deck/artifact/library-pin", `{"library":"lib","version":1,"if_pinned_version":0}`)
	w := f.call("lib-editor", "PUT", "deck/artifact/library-pin", `{"library":"lib","version":1,"if_pinned_library":"renamed-away","if_pinned_version":1}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"artifact_version_conflict"`) {
		t.Fatalf("unresolvable if_pinned_library %d %s", w.Code, w.Body)
	}
	if got := f.must(200, "lib-editor", "GET", "deck/artifact/library-pin", ""); got["library_version"] != float64(1) {
		t.Fatalf("pin changed %v", got)
	}
}
