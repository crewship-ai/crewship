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

func artifactDiscussionCall(t *testing.T, h *PageHandler, ws, user, role, operation, threadID string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body := ""
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = string(encoded)
	}
	method := "POST"
	if operation == "list" {
		method = "GET"
	}
	if operation == "resolve" {
		method = "PATCH"
	}
	req := pagesGrantRequest(t, method, "/api/v1/pages/report/artifact/threads", ws, user, role, body)
	req.SetPathValue("slug", "report")
	req.SetPathValue("threadId", threadID)
	rr := httptest.NewRecorder()
	switch operation {
	case "list":
		h.ListArtifactThreads(rr, req)
	case "create":
		h.CreateArtifactThread(rr, req)
	case "reply":
		h.ReplyArtifactThread(rr, req)
	case "resolve":
		h.ResolveArtifactThread(rr, req)
	default:
		t.Fatalf("unknown discussion operation: %s", operation)
	}
	return rr
}
func artifactDiscussionCreate(t *testing.T, h *PageHandler, ws, user, body, mutation string, revision int64) *httptest.ResponseRecorder {
	t.Helper()
	return artifactDiscussionCall(t, h, ws, user, "OWNER", "create", "", map[string]any{"revision": revision, "anchor": artifactCommentAnchor{SchemaVersion: 1, Type: "document"}, "body": body, "mutation_id": mutation})
}
func discussionThread(t *testing.T, rr *httptest.ResponseRecorder) artifactThreadWire {
	t.Helper()
	var thread artifactThreadWire
	if err := json.Unmarshal(rr.Body.Bytes(), &thread); err != nil {
		t.Fatal(err)
	}
	return thread
}
func discussionFixture(t *testing.T) (*PageHandler, *pagesFakeClock, string, string, string) {
	t.Helper()
	h, _, clock, ws, user, pageID := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Original</h1>", "first", 0), 201)
	return h, clock, ws, user, pageID
}

func TestArtifactComments_RevisionAnchorsRepliesResolveAndRetry(t *testing.T) {
	h, _, ws, user, _ := discussionFixture(t)
	rr := artifactDiscussionCreate(t, h, ws, user, "<script>not executable</script> @agent does not dispatch", "create", 1)
	artifactExpect(t, rr, 201)
	thread := discussionThread(t, rr)
	if thread.Version != 1 || thread.Revision != 1 || thread.Anchor.Type != "document" || len(thread.Comments) != 1 || thread.Comments[0].AuthorUserID != user || thread.CreatedByName == "" {
		t.Fatalf("created thread=%+v", thread)
	}
	if strings.Contains(rr.Body.String(), "<script>") || rr.Header().Get("Cache-Control") != "no-store" || rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("private comment source must remain escaped JSON")
	}
	artifactExpect(t, artifactDiscussionCreate(t, h, ws, user, "different", "create", 1), 409)
	reply := map[string]any{"body": "First reply", "base_version": 1, "mutation_id": "reply"}
	rr = artifactDiscussionCall(t, h, ws, user, "OWNER", "reply", thread.ID, reply)
	artifactExpect(t, rr, 201)
	thread = discussionThread(t, rr)
	if thread.Version != 2 || len(thread.Comments) != 2 || thread.Comments[0].Body != "<script>not executable</script> @agent does not dispatch" || thread.Comments[1].Body != "First reply" {
		t.Fatalf("reply order/state=%+v", thread)
	}
	resolve := map[string]any{"resolved": true, "base_version": 2, "mutation_id": "resolve"}
	rr = artifactDiscussionCall(t, h, ws, user, "OWNER", "resolve", thread.ID, resolve)
	artifactExpect(t, rr, 200)
	thread = discussionThread(t, rr)
	if thread.Status != "resolved" || thread.Version != 3 || thread.ResolvedByUserID != user || thread.ResolvedAt == "" {
		t.Fatalf("resolve=%+v", thread)
	}
	artifactExpect(t, artifactDiscussionCall(t, h, ws, user, "OWNER", "resolve", thread.ID, resolve), 200)
	rr = artifactDiscussionCall(t, h, ws, user, "OWNER", "reply", thread.ID, reply)
	artifactExpect(t, rr, 200)
	retried := discussionThread(t, rr)
	if retried.Version != 3 || retried.AppliedVersion == nil || *retried.AppliedVersion != 2 || len(retried.Comments) != 2 {
		t.Fatalf("retry duplicated or lost original result: %+v", retried)
	}
	rr = artifactDiscussionCall(t, h, ws, user, "OWNER", "resolve", thread.ID, map[string]any{"resolved": false, "base_version": 2, "mutation_id": "stale"})
	artifactExpect(t, rr, 409)
	if !strings.Contains(rr.Body.String(), `"code":"stale_thread"`) {
		t.Fatal("CAS failure lacks stable code")
	}
	rr = artifactDiscussionCall(t, h, ws, user, "OWNER", "resolve", thread.ID, map[string]any{"resolved": false, "base_version": 3, "mutation_id": "reopen"})
	artifactExpect(t, rr, 200)
	thread = discussionThread(t, rr)
	if thread.Status != "open" || thread.Version != 4 || thread.ResolvedByUserID != "" || thread.ResolvedAt != "" {
		t.Fatalf("reopen=%+v", thread)
	}
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>New source</h1>", "second", 1), 201)
	req := pagesGrantRequest(t, "GET", "/?revision=2", ws, user, "OWNER", "")
	req.SetPathValue("slug", "report")
	rr = httptest.NewRecorder()
	h.ListArtifactThreads(rr, req)
	artifactExpect(t, rr, 200)
	var list struct {
		Threads    []artifactThreadWire `json:"threads"`
		CanComment bool                 `json:"can_comment"`
		CanResolve bool                 `json:"can_resolve"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Threads) != 0 || !list.CanComment || !list.CanResolve {
		t.Fatalf("old anchor moved to new revision: %+v", list)
	}
	req = pagesGrantRequest(t, "GET", "/?revision=1", ws, user, "OWNER", "")
	req.SetPathValue("slug", "report")
	rr = httptest.NewRecorder()
	h.ListArtifactThreads(rr, req)
	artifactExpect(t, rr, 200)
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Threads) != 1 || list.Threads[0].Revision != 1 || list.Threads[0].ID != thread.ID {
		t.Fatal("revision anchor was not retained")
	}
	var assignments int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM assignments`).Scan(&assignments); err != nil || assignments != 0 {
		t.Fatalf("a comment mention dispatched work: %d %v", assignments, err)
	}
	var mutations int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_comment_mutations`).Scan(&mutations); err != nil || mutations != 4 {
		t.Fatalf("durable audit/dedup count=%d %v", mutations, err)
	}
}

func TestArtifactComments_ACLThreadIsolationAndUseTimeRevocation(t *testing.T) {
	h, _, ws, user, pageID := discussionFixture(t)
	rr := artifactDiscussionCreate(t, h, ws, user, "Private comment", "first", 1)
	artifactExpect(t, rr, 201)
	thread := discussionThread(t, rr)
	if _, err := h.db.Exec(`INSERT INTO users(id,email,full_name) VALUES ('comment-reader','comments@example.invalid','Reader'); INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES ('comment-member',?,'comment-reader','MEMBER')`, ws); err != nil {
		t.Fatal(err)
	}
	artifactExpect(t, artifactDiscussionCall(t, h, ws, "comment-reader", "MEMBER", "list", "", nil), 404)
	if _, err := h.db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES (?,'user','comment-reader','read',?)`, pageID, user); err != nil {
		t.Fatal(err)
	}
	rr = artifactDiscussionCall(t, h, ws, "comment-reader", "MEMBER", "list", "", nil)
	artifactExpect(t, rr, 200)
	if !strings.Contains(rr.Body.String(), `"can_comment":false`) || !strings.Contains(rr.Body.String(), `"can_resolve":false`) {
		t.Fatal("reader received write capability")
	}
	artifactExpect(t, artifactDiscussionCall(t, h, ws, "comment-reader", "MEMBER", "reply", thread.ID, map[string]any{"body": "forbidden", "base_version": 1, "mutation_id": "forbidden"}), 403)
	if _, err := h.db.Exec(`UPDATE page_grants SET level='write' WHERE subject_id='comment-reader'`); err != nil {
		t.Fatal(err)
	}
	raw := `{"body":"revoked mid-upload","base_version":1,"mutation_id":"revoked"}`
	req := pagesGrantRequest(t, "POST", "/", ws, "comment-reader", "MEMBER", raw)
	req.SetPathValue("slug", "report")
	req.SetPathValue("threadId", thread.ID)
	req.Body = &artifactRevokingBody{reader: strings.NewReader(raw), revoke: func() {
		if _, err := h.db.Exec(`DELETE FROM page_grants WHERE subject_id='comment-reader'`); err != nil {
			t.Fatal(err)
		}
	}}
	rr = httptest.NewRecorder()
	h.ReplyArtifactThread(rr, req)
	artifactExpect(t, rr, 404)
	// A valid thread identifier from another authorized Page is still not a
	// thread of this artifact; the page fence is checked on every operation.
	req = pagesGrantRequest(t, "POST", "/", ws, user, "OWNER", `{"slug":"other-report","name":"Other","panels":[],"allow_empty":true}`)
	rr = httptest.NewRecorder()
	h.Create(rr, req)
	artifactExpect(t, rr, 201)
	req = pagesGrantRequest(t, "POST", "/", ws, user, "OWNER", `{"html":"<p>other</p>","base_rev":0,"mutation_id":"other"}`)
	req.SetPathValue("slug", "other-report")
	rr = httptest.NewRecorder()
	h.PublishArtifact(rr, req)
	artifactExpect(t, rr, 201)
	req = pagesGrantRequest(t, "POST", "/", ws, user, "OWNER", `{"body":"cross-page","base_version":1,"mutation_id":"cross-page"}`)
	req.SetPathValue("slug", "other-report")
	req.SetPathValue("threadId", thread.ID)
	rr = httptest.NewRecorder()
	h.ReplyArtifactThread(rr, req)
	artifactExpect(t, rr, 404)
	artifactExpect(t, artifactDiscussionCall(t, h, "foreign", user, "OWNER", "list", "", nil), 404)
	req = pagesRequest(t, "GET", "/", ws, user, "OWNER", "")
	req.SetPathValue("slug", "report")
	rr = httptest.NewRecorder()
	h.ListArtifactThreads(rr, req)
	artifactExpect(t, rr, 401)
}

func TestArtifactComments_ConcurrentCASAndAnchorValidation(t *testing.T) {
	h, _, ws, user, _ := discussionFixture(t)
	h.journal = nil
	rr := artifactDiscussionCreate(t, h, ws, user, "Root", "first", 1)
	artifactExpect(t, rr, 201)
	thread := discussionThread(t, rr)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr := artifactDiscussionCall(t, h, ws, user, "OWNER", "reply", thread.ID, map[string]any{"body": fmt.Sprint(i), "base_version": 1, "mutation_id": fmt.Sprint("concurrent-", i)})
			codes <- rr.Code
		}()
	}
	wg.Wait()
	close(codes)
	seen := map[int]int{}
	for code := range codes {
		seen[code]++
	}
	if seen[201] != 1 || seen[409] != 1 {
		t.Fatalf("CAS publications=%v", seen)
	}
	for _, anchor := range []any{map[string]any{"schema_version": 1, "type": "point"}, map[string]any{"schema_version": 2, "type": "document"}} {
		artifactExpect(t, artifactDiscussionCall(t, h, ws, user, "OWNER", "create", "", map[string]any{"revision": 1, "anchor": anchor, "body": "bad anchor", "mutation_id": "bad"}), 422)
	}
	artifactExpect(t, artifactDiscussionCreate(t, h, ws, user, "missing rev", "missing", 999), 404)
	artifactExpect(t, artifactDiscussionCreate(t, h, ws, user, strings.Repeat("x", maxArtifactCommentBytes+1), "big", 1), 413)
	artifactExpect(t, artifactDiscussionCreate(t, h, ws, user, " \n ", "empty", 1), 422)
	artifactExpect(t, artifactDiscussionCall(t, h, ws, user, "OWNER", "reply", thread.ID, map[string]any{"body": "spoof", "base_version": 2, "mutation_id": "spoof", "author_user_id": "other"}), 400)
	artifactExpect(t, artifactDiscussionCall(t, h, ws, user, "OWNER", "reply", thread.ID, map[string]any{"body": "move anchor", "base_version": 2, "revision": 2, "mutation_id": "move"}), 400)
}

func TestArtifactComments_LimitsAndRetryBeforeRateLimit(t *testing.T) {
	h, clock, ws, user, _ := discussionFixture(t)
	var thread artifactThreadWire
	for i := range maxArtifactCommentHourlyMutations {
		rr := artifactDiscussionCreate(t, h, ws, user, "Root", fmt.Sprint(i), 1)
		artifactExpect(t, rr, 201)
		thread = discussionThread(t, rr)
	}
	artifactExpect(t, artifactDiscussionCreate(t, h, ws, user, "Root", "0", 1), 200)
	artifactExpect(t, artifactDiscussionCreate(t, h, ws, user, "Rate limit", "rate", 1), 429)
	clock.advance(2 * time.Hour)
	for i := maxArtifactCommentHourlyMutations; i < maxArtifactThreads; i++ {
		artifactExpect(t, artifactDiscussionCreate(t, h, ws, user, "Root", fmt.Sprint(i), 1), 201)
	}
	artifactExpect(t, artifactDiscussionCreate(t, h, ws, user, "Thread limit", "cap", 1), 409)
	clock.advance(2 * time.Hour)
	for i := 1; i < maxArtifactThreadComments; i++ {
		rr := artifactDiscussionCall(t, h, ws, user, "OWNER", "reply", thread.ID, map[string]any{"body": "reply", "base_version": i, "mutation_id": fmt.Sprint("reply-", i)})
		artifactExpect(t, rr, 201)
	}
	rr := artifactDiscussionCall(t, h, ws, user, "OWNER", "reply", thread.ID, map[string]any{"body": "over cap", "base_version": maxArtifactThreadComments, "mutation_id": "overcap"})
	artifactExpect(t, rr, 409)
	if !strings.Contains(rr.Body.String(), `"code":"comment_limit"`) {
		t.Fatal("comment cap lacks stable code")
	}
}

func TestArtifactComments_BackupRestoreAnchorsAndAudit(t *testing.T) {
	for _, fork := range []string{"", "comments-fork"} {
		t.Run("fork="+fork, func(t *testing.T) {
			h, _, ws, user, _ := discussionFixture(t)
			rr := artifactDiscussionCreate(t, h, ws, user, "Preserve original comment", "first-comment", 1)
			artifactExpect(t, rr, 201)
			thread := discussionThread(t, rr)
			artifactExpect(t, artifactDiscussionCall(t, h, ws, user, "OWNER", "reply", thread.ID, map[string]any{"body": "Preserve reply", "base_version": 1, "mutation_id": "reply"}), 201)
			artifactExpect(t, artifactDiscussionCall(t, h, ws, user, "OWNER", "resolve", thread.ID, map[string]any{"resolved": true, "base_version": 2, "mutation_id": "resolve"}), 200)
			artifactExpect(t, artifactSave(t, h, ws, user, "<h1>New source</h1>", "second", 1), 201)
			const pass = "artifact-comments-roundtrip-password"
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
			var id string
			if err := target.QueryRow(`SELECT t.id FROM page_artifact_threads t JOIN pages p ON p.id=t.page_id WHERE p.workspace_id=?`, restored.RestoredWorkspaceID).Scan(&id); err != nil {
				t.Fatal(err)
			}
			var pageID string
			if err := target.QueryRow(`SELECT page_id FROM page_artifact_threads WHERE id=?`, id).Scan(&pageID); err != nil {
				t.Fatal(err)
			}
			restoredHandler := NewPageHandler(target, nil, newTestLogger())
			v, err := restoredHandler.loadArtifactThread(t.Context(), pageID, id)
			if err != nil {
				t.Fatal(err)
			}
			if v.Revision != 1 || v.Anchor.SchemaVersion != 1 || v.Anchor.Type != "document" || v.Status != "resolved" || v.Version != 3 || len(v.Comments) != 2 || v.Comments[0].Body != "Preserve original comment" || v.Comments[1].Body != "Preserve reply" {
				t.Fatalf("discussion restore lost anchor/content/state: %+v", v)
			}
			var mutations int
			if err := target.QueryRow(`SELECT COUNT(*) FROM page_artifact_comment_mutations WHERE page_id=?`, pageID).Scan(&mutations); err != nil || mutations != 3 {
				t.Fatalf("audit restored=%d %v", mutations, err)
			}
			if _, err := target.Exec(`DELETE FROM pages WHERE id=?`, pageID); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"page_artifact_threads", "page_artifact_comments", "page_artifact_comment_mutations"} {
				var n int
				if err := target.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
					t.Fatalf("cascade cleanup %s=%d %v", table, n, err)
				}
			}
		})
	}
}

func TestArtifactRunBackupRestoreDoesNotResurrectAuthority(t *testing.T) {
	for _, fork := range []string{"", "run-authority-fork"} {
		t.Run("fork="+fork, func(t *testing.T) {
			h, ws, user, _, token := artifactRunFixture(t)
			artifactExpect(t, artifactAgentSave(t, h, ws, token, `{"html":"<p>Published by authorized agent</p>","base_rev":0,"mutation_id":"source"}`), 201)
			const pass = "artifact-run-authority-roundtrip-password"
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
			for _, table := range []string{"artifact_run_principals", "page_artifact_run_delegations"} {
				var n int
				if err := target.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
					t.Fatalf("restore resurrected %s authority: %d %v", table, n, err)
				}
			}
			restoredHandler := NewPageHandler(target, nil, newTestLogger())
			restoredHandler.artifactStoragePath = root
			restoredHandler.artifactMasterToken = h.artifactMasterToken
			artifactExpect(t, artifactAgentSave(t, restoredHandler, restored.RestoredWorkspaceID, token, `{"html":"<p>must not publish</p>","base_rev":1,"mutation_id":"revived"}`), 403)
			var count int
			if err := target.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("restored immutable provenance changed: %d %v", count, err)
			}
		})
	}
}
