package main

// Acceptance for `crewship workspace logo set|remove` (#3005): a real CLI
// process against the real router on a migrated SQLite, so the file has to
// pass the server's image checks and the logo has to come back through
// `workspace get` and the serve route.

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestAcceptance_WorkspaceLogo_SetAndRemove(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "clogoacceptancews000"
	token := "crewship_cli_wslogo000000000000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('clogoacceptancews000','Logo','logo-cli')`,
		`INSERT INTO users(id,email,full_name) VALUES('logo-cli-owner','logo@example.invalid','Logo Owner')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('logo-cli-member','clogoacceptancews000','logo-cli-owner','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('logo-cli-token','logo-cli-owner','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default(), api.WithStoragePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+ws+"\ntoken: "+token+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) string {
		out, err := run(args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}
	logoURL := func() *string {
		var got struct {
			LogoURL *string `json:"logo_url"`
		}
		out := must("workspace", "get")
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("workspace get: %v\n%s", err, out)
		}
		return got.LogoURL
	}

	// A real 16×16 PNG.
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(t.TempDir(), "logo.png")
	if err := os.WriteFile(img, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	must("workspace", "logo", "set", img)
	u := logoURL()
	if u == nil || !strings.HasPrefix(*u, "/api/v1/workspaces/"+ws+"/logo") {
		t.Fatalf("logo_url after set = %v", u)
	}
	req, _ := http.NewRequest("GET", srv.URL+*u, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("serve = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// Not an image: refused, and the logo stays.
	txt := filepath.Join(t.TempDir(), "logo.txt")
	if err := os.WriteFile(txt, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := run("workspace", "logo", "set", txt); err == nil {
		t.Fatalf("a text file was accepted:\n%s", out)
	}
	if logoURL() == nil {
		t.Fatal("a refused upload cleared the logo")
	}

	must("workspace", "logo", "remove")
	if u := logoURL(); u != nil {
		t.Fatalf("logo_url after remove = %q", *u)
	}
}
