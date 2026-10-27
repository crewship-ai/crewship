package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
)

func artifactProjectFixture(t *testing.T) (*PageHandler, string, string, func(string, string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	h, _, _, ws, owner := newPagesFixture(t)
	h.db = artifactProjectFullDurability(t, h.db)
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	token := mintTokenFor(t, h.db, owner, "artifact-project-owner")
	tokens := map[string]string{owner: token}
	call := func(user, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/pages/"+path, strings.NewReader(body))
		tok := tokens[user]
		if tok == "" {
			tok = mintTokenFor(t, h.db, user, "artifact-reader")
			tokens[user] = tok
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, "OWNER")
		ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
		req = req.WithContext(ctx)
		req.SetPathValue("slug", "three-screens")
		w := httptest.NewRecorder()
		switch {
		case path == "artifacts":
			h.CreateArtifactProject(w, req)
		case strings.Contains(path, "restore"):
			h.RestoreArtifactProject(w, req)
		case strings.Contains(path, "share"):
			h.ShareArtifactProject(w, req)
		case strings.Contains(path, "comments") && method == "POST":
			h.CreateArtifactProjectComment(w, req)
		case strings.Contains(path, "versions") && method == "POST":
			h.PublishArtifactProject(w, req)
		default:
			h.GetArtifactProject(w, req)
		}
		return w
	}
	return h, ws, owner, call
}
func artifactProjectBody(base int, label string) string {
	return fmt.Sprintf(`{"base_version":%d,"entrypoint":"index.html","files":[{"path":"index.html","media_type":"text/html","content":%q},{"path":"details.html","media_type":"text/html","content":"<h1 id=details>Details</h1><a href=settings.html>Settings</a>"},{"path":"settings.html","media_type":"text/html","content":"<h1 id=settings>Settings</h1><a href=index.html>Home</a>"}]}`, base, `<h1 id="hero">`+label+`</h1><a href="details.html">Details</a>`)
}
func TestArtifactProjectAtomicVersionsRestoreAndSharedReader(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	body := strings.TrimPrefix(artifactProjectBody(0, "Version one"), "{")
	created := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Three screens",`+body)
	if created.Code != 201 {
		t.Fatalf("create %d %s", created.Code, created.Body)
	}
	reader := "artifact-reader"
	mustExec(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?,'artifact-reader@example.com','Reader')`, reader)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, reader)
	if got := call(reader, "GET", "three-screens/artifact", ""); got.Code != 404 {
		t.Fatalf("private read %d", got.Code)
	}
	if got := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); got.Code != 200 {
		t.Fatalf("share %d %s", got.Code, got.Body)
	}
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='three-screens'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'read',?,datetime('now'))`, page, reader, owner)
	if got := call(owner, "POST", "three-screens/artifact/versions", artifactProjectBody(1, "Version two")); got.Code != 200 {
		t.Fatalf("v2 %d %s", got.Code, got.Body)
	}
	if got := call(owner, "POST", "three-screens/artifact/versions", artifactProjectBody(1, "Lost edit")); got.Code != 409 {
		t.Fatalf("CAS %d %s", got.Code, got.Body)
	}
	got := call(reader, "GET", "three-screens/artifact", "")
	if got.Code != 200 || strings.Contains(got.Body.String(), "Version two") || !strings.Contains(got.Body.String(), "Version one") {
		t.Fatalf("pinned reader %d %s", got.Code, got.Body)
	}
	for _, suffix := range []string{"versions", "restore", "share", "comments"} {
		out := call(reader, "POST", "three-screens/artifact/"+suffix, artifactProjectBody(2, "reader write"))
		if out.Code != 403 && out.Code != 404 {
			t.Errorf("reader %s %d %s", suffix, out.Code, out.Body)
		}
	}
	if got := call(owner, "POST", "three-screens/artifact/restore", `{"base_version":2,"version":1}`); got.Code != 200 {
		t.Fatalf("restore %d %s", got.Code, got.Body)
	}
	fresh := NewPageHandler(h.db, nil, newTestLogger())
	_ = fresh
	var latest int
	if err := h.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id=?`, page).Scan(&latest); err != nil || latest != 3 {
		t.Fatalf("restore head %d %v", latest, err)
	}
}
func TestArtifactProjectCommentsRejectStaleAnchorAndRevokedCredentials(t *testing.T) {
	h, _, owner, call := artifactProjectFixture(t)
	body := strings.TrimPrefix(artifactProjectBody(0, "Version one"), "{")
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Three screens",`+body); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	req := `{"version":1,"path":"index.html","selector":"#hero","quote":"Version one","text":"Make this title blue","send_to_agent":false}`
	if w := call(owner, "POST", "three-screens/artifact/comments", req); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(owner, "POST", "three-screens/artifact/versions", artifactProjectBody(1, "Version two")); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(owner, "POST", "three-screens/artifact/comments", req); w.Code != 409 {
		t.Fatal("stale anchor", w.Code, w.Body)
	}
	mustExec(t, h.db, `UPDATE cli_tokens SET revoked_at=datetime('now') WHERE user_id=?`, owner)
	if w := call(owner, "GET", "three-screens/artifact", ""); w.Code != 403 && w.Code != 401 {
		t.Fatal("revoked", w.Code, w.Body)
	}
	var n int
	_ = h.db.QueryRow(`SELECT count(*) FROM artifact_project_comments`).Scan(&n)
	if n != 1 {
		t.Fatal("comment count", n)
	}
}
func TestArtifactProjectDefaultOffAndInvalidCreateRollsBack(t *testing.T) {
	h, _, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "")
	if w := call(owner, "POST", "artifacts", `{}`); w.Code != 404 {
		t.Fatal(w.Code, w.Body)
	}
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Project","entrypoint":"missing.html","files":[{"path":"index.html","content":"bad","media_type":"text/html"}]}`); w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	var n int
	_ = h.db.QueryRow(`SELECT count(*) FROM pages WHERE slug='three-screens'`).Scan(&n)
	if n != 0 {
		t.Fatal("partial Page survived", n)
	}
}

func TestArtifactProjectCannotAttachToExistingPageAndAnchorsFailClosed(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	pagesCreate(t, h, ws, owner, "three-screens")
	w := call(owner, "POST", "three-screens/artifact/versions", artifactProjectBody(0, "unwanted format"))
	if w.Code != 404 {
		t.Fatalf("existing Page became project %d %s", w.Code, w.Body)
	}
	var n int
	_ = h.db.QueryRow(`SELECT count(*) FROM artifact_version_heads`).Scan(&n)
	if n != 0 {
		t.Fatal("mixed content attached", n)
	}
	for _, test := range []struct {
		html, selector, quote string
		want                  bool
	}{
		{`<h1 id="hero">Original title</h1>`, "#hero", "Original title", true},
		{`<h1 id="hero">Changed title</h1>`, "#hero", "Original title", false},
		{`<h1 id="hero">Original title</h1><p id="hero">Original title</p>`, "#hero", "Original title", false},
		{`<h1 id="other">Original title</h1>`, "#hero", "Original title", false},
		{`<h1 id="hero">Original title</h1>`, "body > h1:nth-child(1)", "Original title", false},
	} {
		if got := artifactProjectAnchor([]byte(test.html), test.selector, test.quote); got != test.want {
			t.Errorf("anchor %q %q got %v", test.html, test.selector, got)
		}
	}
}
func TestArtifactProjectWorkspaceFlagAndRevocation(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "")
	mustExec(t, h.db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('artifact-project-override','ff_artifact_projects',?,1)`, ws)
	body := strings.TrimPrefix(artifactProjectBody(0, "Workspace flag"), "{")
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Project",`+body); w.Code != 201 {
		t.Fatalf("workspace enabled %d %s", w.Code, w.Body)
	}
	mustExec(t, h.db, `UPDATE feature_flag_overrides SET enabled=0 WHERE id='artifact-project-override'`)
	if w := call(owner, "GET", "three-screens/artifact", ""); w.Code != 404 {
		t.Fatalf("disabled flag %d %s", w.Code, w.Body)
	}
}

func TestArtifactProjectCannotBecomePanelsOrPagesApplication(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	body := strings.TrimPrefix(artifactProjectBody(0, "First"), "{")
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Project",`+body); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	r := pagesRequest(t, "PATCH", "/", ws, owner, "OWNER", `{"panels":[]}`)
	r.SetPathValue("slug", "three-screens")
	w := httptest.NewRecorder()
	h.Update(w, r)
	if w.Code != 409 {
		t.Fatalf("project became panels %d %s", w.Code, w.Body)
	}
	r = pagesRequest(t, "PUT", "/", ws, owner, "OWNER", `{}`)
	r.SetPathValue("slug", "three-screens")
	w = httptest.NewRecorder()
	h.PutProject(w, r)
	if w.Code != 409 {
		t.Fatalf("project became app %d %s", w.Code, w.Body)
	}
}

func TestArtifactVersionHistoryPreservesNativeCustodyClassification(t *testing.T) {
	h, _, _, ws, owner := newPagesFixture(t)
	mustExec(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES('native-version-page',?,'native-version','Native',?,'{}',1)`, ws, owner)
	mustExec(t, h.db, `INSERT INTO gridcraft_heads(page_id,workspace_id,schema_version,head_rev,generation,snapshot) VALUES('native-version-page',?,1,1,1,x'7b7d')`, ws)
	mustExec(t, h.db, `INSERT INTO artifact_version_heads(workspace_id,page_id,latest_version,content_type) VALUES(?,'native-version-page',1,'gridcraft')`, ws)
	_, kind, err := privatePageOwnershipSQL(t.Context(), h.db, "p")
	if err != nil {
		t.Fatal(err)
	}
	var got string
	if err = h.db.QueryRow("SELECT " + kind + " FROM pages p WHERE id='native-version-page'").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "workbook" {
		t.Fatalf("native history bypasses workbook custody classification: %q", got)
	}
}

func TestArtifactProjectCreateFinalAuthorityAndShareAuditAreAtomic(t *testing.T) {
	for _, change := range []string{"credential revoked", "user suspended"} {
		t.Run(change, func(t *testing.T) {
			h, _, owner, call := artifactProjectFixture(t)
			mutation := `UPDATE cli_tokens SET revoked_at=datetime('now') WHERE user_id='` + owner + `';`
			if change == "user suspended" {
				mutation = `UPDATE users SET suspended_at=datetime('now') WHERE id='` + owner + `';`
			}
			mustExec(t, h.db, `CREATE TRIGGER artifact_revoke_after_staging AFTER INSERT ON artifact_versions BEGIN `+mutation+` END`)
			body := strings.TrimPrefix(artifactProjectBody(0, "Must rollback"), "{")
			w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Project",`+body)
			if w.Code != 403 {
				t.Fatalf("final authority %d %s", w.Code, w.Body)
			}
			var n int
			_ = h.db.QueryRow(`SELECT count(*) FROM pages WHERE slug='three-screens'`).Scan(&n)
			if n != 0 {
				t.Fatal("revocation committed Page", n)
			}
			_ = h.db.QueryRow(`SELECT count(*) FROM artifact_versions`).Scan(&n)
			if n != 0 {
				t.Fatal("revocation committed version", n)
			}
		})
	}
	h, _, owner, call := artifactProjectFixture(t)
	body := strings.TrimPrefix(artifactProjectBody(0, "First"), "{")
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Project",`+body); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var n int
	_ = h.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='artifact.project_shared'`).Scan(&n)
	if n != 1 {
		t.Fatal("rechecked share duplicated audit", n)
	}
}

// General API fixtures deliberately use NORMAL for speed. This pilot promises
// FULL commit durability, so its boundary tests reopen the owned migrated file
// with the same production database.Open defaults on every pooled connection.
func artifactProjectFullDurability(t *testing.T, db *sql.DB) *sql.DB {
	t.Helper()
	var sequence int
	var name, path string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	full, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = full.Close() })
	return full.DB
}

type artifactProjectUnchainedEmitter struct{ journal.Emitter }

func TestArtifactProjectShareRequiresAtomicAudit(t *testing.T) {
	h, _, owner, call := artifactProjectFixture(t)
	body := strings.TrimPrefix(artifactProjectBody(0, "First"), "{")
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Project",`+body); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	emitter := h.journal
	h.SetJournal(artifactProjectUnchainedEmitter{})
	w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`)
	if w.Code != 500 {
		t.Fatalf("unchained publication acknowledged %d %s", w.Code, w.Body)
	}
	var private, pin int
	if err := h.db.QueryRow(`SELECT p.private_audience,v.shared_version FROM pages p JOIN artifact_version_heads v ON v.page_id=p.id WHERE p.slug='three-screens'`).Scan(&private, &pin); err != nil || private != 1 || pin != 0 {
		t.Fatal("audit refusal committed audience/pin", private, pin, err)
	}
	h.SetJournal(emitter)
	if w = call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var n int
	if err := h.db.QueryRow(`SELECT count(*) FROM journal_entries WHERE entry_type='artifact.project_shared'`).Scan(&n); err != nil || n != 1 {
		t.Fatal("share journal mismatch", n, err)
	}
}
