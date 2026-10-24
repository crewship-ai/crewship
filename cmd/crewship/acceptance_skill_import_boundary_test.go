//go:build !clionly

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Real CLI process -> real authenticated router -> actual importer. The owned
// fake Git never connects anywhere; it records whether forbidden clone starts.
func TestAcceptance_SkillRepoRejectsLoopbackBeforeGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Git wrapper fixture requires a POSIX shell")
	}
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	db := testutil.MigratedDB(t).DB
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('cli-import-ws','Import','cli-import','2026-01-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name,instance_role) VALUES('cli-import-user','cli-import@example.test','Importer','ADMIN')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('cli-import-member','cli-import-ws','cli-import-user','OWNER')`,
		`INSERT INTO app_settings(key,value,updated_at) VALUES('instance.admin_bootstrapped','already named',datetime('now'))`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	token := "crewship_cli_importboundary0000000000000"
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('cli-import-token','cli-import-user','owned-test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	var imports atomic.Int32
	var importStatus atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/skills/bulk-import") {
			imports.Add(1)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, r)
			importStatus.Store(int32(recorder.Code))
			for key, values := range recorder.Header() {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(recorder.Body.Bytes())
			return
		}
		router.ServeHTTP(w, r)
	}))
	defer srv.Close()
	config := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(config, []byte("server: "+srv.URL+"\nworkspace: cli-import\ntoken: "+token+"\nformat: table\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t) // Build before introducing the owned fake Git.
	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM skills`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "clone-called")
	quoted := "'" + strings.ReplaceAll(marker, "'", "'\\''") + "'"
	script := "#!/bin/sh\ncase \"$1\" in --version|version) printf 'git version 2.45.0\\n'; exit 0;; esac\nprintf 'clone-called\\n' > " + quoted + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "skill", "import", "--repo", "https://localhost./owned.git")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+config, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
	out, runErr := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("CLI timed out: %v\n%s", ctx.Err(), out)
	}
	if runErr == nil {
		t.Fatalf("forbidden import unexpectedly succeeded: %s", out)
	}
	if imports.Load() != 1 {
		t.Fatalf("real CLI made %d import requests, want1; output %s", imports.Load(), out)
	}
	if got := importStatus.Load(); got != http.StatusBadGateway {
		t.Fatalf("actual importer HTTP status %d, want502; output %s", got, out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("forbidden localhost-dot import reached owned Git subprocess (marker error %v); output %s", err, out)
	}
	var skills int
	if err := db.QueryRow(`SELECT COUNT(*) FROM skills`).Scan(&skills); err != nil {
		t.Fatal(err)
	}
	if skills != before {
		t.Fatalf("blocked import changed row count from%d to%d", before, skills)
	}
	t.Log(fmt.Sprintf("Built CLI reached actual authenticated importer exactly once, rejected before clone, no rows persisted; output: %s", out))
}
