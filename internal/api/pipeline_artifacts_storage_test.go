package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRoutineArtifacts_SharedAttachmentSurvivesDeleteAndGC(t *testing.T) {
	f := newAttachmentFixture(t)
	db := f.h.db
	seedRunsPipeline(t, db, f.wsID, "storage-pipeline", "storage")
	seedRunRow(t, db, f.wsID, "storage-pipeline", "storage", "storage-run", "completed")
	if _, err := db.Exec(`INSERT INTO pipeline_step_executions(id,run_id,step_id,execution_path,attempt,kind,status,started_at) VALUES ('storage-step','storage-run','write','/write',1,'script','completed','2026-10-13T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	content := []byte("shared attachment and routine output")
	att, code := upload(t, f, "result.txt", content)
	if code != http.StatusCreated {
		t.Fatalf("upload: %d", code)
	}
	shared := filepath.Join(f.storage, "crews", f.crewID, "shared")
	if err := os.MkdirAll(shared, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "result.txt"), content, 0600); err != nil {
		t.Fatal(err)
	}
	publisher := NewRoutineArtifactPublisher(db, f.storage)
	// A publisher must join the same critical section as deletion and GC.
	unlock := lockAttachmentBlob(f.wsID, att.SHA256)
	done := make(chan error, 1)
	go func() {
		done <- publisher.PublishRunArtifacts(context.Background(), f.wsID, f.crewID, "storage-run", "storage-step", `{"artifacts":[{"kind":"file","path":"/crew/shared/result.txt"}]}`, "available")
	}()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("publisher bypassed blob lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	sweep := make(chan error, 1)
	go func() { _, err := reclaimAttachmentBlobs(context.Background(), db, f.storage, f.wsID); sweep <- err }()
	select {
	case err := <-sweep:
		unlock()
		t.Fatalf("GC bypassed publisher lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-sweep; err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	f.h.Delete(rr, scopedReq(t, f, "DELETE", "ENG-1", att.ID, f.wsID))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete attachment: %d %s", rr.Code, rr.Body.String())
	}
	if n, err := reclaimAttachmentBlobs(context.Background(), db, f.storage, f.wsID); err != nil || n != 0 {
		t.Fatalf("routine-owned blob reclaimed: %d %v", n, err)
	}
	got, err := readAttachmentBlob(f.storage, f.wsID, att.SHA256)
	if err != nil || string(got) != string(content) {
		t.Fatalf("routine bytes lost: %q %v", got, err)
	}
	// Once the final real owner disappears the shared bytes become garbage.
	if _, err := db.Exec(`DELETE FROM pipeline_runs WHERE id='storage-run'`); err != nil {
		t.Fatal(err)
	}
	if n, err := reclaimAttachmentBlobs(context.Background(), db, f.storage, f.wsID); err != nil || n != 1 {
		t.Fatalf("last owner removal: reclaimed=%d err=%v", n, err)
	}
}
