package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Chat attachment LIFECYCLE — list and delete (audit §3, the lifecycle gap).
//
// Until this change a chat attachment could be created and never enumerated or
// reclaimed: there was no list and no delete on any surface, so the metadata
// row could not be the authority for anything and the bytes could only be
// removed by deleting the whole chat.

// The two routes exist on the production router. An unregistered path is a
// ServeMux 404 with a plain-text body BEFORE any auth middleware runs, so an
// unauthenticated request separates "route missing" (404 + "page not found")
// from "route present" (401 from authed).
func TestChatAttachmentLifecycleRoutes_AreRegistered(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)

	r, err := NewRouter(db, "test-secret-for-jwt-signing-32chars!!", newTestLogger(),
		WithSocketPath("/tmp/crewship-chat-attachments-route-test.sock"),
		WithInternalToken("internal-test-token"),
		WithInternalBaseURL("http://127.0.0.1:0"),
	)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	for _, tc := range []struct{ method, path, what string }{
		{http.MethodGet, "/api/v1/agents/a1/chats/c1/attachments",
			"list a chat's attachments"},
		{http.MethodDelete, "/api/v1/agents/a1/chats/c1/attachments/att1",
			"delete one chat attachment"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code == http.StatusNotFound && strings.Contains(rr.Body.String(), "page not found") {
			t.Errorf("%s %s is not registered — nothing can %s, so the bytes can be created "+
				"but never enumerated or reclaimed", tc.method, tc.path, tc.what)
			continue
		}
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401 for an unauthenticated call; body=%s",
				tc.method, tc.path, rr.Code, rr.Body.String())
		}
	}
}

// seedSecondChatForAttachment adds another chat on the SAME agent, so the list
// has something it must not return.
func seedSecondChatForAttachment(t *testing.T, h *ProxyHandler, wsID, userID, agentID string) string {
	t.Helper()
	const chatID = "chat-ipc-2"
	if _, err := h.db.Exec(
		`INSERT INTO chats (id, agent_id, workspace_id, created_by, status) VALUES (?, ?, ?, ?, 'ACTIVE')`,
		chatID, agentID, wsID, userID); err != nil {
		t.Fatalf("seed second chat: %v", err)
	}
	return chatID
}

// seedForeignTenant gives a second workspace its own user, so a cross-tenant
// call is a real one rather than a doctored context.
func seedForeignTenant(t *testing.T, h *ProxyHandler) (userID, wsID string) {
	t.Helper()
	userID = "user-foreign-attach"
	wsID = "ws-foreign-attach"
	if _, err := h.db.Exec(
		`INSERT INTO users (id, email, full_name) VALUES (?, 'foreign@example.com', 'Foreign')`,
		userID); err != nil {
		t.Fatalf("seed foreign user: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', 'foreign-attach')`, wsID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	return userID, wsID
}

func listChatAttachments(t *testing.T, h *ProxyHandler, userID, wsID, agentID, chatID string) (int, []map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/agents/"+agentID+"/chats/"+chatID+"/attachments", nil)
	req.SetPathValue("agentId", agentID)
	req.SetPathValue("chatId", chatID)
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.ListAgentChatAttachments(rr, req)
	var out []map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out, rr.Body.String()
}

func deleteChatAttachment(t *testing.T, h *ProxyHandler, userID, wsID, agentID, chatID, attachmentID string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete,
		"/api/v1/agents/"+agentID+"/chats/"+chatID+"/attachments/"+attachmentID, nil)
	req.SetPathValue("agentId", agentID)
	req.SetPathValue("chatId", chatID)
	req.SetPathValue("attachmentId", attachmentID)
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.DeleteAgentChatAttachment(rr, req)
	return rr.Code, rr.Body.String()
}

// attachmentIDForPath returns the id of the row whose relative path the upload
// response reported — the identity a client would hold.
func attachmentIDForPath(t *testing.T, h *ProxyHandler, chatID, relPath string) string {
	t.Helper()
	var id string
	if err := h.db.QueryRow(
		`SELECT id FROM attachments WHERE chat_id = ? AND storage_key = ?`,
		chatID, storageKeyForRelPath(relPath)).Scan(&id); err != nil {
		t.Fatalf("no attachment row for path %q: %v", relPath, err)
	}
	return id
}

// The list is one chat's attachments, in one workspace, and nothing else.
func TestListChatAttachments_ScopedToChatAndWorkspace(t *testing.T) {
	h, _, userID, wsID, agentID, chatID := newChatAttachmentFixture(t)
	other := seedSecondChatForAttachment(t, h, wsID, userID, agentID)

	if code, _, body := uploadChatAttachment(t, h, userID, wsID, agentID, chatID, "mine.txt", "mine"); code != http.StatusCreated {
		t.Fatalf("seed upload: status=%d body=%s", code, body)
	}
	if code, _, body := uploadChatAttachment(t, h, userID, wsID, agentID, other, "theirs.txt", "theirs"); code != http.StatusCreated {
		t.Fatalf("seed upload (other chat): status=%d body=%s", code, body)
	}

	code, items, body := listChatAttachments(t, h, userID, wsID, agentID, chatID)
	if code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body=%s", code, body)
	}
	if len(items) != 1 {
		t.Fatalf("list returned %d attachments, want 1 (only this chat's); body=%s", len(items), body)
	}
	got := items[0]
	if got["filename"] != "mine.txt" {
		t.Errorf("filename = %v, want mine.txt — the list is leaking another chat's files", got["filename"])
	}
	// The list is what a caller uses to REACH an attachment, so it has to carry
	// both the identity and the agent-visible path.
	for _, k := range []string{"id", "sha256", "size_bytes", "path", "agent_path", "created_at"} {
		if _, ok := got[k]; !ok {
			t.Errorf("list row is missing %q — %v", k, got)
		}
	}
	if p, _ := got["path"].(string); !strings.HasPrefix(p, "attachments/"+chatID+"/") {
		t.Errorf("path = %q, want it under attachments/%s/", p, chatID)
	}
	if ap, _ := got["agent_path"].(string); ap != "/output/alex/"+got["path"].(string) {
		t.Errorf("agent_path = %q does not match path %q", ap, got["path"])
	}

	// Cross-tenant: another workspace's caller cannot enumerate this chat, and
	// gets the same answer as for a chat that never existed.
	foreignUser, foreignWS := seedForeignTenant(t, h)
	code, _, body = listChatAttachments(t, h, foreignUser, foreignWS, agentID, chatID)
	if code != http.StatusNotFound {
		t.Errorf("cross-tenant list status = %d, want 404 (the convention every other chat route uses); body=%s",
			code, body)
	}
}

// A reserved-but-unpublished row is not an attachment yet: it was never
// returned to a caller and must never appear in a listing.
func TestListChatAttachments_OmitsUnpublishedRows(t *testing.T) {
	h, _, userID, wsID, agentID, chatID := newChatAttachmentFixture(t)
	if _, err := h.db.Exec(`
		INSERT INTO attachments
			(id, workspace_id, owner_type, chat_id, filename, content_type, size_bytes,
			 sha256, storage_key, state, created_at)
		VALUES ('att-pending', ?, 'chat', ?, 'half.txt', 'text/plain', 4, ?, ?, 'pending', '2026-01-01T00:00:00Z')`,
		wsID, chatID, attachmentDigest([]byte("half")),
		"crew-ipc/alex/attachments/"+chatID+"/att-pending/half.txt"); err != nil {
		t.Fatalf("seed pending row: %v", err)
	}

	code, items, body := listChatAttachments(t, h, userID, wsID, agentID, chatID)
	if code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body=%s", code, body)
	}
	if len(items) != 0 {
		t.Errorf("list returned %d row(s) for an upload that never published; body=%s", len(items), body)
	}
}

// Delete removes the row AND the bytes, and says the same thing the second
// time it is called.
func TestDeleteChatAttachment_RemovesRowAndBytesIdempotently(t *testing.T) {
	h, store, userID, wsID, agentID, chatID := newChatAttachmentFixture(t)
	code, resp, body := uploadChatAttachment(t, h, userID, wsID, agentID, chatID, "evidence.pdf", "bytes-A")
	if code != http.StatusCreated {
		t.Fatalf("upload: status=%d body=%s", code, body)
	}
	attID := attachmentIDForPath(t, h, chatID, resp.Path)
	if _, ok := store.get(storageKeyForRelPath(resp.Path)); !ok {
		t.Fatalf("upload stored nothing at %q", resp.Path)
	}

	code, body = deleteChatAttachment(t, h, userID, wsID, agentID, chatID, attID)
	if code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204; body=%s", code, body)
	}
	var rows int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE id = ?`, attID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("delete left the metadata row behind")
	}
	if _, ok := store.get(storageKeyForRelPath(resp.Path)); ok {
		t.Errorf("delete left the bytes at %q — they are now unreachable: no row names them and "+
			"the content-addressed sweep never walks this tree", resp.Path)
	}
	// What it asked the storage layer to remove is the attachment's OWN
	// directory, so a deleted upload leaves no empty directory behind. It must
	// never be the chat's shared directory.
	wantTarget := "crew-ipc/alex/attachments/" + chatID + "/" + attID
	if got := store.deletedPaths(); len(got) != 1 || got[0] != wantTarget {
		t.Errorf("unlinked %v, want exactly [%s]", got, wantTarget)
	}

	// Twice in a row. "It is not attached any more" is already true, which is
	// what the caller asked for.
	code, body = deleteChatAttachment(t, h, userID, wsID, agentID, chatID, attID)
	if code != http.StatusNoContent {
		t.Errorf("second delete status = %d, want 204 (idempotent); body=%s", code, body)
	}
}

// Another workspace's attachment is refused, and survives the attempt.
func TestDeleteChatAttachment_CrossTenantRefused(t *testing.T) {
	h, store, userID, wsID, agentID, chatID := newChatAttachmentFixture(t)
	code, resp, body := uploadChatAttachment(t, h, userID, wsID, agentID, chatID, "private.pdf", "secret")
	if code != http.StatusCreated {
		t.Fatalf("upload: status=%d body=%s", code, body)
	}
	attID := attachmentIDForPath(t, h, chatID, resp.Path)

	foreignUser, foreignWS := seedForeignTenant(t, h)
	code, body = deleteChatAttachment(t, h, foreignUser, foreignWS, agentID, chatID, attID)
	if code != http.StatusNotFound {
		t.Errorf("cross-tenant delete status = %d, want 404; body=%s", code, body)
	}
	var rows int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE id = ?`, attID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("cross-tenant delete removed the row")
	}
	if _, ok := store.get(storageKeyForRelPath(resp.Path)); !ok {
		t.Errorf("cross-tenant delete removed another workspace's bytes")
	}
}

// An attachment uploaded BEFORE the id segment existed keeps working.
//
// Its storage_key is the two-segment legacy form,
// <crew>/<slug>/attachments/<chatId>/<filename>. storage_key has always been
// the authority on where an attachment lives — that is why it is a column
// rather than something derived on read — so the row lists at its original
// path, and the delete unlinks exactly the bytes it names rather than a path
// recomputed under the new scheme.
func TestChatAttachments_LegacyPathRowsStillWork(t *testing.T) {
	h, store, userID, wsID, agentID, chatID := newChatAttachmentFixture(t)

	legacyKey := "crew-ipc/alex/attachments/" + chatID + "/old-report.pdf"
	legacyRel := "attachments/" + chatID + "/old-report.pdf"
	// The bytes are where the OLD upload path put them.
	store.mu.Lock()
	store.files[legacyKey] = "legacy-bytes"
	store.mu.Unlock()
	if _, err := h.db.Exec(`
		INSERT INTO attachments
			(id, workspace_id, owner_type, chat_id, filename, content_type, size_bytes,
			 sha256, storage_key, created_at)
		VALUES ('att-legacy', ?, 'chat', ?, 'old-report.pdf', 'application/pdf', 12, ?, ?, '2026-08-01T09:00:00Z')`,
		wsID, chatID, attachmentDigest([]byte("legacy-bytes")), legacyKey); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	// The migration's DEFAULT is what makes an existing row published rather
	// than a candidate for the reclaimer.
	var state string
	if err := h.db.QueryRow(`SELECT state FROM attachments WHERE id = 'att-legacy'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != attachmentStateStored {
		t.Fatalf("a row that predates the state column has state %q, want %q — every row that "+
			"already exists was written AFTER its bytes landed, so it is published by construction",
			state, attachmentStateStored)
	}

	code, items, body := listChatAttachments(t, h, userID, wsID, agentID, chatID)
	if code != http.StatusOK || len(items) != 1 {
		t.Fatalf("list status=%d items=%d, want 200 and 1; body=%s", code, len(items), body)
	}
	if p, _ := items[0]["path"].(string); p != legacyRel {
		t.Errorf("legacy path = %q, want %q — the recorded key is the authority, not a recomputed path", p, legacyRel)
	}

	if code, body := deleteChatAttachment(t, h, userID, wsID, agentID, chatID, "att-legacy"); code != http.StatusNoContent {
		t.Fatalf("delete legacy attachment: status=%d body=%s", code, body)
	}
	if _, ok := store.get(legacyKey); ok {
		t.Errorf("delete did not unlink the legacy blob at %q", legacyKey)
	}
	// Crucially it unlinked the FILE, not the chat's shared directory — a
	// legacy key's parent holds every other attachment in the conversation.
	if got := store.deletedPaths(); len(got) != 1 || got[0] != legacyKey {
		t.Errorf("unlinked %v, want exactly [%s] — a legacy attachment's parent directory is shared",
			got, legacyKey)
	}
	var rows int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE id = 'att-legacy'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("delete left the legacy row behind")
	}
}
