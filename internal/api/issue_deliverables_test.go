package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIssueDeliverables_PublishDeduplicatesAndConfinesReads(t *testing.T) {
	h, _, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-80", "TODO")
	if _, err := h.db.Exec(`INSERT INTO chats(id,workspace_id,agent_id,title) VALUES('deliverable-chat',?,?,'Artifacts')`, ws, lead); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO assignments(id,workspace_id,mission_id,chat_id,assigned_by_id,assigned_to_id,task,status) VALUES('deliverable-assignment',?,?,'deliverable-chat',?,?,'Report','COMPLETED')`, ws, id, lead, lead); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	shared := filepath.Join(root, "crews", crew, "shared")
	if err := os.MkdirAll(shared, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "report.md"), []byte("Verified result"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "private.md")
	os.WriteFile(outside, []byte("Private"), 0600)
	if err := os.Symlink(outside, filepath.Join(shared, "escape.md")); err != nil {
		t.Fatal(err)
	}
	att := NewAttachmentHandler(h.db, nil, h.logger)
	att.SetStoragePath(root)
	ah := &AssignmentHandler{db: h.db, logger: h.logger, attachments: att}
	result := "---HANDOFF---\nsummary: verified\nconfidence: high\nartifacts: /crew/shared/report.md, /crew/shared/escape.md, /etc/passwd\noutcome: SUCCEEDED\n---END HANDOFF---"
	for range 2 {
		ah.publishIssueDeliverables(context.Background(), "deliverable-assignment", result)
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE mission_id=?`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("attachments=%d err=%v", count, err)
	}
}
