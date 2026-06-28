package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/provider"
)

// gitDiffMock reads the per-call CRW_NONCE the handler injects and emits a
// realistic nonce-prefixed diff, so the test exercises the full path:
// nonce → Exec env → marker emission → parseGitDiff → JSON response.
type gitDiffMock struct{ *mockContainer }

func (g *gitDiffMock) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	nonce := ""
	for _, e := range cfg.Env {
		if strings.HasPrefix(e, "CRW_NONCE=") {
			nonce = strings.TrimPrefix(e, "CRW_NONCE=")
		}
	}
	out := nonce + "STATUS\n" +
		"M\tmain.go\n" +
		"A\tREADME.md\n" +
		nonce + "NUMSTAT\n" +
		"3\t1\tmain.go\n" +
		"10\t0\tREADME.md\n" +
		nonce + "DIFF\n" +
		"diff --git a/main.go b/main.go\n@@ -1 +1 @@\n-old\n+new // __DIFF__ bare word in body\n"
	return &provider.ExecResult{ExecID: "x", Reader: io.NopCloser(strings.NewReader(out))}, nil
}

// TestHandleContainerGitDiff_FullPath drives the real handler end-to-end
// with a mock that honours the nonce, asserting the parsed file list + diff.
func TestHandleContainerGitDiff_FullPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "gd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db.DB, newSilentLogger()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES ('w1','W','w',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('c1','w1','C','crew-x',?,?)`, now, now)

	cfg := config.Default()
	cfg.Auth.JWTSecret = "test-secret-for-server-tests-32-chars"
	logger := logging.New("error", "json", nil)
	s := New(cfg, logger, &Deps{Container: &gitDiffMock{&mockContainer{}}, DB: db.DB})
	s.startedAt = time.Now()
	t.Cleanup(func() {
		s.StopBackground()
		if s.fileWatcher != nil {
			s.fileWatcher.Close()
		}
	})

	req := httptest.NewRequest("GET", "/crews/c1/git-diff", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if body["is_repo"] != true {
		t.Fatalf("is_repo = %v, want true; body=%s", body["is_repo"], rec.Body.String())
	}
	files, _ := body["files"].([]interface{})
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2 (the bare '__DIFF__' in the diff body must not leak); body=%s", len(files), rec.Body.String())
	}
	if diff, _ := body["diff"].(string); !strings.Contains(diff, "+new") {
		t.Fatalf("diff body missing: %q", body["diff"])
	}
}
