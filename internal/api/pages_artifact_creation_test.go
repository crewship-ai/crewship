package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func artifactCreationOptIn(t *testing.T, h *PageHandler, ws, user, method string) *httptest.ResponseRecorder {
	t.Helper()
	r := pagesGrantRequest(t, method, "/", ws, user, "OWNER", `{}`)
	r.SetPathValue("runId", "artifact-run")
	rr := httptest.NewRecorder()
	if method == "DELETE" {
		h.RevokeArtifactCreation(rr, r)
	} else {
		h.AuthorizeArtifactCreation(rr, r)
	}
	return rr
}
func artifactCreateAgent(t *testing.T, h *PageHandler, ws, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, ws)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
	rr := httptest.NewRecorder()
	h.CreateArtifactAgent(rr, r.WithContext(ctx))
	return rr
}
func artifactPublishCreated(t *testing.T, h *PageHandler, ws, token, slug, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.SetPathValue("slug", slug)
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, ws)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
	rr := httptest.NewRecorder()
	h.PublishArtifactAgent(rr, r.WithContext(ctx))
	return rr
}

const creationBody = `{"slug":"agent-created","name":"Agent report","html":"<h1>Created by authorized run</h1>","mutation_id":"create-one"}`

func TestArtifactCreation_DefaultOffAtomicOwnershipAndRetry(t *testing.T) {
	h, ws, user, _, token := artifactRunFixture(t)
	artifactExpect(t, artifactCreateAgent(t, h, ws, token, creationBody), 403)
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_creations`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("default-off created page: %d %v", n, err)
	}
	rr := artifactCreationOptIn(t, h, ws, user, "POST")
	artifactExpect(t, rr, 201)
	var authorization struct {
		Remaining    int    `json:"remaining"`
		MaxArtifacts int    `json:"max_artifacts"`
		Revoked      bool   `json:"revoked"`
		Expires      string `json:"expires_at"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &authorization); err != nil {
		t.Fatal(err)
	}
	if authorization.Remaining != 1 || authorization.MaxArtifacts != 1 || authorization.Revoked || authorization.Expires == "" {
		t.Fatalf("authorization=%+v", authorization)
	}
	artifactExpect(t, artifactCreationOptIn(t, h, ws, user, "POST"), 200)
	rr = artifactCreateAgent(t, h, ws, token, creationBody)
	artifactExpect(t, rr, 201)
	var result struct {
		PageID   string `json:"page_id"`
		Slug     string `json:"slug"`
		Revision int64  `json:"revision"`
		URL      string `json:"url"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.PageID == "" || result.Slug != "agent-created" || result.Revision != 1 || result.URL != "/pages/agent-created" {
		t.Fatalf("result=%+v", result)
	}
	artifactExpect(t, artifactCreateAgent(t, h, ws, token, creationBody), 200)
	artifactExpect(t, artifactCreateAgent(t, h, ws, token, strings.Replace(creationBody, "Agent report", "Changed report", 1)), 409)
	artifactExpect(t, artifactCreateAgent(t, h, ws, token, strings.ReplaceAll(creationBody, "create-one", "create-two")), 409)
	var owner, agent, run, sourceChat string
	var crew string
	if err := h.db.QueryRow(`SELECT p.owner_user_id,COALESCE(p.owner_crew_id,''),r.author_agent_id,r.author_run_id,r.source_chat_id FROM pages p JOIN page_artifact_revisions r ON r.page_id=p.id WHERE p.id=?`, result.PageID).Scan(&owner, &crew, &agent, &run, &sourceChat); err != nil {
		t.Fatal(err)
	}
	if owner != user || crew != "" || agent != "artifact-agent" || run != "artifact-run" || sourceChat != "artifact-chat" {
		t.Fatalf("owner/attribution %s %s %s %s %s", owner, crew, agent, run, sourceChat)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_grants WHERE page_id=? AND subject_type='agent' AND subject_id='artifact-agent' AND level='write' AND granted_by_user_id=?`, result.PageID, user).Scan(&n); err != nil || n != 1 {
		t.Fatalf("bounded grant=%d %v", n, err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=?`, result.PageID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("creation retry duplicated revision: %d %v", n, err)
	}
	artifactExpect(t, artifactPublishCreated(t, h, ws, token, "agent-created", `{"html":"<h1>Next</h1>","base_rev":1,"mutation_id":"next"}`), 201)
	artifactExpect(t, artifactCreationOptIn(t, h, ws, user, "DELETE"), 204)
	artifactExpect(t, artifactPublishCreated(t, h, ws, token, "agent-created", `{"html":"<h1>Revoked</h1>","base_rev":2,"mutation_id":"denied"}`), 403)
	artifactExpect(t, artifactCreateAgent(t, h, ws, token, creationBody), 403)
	// Revoking creation affects only the newly created page/run, not a separate
	// pre-existing page the human explicitly delegated through the original API.
	artifactExpect(t, artifactAgentSave(t, h, ws, token, `{"html":"<p>existing</p>","base_rev":0,"mutation_id":"existing"}`), 201)
}
func TestArtifactCreation_RevocationDuringUploadAndOwnerSpoof(t *testing.T) {
	h, ws, user, _, token := artifactRunFixture(t)
	artifactExpect(t, artifactCreationOptIn(t, h, ws, user, "POST"), 201)
	r := httptest.NewRequest("POST", "/", strings.NewReader(creationBody))
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, ws)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
	r.Body = &artifactRevokingBody{reader: strings.NewReader(creationBody), revoke: func() {
		if _, err := h.db.Exec(`UPDATE artifact_creation_authorizations SET revoked_at='2026-10-13T00:00:00.000000000Z'`); err != nil {
			t.Fatal(err)
		}
	}}
	rr := httptest.NewRecorder()
	h.CreateArtifactAgent(rr, r.WithContext(ctx))
	artifactExpect(t, rr, 403)
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE slug='agent-created'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("revoked creation committed: %d %v", n, err)
	}
	artifactExpect(t, artifactCreateAgent(t, h, ws, token, strings.Replace(creationBody, `"name":`, `"owner_user_id":"victim","name":`, 1)), 400)
	artifactExpect(t, artifactCreateAgent(t, h, ws, "bad-token", creationBody), 403)
	r = pagesGrantRequest(t, "POST", "/", ws, "other-user", "OWNER", `{}`)
	r.SetPathValue("runId", "artifact-run")
	rr = httptest.NewRecorder()
	h.AuthorizeArtifactCreation(rr, r)
	artifactExpect(t, rr, 403)
}

func TestArtifactCreation_ConcurrentOneArtifactAndDeletionDoesNotResetAllowance(t *testing.T) {
	h, ws, user, _, token := artifactRunFixture(t)
	h.journal = nil
	artifactExpect(t, artifactCreationOptIn(t, h, ws, user, "POST"), 201)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := creationBody
			if i == 1 {
				body = strings.ReplaceAll(strings.ReplaceAll(body, "agent-created", "agent-created-second"), "create-one", "create-second")
			}
			codes <- artifactCreateAgent(t, h, ws, token, body).Code
		}()
	}
	wg.Wait()
	close(codes)
	seen := map[int]int{}
	for code := range codes {
		seen[code]++
	}
	if seen[201] != 1 || seen[409] != 1 {
		t.Fatalf("concurrent creation=%v", seen)
	}
	if _, err := h.db.Exec(`DELETE FROM pages WHERE id IN (SELECT page_id FROM page_artifact_creations)`); err != nil {
		t.Fatal(err)
	}
	artifactExpect(t, artifactCreateAgent(t, h, ws, token, `{"slug":"another","name":"Another","html":"<p>x</p>","mutation_id":"another"}`), 409)
}

func TestArtifactCreation_BackupPreservesPageButNotCreationAuthority(t *testing.T) {
	h, ws, user, _, token := artifactRunFixture(t)
	artifactExpect(t, artifactCreationOptIn(t, h, ws, user, "POST"), 201)
	artifactExpect(t, artifactCreateAgent(t, h, ws, token, creationBody), 201)
	execOrFatal(t, h.db, `INSERT INTO page_artifact_requests(id,page_id,human_user_id,chat_id,agent_id,base_rev,submission_id,fingerprint,instruction,snapshot_json,receipt_json,status,run_id,created_at,updated_at) SELECT 'backup-request',page_id,?,'artifact-chat','artifact-agent',1,'backup-submission','fingerprint','preserve my instruction','{}','live-authority','queued','artifact-run','2020-01-01T00:00:00.000Z','2020-01-01T00:00:00.000Z' FROM page_artifact_creations`, user)
	const pass = "artifact-creation-backup-password"
	created, err := backup.CreateBackup(t.Context(), h.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: pass, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: h.artifactStoragePath})
	if err != nil {
		t.Fatal(err)
	}
	target := setupTestDB(t)
	root := storageDir(t)
	if _, err := backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Path: created.Path, Passphrase: pass, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: root}); err != nil {
		t.Fatal(err)
	}
	var status, receipt, instruction string
	var runID any
	if err := target.QueryRow(`SELECT status,receipt_json,run_id,instruction FROM page_artifact_requests WHERE id='backup-request'`).Scan(&status, &receipt, &runID, &instruction); err != nil || status != "failed" || receipt != "{}" || runID != nil || instruction != "preserve my instruction" {
		t.Fatalf("request restore unsafe/lost: %s %s %v %s %v", status, receipt, runID, instruction, err)
	}
	var n int
	if err := target.QueryRow(`SELECT COUNT(*) FROM artifact_creation_authorizations`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("restored live creation authority: %d %v", n, err)
	}
	if err := target.QueryRow(`SELECT COUNT(*) FROM page_artifact_creations c JOIN pages p ON p.id=c.page_id WHERE p.slug='agent-created'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("creation audit lost: %d %v", n, err)
	}
}

func TestArtifactCreation_RollbackOrphanCollectedByBackgroundWorker(t *testing.T) {
	h, ws, user, _, token := artifactRunFixture(t)
	artifactExpect(t, artifactCreationOptIn(t, h, ws, user, "POST"), 201)
	orphanHTML := "<p>Unique rejected creation</p>"
	body, _ := json.Marshal(map[string]string{"slug": "report", "name": "Conflict", "html": orphanHTML, "mutation_id": "conflict"})
	artifactExpect(t, artifactCreateAgent(t, h, ws, token, string(body)), 409)
	orphan := blobPathFor(h.artifactStoragePath, ws, fmt.Sprintf("%x", sha256.Sum256([]byte(orphanHTML))))
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("rollback blob missing: %v", err)
	}
	artifactExpect(t, artifactCreateAgent(t, h, ws, token, creationBody), 201)
	live := blobPathFor(h.artifactStoragePath, ws, fmt.Sprintf("%x", sha256.Sum256([]byte("<h1>Created by authorized run</h1>"))))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	StartAttachmentBlobGC(ctx, h.db, newTestLogger(), h.artifactStoragePath, 10*time.Millisecond)
	waitForBlobGone(t, orphan, "rejected creation blob")
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("live creation blob collected: %v", err)
	}
}

func TestArtifactCreation_PrincipalCleanupPreservesHistoryAndGrace(t *testing.T) {
	h, ws, user, _, token := artifactRunFixture(t)
	artifactExpect(t, artifactCreationOptIn(t, h, ws, user, "POST"), 201)
	artifactExpect(t, artifactCreateAgent(t, h, ws, token, creationBody), 201)
	now := time.Now().UTC()
	execOrFatal(t, h.db, `UPDATE artifact_run_principals SET expires_at=? WHERE run_id='artifact-run'`, tsformat.Format(now.Add(-23*time.Hour)))
	h.cleanupArtifactRunPrincipals(t.Context(), now)
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM artifact_run_principals`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("grace: %d %v", n, err)
	}
	h.cleanupArtifactRunPrincipals(t.Context(), now.Add(2*time.Hour))
	for _, table := range []string{"artifact_run_principals", "artifact_creation_authorizations", "page_artifact_run_delegations"} {
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("runtime %s: %d %v", table, n, err)
		}
	}
	for _, table := range []string{"page_artifact_creations", "page_artifact_revisions"} {
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n < 1 {
			t.Fatalf("history %s: %d %v", table, n, err)
		}
	}
}
