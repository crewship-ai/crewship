//go:build linux && ((docs_foundation && docs_export) || conformance)

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Downloads a schema 4 document through the CLI binary, the Router, the real Go
// authority and the real tools/docs-service engine, then opens the file and
// checks what a reader of the Markdown sees. Opt-in at compile time (it needs
// Node 24 and the frozen docs-service install), so it never reports a skip:
//
//	pnpm --dir tools/docs-service install --frozen-lockfile
//	go test -tags docs_foundation,docs_export ./cmd/crewship -run '^TestAcceptance_DocumentExportRealEngine$' -count=1
func TestAcceptance_DocumentExportRealEngine(t *testing.T) {
	nodeVersion, err := exec.Command("node", "--version").Output()
	if err != nil || !strings.HasPrefix(string(nodeVersion), "v24.") {
		t.Fatalf("Node24 required: %s %v", nodeVersion, err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	db := testutil.MigratedDB(t).DB
	const workspace = "cdocexportrealworkspace001"
	const token = "crewship_cli_doc_export_real_owner_fixture"
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('doc-xr-owner','doc-xr-owner@example.invalid')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('cdocexportrealworkspace001','Doc export real','doc-export-real')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('doc-xr-m1','cdocexportrealworkspace001','doc-xr-owner','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('doc-xr-token','doc-xr-owner','staging',?,'STANDARD')`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "doc-xr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	authoritySocket, engineSocket := filepath.Join(dir, "a.sock"), filepath.Join(dir, "e.sock")
	secret := strings.Repeat("doc-export-real-secret-", 2)
	secretFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(secretFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	audit := journal.NewWriter(db, slog.Default(), journal.WriterOptions{ChainKey: bytes.Repeat([]byte{5}, 32)})
	t.Cleanup(func() { _ = audit.Close() })
	router, err := api.NewRouter(db, strings.Repeat("doc-export-real-jwt-", 2), slog.Default(), api.WithJournal(audit), api.WithDocumentAuthority(authoritySocket, secretFile), api.WithDocumentService(engineSocket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	logPath := filepath.Join(dir, "engine.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	node := exec.Command("node", filepath.Join(root, "tools/docs-service/service.mjs"))
	node.Env = append(os.Environ(), "CREWSHIP_DOCS_SOCKET="+engineSocket, "CREWSHIP_DOCS_AUTHORITY_SOCKET="+authoritySocket, "CREWSHIP_DOCS_SERVICE_TOKEN_FILE="+secretFile)
	node.Stdout, node.Stderr = logFile, logFile
	if err := node.Start(); err != nil {
		t.Fatal(err)
	}
	engine := &durabilityEngine{name: "export", socket: engineSocket, cmd: node, log: logPath, exited: make(chan struct{})}
	go func() { _ = node.Wait(); close(engine.exited) }()
	t.Cleanup(engine.kill)
	if err := engine.waitReady(secret, 20*time.Second); err != nil {
		logged, _ := os.ReadFile(logPath)
		t.Fatalf("engine not ready: %v\n%s", err, logged)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	binary := buildCrewshipBinary(t)
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: %s\nformat: json\n", server.URL, token, workspace)), 0600); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	require := func(input string, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Dir = work
		cmd.Stdin = strings.NewReader(input)
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "CREWSHIP_") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("CLI %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	created := require(strings.Replace(emptyDocPage, "slug: human-document", "slug: export-real", 1), "page", "create", "--allow-empty", "--file", "-")
	var page struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(created, &page) != nil || page.ID == "" {
		t.Fatalf("Page create: %s", created)
	}
	text := func(value string, marks ...string) map[string]any {
		node := map[string]any{"type": "text", "text": value}
		if len(marks) > 0 {
			list := []map[string]any{}
			for _, mark := range marks {
				list = append(list, map[string]any{"type": mark})
			}
			node["marks"] = list
		}
		return node
	}
	block := func(kind, id string, attrs map[string]any, content ...any) map[string]any {
		if attrs == nil {
			attrs = map[string]any{}
		}
		attrs["id"] = id
		return map[string]any{"type": kind, "attrs": attrs, "content": content}
	}
	cell := func(kind, id, value string) map[string]any {
		return block(kind, id, nil, block("paragraph", id+"-p", nil, text(value)))
	}
	body := map[string]any{"type": "doc", "content": []any{
		block("heading", "title", map[string]any{"level": 1}, text("Launch notes")),
		block("paragraph", "intro", nil, text("Ship "), text("only", "bold"), text(" partners <b>first</b>.")),
		block("table", "t", nil,
			block("tableRow", "r1", nil, cell("tableHeader", "h1", "Owner"), cell("tableHeader", "h2", "Share")),
			block("tableRow", "r2", nil, cell("tableCell", "c1", "Jana"), cell("tableCell", "c2", "40 %"))),
		block("codeBlock", "py", map[string]any{"language": "python"}, text("def f():\n\treturn 1  \n")),
		block("codeBlock", "tex", map[string]any{"language": "latex"}, text(`e^{i\pi} + 1 = 0`)),
		block("taskList", "tasks", nil, block("taskItem", "done", map[string]any{"checked": true}, block("paragraph", "done-p", nil, text("Legal review")))),
	}}
	request, _ := json.Marshal(map[string]any{"id": "export-real-init", "schema": 4, "page_id": page.ID, "body": body})
	require(string(request), "doc", "initialize", "export-real", "--file", "-")
	require("", "doc", "export", "export-real")
	saved, err := os.ReadFile(filepath.Join(work, "export-real.md"))
	if err != nil {
		t.Fatal(err)
	}
	markdown := string(saved)
	if evidence := os.Getenv("TEST_DOCUMENT_EXPORT_EVIDENCE_DIR"); evidence != "" {
		if err := os.WriteFile(filepath.Join(evidence, "export-real.md"), saved, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{
		"# Launch notes\n",
		"Ship **only** partners \\<b\\>first\\</b\\>.",
		"| Owner | Share |",
		"| Jana | 40 % |",
		"```python\ndef f():\n\treturn 1  \n",
		"```latex\ne^{i\\pi} + 1 = 0\n```",
		"- [x] Legal review",
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("exported Markdown lacks %q:\n%s", want, markdown)
		}
	}
	if strings.Contains(markdown, "<b>first") {
		t.Errorf("raw HTML from text reached the Markdown unescaped:\n%s", markdown)
	}
	// What Markdown cannot represent is refused with its reason, not dropped.
	created = require(strings.Replace(emptyDocPage, "slug: human-document", "slug: export-unmappable", 1), "page", "create", "--allow-empty", "--file", "-")
	if json.Unmarshal(created, &page) != nil || page.ID == "" {
		t.Fatalf("Page create: %s", created)
	}
	nested := map[string]any{"type": "doc", "content": []any{block("table", "nt", nil,
		block("tableRow", "nr", nil, block("tableCell", "nc", nil, block("codeBlock", "ncode", map[string]any{"language": "go"}, text("x := 1")))))}}
	request, _ = json.Marshal(map[string]any{"id": "export-unmappable-init", "schema": 4, "page_id": page.ID, "body": nested})
	require(string(request), "doc", "initialize", "export-unmappable", "--file", "-")
	cmd := exec.CommandContext(t.Context(), binary, "doc", "export", "export-unmappable")
	cmd.Dir = work
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "CREWSHIP_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "422") || !strings.Contains(string(out), "code block inside a simple table") {
		t.Fatalf("unrepresentable export must fail with its reason: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(work, "export-unmappable.md")); err == nil {
		t.Fatal("a refused export wrote a file")
	}
}
