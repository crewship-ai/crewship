package api

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestBoardRequestPreviewDetachedBackupForkGC(t *testing.T) {
	h, ws, user, board, _, _ := boardJobFixture(t)
	body := boardRequestWithPreview(t, strings.Replace(boardDiagramRequest, "diagram-request", "preview-storage", 1), boardPreviewPNG(t, 24, 20))
	w := boardJobCall(t, h, ws, user, "", body, h.CreateBoardRequest)
	artifactExpect(t, w, 202)
	var request struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &request); err != nil {
		t.Fatal(err)
	}
	var sha string
	if err := h.db.QueryRow(`SELECT sha256 FROM page_board_request_previews WHERE request_id=?`, request.ID).Scan(&sha); err != nil {
		t.Fatal(err)
	}
	original, err := readAttachmentBlob(h.artifactStoragePath, ws, sha)
	if err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `DELETE FROM pages WHERE id=?`, board)
	if n, err := reclaimAttachmentBlobs(t.Context(), h.db, h.artifactStoragePath, ws); err != nil || n != 0 {
		t.Fatalf("detached helper collected: %d %v", n, err)
	}
	const pass = "board-preview-storage-roundtrip-password"
	created, err := backup.CreateBackup(t.Context(), h.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: pass, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: h.artifactStoragePath})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []string{"", "preview-fork"} {
		t.Run(fork, func(t *testing.T) {
			target := setupTestDB(t)
			root := storageDir(t)
			restored, err := backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Path: created.Path, Passphrase: pass, AsWorkspace: fork, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: root})
			if err != nil {
				t.Fatal(err)
			}
			var id, restoredSHA, status, receipt string
			var boardID *string
			if err = target.QueryRow(`SELECT p.request_id,p.sha256,r.board_page_id,r.status,r.receipt_json FROM page_board_request_previews p JOIN page_board_requests r ON r.id=p.request_id WHERE r.workspace_id=?`, restored.RestoredWorkspaceID).Scan(&id, &restoredSHA, &boardID, &status, &receipt); err != nil {
				t.Fatal(err)
			}
			if restoredSHA != sha || boardID != nil || status != "failed" || receipt != "" {
				t.Fatalf("detached history/authority changed %s %v %s %q", restoredSHA, boardID, status, receipt)
			}
			if fork != "" && id == request.ID {
				t.Fatal("fork failed to remap helper's job identity")
			}
			data, err := readAttachmentBlob(root, restored.RestoredWorkspaceID, restoredSHA)
			if err != nil || !bytes.Equal(data, original) {
				t.Fatalf("helper bytes lost on restore: %v", err)
			}
			if n, err := reclaimAttachmentBlobs(t.Context(), target, root, restored.RestoredWorkspaceID); err != nil || n != 0 {
				t.Fatalf("restored retained helper collected %d %v", n, err)
			}
			execOrFatal(t, target, `DELETE FROM page_board_requests WHERE id=?`, id)
			if n, err := reclaimAttachmentBlobs(t.Context(), target, root, restored.RestoredWorkspaceID); err != nil || n != 1 {
				t.Fatalf("deleted job helper not collected %d %v", n, err)
			}
		})
	}
	execOrFatal(t, h.db, `DELETE FROM page_board_requests WHERE id=?`, request.ID)
	if n, err := reclaimAttachmentBlobs(t.Context(), h.db, h.artifactStoragePath, ws); err != nil || n != 1 {
		t.Fatalf("source deleted helper not collected %d %v", n, err)
	}
}
