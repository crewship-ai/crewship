package api

// Four defects the #1768 attachment review found, each pinned by the test that
// fails without its fix (#1791 review).
//
//	1. the agent door took workspace_id from the BODY and never checked it
//	   against the internal token's binding — a cross-tenant WRITE;
//	2. the blob reclaim deleted by absence, so it raced any upload in flight;
//	3. the refcount counted rows by digest alone, so a chat row (whose blob is
//	   somewhere else entirely) pinned an issue attachment's bytes forever;
//	4. the de-duplication key carried no filename, so the second of two
//	   identically-valued files was answered 200 under the FIRST one's name.
//
// They are one file because they are one subject — what the (workspace, digest)
// key is allowed to decide — and reading them together is what makes the shape
// of the mistake visible.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// ── fixtures ──────────────────────────────────────────────────────────────

// seedOtherTenantIssue gives the second tenant a crew, an agent and an issue.
//
// The identifier is the victim's own — `idx_mission_identifier` is UNIQUE across
// the whole instance, so an attacker cannot collide with it and does not need to:
// identifiers are short, sequential and printed in every notification, so naming
// one belonging to another tenant costs nothing. That is the pair the attacker
// sends, and it is why the workspace predicate inside resolveIssueIn does not
// help — the attacker supplies a consistent pair, just not THEIR pair.
func seedOtherTenantIssue(t *testing.T, f attachFixture, wsID, identifier string) string {
	t.Helper()
	if _, err := f.h.db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES ('crew-other', ?, 'Other Crew', 'other-eng', 'ENG')`,
		wsID); err != nil {
		t.Fatalf("insert other crew: %v", err)
	}
	if _, err := f.h.db.Exec(`
		INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		VALUES ('agent-other-lead', ?, 'crew-other', 'Lead', 'lead', 'LEAD', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
		wsID); err != nil {
		t.Fatalf("insert other agent: %v", err)
	}
	return seedIssue(t, f.h.db, wsID, "crew-other", "agent-other-lead", identifier, "BACKLOG")
}

// ── FINDING 1: the agent door's body-carried workspace_id ─────────────────

// A crew-bound internal token naming ANOTHER workspace in the body is refused.
//
// requireInternal pins the workspace it can see — the QUERY — and the read
// routes on this surface take it from there, so they were always covered. Attach
// takes it from the JSON BODY, which the middleware never parses; without the
// explicit assert the handler resolved the victim's issue, wrote the blob under
// the victim's storage tree, inserted a row in the victim's workspace and put an
// `attachment_added` entry on the victim's issue timeline — which then notifies
// the victim's users. The agent_id check two lines further down does not catch
// it: it validates the id against the ATTACKER-SUPPLIED workspace.
func TestAttachment_AgentAttach_RefusesForeignWorkspaceInBody(t *testing.T) {
	f := newAttachmentFixture(t)
	_, otherWS := seedOtherTenant(t, f)
	victimIssue := seedOtherTenantIssue(t, f, otherWS, "OTH-1")

	body := fmt.Sprintf(`{"workspace_id":%q,"agent_id":"agent-worker","filename":"notes.md","content_base64":%q}`,
		otherWS, base64.StdEncoding.EncodeToString([]byte("# planted by another tenant\n")))
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.SetPathValue("identifier", "OTH-1")
	// The caller's token is bound to the FIRST workspace — this is a sidecar in
	// tenant A talking about tenant B.
	req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, f.wsID))

	rr := httptest.NewRecorder()
	f.internal.Attach(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 — a token bound to %s must not write into %s (body=%s)",
			rr.Code, f.wsID, otherWS, rr.Body.String())
	}

	// The refusal has to be a refusal, not a 403 after the write.
	var rows int
	if err := f.h.db.QueryRow(
		`SELECT COUNT(*) FROM attachments WHERE workspace_id = ?`, otherWS).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Errorf("%d attachment row(s) landed in the victim workspace", rows)
	}
	var activity int
	if err := f.h.db.QueryRow(
		`SELECT COUNT(*) FROM mission_activity WHERE mission_id = ?`, victimIssue).Scan(&activity); err != nil {
		t.Fatalf("count activity: %v", err)
	}
	if activity != 0 {
		t.Errorf("%d row(s) landed on the victim's issue timeline — that is what notifies their users", activity)
	}
	blobs, _ := filepath.Glob(filepath.Join(f.storage, "attachments", otherWS, "*", "*"))
	if len(blobs) != 0 {
		t.Errorf("%d blob(s) landed under the victim's storage tree", len(blobs))
	}
}

// The sibling READ routes of the same file, checked rather than assumed.
//
// They read workspace_id from the query, which requireInternal validates against
// the token binding (403) and injects when omitted — so the omission Attach had
// is not shared. This pins that: it is the reason the fix is one assert in one
// handler and not three.
func TestAttachment_AgentRead_QueryWorkspaceIsPinnedByTheToken(t *testing.T) {
	f := newAttachmentFixture(t)
	_, otherWS := seedOtherTenant(t, f)

	ih := NewInternalHandler(f.h.db, bindTestMaster, newTestLogger())
	token := internaltoken.DeriveWorkspaceToken(bindTestMaster, f.wsID)

	for _, tc := range []struct {
		name string
		run  http.HandlerFunc
	}{
		{"list", f.internal.List},
		{"read", f.internal.Read},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := bindTestRequest(token, "?workspace_id="+otherWS)
			req.SetPathValue("identifier", "ENG-1")
			rr := httptest.NewRecorder()
			ih.requireInternal(tc.run).ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403 (body=%s)", rr.Code, rr.Body.String())
			}
		})
	}
}

// ── FINDING 2: reclaim vs. an upload in flight ────────────────────────────

// The upload takes the blob's lock BEFORE it writes the file, and holds it past
// the INSERT.
//
// This is the half of the invariant the sweep cannot demonstrate on its own: a
// reclaim that locks correctly still deletes an in-flight upload's bytes if the
// uploader never took the lock. So the test holds the lock for the digest the
// upload is about to use and asserts the blob does NOT appear — i.e. the upload
// is waiting — then releases and watches it complete.
//
// It was written after the reclaim-side test survived deleting the upload's
// lock, which is exactly the mutation this one is here to catch.
func TestAttachment_UploadHoldsTheBlobLockAcrossItsInsert(t *testing.T) {
	f := newAttachmentFixture(t)

	content := []byte("written only once the lock is free\n")
	sha := attachmentDigest(content)
	blob := filepath.Join(f.storage, "attachments", f.wsID, sha[:2], sha)
	req := uploadReq(t, f, "ENG-1", "held.log", content)

	unlock := lockAttachmentBlob(f.wsID, sha)
	released := false
	release := func() {
		if !released {
			released = true
			unlock()
		}
	}
	defer release()

	done := make(chan int, 1)
	go func() {
		rr := httptest.NewRecorder()
		f.h.Upload(rr, req)
		done <- rr.Code
	}()

	select {
	case code := <-done:
		release()
		t.Fatalf("the upload finished (%d) while another holder had the (workspace, digest) lock — "+
			"its blob write and its INSERT are not one section, so a sweep landing between them "+
			"deletes the bytes of a row that commits a moment later", code)
	case <-time.After(250 * time.Millisecond):
	}
	if _, err := os.Stat(blob); err == nil {
		release()
		t.Fatalf("the upload wrote its blob without holding the lock — the file is on disk with "+
			"nothing naming it, which is precisely what the sweep calls garbage (%s)", blob)
	}

	release()
	if code := <-done; code != http.StatusCreated {
		t.Fatalf("upload after release = %d, want 201", code)
	}
	if _, err := os.Stat(blob); err != nil {
		t.Errorf("the released upload left no blob: %v", err)
	}
	var rows int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE sha256 = ?`, sha).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("rows = %d, want 1", rows)
	}
}

// A blob written but not yet inserted must survive a concurrent reclaim.
//
// This is the race stated exactly: the upload holds the (workspace, digest)
// lock across "write the blob" AND "insert the row", so a reclaim cannot observe
// the gap between them. The test stands in for the uploader by taking that same
// lock, and asserts the sweep BLOCKS rather than deleting — asserting on the
// mechanism, because a sweep that deletes by absence is racing by construction
// and no amount of narrowing turns that into a proof.
func TestAttachment_ReclaimCannotSeeAnUploadMidFlight(t *testing.T) {
	f := newAttachmentFixture(t)

	content := []byte("bytes written, row not yet committed\n")
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])

	// Enter the uploader's critical section and get as far as the blob.
	unlock := lockAttachmentBlob(f.wsID, sha)
	if _, _, err := storeAttachmentBlob(f.storage, f.wsID, content); err != nil {
		t.Fatalf("store blob: %v", err)
	}
	blob := filepath.Join(f.storage, "attachments", f.wsID, sha[:2], sha)

	done := make(chan int, 1)
	go func() {
		n, err := reclaimAttachmentBlobs(context.Background(), f.h.db, f.storage, f.wsID)
		if err != nil {
			t.Errorf("reclaim: %v", err)
		}
		done <- n
	}()

	select {
	case n := <-done:
		unlock()
		t.Fatalf("the sweep finished (%d removed) while an upload held the blob's lock — "+
			"it deletes by absence, so it cannot tell 'no row names this' from 'the row is one "+
			"statement away'", n)
	case <-time.After(250 * time.Millisecond):
		// Still blocked, which is the whole assertion.
	}
	if _, err := os.Stat(blob); err != nil {
		t.Fatalf("the in-flight blob was removed: %v", err)
	}

	// Finish the upload the way attachBytes does, then release.
	if _, err := f.h.db.Exec(`
		INSERT INTO attachments
			(id, workspace_id, owner_type, mission_id, filename, content_type, size_bytes, sha256, storage_key, created_at)
		SELECT ?, ?, 'issue', id, 'inflight.log', 'text/plain; charset=utf-8', ?, ?, ?, datetime('now')
		FROM missions WHERE identifier = 'ENG-1' AND workspace_id = ?`,
		generateCUID(), f.wsID, len(content), sha, attachmentStorageKey(f.wsID, sha), f.wsID); err != nil {
		unlock()
		t.Fatalf("insert row: %v", err)
	}
	unlock()

	if n := <-done; n != 0 {
		t.Errorf("the sweep removed %d blob(s); the only one it saw belongs to a live row", n)
	}
	if _, err := os.Stat(blob); err != nil {
		t.Errorf("the blob of a committed row was removed: %v", err)
	}
}

// The same race, driven as traffic rather than as a lock: uploads to one issue
// while sweeps run against the whole workspace. Every row the API said it
// created must have its bytes.
func TestAttachment_ConcurrentUploadsSurviveConcurrentReclaims(t *testing.T) {
	f := newAttachmentFixture(t)

	const uploads = 24
	shas := make([]string, uploads)
	codes := make([]int, uploads)

	// The sweeper runs for as long as the uploads do, so every upload overlaps
	// one. It is the workspace-wide sweep the issue-delete handler used to run.
	stop := make(chan struct{})
	swept := make(chan struct{})
	go func() {
		defer close(swept)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := reclaimAttachmentBlobs(context.Background(), f.h.db, f.storage, f.wsID); err != nil {
				t.Errorf("reclaim: %v", err)
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < uploads; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			content := []byte(fmt.Sprintf("upload number %d\n", i))
			rr := httptest.NewRecorder()
			f.h.Upload(rr, uploadReq(t, f, "ENG-1", fmt.Sprintf("u%d.log", i), content))
			codes[i] = rr.Code
			var att attachmentResponse
			_ = json.Unmarshal(rr.Body.Bytes(), &att)
			shas[i] = att.SHA256
		}(i)
	}
	wg.Wait()
	close(stop)
	<-swept

	for i := 0; i < uploads; i++ {
		if codes[i] != http.StatusCreated {
			t.Errorf("upload %d → %d, want 201 — a sweep that deletes an in-flight .tmp-* file "+
				"makes the uploader's rename fail with a 500", i, codes[i])
			continue
		}
		blob := filepath.Join(f.storage, "attachments", f.wsID, shas[i][:2], shas[i])
		if _, err := os.Stat(blob); err != nil {
			t.Errorf("upload %d: the API reported success and the bytes are gone (%v) — "+
				"listed in the UI, 404 on every download, forever", i, err)
		}
	}
}

// A .tmp-* file young enough to be an upload in progress is left alone; an old
// one is the leftover of a crashed write and goes.
//
// The uploader holds the digest's lock while it writes, but the reclaim cannot
// know which digest a `.tmp-1234` belongs to — the name carries none. The age
// floor is what stands in for the lock there, and it is the reason the sweep
// does not have to guess.
func TestAttachment_ReclaimLeavesYoungTempFilesAlone(t *testing.T) {
	f := newAttachmentFixture(t)
	att, code := upload(t, f, "seed.log", []byte("so the shard dir exists\n"))
	if code != http.StatusCreated {
		t.Fatalf("seed upload = %d", code)
	}
	shard := filepath.Join(f.storage, "attachments", f.wsID, att.SHA256[:2])

	young := filepath.Join(shard, ".tmp-inflight")
	if err := os.WriteFile(young, []byte("half an upload"), 0o640); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	old := filepath.Join(shard, ".tmp-crashed")
	if err := os.WriteFile(old, []byte("a write that died"), 0o640); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	stale := time.Now().Add(-2 * attachmentTempReclaimAge)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if _, err := reclaimAttachmentBlobs(context.Background(), f.h.db, f.storage, f.wsID); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if _, err := os.Stat(young); err != nil {
		t.Errorf("the sweep deleted a temp file an upload may still be writing (%v) — "+
			"the rename that follows it fails, and a 500 is what the uploader sees", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("a temp file older than the floor survived: %v", err)
	}
}

// ── FINDING 3: a chat row pinning an issue's blob ─────────────────────────

// Deleting the last issue attachment naming a digest removes the bytes even
// when a CHAT row carries the same digest.
//
// A chat attachment's blob is not content-addressed — it lives at
// <crew>/<agent>/attachments/<chat>/<filename>, which is the agent-visible
// contract — so it is a different file with the same hash. Counting rows by
// (workspace, sha256) made that unrelated file pin the issue attachment's bytes
// forever: the user deletes the file, the API says ok, the bytes stay. For
// something a user deleted deliberately that is the failure that matters.
func TestAttachment_ChatRowDoesNotPinAnIssueBlob(t *testing.T) {
	f := newAttachmentFixture(t)
	content := []byte("a log pasted into chat and attached to the issue\n")
	att, code := upload(t, f, "shared.log", content)
	if code != http.StatusCreated {
		t.Fatalf("upload = %d", code)
	}

	// A chat row for the same bytes, shaped exactly as recordChatAttachment
	// writes it — same digest, its own storage_key.
	if _, err := f.h.db.Exec(
		`INSERT INTO chats (id, agent_id, workspace_id) VALUES ('chat-1', 'agent-worker', ?)`, f.wsID); err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	if _, err := f.h.db.Exec(`
		INSERT INTO attachments (id, workspace_id, owner_type, chat_id, filename, content_type,
			size_bytes, sha256, storage_key, created_at)
		VALUES (?, ?, 'chat', 'chat-1', 'shared.log', 'text/plain; charset=utf-8', ?, ?, ?, datetime('now'))`,
		generateCUID(), f.wsID, len(content), att.SHA256,
		f.crewID+"/worker/attachments/chat-1/shared.log"); err != nil {
		t.Fatalf("insert chat attachment row: %v", err)
	}

	blob := filepath.Join(f.storage, "attachments", f.wsID, att.SHA256[:2], att.SHA256)
	rr := httptest.NewRecorder()
	f.h.Delete(rr, scopedReq(t, f, "DELETE", "ENG-1", att.ID, f.wsID))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete = %d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(blob); !os.IsNotExist(err) {
		t.Errorf("the deleted file's bytes are still on disk (%v) — a chat row whose blob lives "+
			"somewhere else entirely held the refcount above zero", err)
	}

	// And the sweep agrees: it must not treat the digest as live either.
	if _, err := reclaimAttachmentBlobs(context.Background(), f.h.db, f.storage, f.wsID); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if _, err := os.Stat(blob); !os.IsNotExist(err) {
		t.Errorf("the sweep left the blob too: %v", err)
	}
}

// The other direction, so the fix above cannot be "ignore chat rows" applied too
// widely: a chat attachment's OWN bytes are never touched by the issue store.
// They are not in its tree at all, and the crew-files surface owns them.
func TestAttachment_ReclaimNeverTouchesChatBlobs(t *testing.T) {
	f := newAttachmentFixture(t)
	chatBlob := filepath.Join(f.storage, f.crewID, "worker", "attachments", "chat-1", "notes.md")
	if err := os.MkdirAll(filepath.Dir(chatBlob), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(chatBlob, []byte("chat bytes\n"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := reclaimAttachmentBlobs(context.Background(), f.h.db, f.storage, f.wsID); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if _, err := os.Stat(chatBlob); err != nil {
		t.Errorf("the issue sweep reached outside attachments/<workspace>/: %v", err)
	}
}

// ── FINDING 4: two names, one digest ──────────────────────────────────────

// crash-before-fix.log and crash-after-fix.log with identical bytes are TWO
// attachments.
//
// The de-duplication key was (owner, sha256) with no filename, so the second
// upload hit the UNIQUE index, was answered 200 with the FIRST file's name, and
// wrote no activity row. The user sees one file, believes both are attached, and
// the agent reading the issue is told the second name never existed. Zero-byte
// files collide across every unrelated pair for the same reason.
func TestAttachment_SameBytesUnderTwoNamesAreTwoAttachments(t *testing.T) {
	f := newAttachmentFixture(t)
	content := []byte("panic: same crash, captured twice\n")

	before, code := upload(t, f, "crash-before-fix.log", content)
	if code != http.StatusCreated {
		t.Fatalf("first upload = %d", code)
	}
	after, code := upload(t, f, "crash-after-fix.log", content)
	if code != http.StatusCreated {
		t.Fatalf("second upload = %d, want 201 — it is a different file with the same bytes, "+
			"and a 200 hands back the first one's name", code)
	}
	if after.ID == before.ID {
		t.Fatalf("the second upload returned the FIRST row (%q)", before.ID)
	}
	if after.Filename != "crash-after-fix.log" {
		t.Errorf("second upload came back as %q — the name the user chose was discarded", after.Filename)
	}
	if after.SHA256 != before.SHA256 {
		t.Fatalf("identical bytes hashed differently — the fixture is wrong")
	}

	var activity int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM mission_activity WHERE action = ?`,
		string(actionAttachmentAdded)).Scan(&activity); err != nil {
		t.Fatalf("count activity: %v", err)
	}
	if activity != 2 {
		t.Errorf("activity rows = %d, want 2 — an attachment with no audit row reads as one "+
			"that was never attached", activity)
	}

	// One blob, two rows: deleting the first must not take the second's bytes.
	blob := filepath.Join(f.storage, "attachments", f.wsID, before.SHA256[:2], before.SHA256)
	rr := httptest.NewRecorder()
	f.h.Delete(rr, scopedReq(t, f, "DELETE", "ENG-1", before.ID, f.wsID))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete = %d", rr.Code)
	}
	if _, err := os.Stat(blob); err != nil {
		t.Fatalf("deleting one of two rows sharing a blob removed the bytes: %v", err)
	}
	rr = httptest.NewRecorder()
	f.h.Delete(rr, scopedReq(t, f, "DELETE", "ENG-1", after.ID, f.wsID))
	if rr.Code != http.StatusOK {
		t.Fatalf("second delete = %d", rr.Code)
	}
	if _, err := os.Stat(blob); !os.IsNotExist(err) {
		t.Errorf("the blob survived the delete of its last reference: %v", err)
	}
}

// The SAME name and the same bytes is still one attachment — the idempotent
// retry the 200 exists for. The fix must not turn a double-clicked upload into
// two rows and two timeline entries.
func TestAttachment_SameNameSameBytesIsStillOneAttachment(t *testing.T) {
	f := newAttachmentFixture(t)
	content := []byte("double click\n")
	first, code := upload(t, f, "a.log", content)
	if code != http.StatusCreated {
		t.Fatalf("first = %d", code)
	}
	second, code := upload(t, f, "a.log", content)
	if code != http.StatusOK {
		t.Fatalf("retry = %d, want 200", code)
	}
	if second.ID != first.ID {
		t.Errorf("a retried upload created a second row")
	}
	var rows int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("rows = %d, want 1", rows)
	}
}

// An agent attaching two identically-valued files under different names gets the
// same answer as a human. The store's rules are the store's, not whichever door
// someone remembered.
func TestAttachment_AgentAttach_SameBytesUnderTwoNames(t *testing.T) {
	f := newAttachmentFixture(t)
	payload := base64.StdEncoding.EncodeToString([]byte("identical\n"))

	attach := func(name string) (agentAttachment, int) {
		body := fmt.Sprintf(`{"workspace_id":%q,"agent_id":"agent-worker","filename":%q,"content_base64":%q}`,
			f.wsID, name, payload)
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.SetPathValue("identifier", "ENG-1")
		req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, f.wsID))
		rr := httptest.NewRecorder()
		f.internal.Attach(rr, req)
		var got agentAttachment
		_ = json.Unmarshal(rr.Body.Bytes(), &got)
		return got, rr.Code
	}

	if _, code := attach("run-1.log"); code != http.StatusCreated {
		t.Fatalf("first attach = %d", code)
	}
	second, code := attach("run-2.log")
	if code != http.StatusCreated {
		t.Fatalf("second attach = %d, want 201", code)
	}
	if !strings.Contains(second.Filename, "run-2.log") {
		t.Errorf("agent got back %q, want its own filename", second.Filename)
	}
	var rows int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 2 {
		t.Errorf("rows = %d, want 2", rows)
	}
}

// A guard for the store-level helper the two doors share, so the byte-identical
// case is pinned below the HTTP layer too.
func TestAttachmentContentAddressedKeyMatchesTheSQLPredicate(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	if got := attachmentStorageKey("ws_1", sha); got != "attachments/ws_1/ab/"+sha {
		t.Fatalf("storage key = %q", got)
	}
	if !bytes.Contains([]byte(attachmentContentAddressedPredicate), []byte("storage_key")) {
		t.Errorf("the refcount predicate no longer looks at storage_key: %q", attachmentContentAddressedPredicate)
	}
}
