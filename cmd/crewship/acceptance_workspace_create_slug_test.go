package main

// Acceptance for `crewship workspace create` without --slug (#5116): the help
// promises a slug derived from the name, so a real CLI process against the
// real router has to create the workspace from --name alone, and fail with a
// non-zero exit (never a silent success) when no slug can be derived.

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestAcceptance_WorkspaceCreate_DerivesSlugFromName(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	token := "crewship_cli_wsslug000000000000000000000"
	for _, q := range []string{
		`INSERT INTO users(id,email,full_name) VALUES('slug-cli-owner','slug@example.invalid','Slug Owner')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('slug-cli-token','slug-cli-owner','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default(), api.WithStoragePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\ntoken: "+token+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		// Stdin stays closed: the command must never wait for input.
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	created := func(args ...string) (string, string) {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		var got struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("CLI %v: not JSON: %v\n%s", args, err, out)
		}
		return got.ID, got.Slug
	}

	cases := []struct{ name, want string }{
		{"Proklik 7. 11.", "proklik-7-11"},
		{"Účetnictví a daně", "ucetnictvi-a-dane"},
		{"  QA -- Night__Run  ", "qa-night-run"},
	}
	for _, c := range cases {
		id, slug := created("workspace", "create", "--name", c.name)
		if id == "" || slug != c.want {
			t.Fatalf("create %q: id=%q slug=%q, want slug %q", c.name, id, slug, c.want)
		}
		var stored string
		if err := db.QueryRow(`SELECT slug FROM workspaces WHERE id = ?`, id).Scan(&stored); err != nil || stored != c.want {
			t.Fatalf("stored slug for %q = %q (%v), want %q", c.name, stored, err, c.want)
		}
	}

	// A long name is cut to the server's 50-character limit without a trailing hyphen.
	_, slug := created("workspace", "create", "--name", strings.Repeat("abcd ", 20))
	if len(slug) > 50 || strings.HasSuffix(slug, "-") || !strings.HasPrefix(slug, "abcd-abcd") {
		t.Fatalf("long name slug = %q", slug)
	}

	// An explicit --slug still wins.
	if _, slug := created("workspace", "create", "--name", "Explicit", "--slug", "my-own"); slug != "my-own" {
		t.Fatalf("explicit slug = %q", slug)
	}

	// The same derived slug again is the server's conflict, and a non-zero exit.
	if out, err := run("workspace", "create", "--name", "Proklik 7. 11."); err == nil {
		t.Fatalf("duplicate derived slug exited 0:\n%s", out)
	}

	// Nothing derivable: a non-zero exit that names --slug, and no workspace.
	var before int
	_ = db.QueryRow(`SELECT COUNT(*) FROM workspaces`).Scan(&before)
	out, err := run("workspace", "create", "--name", "日本語")
	if err == nil || !strings.Contains(out, "--slug") {
		t.Fatalf("underivable name: err=%v, want non-zero exit naming --slug:\n%s", err, out)
	}
	var after int
	_ = db.QueryRow(`SELECT COUNT(*) FROM workspaces`).Scan(&after)
	if after != before {
		t.Fatalf("underivable name created a workspace (%d -> %d)", before, after)
	}
}
