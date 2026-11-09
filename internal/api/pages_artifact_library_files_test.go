package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// A pin carries the library's licensed fonts; every reader of the consumer may
// read them to render, nobody outside the consumer may, and losing access to
// the library changes nothing (#5248).
func TestArtifactLibraryPinFilesFollowTheConsumer(t *testing.T) {
	f := newLibraryFixture(t)
	font := "data:font/woff2;base64," + base64.StdEncoding.EncodeToString(append([]byte("wOF2"), make([]byte, 64)...))
	body := fmt.Sprintf(`{"base_version":1,"entrypoint":"index.html","files":[{"path":"index.html","media_type":"text/html","content":"<main>Library</main>"},{"path":"design-system.json","media_type":"application/json","content":%q},{"path":"fonts.json","media_type":"application/json","content":%q},{"path":"fonts/Inter.woff2","media_type":"font/woff2","content":%q},{"path":"fonts/OFL.txt","media_type":"text/plain","content":"SIL Open Font License, Version 1.1"}]}`,
		`{"colors":{"primary":"#111"}}`, `{"schema_version":1,"fonts":[{"file":"fonts/Inter.woff2","family":"Inter"}]}`, font)
	f.must(200, f.owner, "POST", "lib/artifact/versions", body)
	f.must(200, f.owner, "POST", "lib/artifact/share", `{"base_version":2,"version":2}`)
	f.must(200, f.owner, "POST", "lib/artifact/library/publish", `{"version":2}`)
	pin := f.must(200, "lib-editor", "PUT", "deck/artifact/library-pin", `{"library":"lib","version":2,"if_pinned_version":0}`)
	if !strings.Contains(fmt.Sprint(pin["files"]), "fonts/Inter.woff2") || strings.Contains(fmt.Sprint(pin["files"]), "wOF2") {
		t.Fatalf("pin lists files without bytes: %v", pin["files"])
	}

	// The library goes private: the reader of the consumer still renders.
	mustExec(t, f.h.db, `DELETE FROM page_grants WHERE page_id=?`, f.libID)
	got := f.must(200, "lib-reader", "GET", "deck/artifact/library-pin/files", "")
	files := got["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("files %v", files)
	}
	inter := files[0].(map[string]any)
	if inter["path"] != "fonts/Inter.woff2" || inter["status"] != "copied" || inter["family"] != "Inter" || "data:font/woff2;base64,"+inter["content"].(string) != font {
		t.Fatalf("font %v", inter)
	}
	// Another workspace's owner and an unpinned project see nothing.
	if w := f.call("outsider", "GET", "deck/artifact/library-pin/files", ""); w.Code != 404 {
		t.Fatalf("outsider %d", w.Code)
	}
	if w := f.call(f.owner, "GET", "lib/artifact/library-pin/files", ""); w.Code != 404 {
		t.Fatalf("unpinned project %d", w.Code)
	}
}

// Authority on the pinned files is exactly the authority on the pin (#5248
// review): the same status for every caller, a private consumer hidden from a
// member without read, and a run held to the page it is bound to.
func TestArtifactLibraryPinFilesShareThePinAuthority(t *testing.T) {
	f := newLibraryFixture(t)
	f.must(200, f.owner, "POST", "lib/artifact/library/publish", `{"version":1}`)
	f.must(201, f.owner, "POST", "artifacts", `{"slug":"private-deck","name":"Private deck",`+strings.TrimPrefix(artifactProjectBody(0, "Private"), "{"))
	f.must(200, f.owner, "PUT", "private-deck/artifact/library-pin", `{"library":"lib","version":1,"if_pinned_version":0}`)
	f.must(200, f.owner, "PUT", "deck/artifact/library-pin", `{"library":"lib","version":1,"if_pinned_version":0}`)
	f.member("lib-stranger", "MEMBER")
	for _, user := range []string{f.owner, "lib-editor", "lib-reader", "lib-admin", "lib-stranger", "outsider"} {
		for _, slug := range []string{"deck", "private-deck"} {
			pin := f.call(user, "GET", slug+"/artifact/library-pin", "")
			files := f.call(user, "GET", slug+"/artifact/library-pin/files", "")
			if pin.Code != files.Code {
				t.Errorf("%s on %s: pin %d, files %d %s", user, slug, pin.Code, files.Code, files.Body)
			}
			if user == "lib-stranger" && slug == "private-deck" && files.Code != 404 {
				t.Errorf("member without read on a private consumer: %d %s", files.Code, files.Body)
			}
		}
	}

	// A run whose consent binds it to the library page cannot read the deck's
	// pinned files, nor its pin.
	db := f.h.db
	mustExec(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('pin-crew',?,'Pin crew','pin-crew')`, f.ws)
	mustExec(t, db, `INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('pin-agent',?,'pin-crew','Pin agent','pin-agent')`, f.ws)
	mustExec(t, db, `INSERT INTO chats(id,workspace_id,agent_id,title) VALUES('pin-chat',?,'pin-agent','')`, f.ws)
	mustExec(t, db, `INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts,author_user_id) VALUES('pin-message','pin-chat','pin-agent','user','use the brand fonts','2026-11-09T00:00:00Z',?)`, f.owner)
	mustExec(t, db, `INSERT INTO artifact_run_principals(run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at) VALUES('pin-run',?,'pin-agent','pin-crew',?,'pin-chat','pin-message','m',1,'g',1,'2099-01-01T00:00:00Z')`, f.ws, f.owner)
	mustExec(t, db, `INSERT INTO artifact_project_run_consents(run_id,workspace_id,human_user_id,chat_id,message_id,expires_at,created_at,result_page_id) VALUES('pin-run',?,?,'pin-chat','pin-message','2099-01-01T00:00:00Z','2026-11-09T00:00:00Z',?)`, f.ws, f.owner, f.libID)
	run := &artifactRunAuthority{WorkspaceID: f.ws, AgentID: "pin-agent", CrewID: "pin-crew", RunID: "pin-run", HumanUserID: f.owner}
	for _, route := range []string{"library-pin", "library-pin/files"} {
		req := httptest.NewRequest("GET", "/api/v1/pages/deck/artifact/"+route, nil)
		req.Header.Set("Authorization", "Bearer "+f.tokens[f.owner])
		ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: f.owner}), f.ws, "OWNER")
		ctx = context.WithValue(context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken), artifactRunKey{}, run)
		req = req.WithContext(ctx)
		req.SetPathValue("slug", "deck")
		w := httptest.NewRecorder()
		if route == "library-pin" {
			f.h.GetArtifactLibraryPin(w, req)
		} else {
			f.h.GetArtifactLibraryPinFiles(w, req)
		}
		if w.Code != 404 || strings.Contains(w.Body.String(), "fonts/") {
			t.Fatalf("run bound to another page read %s: %d %s", route, w.Code, w.Body)
		}
	}
}
