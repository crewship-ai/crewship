package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPageArtifactPublishGCAndSharedAttachmentLifetime(t *testing.T) {
	f := newAttachmentFixture(t)
	production := f.h.db
	h := NewPageHandler(production, nil, newTestLogger())
	h.artifactStoragePath = f.storage
	created := httptest.NewRecorder()
	h.Create(created, pagesGrantRequest(t, "POST", "/api/v1/pages", f.wsID, f.userID, "OWNER", `{"slug":"report","name":"Shared report","panels":[],"allow_empty":true}`))
	artifactExpect(t, created, 201)
	var page string
	if err := production.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug='report'`, f.wsID).Scan(&page); err != nil {
		t.Fatal(err)
	}
	const html = "<h1>Shared historical source and issue attachment</h1>"
	sha := attachmentDigest([]byte(html))
	gap, resume := make(chan struct{}), make(chan struct{})
	var resumeOnce sync.Once
	release := func() { resumeOnce.Do(func() { close(resume) }) }
	hook := &reviewQueryHook{match: "UPDATE pages SET updated_at=updated_at", nth: 1, run: func() { close(gap); <-resume }}
	// A deferred real SQLite transaction has acquired no writer fence or read
	// snapshot at this hook. The separate GC handle can query freely; only the
	// publisher's blob critical section can protect these not-yet-owned bytes.
	hooked := sql.OpenDB(&reviewHookConnector{dsn: reviewDBPath(t, production) + "?_pragma=busy_timeout(1000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_txlock=deferred", inner: production.Driver(), hook: hook})
	t.Cleanup(func() { _ = hooked.Close() })
	h.db = hooked
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	publishDone, gcDone := make(chan struct{}), make(chan struct{})
	gcStarted := false
	t.Cleanup(func() {
		release()
		cancel()
		select {
		case <-publishDone:
		case <-time.After(5 * time.Second):
			t.Error("publication did not drain")
		}
		if gcStarted {
			select {
			case <-gcDone:
			case <-time.After(5 * time.Second):
				t.Error("collection did not drain")
			}
		}
		h.db = production
	})
	request := pagesGrantRequest(t, "POST", "/api/v1/pages/report/artifact/revisions", f.wsID, f.userID, "OWNER", fmt.Sprintf(`{"html":%q,"base_rev":0,"mutation_id":"publish-gap"}`, html))
	requestContext, requestCancel := context.WithTimeout(request.Context(), 10*time.Second)
	defer requestCancel()
	request = request.WithContext(requestContext)
	request.SetPathValue("slug", "report")
	published := httptest.NewRecorder()
	go func() { defer close(publishDone); h.PublishArtifact(published, request) }()
	select {
	case <-gap:
	case <-publishDone:
		t.Fatalf("publication missed the storage/metadata gap: %d %s", published.Code, published.Body.String())
	case <-ctx.Done():
		t.Fatal("publication never reached the storage/metadata gap")
	}
	path, err := attachmentBlobPath(f.storage, f.wsID, sha)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("publication did not stage its immutable blob", err)
	}
	var revisions int
	if err = production.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=?`, page).Scan(&revisions); err != nil || revisions != 0 {
		t.Fatalf("gap already has committed references: %d %v", revisions, err)
	}
	var collected int
	var collectErr error
	gcStarted = true
	go func() {
		defer close(gcDone)
		collected, collectErr = reclaimAttachmentBlobs(ctx, production, f.storage, f.wsID)
	}()
	select {
	case <-gcDone:
		t.Fatalf("GC entered uncommitted publication gap: collected=%d err=%v", collected, collectErr)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	<-publishDone
	<-gcDone
	artifactExpect(t, published, 201)
	if collectErr != nil || collected != 0 {
		t.Fatalf("concurrent GC lost publication: %d %v", collected, collectErr)
	}
	h.db = production
	// The attachment and historical revision are two owners of the same blob,
	// despite their different MIME types and HTTP surfaces.
	attachment, status := upload(t, f, "report.txt", []byte(html))
	if status != http.StatusCreated || attachment.SHA256 != sha {
		t.Fatalf("shared attachment: %d digest=%s", status, attachment.SHA256)
	}
	artifactExpect(t, artifactSave(t, h, f.wsID, f.userID, "<h1>New head</h1>", "new-head", 1), 201)
	deleted := httptest.NewRecorder()
	f.h.Delete(deleted, scopedReq(t, f, "DELETE", "ENG-1", attachment.ID, f.wsID))
	artifactExpect(t, deleted, 200)
	if n, err := reclaimAttachmentBlobs(t.Context(), production, f.storage, f.wsID); err != nil || n != 0 {
		t.Fatalf("attachment deletion collected retained artifact sources: %d %v", n, err)
	}
	historical := artifactCall(t, h, f.wsID, f.userID, "OWNER", "GET", "?revision=1", "")
	artifactExpect(t, historical, 200)
	var content struct {
		HTML string `json:"html"`
	}
	if err := json.Unmarshal(historical.Body.Bytes(), &content); err != nil || content.HTML != html {
		t.Fatalf("historical shared bytes lost: %q %v", content.HTML, err)
	}
	attachment, status = upload(t, f, "report.txt", []byte(html))
	if status != http.StatusCreated {
		t.Fatalf("reattach: %d", status)
	}
	deletePage := pagesGrantRequest(t, "DELETE", "/api/v1/pages/report", f.wsID, f.userID, "OWNER", "")
	deletePage.SetPathValue("slug", "report")
	deleted = httptest.NewRecorder()
	h.Delete(deleted, deletePage)
	artifactExpect(t, deleted, 204)
	if n, err := reclaimAttachmentBlobs(t.Context(), production, f.storage, f.wsID); err != nil || n != 1 {
		t.Fatalf("Page deletion must collect only unshared head: %d %v", n, err)
	}
	download := httptest.NewRecorder()
	f.h.Download(download, scopedReq(t, f, "GET", "ENG-1", attachment.ID, f.wsID))
	artifactExpect(t, download, 200)
	if download.Body.String() != html {
		t.Fatal("Page deletion lost attachment bytes")
	}
	deleted = httptest.NewRecorder()
	f.h.Delete(deleted, scopedReq(t, f, "DELETE", "ENG-1", attachment.ID, f.wsID))
	artifactExpect(t, deleted, 200)
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("final owner deletion retained shared blob: %v", err)
	}
}

func TestPageArtifactAllRetainedRevisionsPinnedRejectsPublishAndRestore(t *testing.T) {
	h, _, _, ws, user, page := artifactFixture(t)
	const html = "<h1>Retained source</h1>"
	artifactExpect(t, artifactSave(t, h, ws, user, html, "original", 0), 201)
	for revision := 2; revision <= 200; revision++ {
		// Historical rows avoid spending wall-clock hours on the unrelated rate
		// limit. All sources still use real verified bytes from the first save.
		execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,author_user_id,mutation_id,request_digest,created_at,manifest_json) SELECT ?,page_id,?,sha256,author_user_id,?,'historical','2000-01-01T00:00:00.000Z',manifest_json FROM page_artifact_revisions WHERE page_id=? AND revision=1`, fmt.Sprint("pinned-source-", revision), revision, fmt.Sprint("pinned-write-", revision), page)
	}
	execOrFatal(t, h.db, `UPDATE page_artifacts SET head_rev=200 WHERE page_id=?`, page)
	for revision := 1; revision <= 200; revision++ {
		// Resolved historical discussions retain their original sources without
		// occupying the separate100-open-thread capacity.
		execOrFatal(t, h.db, `INSERT INTO page_artifact_threads(id,page_id,revision,anchor_json,status,version,created_by_user_id,resolved_by_user_id,resolved_at,created_at,updated_at) VALUES(?,?,?,'{"schema_version":1,"type":"document"}','resolved',2,?,?,'2000-01-01T00:00:00.000Z','2000-01-01T00:00:00.000Z','2000-01-01T00:00:00.000Z')`, fmt.Sprint("pinned-thread-", revision), page, revision, user, user)
		execOrFatal(t, h.db, `INSERT INTO page_artifact_comments(id,thread_id,thread_version,body,author_user_id,created_at) VALUES(?,?,1,'Keep original source',?,'2000-01-01T00:00:00.000Z')`, fmt.Sprint("pinned-comment-", revision), fmt.Sprint("pinned-thread-", revision), user)
	}
	for _, operation := range []string{"publish", "restore"} {
		t.Run(operation, func(t *testing.T) {
			var response *httptest.ResponseRecorder
			if operation == "publish" {
				response = artifactSave(t, h, ws, user, "<h1>Must not commit</h1>", "pinned-publish", 200)
			} else {
				response = artifactCall(t, h, ws, user, "OWNER", "POST", "/restore", `{"revision":1,"base_rev":200,"mutation_id":"pinned-restore"}`)
			}
			artifactExpect(t, response, 409)
			if !strings.Contains(response.Body.String(), `"code":"retention_pinned"`) {
				t.Fatalf("missing retention-specific conflict: %s", response.Body.String())
			}
			var head, retained, threads, writes, receipts int
			if err := h.db.QueryRow(`SELECT head_rev,(SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=?),(SELECT COUNT(*) FROM page_artifact_threads WHERE page_id=?),(SELECT COUNT(*) FROM artifact_workspace_writes WHERE workspace_id=?),(SELECT COUNT(*) FROM page_artifact_mutation_receipts WHERE page_id=?) FROM page_artifacts WHERE page_id=?`, page, page, ws, page, page).Scan(&head, &retained, &threads, &writes, &receipts); err != nil {
				t.Fatal(err)
			}
			if head != 200 || retained != 200 || threads != 200 || writes != 1 || receipts != 1 {
				t.Fatalf("retention denial changed durable state: head=%d sources=%d threads=%d rate=%d receipts=%d", head, retained, threads, writes, receipts)
			}
			for _, revision := range []int{1, 200} {
				artifactExpect(t, artifactCall(t, h, ws, user, "OWNER", "GET", fmt.Sprintf("?revision=%d", revision), ""), 200)
			}
		})
	}
	// A full pinned history still permits a committed exact retry, without
	// reserving another source slot or spending another workspace rate receipt.
	retry := artifactSave(t, h, ws, user, html, "original", 0)
	artifactExpect(t, retry, 200)
	var receipt struct {
		Revision int `json:"revision"`
		Head     int `json:"head_rev"`
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &receipt); err != nil || receipt.Revision != 1 || receipt.Head != 200 {
		t.Fatalf("pinned history lost exact retry: %s %v", retry.Body.String(), err)
	}
}
