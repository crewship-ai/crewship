//go:build unix

package main

import (
	"bytes"
	"context"
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
	"unicode/utf16"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Drives `crewship doc comments|comment|comment-resolve` through the real
// binary, public routes, CLI token scopes, Page grants and the private Go
// authority with SQLite. The controlled engine computes a synthetic anchor;
// real anchor resolution is covered by tools/docs-service.
func TestAcceptance_DocumentCommentsBinary(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const workspace = "cdoccommentsworkspace00001"
	tokens := map[string]string{"owner": "crewship_cli_doc_comments_owner_fixture", "commenter": "crewship_cli_doc_comments_commenter_fixture", "reader": "crewship_cli_doc_comments_reader_fixture", "outsider": "crewship_cli_doc_comments_outsider_fixture"}
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('doc-c-owner','doc-c-owner@example.invalid'),('doc-c-commenter','doc-c-commenter@example.invalid'),('doc-c-reader','doc-c-reader@example.invalid'),('doc-c-outsider','doc-c-outsider@example.invalid')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('cdoccommentsworkspace00001','Doc comments','doc-comments')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('doc-c-m1','cdoccommentsworkspace00001','doc-c-owner','OWNER'),('doc-c-m2','cdoccommentsworkspace00001','doc-c-commenter','MEMBER'),('doc-c-m3','cdoccommentsworkspace00001','doc-c-reader','MEMBER'),('doc-c-m4','cdoccommentsworkspace00001','doc-c-outsider','MEMBER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for who, token := range tokens {
		if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES(?,?,'staging',?,'STANDARD')`, "doc-c-token-"+who, "doc-c-"+who, sha256HexToken(token)); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := os.MkdirTemp("", "doc-comments-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	authoritySocket, engineSocket := filepath.Join(dir, "a.sock"), filepath.Join(dir, "e.sock")
	secret := strings.Repeat("doc-comments-secret-", 3)
	secretFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(secretFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	audit := journal.NewWriter(db, slog.Default(), journal.WriterOptions{ChainKey: bytes.Repeat([]byte{9}, 32)})
	t.Cleanup(func() { _ = audit.Close() })
	router, err := api.NewRouter(db, strings.Repeat("doc-comments-jwt-", 3), slog.Default(), api.WithJournal(audit), api.WithDocumentAuthority(authoritySocket, secretFile), api.WithDocumentService(engineSocket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	engine := &commentBinaryEngine{human: newHumanBinaryEngine(authoritySocket, secret)}
	stop, err := documents.ServePrivateUnix(engineSocket, engine)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stop(context.Background()); engine.human.client.CloseIdleConnections() })
	if _, status, err := engine.human.call(t.Context(), "", workspace, "", "engine/recover", map[string]any{"boot": "doc-comments-controlled-engine", "protocol": 3, "schema": documents.LatestSchema}); err != nil || status != 200 {
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
	run := func(cfg, input string, args ...string) ([]byte, error) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Dir = dir
		cmd.Stdin = strings.NewReader(input)
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "CREWSHIP_") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		return cmd.CombinedOutput()
	}
	require := func(cfg, input string, args ...string) []byte {
		t.Helper()
		out, err := run(cfg, input, args...)
		if err != nil {
			t.Fatalf("binary %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	refuse := func(cfg, input, status string, args ...string) {
		t.Helper()
		if out, err := run(cfg, input, args...); err == nil || !bytes.Contains(out, []byte(status)) {
			t.Fatalf("binary %s must fail with %s: %v\n%s", strings.Join(args, " "), status, err, out)
		}
	}
	mint := func(base string, scopes []string) string {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"name": "doc comments scoped", "scopes": scopes})
		req, _ := http.NewRequestWithContext(t.Context(), "POST", server.URL+"/api/v1/auth/cli-token", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+base)
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var token struct{ Token string }
		if (resp.StatusCode != 201 && resp.StatusCode != 200) || json.Unmarshal(body, &token) != nil || token.Token == "" {
			t.Fatalf("scoped token mint: %d %s", resp.StatusCode, body)
		}
		return token.Token
	}
	owner := config(mint(tokens["owner"], []string{"pages:read", "pages:write"}))
	ownerReadOnly := config(mint(tokens["owner"], []string{"pages:read"}))
	commenter := config(mint(tokens["commenter"], []string{"pages:read", "pages:write"}))
	reader := config(mint(tokens["reader"], []string{"pages:read", "pages:write"}))
	outsider := config(mint(tokens["outsider"], []string{"pages:read", "pages:write"}))

	created := require(config(tokens["owner"]), strings.Replace(emptyDocPage, "slug: human-document", "slug: commented-document", 1), "page", "create", "--allow-empty", "--file", "-")
	var page struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(created, &page) != nil || page.ID == "" {
		t.Fatalf("Page create response: %s", created)
	}
	// One Comment grant, one Read grant and one member with neither.
	if _, err := db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user','doc-c-commenter','comment','doc-c-owner'),(?,'user','doc-c-reader','read','doc-c-owner')`, page.ID, page.ID); err != nil {
		t.Fatal(err)
	}
	initial := fmt.Sprintf(`{"id":"comments-init","schema":4,"page_id":%q,"body":{"type":"doc","content":[{"type":"paragraph","attrs":{"id":"intro"},"content":[{"type":"text","text":"Bloky kódu a ukázky"}]}]}}`, page.ID)
	require(owner, initial, "doc", "initialize", "commented-document", "--file", "-")
	var state struct {
		Schema     int    `json:"schema"`
		Rev        int64  `json:"rev"`
		Epoch      string `json:"epoch"`
		CanComment bool   `json:"can_comment"`
		CanResolve bool   `json:"can_resolve"`
	}
	if raw := require(owner, "", "doc", "read", "commented-document"); json.Unmarshal(raw, &state) != nil || state.Schema != 4 || !state.CanComment || !state.CanResolve {
		t.Fatalf("schema 4 read and comment capability: %s", raw)
	}
	type listing struct {
		Comments []struct {
			ID, Body, Actor, Parent, Status, Quote string
			Version                                int64
			Resolved                               bool
		} `json:"comments"`
		CanComment bool `json:"can_comment"`
		CanResolve bool `json:"can_resolve"`
	}
	list := func(cfg string) listing {
		t.Helper()
		raw := require(cfg, "", "doc", "comments", "commented-document")
		var value listing
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatalf("comments listing: %s %v", raw, err)
		}
		return value
	}
	if got := list(owner); len(got.Comments) != 0 || !got.CanComment {
		t.Fatalf("empty listing: %+v", got)
	}
	request := func(fields map[string]any) string {
		base := map[string]any{"schema": 4, "page_id": page.ID, "epoch": state.Epoch, "ifRev": state.Rev}
		for k, v := range fields {
			base[k] = v
		}
		raw, _ := json.Marshal(base)
		return string(raw)
	}
	thread := request(map[string]any{"id": "thread-blocks", "block": "intro", "from": 0, "to": 10, "text": "Ukázka komentáře"})
	receipt := require(owner, thread, "doc", "comment", "commented-document", "--file", "-")
	retry := require(owner, thread, "doc", "comment", "commented-document", "--file", "-")
	var first, second map[string]any
	if json.Unmarshal(receipt, &first) != nil || json.Unmarshal(retry, &second) != nil || first["kind"] != "comment" || first["actor"] != "user:doc-c-owner" || second["rev"] != first["rev"] {
		t.Fatalf("comment receipt and exact retry: %s %s", receipt, retry)
	}
	refuse(owner, request(map[string]any{"id": "thread-blocks", "block": "intro", "from": 0, "to": 10, "text": "Changed under the same id"}), "409", "doc", "comment", "commented-document", "--file", "-")
	refuse(owner, request(map[string]any{"id": "stale", "block": "intro", "from": 0, "to": 5, "text": "x", "ifRev": state.Rev + 1}), "409", "doc", "comment", "commented-document", "--file", "-")
	refuse(ownerReadOnly, request(map[string]any{"id": "scoped-out", "block": "intro", "from": 0, "to": 5, "text": "x"}), "403", "doc", "comment", "commented-document", "--file", "-")
	require(commenter, request(map[string]any{"id": "reply-commenter", "parent": "thread-blocks", "text": "Odpověď"}), "doc", "comment", "commented-document", "--file", "-")
	refuse(reader, request(map[string]any{"id": "reader-reply", "parent": "thread-blocks", "text": "Not allowed"}), "403", "doc", "comment", "commented-document", "--file", "-")
	refuse(outsider, request(map[string]any{"id": "outsider-reply", "parent": "thread-blocks", "text": "Not allowed"}), "403", "doc", "comment", "commented-document", "--file", "-")
	// Reading comments follows reading the document exactly, never more.
	_, readErr := run(outsider, "", "doc", "read", "commented-document")
	_, listErr := run(outsider, "", "doc", "comments", "commented-document")
	if (readErr == nil) != (listErr == nil) {
		t.Fatalf("outsider comment listing (%v) differs from document read (%v)", listErr, readErr)
	}

	if got := list(reader); len(got.Comments) != 2 || got.CanComment || got.CanResolve {
		t.Fatalf("reader listing and capability: %+v", got)
	}
	got := list(commenter)
	if len(got.Comments) != 2 || !got.CanComment || got.Comments[0].ID != "thread-blocks" || got.Comments[0].Quote != "Bloky kódu" || got.Comments[0].Status != "attached" || got.Comments[1].Parent != "thread-blocks" || got.Comments[1].Actor != "user:doc-c-commenter" {
		t.Fatalf("thread listing: %+v", got)
	}
	refuse(reader, request(map[string]any{"id": "reader-resolve", "comment": "thread-blocks", "version": 1, "resolved": true}), "403", "doc", "comment-resolve", "commented-document", "--file", "-")
	require(commenter, request(map[string]any{"id": "resolve-1", "comment": "thread-blocks", "version": 1, "resolved": true}), "doc", "comment-resolve", "commented-document", "--file", "-")
	refuse(owner, request(map[string]any{"id": "resolve-stale", "comment": "thread-blocks", "version": 1, "resolved": false}), "409", "doc", "comment-resolve", "commented-document", "--file", "-")
	if got := list(owner); !got.Comments[0].Resolved || got.Comments[0].Status != "resolved" || got.Comments[0].Version != 2 {
		t.Fatalf("resolved thread: %+v", got.Comments[0])
	}
	var rev int64
	var comments int
	if err := db.QueryRow(`SELECT head_rev FROM documents WHERE page_id=?`, page.ID).Scan(&rev); err != nil || rev != state.Rev {
		t.Fatalf("comments changed the document revision: %d %v", rev, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM document_comments WHERE page_id=?`, page.ID).Scan(&comments); err != nil || comments != 2 {
		t.Fatalf("stored comments: %d %v", comments, err)
	}
}

// commentBinaryEngine answers the three comment operations the way the
// managed engine does (closed request, current revision, engine-made anchor)
// and leaves every other operation to the controlled human engine.
type commentBinaryEngine struct {
	human *humanBinaryEngine
}

func (e *commentBinaryEngine) raw(r *http.Request, page, operation string, body any) ([]byte, int, error) {
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(r.Context(), "POST", "http://authority/internal/docs/v1/"+page+"/"+operation, bytes.NewReader(payload))
	if err != nil {
		return nil, 500, err
	}
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	req.Header.Set("X-Workspace-ID", r.Header.Get("X-Workspace-ID"))
	req.Header.Set("X-Crewship-Docs-Service", e.human.secret)
	resp, err := e.human.client.Do(req)
	if err != nil {
		return nil, 503, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return data, resp.StatusCode, err
}

func (e *commentBinaryEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if r.Header.Get("X-Crewship-Docs-Service") != e.human.secret || len(parts) != 4 || (parts[3] != "comments" && parts[3] != "comment" && parts[3] != "resolve") {
		e.human.ServeHTTP(w, r)
		return
	}
	page, op := parts[2], parts[3]
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	reply := func(status int, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
	}
	forward := func(data []byte, status int, err error) {
		if err != nil {
			reply(503, map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(data)
	}
	data, status, err := e.raw(r, page, "state", map[string]any{})
	if err != nil || status >= 400 {
		forward(data, status, err)
		return
	}
	var state struct {
		Rev         int64  `json:"rev"`
		Generation  int64  `json:"generation"`
		Incarnation string `json:"incarnation"`
		Snapshot    []byte `json:"snapshot"`
	}
	if json.Unmarshal(data, &state) != nil {
		reply(502, map[string]string{"error": "state"})
		return
	}
	doc, err := humanBinaryDecode(state.Snapshot)
	if err != nil {
		reply(502, map[string]string{"error": err.Error()})
		return
	}
	text := doc.Content[0].Content[0].Text
	if op == "comments" {
		data, status, err = e.raw(r, page, "comments", map[string]any{})
		if err != nil || status >= 400 {
			forward(data, status, err)
			return
		}
		var comments []map[string]any
		if json.Unmarshal(data, &comments) != nil {
			reply(502, map[string]string{"error": "comments"})
			return
		}
		for _, c := range comments {
			if anchor, ok := c["anchor"].(map[string]any); ok {
				c["quote"], c["block"], c["status"] = anchor["quote"], anchor["block"], "attached"
				if c["resolved"] == true {
					c["status"] = "resolved"
				}
			}
		}
		reply(200, map[string]any{"comments": comments})
		return
	}
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		reply(400, map[string]string{"error": "object required"})
		return
	}
	epoch := fmt.Sprintf("%s:controlled-human-engine:%v", state.Incarnation, state.Generation)
	if in["epoch"] != epoch || in["page_id"] != page {
		reply(409, map[string]string{"error": "old epoch or foreign Page"})
		return
	}
	if rev, _ := in["ifRev"].(float64); int64(rev) != state.Rev {
		reply(409, map[string]string{"error": "comment revision changed"})
		return
	}
	fingerprint := humanBinaryHash(op + ":" + string(raw))
	body := map[string]any{"id": in["id"], "fingerprint": fingerprint, "ifRev": state.Rev, "generation": state.Generation}
	if op == "resolve" {
		body["comment"], body["version"], body["resolved"] = in["comment"], in["version"], in["resolved"]
	} else {
		body["body"] = in["text"]
		if parent, ok := in["parent"]; ok {
			body["parent"] = parent
		} else {
			from, _ := in["from"].(float64)
			to, _ := in["to"].(float64)
			units := utf16.Encode([]rune(text))
			if from < 0 || to <= from || int(to) > len(units) {
				reply(400, map[string]string{"error": "invalid range"})
				return
			}
			body["anchor"] = map[string]any{"id": in["id"], "block": in["block"], "quote": string(utf16.Decode(units[int(from):int(to)])), "checkpoint": state.Rev}
		}
	}
	data, status, err = e.raw(r, page, op, body)
	if err == nil && status < 400 {
		var receipt map[string]any
		if json.Unmarshal(data, &receipt) == nil {
			receipt["epoch"] = epoch
			reply(status, receipt)
			return
		}
	}
	forward(data, status, err)
}
