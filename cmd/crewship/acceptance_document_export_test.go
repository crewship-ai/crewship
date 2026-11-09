//go:build unix

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Drives `crewship doc export` through the real binary, public route, CLI
// token scopes, Page grants and the private Go authority with SQLite. The
// controlled engine reads state through the authority with the caller's own
// credential, exactly as the managed engine does, and serializes its
// synthetic paragraph; real Markdown serialization is covered by
// tools/docs-service (schema-v4-node-test.mjs).
func TestAcceptance_DocumentExportBinary(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const workspace = "cdocexportworkspace0000001"
	people := []string{"owner", "reader", "outsider", "restricted"}
	tokens := map[string]string{}
	for _, who := range people {
		tokens[who] = "crewship_cli_doc_export_" + who + "_fixture"
	}
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('doc-x-owner','doc-x-owner@example.invalid'),('doc-x-reader','doc-x-reader@example.invalid'),('doc-x-outsider','doc-x-outsider@example.invalid'),('doc-x-restricted','doc-x-restricted@example.invalid')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('cdocexportworkspace0000001','Doc export','doc-export')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('doc-x-m1','cdocexportworkspace0000001','doc-x-owner','OWNER'),('doc-x-m2','cdocexportworkspace0000001','doc-x-reader','MEMBER'),('doc-x-m3','cdocexportworkspace0000001','doc-x-outsider','MEMBER')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('doc-x-m4','cdocexportworkspace0000001','doc-x-restricted','MEMBER','restricted')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, who := range people {
		if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES(?,?,'staging',?,'STANDARD')`, "doc-x-token-"+who, "doc-x-"+who, sha256HexToken(tokens[who])); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := os.MkdirTemp("", "doc-export-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	authoritySocket, engineSocket := filepath.Join(dir, "a.sock"), filepath.Join(dir, "e.sock")
	secret := strings.Repeat("doc-export-secret-", 3)
	secretFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(secretFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	audit := journal.NewWriter(db, slog.Default(), journal.WriterOptions{ChainKey: bytes.Repeat([]byte{7}, 32)})
	t.Cleanup(func() { _ = audit.Close() })
	router, err := api.NewRouter(db, strings.Repeat("doc-export-jwt-", 3), slog.Default(), api.WithJournal(audit), api.WithDocumentAuthority(authoritySocket, secretFile), api.WithDocumentService(engineSocket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	engine := &exportBinaryEngine{human: newHumanBinaryEngine(authoritySocket, secret)}
	stop, err := documents.ServePrivateUnix(engineSocket, engine)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stop(context.Background()); engine.human.client.CloseIdleConnections() })
	if _, status, err := engine.human.call(t.Context(), "", workspace, "", "engine/recover", map[string]any{"boot": "doc-export-controlled-engine", "protocol": 3, "schema": documents.LatestSchema}); err != nil || status != 200 {
		t.Fatalf("authority recovery: %d %v", status, err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	binary := buildCrewshipBinary(t)
	config := func(token string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(path, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: %s\nformat: json\n", server.URL, token, workspace)), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	work := t.TempDir()
	run := func(cfg, input string, args ...string) ([]byte, error) {
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
		return cmd.Output()
	}
	require := func(cfg, input string, args ...string) []byte {
		t.Helper()
		out, err := run(cfg, input, args...)
		if err != nil {
			stderr := ""
			if exit, ok := err.(*exec.ExitError); ok {
				stderr = string(exit.Stderr)
			}
			t.Fatalf("binary %s: %v\n%s%s", strings.Join(args, " "), err, out, stderr)
		}
		return out
	}
	failure := func(cfg string, args ...string) string {
		t.Helper()
		out, err := run(cfg, "", args...)
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("binary %s must fail: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out) + string(exit.Stderr)
	}
	owner, reader, outsider, restricted := config(tokens["owner"]), config(tokens["reader"]), config(tokens["outsider"]), config(tokens["restricted"])

	created := require(owner, strings.Replace(emptyDocPage, "slug: human-document", "slug: export-notes", 1), "page", "create", "--allow-empty", "--file", "-")
	var page struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(created, &page) != nil || page.ID == "" {
		t.Fatalf("Page create response: %s", created)
	}
	if _, err := db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user','doc-x-reader','read','doc-x-owner'),(?,'user','doc-x-restricted','read','doc-x-owner')`, page.ID, page.ID); err != nil {
		t.Fatal(err)
	}
	const text = "Export <b>stays</b> text: Příliš žluťoučký kůň"
	initial := fmt.Sprintf(`{"id":"export-init","schema":4,"page_id":%q,"body":{"type":"doc","content":[{"type":"paragraph","attrs":{"id":"intro"},"content":[{"type":"text","text":%q}]}]}}`, page.ID, text)
	require(owner, initial, "doc", "initialize", "export-notes", "--file", "-")
	want := text + "\n"

	// The owner downloads the file; the receipt names it and hashes its bytes.
	var receipt struct {
		Path       string `json:"path"`
		MediaType  string `json:"media_type"`
		ByteLength int    `json:"byte_length"`
		SHA256     string `json:"sha256"`
	}
	if raw := require(owner, "", "doc", "export", "export-notes"); json.Unmarshal(raw, &receipt) != nil || receipt.Path != "export-notes.md" || receipt.MediaType != "text/markdown" {
		t.Fatalf("export receipt: %s", raw)
	}
	saved, err := os.ReadFile(filepath.Join(work, "export-notes.md"))
	sum := sha256.Sum256(saved)
	if err != nil || string(saved) != want || receipt.ByteLength != len(saved) || receipt.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("exported file %q (%v) receipt %+v", saved, err, receipt)
	}
	// An existing file is never replaced silently.
	if out := failure(owner, "doc", "export", "export-notes"); !strings.Contains(out, "exists; pass --force") {
		t.Fatalf("overwrite without --force: %s", out)
	}
	require(owner, "", "doc", "export", "export-notes", "--force")
	// A reader with a Read grant may export what it may read; stdout carries the bytes.
	if out := require(reader, "", "doc", "export", "export-notes", "-o", "-"); string(out) != want {
		t.Fatalf("reader export to stdout: %q", out)
	}
	if out := failure(owner, "doc", "export", "export-notes", "--as", "pdf"); !strings.Contains(out, "--as must be md or docx") {
		t.Fatalf("unsupported format: %s", out)
	}
	// Word: the same read, delivered as a .docx package the CLI writes intact.
	var docxReceipt struct {
		Path      string `json:"path"`
		MediaType string `json:"media_type"`
		SHA256    string `json:"sha256"`
	}
	if raw := require(reader, "", "doc", "export", "export-notes", "--as", "docx"); json.Unmarshal(raw, &docxReceipt) != nil || docxReceipt.Path != "export-notes.docx" || docxReceipt.MediaType != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		t.Fatalf("docx receipt: %s", raw)
	}
	docx, err := os.ReadFile(filepath.Join(work, "export-notes.docx"))
	docxSum := sha256.Sum256(docx)
	if err != nil || docxReceipt.SHA256 != hex.EncodeToString(docxSum[:]) {
		t.Fatalf("docx file (%v) receipt %+v", err, docxReceipt)
	}
	archive, err := zip.NewReader(bytes.NewReader(docx), int64(len(docx)))
	if err != nil || len(archive.File) != 1 || archive.File[0].Name != "word/document.xml" {
		t.Fatalf("exported docx does not open: %v", err)
	}
	part, err := archive.File[0].Open()
	body, _ := io.ReadAll(part)
	if err != nil || !strings.Contains(string(body), "<w:t>"+strings.TrimSuffix(want, "\n")+"</w:t>") {
		t.Fatalf("exported docx text: %s (%v)", body, err)
	}
	// Export never reaches further than read: a member without a grant fails
	// with exactly the status doc read gives it, and without content.
	status := func(out string) string {
		var envelope struct {
			Error struct {
				Status int `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &envelope) != nil || envelope.Error.Status == 0 {
			t.Fatalf("no error status in %s", out)
		}
		return fmt.Sprint(envelope.Error.Status)
	}
	readOut, exportOut := failure(outsider, "doc", "read", "export-notes"), failure(outsider, "doc", "export", "export-notes")
	if status(readOut) != status(exportOut) || strings.Contains(exportOut, "stays") {
		t.Fatalf("outsider export must fail exactly like read: read=%s export=%s", readOut, exportOut)
	}
	// Private output: anyone but its custodian, a reader grant included, gets
	// the same 404 as for a document that does not exist.
	if _, err := db.Exec(`UPDATE pages SET private_audience=1 WHERE id=?`, page.ID); err != nil {
		t.Fatal(err)
	}
	missing := failure(outsider, "doc", "export", "no-such-document")
	for name, cfg := range map[string]string{"outsider": outsider, "reader": reader} {
		for _, as := range []string{"md", "docx"} {
			out := failure(cfg, "doc", "export", "export-notes", "--as", as, "-o", "-")
			if status(out) != "404" || status(missing) != "404" || strings.Contains(out, "stays") || strings.Contains(out, page.ID) {
				t.Fatalf("%s %s export of private output must look missing: %s (missing: %s)", name, as, out, missing)
			}
		}
	}
	if out := require(owner, "", "doc", "export", "export-notes", "-o", "-"); string(out) != want {
		t.Fatalf("custodian export of private output: %q", out)
	}
	// Export is not a restricted-member surface: the member gets the same 404
	// as for a document that does not exist, even holding a read grant.
	restrictedOut, restrictedMissing := failure(restricted, "doc", "export", "export-notes"), failure(restricted, "doc", "export", "no-such-document")
	if strings.Contains(restrictedOut, "stays") || status(restrictedOut) != "404" || status(restrictedMissing) != "404" || strings.Contains(restrictedOut, page.ID) {
		t.Fatalf("restricted member export must look missing: %s (missing: %s)", restrictedOut, restrictedMissing)
	}
	if _, err := os.Stat(filepath.Join(work, "no-such-document.md")); err == nil {
		t.Fatal("a refused export wrote a file")
	}
	// A deleted document is gone for export too.
	require(owner, "", "page", "delete", "export-notes", "--yes")
	if out := failure(owner, "doc", "export", "export-notes", "-o", "-"); strings.Contains(out, "stays") || !strings.Contains(out, "404") {
		t.Fatalf("deleted document export: %s", out)
	}
	if engine.exports == 0 {
		t.Fatal("export never reached the engine")
	}
}

// exportBinaryEngine answers export the way the managed engine does: it reads
// confirmed state through the private authority with the caller's credential
// and returns {"markdown": ...}. Every other operation goes to the controlled
// human engine.
type exportBinaryEngine struct {
	human   *humanBinaryEngine
	exports int
}

func (e *exportBinaryEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if r.Header.Get("X-Crewship-Docs-Service") != e.human.secret || len(parts) != 4 || parts[3] != "export" {
		e.human.ServeHTTP(w, r)
		return
	}
	var request struct {
		Format string `json:"format"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&request)
	state, status, err := e.human.call(r.Context(), r.Header.Get("Authorization"), r.Header.Get("X-Workspace-ID"), parts[2], "state", map[string]any{})
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(503)
		return
	}
	if status >= 400 {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(state)
		return
	}
	encoded, _ := state["snapshot"].(string)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		w.WriteHeader(502)
		return
	}
	doc, err := humanBinaryDecode(raw)
	if err != nil {
		w.WriteHeader(502)
		return
	}
	e.exports++
	text := doc.Content[0].Content[0].Text
	if request.Format == "docx" {
		// A minimal Word package, the shape the managed formatter returns.
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		part, _ := zw.Create("word/document.xml")
		_, _ = fmt.Fprintf(part, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>%s</w:t></w:r></w:p></w:body></w:document>`, text)
		_ = zw.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{"docx": base64.StdEncoding.EncodeToString(buf.Bytes())})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"markdown": text + "\n"})
}
