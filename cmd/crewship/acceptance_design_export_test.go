package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// The Design HTML export contract (#4449) as people and scripts use it: the
// real binary against a real router, an owner and a shared-version reader.
func TestAcceptance_DesignHTMLExportBinary(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Design engine needs node on PATH")
	}
	t.Setenv("CREWSHIP_ARTIFACTS_DESIGN_ENABLED", "true")
	copy := testutil.MigratedDB(t)
	path := copy.Path()
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	db := opened.DB
	const workspace = "cdesignexportcliworkspacex"
	tokens := map[string]string{"design-export-owner": "crewship_cli_design_export_owner_fixture", "design-export-reader": "crewship_cli_design_export_reader_fixture"}
	for _, query := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cdesignexportcliworkspacex','Design Export','design-export-cli')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for user, token := range tokens {
		role := "MEMBER"
		if user == "design-export-owner" {
			role = "OWNER"
		}
		for _, step := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, []any{user, user + "@example.test", user}},
			{`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,?)`, []any{user + "-member", workspace, user, role}},
			{`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES(?,?,'fixture',?,'STANDARD','["pages:*"]')`, []any{user + "-token", user, sha256HexToken(token)}},
		} {
			if _, err := db.Exec(step.query, step.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	audit := journal.NewWriter(db, logger, journal.WriterOptions{ChainKey: bytes.Repeat([]byte{7}, 32)})
	t.Cleanup(func() { _ = audit.Close() })
	router, err := api.NewRouter(db, "design-export-cli-synthetic-secret", logger, api.WithJournal(audit))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	configs := map[string]string{}
	for user, token := range tokens {
		configs[user] = filepath.Join(t.TempDir(), user+".yaml")
		if err := os.WriteFile(configs[user], []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: %s\nformat: json\n", server.URL, token, workspace)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	binary := buildCrewshipBinary(t)
	work := t.TempDir()
	run := func(user, input string, args ...string) ([]byte, int) {
		t.Helper()
		command := exec.CommandContext(t.Context(), binary, append([]string{"page"}, args...)...)
		command.Dir = work
		command.Stdin = strings.NewReader(input)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "CREWSHIP_") {
				command.Env = append(command.Env, value)
			}
		}
		command.Env = append(command.Env, "CREWSHIP_CONFIG="+configs[user], "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		code := 0
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != 0 {
			return append(stdout.Bytes(), stderr.Bytes()...), code
		}
		return stdout.Bytes(), code
	}
	const owner, reader = "design-export-owner", "design-export-reader"
	must := func(user, input string, args ...string) []byte {
		t.Helper()
		out, code := run(user, input, args...)
		if code != 0 {
			t.Fatalf("%v exit %d: %s", args, code, out)
		}
		return out
	}
	must(owner, "", "design", "create", "checkout", "--name", "Checkout flow")
	must(owner, `{"operations":[
		{"op":"upsert","node":{"id":"cart","type":"frame","name":"Cart","x":0,"y":0,"width":360,"height":640,"fill":"#ffffff"}},
		{"op":"upsert","node":{"id":"pay","type":"frame","name":"Pay","x":400,"y":0,"width":360,"height":640,"fill":"#ffffff"}},
		{"op":"upsert","node":{"id":"go","type":"rect","parentId":"cart","name":"Checkout","x":24,"y":24,"width":120,"height":48,"fill":"#2563eb","linkTo":"pay"}},
		{"op":"upsert","node":{"id":"title","type":"text","parentId":"cart","x":24,"y":100,"width":300,"height":30,"text":"SHARED <b>bold</b>"}}
	]}`, "design", "apply", "checkout", "--file", "-")
	must(owner, "", "design", "share", "checkout", "--version", "2")
	must(owner, `{"operations":[{"op":"upsert","node":{"id":"title","text":"PRIVATE_SAVED"}}]}`, "design", "apply", "checkout", "--file", "-")
	must(owner, "", "grant", "checkout", "--user", reader+"@example.test", "--level", "read")

	type receipt struct {
		Path       string `json:"path"`
		MediaType  string `json:"media_type"`
		Version    int64  `json:"version"`
		ByteLength int    `json:"byte_length"`
		SHA256     string `json:"sha256"`
	}
	exported := func(user string, args ...string) (receipt, []byte) {
		t.Helper()
		var r receipt
		out := must(user, "", append([]string{"design", "export", "checkout"}, args...)...)
		if err := json.Unmarshal(out, &r); err != nil {
			t.Fatalf("receipt %s: %v", out, err)
		}
		file := r.Path
		if !filepath.IsAbs(file) {
			file = filepath.Join(work, file)
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if r.MediaType != "text/html" || r.ByteLength != len(raw) || r.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("receipt does not describe the file: %+v", r)
		}
		return r, raw
	}
	// Owner default: latest retained version, server file name in the working directory.
	latest, html := exported(owner)
	if latest.Version != 3 || latest.Path != "checkout-design-v3.html" || !bytes.Contains(html, []byte("PRIVATE_SAVED")) {
		t.Fatalf("owner default export %+v", latest)
	}
	// Explicit version and output path, deterministic across runs.
	target := filepath.Join(work, "shared.html")
	first, html := exported(owner, "--as", "html", "--version", "2", "-o", target)
	second, _ := exported(owner, "--version", "2", "--out", target)
	if first.Version != 2 || first.SHA256 != second.SHA256 || bytes.Contains(html, []byte("PRIVATE")) {
		t.Fatalf("v2 export %+v %+v", first, second)
	}
	for _, want := range []string{`href="#screen-2"`, `id="screen-2"`, "SHARED &lt;b&gt;bold&lt;/b&gt;", "default-src &#39;none&#39;", "Design version 2"} {
		if !bytes.Contains(html, []byte(want)) {
			t.Errorf("exported HTML lacks %q", want)
		}
	}
	if bytes.Contains(html, []byte("<script")) {
		t.Error("exported HTML contains script")
	}
	// --out - streams the identical document.
	if stdout := must(owner, "", "design", "export", "checkout", "--version", "2", "--out", "-"); !bytes.Equal(stdout, html) {
		t.Error("stdout export differs from the file export")
	}
	// A Read audience gets the shared version and nothing else.
	shared, readerHTML := exported(reader, "-o", filepath.Join(work, "reader.html"))
	if shared.Version != 2 || !bytes.Equal(readerHTML, html) {
		t.Fatalf("reader export %+v", shared)
	}
	for _, tc := range []struct {
		user string
		args []string
		code int
	}{
		{reader, []string{"--version", "3"}, 3},
		{reader, []string{"--version", "1"}, 3},
		{owner, []string{"--version", "99"}, 3},
		{owner, []string{"--as", "pdf"}, 2},
		{owner, []string{"--version", "-1"}, 2},
	} {
		out, code := run(tc.user, "", append([]string{"design", "export", "checkout"}, tc.args...)...)
		if code != tc.code {
			t.Errorf("%s %v: exit %d, want %d: %s", tc.user, tc.args, code, tc.code, out)
		}
		if bytes.Contains(out, []byte("PRIVATE")) {
			t.Errorf("%s %v leaked content", tc.user, tc.args)
		}
	}
	if _, err := os.Stat(filepath.Join(work, "checkout-design-v1.html")); !os.IsNotExist(err) {
		t.Error("refused export wrote a file")
	}
}
