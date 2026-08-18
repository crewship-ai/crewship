package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Chat attachment IDENTITY (audit P0.2 / P0.3).
//
// Two properties are pinned here, and both were false before this change:
//
//   - one upload is one identity at one location. The blob path used to be
//     attachments/<chatId>/<filename>, so a second upload of `evidence.pdf`
//     with different bytes OVERWROTE the first. Two metadata rows then claimed
//     two different checksums for one file, and only the last bytes existed —
//     a message's path did not identify the version the agent read;
//   - a 201 means the row is durable. recordChatAttachment used to run AFTER
//     the write and was best-effort, so a failed INSERT was logged and the
//     endpoint still answered 201: bytes landed, nobody recorded them, and the
//     blob is outside the content-addressed reclaim sweep.
//
// The response SHAPE is a third invariant, pinned separately below: the
// composer reads {filename, size, path, agent_path} and this change must be
// invisible to it.

// fakeCrewFileStore stands in for the crew file tree behind the IPC socket.
//
// A map rather than a temp directory on purpose: what these tests assert is
// that two uploads address two DIFFERENT keys, and a map makes an overwrite
// observable as "one key, last bytes win" without any filesystem semantics in
// the way.
type fakeCrewFileStore struct {
	mu sync.Mutex
	// files is keyed by the ?path= the API layer asked the IPC layer for —
	// i.e. the storage_key the row records.
	files map[string]string
	// saves counts every accepted write, so a test can prove a second upload
	// did not simply skip the write.
	saves int
	// deletes records every ?path= the delete route asked for, so a test can
	// assert WHAT was unlinked and not merely that something was.
	deletes []string
	// deleteStatus, when >= 400, is what the delete half answers instead of
	// removing anything — the storage layer refusing the unlink (a provisioned
	// tree the server uid cannot write, a stopped crew that owns it).
	deleteStatus int
	deleteBody   string
}

func newFakeCrewFileStore() *fakeCrewFileStore {
	return &fakeCrewFileStore{files: map[string]string{}}
}

func (s *fakeCrewFileStore) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		switch {
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/files/save"):
			body, _ := io.ReadAll(r.Body)
			s.mu.Lock()
			s.files[p] = string(body)
			s.saves++
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/files/delete"):
			s.mu.Lock()
			status, errBody := s.deleteStatus, s.deleteBody
			// Recorded whatever the answer is, so a test can tell a removal
			// that was REFUSED from one that was never attempted.
			s.deletes = append(s.deletes, p)
			if status < 400 {
				// RemoveAll semantics, like the storage provider behind the
				// real route: a path that names a directory takes everything
				// under it, and a path that names nothing is still a success.
				delete(s.files, p)
				for k := range s.files {
					if strings.HasPrefix(k, p+"/") {
						delete(s.files, k)
					}
				}
			}
			s.mu.Unlock()
			if status >= 400 {
				writeJSON(w, status, map[string]string{"error": errBody})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		default:
			http.Error(w, "unexpected IPC call "+r.Method+" "+r.URL.Path, http.StatusNotImplemented)
		}
	})
}

func (s *fakeCrewFileStore) get(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.files[key]
	return v, ok
}

func (s *fakeCrewFileStore) deletedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.deletes...)
}

func (s *fakeCrewFileStore) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.files))
	for k := range s.files {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// uploadResponse is the exact wire shape the composer consumes. Decoding into
// a struct would hide an added or renamed field, so the shape test below reads
// the raw object; this type is for the tests that only need the values.
type uploadResponse struct {
	Filename  string `json:"filename"`
	Size      int    `json:"size"`
	Path      string `json:"path"`
	AgentPath string `json:"agent_path"`
}

// newChatAttachmentFixture wires a handler to a fake crew file store and seeds
// the (crew, agent, chat) triple the upload route needs.
func newChatAttachmentFixture(t *testing.T) (h *ProxyHandler, store *fakeCrewFileStore, userID, wsID, agentID, chatID string) {
	t.Helper()
	store = newFakeCrewFileStore()
	sock := newUnixIPCServer(t, store.handler())
	h = newProxyHandlerForTest(t, sock)
	userID = seedTestUser(t, h.db)
	wsID = seedTestWorkspace(t, h.db, userID)
	agentID, chatID = seedChatForAttachment(t, h, wsID, userID)
	return h, store, userID, wsID, agentID, chatID
}

func uploadChatAttachment(t *testing.T, h *ProxyHandler, userID, wsID, agentID, chatID, filename, content string) (int, uploadResponse, string) {
	t.Helper()
	req := attachmentUploadRequest(t, agentID, chatID, filename, content)
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)
	var out uploadResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out, rr.Body.String()
}

// Two uploads of one filename with different bytes are two attachments. Each
// resolves to its OWN checksum, and neither blob is overwritten.
func TestChatAttachment_SameFilenameDifferentBytesKeepBothIdentities(t *testing.T) {
	h, store, userID, wsID, agentID, chatID := newChatAttachmentFixture(t)

	codeA, respA, bodyA := uploadChatAttachment(t, h, userID, wsID, agentID, chatID, "evidence.pdf", "bytes-A")
	if codeA != http.StatusCreated {
		t.Fatalf("first upload status = %d, want 201; body=%s", codeA, bodyA)
	}
	codeB, respB, bodyB := uploadChatAttachment(t, h, userID, wsID, agentID, chatID, "evidence.pdf", "bytes-B")
	if codeB != http.StatusCreated {
		t.Fatalf("second upload status = %d, want 201; body=%s", codeB, bodyB)
	}

	if respA.Path == respB.Path {
		t.Fatalf("both uploads returned the same path %q — the filename is still the storage identity, "+
			"so the second upload overwrote the first and a message's path cannot say which version the agent read",
			respA.Path)
	}

	// Every row must resolve to bytes whose digest is the digest it recorded.
	rows, err := h.db.Query(
		`SELECT id, sha256, storage_key FROM attachments WHERE chat_id = ? ORDER BY created_at, id`, chatID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]string{} // storage_key -> sha256
	var n int
	for rows.Next() {
		var id, sha, key string
		if err := rows.Scan(&id, &sha, &key); err != nil {
			t.Fatal(err)
		}
		n++
		if other, dup := seen[key]; dup && other != sha {
			t.Errorf("two rows share storage_key %q with different checksums (%s, %s) — "+
				"only one set of bytes can survive at that path", key, other, sha)
		}
		seen[key] = sha
		content, ok := store.get(key)
		if !ok {
			t.Fatalf("row %s names storage_key %q but no bytes were ever written there", id, key)
		}
		if got := attachmentDigest([]byte(content)); got != sha {
			t.Errorf("row %s records sha %s but the bytes at %q hash to %s — the recorded checksum "+
				"describes bytes that no longer exist", id, sha, key, got)
		}
		// The identity has to be IN the path: that is what makes the location
		// unique per upload while keeping the filename legible to the agent.
		if !strings.Contains(key, "/"+id+"/") {
			t.Errorf("storage_key %q does not contain the attachment id %s — the location is not "+
				"unique per upload", key, id)
		}
	}
	if n != 2 {
		t.Fatalf("recorded %d attachment rows, want 2 (two files with different bytes are two attachments)", n)
	}
	if got := store.keys(); len(got) != 2 {
		t.Errorf("blob store holds %d file(s) %v, want 2 — one upload must not overwrite another", len(got), got)
	}
	// The agent-relative path the composer was handed addresses the same bytes
	// the row's checksum describes.
	for _, tc := range []struct{ path, want string }{
		{respA.Path, "bytes-A"},
		{respB.Path, "bytes-B"},
	} {
		got, ok := store.get(storageKeyForRelPath(tc.path))
		if !ok {
			t.Errorf("nothing stored at the returned path %q", tc.path)
			continue
		}
		if got != tc.want {
			t.Errorf("path %q holds %q, want %q — the path the agent is told to open no longer "+
				"resolves to the bytes that upload stored", tc.path, got, tc.want)
		}
	}
}

// storageKeyForRelPath is a test-local convenience: the agent-relative path the
// response returns, prefixed with the crew/slug the fixture seeds.
func storageKeyForRelPath(rel string) string {
	return "crew-ipc/alex/" + rel
}

// Re-uploading the SAME bytes under the SAME name stays one attachment: the
// dedupe index exists so a retried or double-clicked upload cannot inflate the
// number of things that name one blob.
func TestChatAttachment_IdenticalReuploadIsOneIdentity(t *testing.T) {
	h, store, userID, wsID, agentID, chatID := newChatAttachmentFixture(t)

	_, first, _ := uploadChatAttachment(t, h, userID, wsID, agentID, chatID, "notes.txt", "same")

	// Lose the bytes underneath the row, as a crash between the reservation and
	// the publish would. The retry has to REPAIR that rather than answer 201
	// for a file that is not there.
	store.mu.Lock()
	delete(store.files, storageKeyForRelPath(first.Path))
	store.mu.Unlock()

	code, second, body := uploadChatAttachment(t, h, userID, wsID, agentID, chatID, "notes.txt", "same")
	if code != http.StatusCreated {
		t.Fatalf("re-upload status = %d, want 201; body=%s", code, body)
	}
	if first.Path != second.Path {
		t.Errorf("identical re-upload moved the file: %q then %q — a retry must resolve to the "+
			"attachment that already exists", first.Path, second.Path)
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE chat_id = ?`, chatID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("identical re-upload produced %d rows, want 1", n)
	}
	if got := store.keys(); len(got) != 1 {
		t.Errorf("identical re-upload produced %d blobs %v, want 1", len(got), got)
	}
	if got, ok := store.get(storageKeyForRelPath(second.Path)); !ok || got != "same" {
		t.Errorf("the retry answered 201 without restoring the bytes at %q (present=%v, content=%q)",
			second.Path, ok, got)
	}
}

// The upload response is byte-identical in SHAPE to what the composer already
// consumes. Four other agents are editing the frontend against these four keys.
func TestChatAttachment_ResponseShapeUnchanged(t *testing.T) {
	h, _, userID, wsID, agentID, chatID := newChatAttachmentFixture(t)

	req := attachmentUploadRequest(t, agentID, chatID, "diagram.png", "\x89PNG")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rr.Body.String())
	}
	want := []string{"agent_path", "filename", "path", "size"}
	got := make([]string, 0, len(raw))
	for k := range raw {
		got = append(got, k)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("response keys = %v, want exactly %v — the composer decodes this object and must "+
			"not have to change", got, want)
	}

	var resp uploadResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Filename != "diagram.png" {
		t.Errorf("filename = %q, want diagram.png", resp.Filename)
	}
	if resp.Size != 4 {
		t.Errorf("size = %d, want 4", resp.Size)
	}
	// path stays agent-relative and keeps the filename legible; agent_path stays
	// the absolute in-container form of the same path.
	if !strings.HasPrefix(resp.Path, "attachments/"+chatID+"/") {
		t.Errorf("path = %q, want it under attachments/%s/", resp.Path, chatID)
	}
	if !strings.HasSuffix(resp.Path, "/diagram.png") {
		t.Errorf("path = %q must end in the readable filename — it is what the agent is told to open", resp.Path)
	}
	if want := "/output/alex/" + resp.Path; resp.AgentPath != want {
		t.Errorf("agent_path = %q, want %q", resp.AgentPath, want)
	}
	if strings.Contains(resp.Path, "%") || strings.Contains(resp.Path, "..") {
		t.Errorf("path %q must be a plain readable path", resp.Path)
	}
	if _, err := url.Parse(resp.AgentPath); err != nil {
		t.Errorf("agent_path %q does not parse as a path: %v", resp.AgentPath, err)
	}
}

// A metadata failure must not be able to produce a success. Before this change
// the row was written after the bytes and best-effort, so an INSERT that failed
// left a stored blob outside every reclaim path with a 201 in front of it.
func TestChatAttachment_MetadataFailureNeverReturnsSuccess(t *testing.T) {
	h, store, userID, wsID, agentID, chatID := newChatAttachmentFixture(t)

	// The bluntest possible metadata failure: the table the row goes in is gone.
	if _, err := h.db.Exec(`DROP TABLE attachments`); err != nil {
		t.Fatalf("drop attachments: %v", err)
	}

	code, _, body := uploadChatAttachment(t, h, userID, wsID, agentID, chatID, "evidence.pdf", "orphan-bytes")
	if code == http.StatusCreated {
		t.Fatalf("upload answered 201 with no recorded metadata — a success response must never mean "+
			"\"bytes landed, nobody recorded it\"; body=%s", body)
	}
	if keys := store.keys(); len(keys) != 0 {
		t.Errorf("upload published %d blob(s) %v it could not record — the bytes are now unreachable "+
			"and outside the content-addressed reclaim sweep", len(keys), keys)
	}
}

// The adoption corner: a row whose recorded location is not in this agent's
// namespace any more (its crew or slug changed after the upload).
//
// The row keeps its identity — that is what an identity is for — and the bytes
// are re-published where this agent can actually address them. The invariant
// the delete relies on has to survive that move: the directory is the
// ATTACHMENT'S id, so it must be the existing row's id and not the one
// generated for the insert that lost the de-duplication race.
func TestChatAttachment_AdoptsALocationOutsideTheAgentNamespace(t *testing.T) {
	h, store, userID, wsID, agentID, chatID := newChatAttachmentFixture(t)

	staleKey := "old-crew/old-slug/attachments/" + chatID + "/att-moved/moved.txt"
	if _, err := h.db.Exec(`
		INSERT INTO attachments
			(id, workspace_id, owner_type, chat_id, filename, content_type, size_bytes,
			 sha256, storage_key, state, created_at)
		VALUES ('att-moved', ?, 'chat', ?, 'moved.txt', 'text/plain', 5, ?, ?, 'stored', '2026-08-01T09:00:00Z')`,
		wsID, chatID, attachmentDigest([]byte("moved")), staleKey); err != nil {
		t.Fatalf("seed moved row: %v", err)
	}

	code, resp, body := uploadChatAttachment(t, h, userID, wsID, agentID, chatID, "moved.txt", "moved")
	if code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", code, body)
	}
	want := "attachments/" + chatID + "/att-moved/moved.txt"
	if resp.Path != want {
		t.Errorf("path = %q, want %q — the adopted location must be built from the EXISTING id",
			resp.Path, want)
	}
	var rows int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE chat_id = ?`, chatID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("recorded %d rows, want 1 — an adopted attachment is the same attachment", rows)
	}
	var key, state string
	if err := h.db.QueryRow(
		`SELECT storage_key, state FROM attachments WHERE id = 'att-moved'`).Scan(&key, &state); err != nil {
		t.Fatal(err)
	}
	if key != storageKeyForRelPath(want) {
		t.Errorf("storage_key = %q, want %q — the row must record where the bytes actually went", key, storageKeyForRelPath(want))
	}
	if state != attachmentStateStored {
		t.Errorf("state = %q, want %q", state, attachmentStateStored)
	}
	if got, ok := store.get(storageKeyForRelPath(want)); !ok || got != "moved" {
		t.Errorf("bytes at %q: present=%v content=%q, want the uploaded bytes", want, ok, got)
	}
}
