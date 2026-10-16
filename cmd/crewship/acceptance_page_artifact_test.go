package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Execute the real CLI process, ordinary human profile and actual Router.
// A committed mutation deliberately loses its HTTP acknowledgement; explicit
// retry preserves its Page identity, CAS and receipt instead of rebasing.
func TestAcceptance_PageArtifactHumanFlow(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "cstaticartifactworkspace0001"
	token := "crewship_cli_staticfixture000000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cstaticartifactworkspace0001','Static','static-cli')`,
		`INSERT INTO users(id,email) VALUES('artifact-cli-owner','static@example.invalid')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('artifact-cli-member','cstaticartifactworkspace0001','artifact-cli-owner','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('artifact-cli-token','artifact-cli-owner','fixture',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithStoragePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	var drop atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/artifact/revisions") && drop.Swap(false) {
			buffered := httptest.NewRecorder()
			router.ServeHTTP(buffered, r)
			if buffered.Code != 201 {
				t.Errorf("lost ACK request did not commit: %d %s", buffered.Code, buffered.Body.String())
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		router.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+server.URL+"\nworkspace: "+ws+"\ntoken: "+token+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		raw, err := cmd.CombinedOutput()
		return string(raw), err
	}
	must := func(args ...string) string {
		raw, err := run(args...)
		if err != nil {
			t.Fatalf("CLI %v: %v %s", args, err, raw)
		}
		return raw
	}
	var page struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(must("page", "artifact", "create", "report", "--name", "Private report")), &page); err != nil || page.ID == "" {
		t.Fatalf("create output: %+v %v", page, err)
	}
	source := filepath.Join(t.TempDir(), "report.html")
	const html = "<h1>Žluťoučký kůň</h1><p>Static revision</p>"
	if err := os.WriteFile(source, []byte(html), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"page", "artifact", "publish", "report", "--page-id", page.ID, "--base-rev", "0", "--mutation-id", "original-cli-attempt", "--file", source}
	drop.Store(true)
	if out, err := run(args...); err == nil {
		t.Fatalf("lost ACK unexpectedly succeeded: %s", out)
	}
	var saved struct {
		PageID   string `json:"page_id"`
		Revision int64  `json:"revision"`
	}
	if err := json.Unmarshal([]byte(must(args...)), &saved); err != nil || saved.PageID != page.ID || saved.Revision != 1 {
		t.Fatalf("exact retry: %+v %v", saved, err)
	}
	var read struct {
		HTML     string `json:"html"`
		Head     int64  `json:"head_rev"`
		CanWrite bool   `json:"can_write"`
	}
	if err := json.Unmarshal([]byte(must("page", "artifact", "show", "report", "--page-id", page.ID, "--format", "json")), &read); err != nil || read.HTML != html || read.Head != 1 || !read.CanWrite {
		t.Fatalf("show JSON: %+v %v", read, err)
	}
	if out, err := run("page", "artifact", "show", "report"); err == nil || !strings.Contains(out, "--page-id") {
		t.Fatalf("missing ID admitted: %v %s", err, out)
	}
	must("page", "artifact", "restore", "report", "--page-id", page.ID, "--base-rev", "1", "--revision", "1", "--mutation-id", "cli-restore")
	var history struct {
		HTML      string `json:"html"`
		HeadRev   int64  `json:"head_rev"`
		Revision  int64  `json:"revision"`
		Revisions []struct {
			Revision     int64  `json:"revision"`
			RestoredFrom *int64 `json:"restored_from"`
		} `json:"revisions"`
	}
	if err := json.Unmarshal([]byte(must("page", "artifact", "show", "report", "--page-id", page.ID)), &history); err != nil {
		t.Fatal(err)
	}
	if history.HTML != html || history.HeadRev != 2 || history.Revision != 2 || len(history.Revisions) != 2 || history.Revisions[0].Revision != 2 || history.Revisions[0].RestoredFrom == nil || *history.Revisions[0].RestoredFrom != 1 || history.Revisions[1].Revision != 1 || history.Revisions[1].RestoredFrom != nil {
		t.Fatalf("restored history: %+v", history)
	}

	// Actual delete/recreate same slug: original command cannot overwrite new Page.
	must("page", "delete", "report", "--yes")
	var replacement struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(must("page", "artifact", "create", "report", "--name", "Replacement")), &replacement); err != nil || replacement.ID == page.ID {
		t.Fatal("Page was not recreated")
	}
	if out, err := run(args...); err == nil || !strings.Contains(out, "identity changed") {
		t.Fatalf("stale CLI overwrote replacement: %v %s", err, out)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=?`, replacement.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("replacement mutated: %d %v", count, err)
	}
}
