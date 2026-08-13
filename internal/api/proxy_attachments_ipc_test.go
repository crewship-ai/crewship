package api

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// AgentChatAttachment forwards the IPC save endpoint's answer to the composer.
// Two things have to hold for that to be worth anything:
//
//   - a FAILED save must not leave a metadata row behind. A row claiming an
//     attachment exists is worse than no row: the file listing, the SHA and the
//     size would all describe bytes that were never written.
//   - the error the user reads must say what went wrong and what to do. The
//     field report for this bug was a toast reading {"error":"failed to save
//     file"} — no mention of the attachment, the crew, or a remedy.
//
// The IPC layer answers in JSON ({"error":"..."}), so forwarding its body as
// the VALUE of our own error field produced a double-encoded blob. These tests
// pin the decode.
func seedChatForAttachment(t *testing.T, h *ProxyHandler, wsID, userID string) (agentID, chatID string) {
	t.Helper()
	const crewID, slug = "crew-ipc", "alex"
	agentID, chatID = "agent-ipc", "chat-ipc"
	if _, err := h.db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Crew', 'crew-ipc-slug')`,
		crewID, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, status) VALUES (?, ?, ?, 'A', ?, 'IDLE')`,
		agentID, wsID, crewID, slug); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO chats (id, agent_id, workspace_id, created_by, status) VALUES (?, ?, ?, ?, 'ACTIVE')`,
		chatID, agentID, wsID, userID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	return agentID, chatID
}

func attachmentUploadRequest(t *testing.T, agentID, chatID, filename, content string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/chats/"+chatID+"/attachments", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.SetPathValue("agentId", agentID)
	req.SetPathValue("chatId", chatID)
	return req
}

func TestAgentChatAttachment_ForwardsIPCErrorLegibly(t *testing.T) {
	cases := []struct {
		name       string
		ipcStatus  int
		ipcBody    string
		wantStatus int
		wantParts  []string
		wantRows   int
	}{
		{
			// The crew is stopped: the bytes are owned by the crew runtime and
			// only it can write them. Actionable, and it says "attachment" —
			// the IPC layer cannot know that is what this was.
			name:       "stopped crew 409 is forwarded with its remedy intact",
			ipcStatus:  http.StatusConflict,
			ipcBody:    `{"error":"the agent's output directory is owned by the crew runtime; files can only be written there while the crew container is running — start the crew and retry"}`,
			wantStatus: http.StatusConflict,
			wantParts: []string{
				"attachment",
				"owned by the crew runtime",
				"start the crew and retry",
			},
		},
		{
			name:       "opaque 500 still names the attachment",
			ipcStatus:  http.StatusInternalServerError,
			ipcBody:    `{"error":"failed to save file"}`,
			wantStatus: http.StatusInternalServerError,
			wantParts:  []string{"attachment", "failed to save file"},
		},
		{
			// http.Error responses from the IPC route are plain text, not
			// JSON. Forward the text rather than dropping it.
			name:       "non-JSON IPC error body is forwarded as text",
			ipcStatus:  http.StatusBadRequest,
			ipcBody:    "invalid path\n",
			wantStatus: http.StatusBadRequest,
			wantParts:  []string{"attachment", "invalid path"},
		},
		{
			name:       "empty error body still produces a sentence",
			ipcStatus:  http.StatusBadGateway,
			ipcBody:    "",
			wantStatus: http.StatusBadGateway,
			wantParts:  []string{"attachment"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.ipcStatus)
				_, _ = w.Write([]byte(tc.ipcBody))
			}))
			h := newProxyHandlerForTest(t, sock)
			userID := seedTestUser(t, h.db)
			wsID := seedTestWorkspace(t, h.db, userID)
			agentID, chatID := seedChatForAttachment(t, h, wsID, userID)

			req := attachmentUploadRequest(t, agentID, chatID, "Sešit1.xlsx", "PK\x03\x04")
			req = withWorkspaceUser(req, userID, wsID, "MANAGER")
			rr := httptest.NewRecorder()
			h.AgentChatAttachment(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			for _, part := range tc.wantParts {
				if !strings.Contains(rr.Body.String(), part) {
					t.Errorf("body %s should contain %q", rr.Body.String(), part)
				}
			}
			// Not a JSON document nested inside a JSON string field.
			if strings.Contains(rr.Body.String(), `\"error\"`) {
				t.Errorf("IPC body was forwarded double-encoded: %s", rr.Body.String())
			}
			// A save that failed must leave no trace in the metadata table.
			var rows int
			if err := h.db.QueryRow(
				`SELECT COUNT(*) FROM attachments WHERE chat_id = ?`, chatID).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if rows != 0 {
				t.Errorf("failed save recorded %d attachment row(s); a row must never outlive a write that did not land", rows)
			}
		})
	}
}

// The other half of the same rule: when the save DOES land, the row is there.
// Without this the test above would pass just as well against a handler that
// never records anything.
func TestAgentChatAttachment_RecordsRowOnlyAfterBytesLand(t *testing.T) {
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
	}))
	h := newProxyHandlerForTest(t, sock)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	agentID, chatID := seedChatForAttachment(t, h, wsID, userID)

	req := attachmentUploadRequest(t, agentID, chatID, "notes.txt", "hello")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.AgentChatAttachment(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var key string
	if err := h.db.QueryRow(
		`SELECT storage_key FROM attachments WHERE chat_id = ? AND filename = ?`,
		chatID, "notes.txt").Scan(&key); err != nil {
		t.Fatalf("no metadata row for a save that succeeded: %v", err)
	}
	if want := "crew-ipc/alex/attachments/" + chatID + "/notes.txt"; key != want {
		t.Errorf("storage_key = %q, want %q", key, want)
	}
}
