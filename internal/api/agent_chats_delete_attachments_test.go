package api

// #1148: deleting a chat must also unlink its on-disk attachment blobs
// under <storage-root>/<crewID>/<agentSlug>/attachments/<chatId>/, which
// AgentChatAttachment writes via the IPC files/save path. Cleanup is
// best-effort (a storage error must not fail the delete) and scoped to
// the one chat — a sibling chat's attachments must survive.

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func seedCrewForAttach(t *testing.T, h *AgentHandler, wsID, crewID string) {
	t.Helper()
	if _, err := h.db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, network_mode, created_at, updated_at)
		 VALUES (?, ?, 'Att', ?, 'restricted', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		crewID, wsID, "att-crew-"+crewID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
}

func seedAgentChatWithCrew(t *testing.T, h *AgentHandler, wsID, crewID, agentID, slug, chatID, createdBy string) {
	t.Helper()
	if _, err := h.db.Exec(
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, status) VALUES (?, ?, ?, 'A', ?, 'IDLE')`,
		agentID, wsID, crewID, slug); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO chats (id, agent_id, workspace_id, created_by, status) VALUES (?, ?, ?, ?, 'ACTIVE')`,
		chatID, agentID, wsID, createdBy); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
}

func TestDeleteChat_RemovesAttachmentBlobs(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	root := t.TempDir()
	h.storagePath = root

	crewID, slug, agentID, chatID := "crew-att", "agent-att-slug", "agent-att", "chat-att-1"
	seedCrewForAttach(t, h, wsID, crewID)
	seedAgentChatWithCrew(t, h, wsID, crewID, agentID, slug, chatID, userID)

	// Write a fake attachment blob at the exact path AgentChatAttachment
	// resolves to (<root>/<crewID>/<slug>/attachments/<chatId>/<file>).
	attDir := filepath.Join(root, crewID, slug, "attachments", chatID)
	if err := os.MkdirAll(attDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attDir, "photo.png"), []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A sibling chat's attachments must NOT be touched.
	otherDir := filepath.Join(root, crewID, slug, "attachments", "chat-other")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	rr := deleteChatReq(t, h, userID, wsID, "MEMBER", agentID, chatID)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(attDir); !os.IsNotExist(err) {
		t.Errorf("attachments dir for deleted chat must be gone, stat err=%v", err)
	}
	if _, err := os.Stat(otherDir); err != nil {
		t.Errorf("sibling chat attachments must survive, stat err=%v", err)
	}
}

// Nil-safe: with no storagePath wired, the delete still succeeds (the
// blob cleanup silently no-ops) — legacy routers/tests must not panic.
func TestDeleteChat_NoStoragePath_StillDeletes(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	// storagePath deliberately unset.
	seedChatForDelete(t, h, wsID, "agent-nostor", "chat-nostor", userID)
	rr := deleteChatReq(t, h, userID, wsID, "MEMBER", "agent-nostor", "chat-nostor")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete without storagePath: status=%d body=%s", rr.Code, rr.Body.String())
	}
}
