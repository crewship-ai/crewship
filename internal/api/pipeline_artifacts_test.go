package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoutineArtifacts_SnapshotAndSharedBlobOwnership(t *testing.T) {
	h, db, user, ws := runsHandlerRig(t)
	crew := seedTestCrew(t, db, ws)
	seedRunsPipeline(t, db, ws, "artifact_pipeline", "files")
	seedRunRow(t, db, ws, "artifact_pipeline", "files", "artifact_run", "completed")
	if _, err := db.Exec(`INSERT INTO pipeline_step_executions(id,run_id,step_id,execution_path,attempt,kind,status,started_at) VALUES ('artifact_execution','artifact_run','write','/write',1,'script','completed','2026-09-08T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	h.storagePath = root
	shared := filepath.Join(root, "crews", crew, "shared")
	if err := os.MkdirAll(shared, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(shared, "report.txt")
	os.WriteFile(file, []byte("first immutable result"), 0600)
	os.WriteFile(filepath.Join(root, "outside.txt"), []byte("private"), 0600)
	os.Symlink(filepath.Join(root, "outside.txt"), filepath.Join(shared, "escape.txt"))
	publisher := NewRoutineArtifactPublisher(db, root)
	ctx := context.Background()
	output := `{"artifacts":[{"kind":"file","path":"/crew/shared/report.txt"},{"kind":"file","path":"/crew/shared/escape.txt"},{"kind":"json","label":"Summary","content":{"count":4}}]}`
	if err := publisher.PublishRunArtifacts(ctx, ws, crew, "artifact_run", "artifact_execution", output, "available"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(file, []byte("replaced file"), 0600)
	if err := publisher.PublishRunArtifacts(ctx, ws, crew, "artifact_run", "artifact_execution", output, "available"); err != nil {
		t.Fatal(err)
	}
	var sha, id string
	if err := db.QueryRow(`SELECT id,sha256 FROM pipeline_run_artifacts WHERE label='report.txt'`).Scan(&id, &sha); err != nil {
		t.Fatal(err)
	}
	data, err := readAttachmentBlob(root, ws, sha)
	if err != nil || string(data) != "first immutable result" {
		t.Fatalf("snapshot mutated: %q %v", data, err)
	}
	if unreferenced, err := attachmentBlobIsUnreferenced(ctx, db, ws, sha); err != nil || unreferenced {
		t.Fatalf("GC would erase owned output: %v %v", unreferenced, err)
	}
	var state string
	db.QueryRow(`SELECT state FROM pipeline_run_artifacts WHERE label='escape.txt'`).Scan(&state)
	if state != "unavailable" {
		t.Fatal("symlink escape was read")
	}
	req := withWorkspaceUser(httptest.NewRequest("GET", "/artifacts?download="+id, nil), user, ws, "OWNER")
	req.SetPathValue("runId", "artifact_run")
	rr := httptest.NewRecorder()
	h.RunArtifacts(rr, req)
	if rr.Code != 200 || rr.Body.String() != "first immutable result" {
		t.Fatalf("download: %d %s", rr.Code, rr.Body.String())
	}
	req = withWorkspaceUser(httptest.NewRequest("GET", "/artifacts?download="+id, nil), user, "foreign", "OWNER")
	req.SetPathValue("runId", "artifact_run")
	rr = httptest.NewRecorder()
	h.RunArtifacts(rr, req)
	if rr.Code != 404 {
		t.Fatal("foreign download not masked")
	}
	if err := publisher.PublishRunArtifacts(ctx, ws, crew, "artifact_run", "artifact_execution", "Read /crew/shared/unrelated.txt", "draft"); err != nil {
		t.Fatal(err)
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM pipeline_run_artifacts`).Scan(&count)
	if count != 3 {
		t.Fatalf("inferred a read as output: %d", count)
	}
}

func TestN12ArtifactPromotionPreservesFailedAttemptsAndSiblingItems(t *testing.T) {
	_, db, _, ws := runsHandlerRig(t)
	crew := seedTestCrew(t, db, ws)
	seedRunsPipeline(t, db, ws, "promotion-pipeline", "promotion")
	seedRunRow(t, db, ws, "promotion-pipeline", "promotion", "promotion-run", "completed")
	for _, row := range []struct {
		id, parent, path, kind, status string
		attempt                        int
	}{
		{"parent", "", "/work", "agent_run", "completed", 2},
		{"old-parent", "", "/work", "agent_run", "failed", 1},
		{"other-parent", "parent", "/work/items/1", "foreach", "completed", 1},
		{"failed", "old-parent", "/work/agent", "agent_attempt", "failed", 1},
		{"accepted", "parent", "/work/agent", "agent_attempt", "completed", 2},
		{"sibling", "other-parent", "/work/items/1/write", "script", "completed", 1},
	} {
		if _, err := db.Exec(`INSERT INTO pipeline_step_executions(id,run_id,parent_execution_id,step_id,execution_path,attempt,kind,status,started_at) VALUES(?,'promotion-run',NULLIF(?,''),'write',?,?,?,?,'2026-09-09T00:00:00Z')`, row.id, row.parent, row.path, row.attempt, row.kind, row.status); err != nil {
			t.Fatal(err)
		}
	}
	publisher := NewRoutineArtifactPublisher(db, t.TempDir())
	ctx := context.Background()
	for _, id := range []string{"failed", "accepted", "sibling", "parent"} {
		state := "draft"
		if id == "parent" {
			state = "available"
		}
		output := `{"artifacts":[{"kind":"text","label":"same","content":"` + id + `"}]}`
		if err := publisher.PublishRunArtifacts(ctx, ws, crew, "promotion-run", id, output, state); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"failed", "sibling"} {
		var content, state string
		if err := db.QueryRow(`SELECT content,state FROM pipeline_run_artifacts WHERE step_execution_id=?`, id).Scan(&content, &state); err != nil {
			t.Fatal(err)
		}
		if content != `"`+id+`"` || state != "draft" {
			t.Fatalf("%s evidence overwritten: %s %s", id, content, state)
		}
	}
	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM pipeline_run_artifacts WHERE step_execution_id='accepted'`).Scan(&state); err != nil || state != "available" {
		t.Fatalf("accepted child was not promoted: %s %v", state, err)
	}
	var duplicates int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pipeline_run_artifacts WHERE step_execution_id='parent'`).Scan(&duplicates); err != nil || duplicates != 0 {
		t.Fatalf("parent inserted duplicate artifact: %d %v", duplicates, err)
	}
}

func TestRoutineArtifacts_PaginationAndLazyContent(t *testing.T) {
	h, db, user, ws := runsHandlerRig(t)
	seedRunsPipeline(t, db, ws, "artifact_pages", "pages")
	seedRunRow(t, db, ws, "artifact_pages", "pages", "paged_run", "completed")
	if _, err := db.Exec(`INSERT INTO pipeline_step_executions(id,run_id,step_id,execution_path,attempt,kind,status,started_at) VALUES ('paged_execution','paged_run','write','/write',1,'script','completed','2026-09-08T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 52; i++ {
		if _, err := db.Exec(`INSERT INTO pipeline_run_artifacts(id,run_id,step_execution_id,kind,label,state,content,created_at) VALUES (?,'paged_run','paged_execution','text',?,'available','immutable text','2026-09-08T00:00:00Z')`, fmt.Sprint("artifact_", i), fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	request := func(scope, query string) *httptest.ResponseRecorder {
		req := withWorkspaceUser(httptest.NewRequest("GET", "/artifacts"+query, nil), user, scope, "OWNER")
		req.SetPathValue("runId", "paged_run")
		rr := httptest.NewRecorder()
		h.RunArtifacts(rr, req)
		return rr
	}
	var page struct {
		Artifacts []map[string]any `json:"artifacts"`
		Cursor    string           `json:"next_cursor"`
	}
	rr := request(ws, "")
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Artifacts) != 50 || page.Cursor == "" {
		t.Fatalf("page: %s", rr.Body.String())
	}
	if _, ok := page.Artifacts[0]["content"]; ok {
		t.Fatal("list downloaded content")
	}
	rr = request(ws, "?after="+page.Cursor)
	json.Unmarshal(rr.Body.Bytes(), &page)
	if len(page.Artifacts) != 2 {
		t.Fatalf("second page: %s", rr.Body.String())
	}
	rr = request(ws, "?artifact_id=artifact_0")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "immutable text") {
		t.Fatal(rr.Body.String())
	}
	if request("foreign", "?artifact_id=artifact_0").Code != 404 {
		t.Fatal("foreign content exposed")
	}
	if request(ws, "?after=-1").Code != 400 {
		t.Fatal("invalid cursor accepted")
	}
}

func TestArtifactDraftParentPreservesChildSnapshot(t *testing.T) {
	_, db, _, ws := runsHandlerRig(t)
	crew := seedTestCrew(t, db, ws)
	seedRunsPipeline(t, db, ws, "draft-artifacts", "draft-artifacts")
	seedRunRow(t, db, ws, "draft-artifacts", "draft-artifacts", "draft-artifact-run", "failed")
	if _, err := db.Exec(`INSERT INTO pipeline_step_executions(id,run_id,parent_execution_id,step_id,execution_path,attempt,kind,status,started_at) VALUES
 ('draft-parent','draft-artifact-run',NULL,'work','/work',1,'agent_run','failed','2026-09-09T00:00:00Z'),
 ('draft-child','draft-artifact-run','draft-parent','agent','/work/agent',1,'agent_attempt','completed','2026-09-09T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	shared := filepath.Join(root, "crews", crew, "shared")
	if err := os.MkdirAll(shared, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(shared, "report.txt")
	if err := os.WriteFile(file, []byte("child snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	publisher := NewRoutineArtifactPublisher(db, root)
	output := `{"artifacts":[{"kind":"file","path":"/crew/shared/report.txt"}]}`
	if err := publisher.PublishRunArtifacts(t.Context(), ws, crew, "draft-artifact-run", "draft-child", output, "draft"); err != nil {
		t.Fatal(err)
	}
	// A required outcome check can fail after a completed agent invocation.
	// A later shared-file write must not replace that invocation's evidence.
	if err := os.WriteFile(file, []byte("later shared file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := publisher.PublishRunArtifacts(t.Context(), ws, crew, "draft-artifact-run", "draft-parent", output, "draft"); err != nil {
		t.Fatal(err)
	}
	var sha, state string
	if err := db.QueryRow(`SELECT sha256,state FROM pipeline_run_artifacts WHERE step_execution_id='draft-child'`).Scan(&sha, &state); err != nil {
		t.Fatal(err)
	}
	data, err := readAttachmentBlob(root, ws, sha)
	if err != nil || string(data) != "child snapshot" || state != "draft" {
		t.Fatalf("child evidence overwritten: %q state=%s err=%v", data, state, err)
	}
	if err := db.QueryRow(`SELECT sha256,state FROM pipeline_run_artifacts WHERE step_execution_id='draft-parent'`).Scan(&sha, &state); err != nil {
		t.Fatal(err)
	}
	data, err = readAttachmentBlob(root, ws, sha)
	if err != nil || string(data) != "later shared file" || state != "draft" {
		t.Fatalf("parent draft was lost: %q state=%s err=%v", data, state, err)
	}
}
