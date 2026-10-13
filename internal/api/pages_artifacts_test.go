package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

func artifactFixture(t *testing.T) (*PageHandler, *pagesJournalSpy, *pagesFakeClock, string, string, string) {
	t.Helper()
	h, spy, clock, ws, user := newPagesFixture(t)
	h.artifactStoragePath = storageDir(t)
	rr := httptest.NewRecorder()
	h.Create(rr, pagesGrantRequest(t, "POST", "/api/v1/pages", ws, user, "OWNER", `{"slug":"report","name":"Report","panels":[],"allow_empty":true}`))
	if rr.Code != 201 {
		t.Fatalf("empty page create: %d %s", rr.Code, rr.Body.String())
	}
	var page struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return h, spy, clock, ws, user, page.ID
}
func artifactCall(t *testing.T, h *PageHandler, ws, user, role, method, query, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := pagesGrantRequest(t, method, "/api/v1/pages/report/artifact"+query, ws, user, role, body)
	r.SetPathValue("slug", "report")
	rr := httptest.NewRecorder()
	if method == "GET" {
		h.GetArtifact(rr, r)
	} else if query == "/restore" {
		h.RestoreArtifact(rr, r)
	} else {
		h.PublishArtifact(rr, r)
	}
	return rr
}
func artifactSave(t *testing.T, h *PageHandler, ws, user, html, id string, base int64) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"html": html, "mutation_id": id, "base_rev": base})
	return artifactCall(t, h, ws, user, "OWNER", "POST", "/revisions", string(body))
}
func artifactExpect(t *testing.T, rr *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rr.Code != status {
		t.Fatalf("status=%d want=%d body=%s", rr.Code, status, rr.Body.String())
	}
}

func TestPageArtifacts_RevisionsRetryRestoreAndGC(t *testing.T) {
	h, spy, _, ws, user, pageID := artifactFixture(t)
	artifactExpect(t, artifactCall(t, h, ws, user, "OWNER", "GET", "", ""), 200)
	patch := pagesGrantRequest(t, "PATCH", "/", ws, user, "OWNER", `{"name":"Renamed staged page"}`)
	patch.SetPathValue("slug", "report")
	patched := httptest.NewRecorder()
	h.Update(patched, patch)
	artifactExpect(t, patched, 200)

	original := `<html><script>window.evil=true</script><h1>Original</h1></html>`
	artifactExpect(t, artifactSave(t, h, ws, user, original, "first", 0), 201)
	artifactExpect(t, artifactSave(t, h, ws, user, original, "first", 0), 200)
	artifactExpect(t, artifactSave(t, h, ws, user, "different", "first", 0), 409)
	artifactExpect(t, artifactSave(t, h, ws, user, "stale", "stale", 0), 409)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Second</h1>", "second", 1), 201)
	rr := artifactCall(t, h, ws, user, "OWNER", "GET", "?revision=1", "")
	artifactExpect(t, rr, 200)
	if rr.Header().Get("Content-Type") != "application/json" || rr.Header().Get("X-Content-Type-Options") != "nosniff" || rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("original source must only be returned as private JSON")
	}
	if strings.Contains(rr.Body.String(), "<script>") {
		t.Fatal("source was emitted as unescaped HTML")
	}
	var got struct {
		HTML      string                 `json:"html"`
		HeadRev   int64                  `json:"head_rev"`
		Revisions []pageArtifactRevision `json:"revisions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.HTML != original || got.HeadRev != 2 || len(got.Revisions) != 2 {
		t.Fatalf("history=%+v", got)
	}
	artifactExpect(t, artifactCall(t, h, ws, user, "OWNER", "POST", "/restore", `{"revision":1,"base_rev":2,"mutation_id":"restore"}`), 201)
	artifactExpect(t, artifactCall(t, h, ws, user, "OWNER", "POST", "/restore", `{"revision":1,"base_rev":2,"mutation_id":"restore"}`), 200)
	artifactExpect(t, artifactCall(t, h, ws, user, "OWNER", "POST", "/restore", `{"revision":2,"base_rev":2,"mutation_id":"restore-stale"}`), 409)
	if len(spy.entries) < 4 {
		t.Fatal("revision saves were not journalled")
	}
	n, err := reclaimAttachmentBlobs(t.Context(), h.db, h.artifactStoragePath, ws)
	if err != nil || n != 2 {
		t.Fatalf("GC should reclaim only two rejected-publication orphans: %d %v", n, err)
	}
	artifactExpect(t, artifactCall(t, h, ws, user, "OWNER", "GET", "?revision=2", ""), 200)

	rollback := pagesGrantRequest(t, "POST", "/", ws, user, "OWNER", `{"to":1}`)
	rollback.SetPathValue("slug", "report")
	rollbackResult := httptest.NewRecorder()
	h.Rollback(rollbackResult, rollback)
	artifactExpect(t, rollbackResult, 409)
	doc := pagesGet(t, h, ws, user, "OWNER", "report")
	if doc["has_artifact"] != true {
		t.Fatal("Page dispatch missing artifact kind")
	}
	if _, err := h.db.Exec(`DELETE FROM pages WHERE id=?`, pageID); err != nil {
		t.Fatal(err)
	}
	n, err = reclaimAttachmentBlobs(t.Context(), h.db, h.artifactStoragePath, ws)
	if err != nil || n != 2 {
		t.Fatalf("deleted page blobs not reclaimed: %d %v", n, err)
	}
}

func TestPageArtifacts_ACLHumanOnlyAndUseTimeRevocation(t *testing.T) {
	h, _, _, ws, user, pageID := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>private</h1>", "first", 0), 201)
	if _, err := h.db.Exec(`INSERT INTO users(id,email,full_name) VALUES ('reader','reader@test.invalid','Reader'); INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES ('reader-member',?,'reader','MEMBER')`, ws); err != nil {
		t.Fatal(err)
	}
	artifactExpect(t, artifactCall(t, h, ws, "reader", "MEMBER", "GET", "", ""), 404)
	if _, err := h.db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES (?,'user','reader','read',?)`, pageID, user); err != nil {
		t.Fatal(err)
	}
	artifactExpect(t, artifactCall(t, h, ws, "reader", "MEMBER", "GET", "?revision=1", ""), 200)
	artifactExpect(t, artifactCall(t, h, ws, "reader", "MEMBER", "POST", "/restore", `{"revision":1,"base_rev":1,"mutation_id":"no-write"}`), 403)
	if _, err := h.db.Exec(`UPDATE page_grants SET level='write' WHERE subject_id='reader'`); err != nil {
		t.Fatal(err)
	}
	// The streamed body removes the grant AFTER the initial authorization. The
	// transaction must rerun the existing live grant resolver before committing.
	body := `{"html":"<h1>revoked</h1>","base_rev":1,"mutation_id":"revoked"}`
	req := pagesGrantRequest(t, "POST", "/", ws, "reader", "MEMBER", body)
	req.SetPathValue("slug", "report")
	req.Body = &artifactRevokingBody{reader: strings.NewReader(body), revoke: func() {
		if _, err := h.db.Exec(`DELETE FROM page_grants WHERE subject_id='reader'`); err != nil {
			t.Fatal(err)
		}
	}}
	rr := httptest.NewRecorder()
	h.PublishArtifact(rr, req)
	artifactExpect(t, rr, 404)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=?`, pageID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("revoked write committed: %d %v", count, err)
	}
	agent := pagesRequest(t, "GET", "/", ws, user, "OWNER", "")
	agent.SetPathValue("slug", "report")
	rr = httptest.NewRecorder()
	h.GetArtifact(rr, agent)
	artifactExpect(t, rr, 401)
	artifactExpect(t, artifactCall(t, h, "foreign", user, "OWNER", "GET", "?revision=1", ""), 404)
}

type artifactRevokingBody struct {
	reader *strings.Reader
	revoke func()
	once   sync.Once
}

func (b *artifactRevokingBody) Read(p []byte) (int, error) {
	b.once.Do(b.revoke)
	return b.reader.Read(p)
}
func (b *artifactRevokingBody) Close() error { return nil }

func TestPageArtifacts_ConcurrentCASAndPageTypeBoundary(t *testing.T) {
	h, _, _, ws, user, pageID := artifactFixture(t)
	h.journal = nil
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- artifactSave(t, h, ws, user, fmt.Sprintf("<h1>%d</h1>", i), fmt.Sprint(i), 0).Code
		}()
	}
	wg.Wait()
	close(codes)
	seen := map[int]int{}
	for code := range codes {
		seen[code]++
	}
	if seen[201] != 1 || seen[409] != 1 {
		t.Fatalf("concurrent publications=%v", seen)
	}
	req := pagesGrantRequest(t, "PATCH", "/", ws, user, "OWNER", pagesSpecBody("report"))
	req.SetPathValue("slug", "report")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	artifactExpect(t, rr, 409)
	// The database boundary also rejects legacy writers that raced their check.
	if _, err := h.db.Exec(`INSERT INTO page_panels(id,page_id,panel_id,schema,title,owner_crew_id,producer_kind,producer_ref,sla_seconds,span) VALUES ('forbidden',?,'panel','status.v1','Panel','crew-lookout','script','script/test',30,8)`, pageID); err == nil {
		t.Fatal("artifact accepted a panel through a legacy writer")
	}
}

func TestPageArtifacts_QuotasAndInputValidation(t *testing.T) {
	h, _, clock, ws, user, _ := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, strings.Repeat("x", maxArtifactHTMLBytes+1), "large", 0), 413)
	artifactExpect(t, artifactSave(t, h, ws, user, "", "empty", 0), 422)
	for i := range maxArtifactHourlyRevisions {
		artifactExpect(t, artifactSave(t, h, ws, user, "<p>ok</p>", fmt.Sprint(i), int64(i)), 201)
	}
	artifactExpect(t, artifactSave(t, h, ws, user, "<p>limited</p>", "rate", 30), 429)
	clock.advance(2 * time.Hour)
	for i := 30; i < maxArtifactRevisions; i++ {
		clock.advance(2 * time.Minute)
		artifactExpect(t, artifactSave(t, h, ws, user, "<p>ok</p>", fmt.Sprint(i), int64(i)), 201)
	}
	clock.advance(2 * time.Hour)
	artifactExpect(t, artifactSave(t, h, ws, user, "<p>limited</p>", "cap", 200), 201)
	// Expired source retains its identity: retry succeeds without recreating it.
	artifactExpect(t, artifactSave(t, h, ws, user, "<p>ok</p>", "0", 0), 200)
	artifactExpect(t, artifactCall(t, h, ws, user, "OWNER", "GET", "?revision=1", ""), 404)
}

func TestPageArtifacts_BackupRestoreHistoricalContent(t *testing.T) {
	for _, fork := range []string{"", "artifact-fork"} {
		t.Run("fork="+fork, func(t *testing.T) {
			h, _, _, ws, user, _ := artifactFixture(t)
			artifactExpect(t, artifactSave(t, h, ws, user, "<h1>First</h1>", "first", 0), 201)
			artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Second</h1>", "second", 1), 201)
			const pass = "page-artifact-roundtrip-password"
			created, err := backup.CreateBackup(t.Context(), h.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: pass, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: h.artifactStoragePath})
			if err != nil {
				t.Fatal(err)
			}
			target := setupTestDB(t)
			root := storageDir(t)
			restored, err := backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Path: created.Path, Passphrase: pass, AsWorkspace: fork, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: root})
			if err != nil {
				t.Fatal(err)
			}
			if created.Manifest.Contents.AttachmentsIncluded != 2 || restored.AttachmentsRestored != 2 {
				t.Fatalf("historical blobs missing from backup: included=%d restored=%d", created.Manifest.Contents.AttachmentsIncluded, restored.AttachmentsRestored)
			}
			// Resolve destinations from restored Pages, including ID remapping.
			rows, err := target.Query(`SELECT r.sha256,p.workspace_id FROM page_artifact_revisions r JOIN pages p ON p.id=r.page_id ORDER BY revision`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			i := 0
			for rows.Next() {
				var sha, targetWS string
				if err := rows.Scan(&sha, &targetWS); err != nil {
					t.Fatal(err)
				}
				data, err := readAttachmentBlob(root, targetWS, sha)
				i++
				if err != nil || string(data) != fmt.Sprintf("<h1>%s</h1>", []string{"First", "Second"}[i-1]) {
					t.Fatalf("historical content=%q %v", data, err)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if i != 2 {
				t.Fatalf("restored revisions=%d", i)
			}
		})
	}
}

func TestPageArtifacts_RejectNonemptyAndCrewOwnedPages(t *testing.T) {
	for _, crewOwned := range []bool{false, true} {
		t.Run(fmt.Sprint(crewOwned), func(t *testing.T) {
			h, _, _, ws, user := newPagesFixture(t)
			h.artifactStoragePath = storageDir(t)
			body := pagesSpecBody("report")
			if crewOwned {
				body = strings.Replace(body, `"name":`, `"owner":"crew/lookout","name":`, 1)
			}
			req := pagesGrantRequest(t, "POST", "/", ws, user, "OWNER", body)
			rr := httptest.NewRecorder()
			h.Create(rr, req)
			artifactExpect(t, rr, 201)
			artifactExpect(t, artifactSave(t, h, ws, user, "<p>must not replace page</p>", "first", 0), 409)
		})
	}
}
