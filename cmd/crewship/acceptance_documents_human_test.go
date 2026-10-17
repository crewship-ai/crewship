//go:build unix

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// This acceptance test proves real CLI/HTTP/auth/SQLite protocol behavior.
// The controlled engine stores synthetic JSON snapshots, not Yjs updates.
// Native Node, document-model, image and provider evidence are separate gates.
func TestAcceptance_HumanDocumentsBinary(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const workspace = "chumandocworkspace00000001"
	const staging = "crewship_cli_human_document_staging_fixture"
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('human-doc-owner','human-doc@example.invalid')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('chumandocworkspace00000001','Human docs','human-docs')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('human-doc-member','chumandocworkspace00000001','human-doc-owner','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('human-doc-staging','human-doc-owner','staging',?,'STANDARD')`, sha256HexToken(staging)); err != nil {
		t.Fatal(err)
	}
	// Short, private paths fit sockaddr_un even in a long managed test directory.
	dir, err := os.MkdirTemp("", "human-doc-binary-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	authoritySocket, engineSocket := filepath.Join(dir, "a.sock"), filepath.Join(dir, "e.sock")
	secret := strings.Repeat("human-binary-secret-", 3)
	secretFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(secretFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	audit := journal.NewWriter(db, slog.Default(), journal.WriterOptions{ChainKey: bytes.Repeat([]byte{7}, 32)})
	t.Cleanup(func() { _ = audit.Close() })
	router, err := api.NewRouter(db, strings.Repeat("human-jwt-secret-", 3), slog.Default(), api.WithJournal(audit), api.WithDocumentAuthority(authoritySocket, secretFile), api.WithDocumentService(engineSocket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	engine := newHumanBinaryEngine(authoritySocket, secret)
	stop, err := documents.ServePrivateUnix(engineSocket, engine)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stop(context.Background()); engine.client.CloseIdleConnections() })
	if _, status, err := engine.call(t.Context(), "", workspace, "", "engine/recover", map[string]any{"boot": "human-binary-controlled-engine", "protocol": 3, "schema": documents.LatestSchema}); err != nil || status != 200 {
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
	stagingConfig := config(staging)
	created := require(stagingConfig, emptyDocPage, "page", "create", "--allow-empty", "--file", "-")
	var page struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(created, &page) != nil || page.ID == "" {
		t.Fatalf("Page create response: %s", created)
	}
	if err := audit.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var summary, payload string
	if err := db.QueryRow(`SELECT summary,payload FROM journal_entries WHERE workspace_id=? AND entry_type=?`, workspace, string(journal.EntryPageSpecChanged)).Scan(&summary, &payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(summary+payload, "human-document") || strings.Contains(summary+payload, "Human document") || strings.Contains(payload, "fingerprint") || !strings.Contains(payload, page.ID) {
		t.Fatalf("creation audit disclosed metadata: %s %s", summary, payload)
	}
	mint := func(scopes []string) (string, string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"name": "human document scoped", "scopes": scopes})
		req, _ := http.NewRequestWithContext(t.Context(), "POST", server.URL+"/api/v1/auth/cli-token", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+staging)
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var token struct{ ID, Token string }
		if resp.StatusCode != 201 && resp.StatusCode != 200 {
			t.Fatalf("real scoped token mint: %d %s", resp.StatusCode, body)
		}
		if json.Unmarshal(body, &token) != nil || token.Token == "" || token.ID == "" {
			t.Fatal("invalid minted token response")
		}
		return token.Token, token.ID
	}
	writer, writerID := mint([]string{"pages:read", "pages:write"})
	writerConfig := config(writer)
	readOnly, _ := mint([]string{"pages:read"})
	readConfig := config(readOnly)
	require(writerConfig, "", "doc", "status")
	initial := `{"id":"human-init","schema":2,"body":{"type":"doc","content":[{"type":"paragraph","attrs":{"id":"intro"},"content":[{"type":"text","text":"Original selected text"}]}]}}`
	init := require(writerConfig, initial, "doc", "initialize", "human-document", "--file", "-")
	var initWire map[string]any
	if json.Unmarshal(init, &initWire) != nil || initWire["schema"] != float64(2) {
		t.Fatalf("initial source schema: %s", init)
	}
	if _, ok := initWire["epoch"].(float64); !ok {
		t.Fatalf("initialization fence must retain numeric source wire: %s", init)
	}
	initRetry := require(writerConfig, initial, "doc", "initialize", "human-document", "--file", "-")
	var initRetryWire map[string]any
	if json.Unmarshal(initRetry, &initRetryWire) != nil || initRetryWire["epoch"] != initWire["epoch"] || initRetryWire["id"] != "human-init" {
		t.Fatalf("initialization retry fence/receipt changed: %s", initRetry)
	}
	type publicState struct {
		Rev     int64                             `json:"rev"`
		Epoch   string                            `json:"epoch"`
		Page    string                            `json:"page_id"`
		Outline []struct{ ID, Hash, Text string } `json:"outline"`
	}
	read := func(cfg string) publicState {
		t.Helper()
		raw := require(cfg, "", "doc", "read", "human-document")
		var state publicState
		if err := json.Unmarshal(raw, &state); err != nil || state.Page != page.ID || len(state.Outline) != 1 {
			t.Fatalf("current document: %s %v", raw, err)
		}
		return state
	}
	before := read(writerConfig)
	if before.Rev != 0 || before.Epoch == "" || before.Outline[0].Text != "Original selected text" {
		t.Fatalf("initial current state: %+v", before)
	}
	outline := require(writerConfig, "", "doc", "outline", "human-document")
	if !strings.Contains(string(outline), before.Outline[0].Hash) {
		t.Fatalf("outline lost hash: %s", outline)
	}
	patch := map[string]any{"id": "human-patch", "schema": 2, "epoch": before.Epoch, "ifRev": before.Rev, "target": "intro", "ifHash": before.Outline[0].Hash, "text": "Confirmed replacement"}
	rawPatch, _ := json.Marshal(patch)
	deniedBefore := engine.patches.Load()
	if out, err := run(readConfig, string(rawPatch), "doc", "patch", "human-document", "--file", "-"); err == nil || !bytes.Contains(out, []byte("403")) {
		t.Fatalf("read-only write: %v %s", err, out)
	}
	if engine.patches.Load() != deniedBefore {
		t.Fatal("public read-only rejection reached engine")
	}
	read(readConfig)
	receipt := require(writerConfig, string(rawPatch), "doc", "patch", "human-document", "--file", "-")
	retry := require(writerConfig, string(rawPatch), "doc", "patch", "human-document", "--file", "-")
	var first, second map[string]any
	if json.Unmarshal(receipt, &first) != nil || json.Unmarshal(retry, &second) != nil || first["id"] != "human-patch" || first["rev"] != float64(1) || first["epoch"] != before.Epoch || first["actor"] != "user:human-doc-owner" || first["kind"] != "human" || second["rev"] != first["rev"] || second["epoch"] != first["epoch"] {
		t.Fatalf("exact receipt retry: %s %s", receipt, retry)
	}
	after := read(writerConfig)
	if after.Rev != 1 || after.Epoch != before.Epoch || after.Outline[0].Text != "Confirmed replacement" || after.Outline[0].Hash == before.Outline[0].Hash {
		t.Fatalf("confirmed state: %+v", after)
	}
	unchanged := func() {
		t.Helper()
		var docs, head, receipts, updates int
		if err := db.QueryRow(`SELECT COUNT(*),MAX(head_rev) FROM documents WHERE workspace_id=? AND page_id=?`, workspace, page.ID).Scan(&docs, &head); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM document_receipts WHERE workspace_id=? AND page_id=?`, workspace, page.ID).Scan(&receipts); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM document_updates WHERE workspace_id=? AND page_id=?`, workspace, page.ID).Scan(&updates); err != nil {
			t.Fatal(err)
		}
		if docs != 1 || head != 1 || receipts != 2 || updates != 1 {
			t.Fatalf("durable counts docs=%d head=%d receipts=%d updates=%d", docs, head, receipts, updates)
		}
	}
	unchanged()
	for _, field := range []string{"ifHash", "ifRev", "epoch"} {
		bad := map[string]any{"id": "stale-" + field, "schema": 2, "epoch": after.Epoch, "ifRev": after.Rev, "target": "intro", "ifHash": after.Outline[0].Hash, "text": "Must not persist"}
		switch field {
		case "ifHash":
			bad[field] = before.Outline[0].Hash
		case "ifRev":
			bad[field] = before.Rev
		case "epoch":
			bad[field] = "old:" + after.Epoch
		}
		raw, _ := json.Marshal(bad)
		calls := engine.patches.Load()
		out, err := run(writerConfig, string(raw), "doc", "patch", "human-document", "--file", "-")
		if err == nil || !bytes.Contains(out, []byte("409")) {
			t.Fatalf("stale %s: %v %s", field, err, out)
		}
		if field != "epoch" && !bytes.Contains(out, []byte(documents.ErrConflict.Error())) {
			t.Fatalf("stale %s did not reach the real Go revision/hash guard: %s", field, out)
		}
		if engine.patches.Load() != calls+1 {
			t.Fatalf("CLI retried/rebased stale %s", field)
		}
		unchanged()
	}
	require(stagingConfig, "", "token", "revoke", writerID, "--yes")
	admittedBeforeRevokedReplay := engine.patches.Load()
	if out, err := run(writerConfig, string(rawPatch), "doc", "patch", "human-document", "--file", "-"); err == nil || (!bytes.Contains(out, []byte("401")) && !bytes.Contains(out, []byte("403"))) {
		t.Fatalf("revoked exact retry must fail authorization: %v %s", err, out)
	}
	if engine.patches.Load() != admittedBeforeRevokedReplay {
		t.Fatal("revoked public replay reached engine")
	}
	if _, status, err := engine.call(t.Context(), "Bearer "+writer, workspace, page.ID, "receipt", map[string]any{"id": "human-patch", "fingerprint": "irrelevant-after-revocation"}); err != nil || (status != 401 && status != 403) {
		t.Fatalf("private authority must reject revoked replay before fingerprint lookup: %d %v", status, err)
	}
	unchanged()
	var snapshot []byte
	if err := db.QueryRow(`SELECT payload FROM document_updates WHERE workspace_id=? AND page_id=? AND rev=1`, workspace, page.ID).Scan(&snapshot); err != nil || !bytes.Contains(snapshot, []byte("Confirmed replacement")) {
		t.Fatalf("persisted synthetic update: %q %v", snapshot, err)
	}
}

type humanBinaryEngine struct {
	client  *http.Client
	secret  string
	patches atomic.Int64
}

func newHumanBinaryEngine(socket, secret string) *humanBinaryEngine {
	return &humanBinaryEngine{secret: secret, client: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}}
}
func (e *humanBinaryEngine) call(ctx context.Context, bearer, workspace, page, operation string, body any) (map[string]any, int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, 500, err
	}
	path := "/internal/docs/v1/" + page + "/" + operation
	if operation == "engine/recover" {
		path = "/internal/docs/v1/engine/recover"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://authority"+path, bytes.NewReader(raw))
	if err != nil {
		return nil, 500, err
	}
	req.Header.Set("Authorization", bearer)
	req.Header.Set("X-Workspace-ID", workspace)
	req.Header.Set("X-Crewship-Docs-Service", e.secret)
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, 503, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, 502, err
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, 502, fmt.Errorf("authority response: %s", data)
	}
	return result, resp.StatusCode, nil
}
func (e *humanBinaryEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Crewship-Docs-Service") != e.secret {
		http.Error(w, "service credential required", 403)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] != "documents" {
		http.NotFound(w, r)
		return
	}
	page, op := parts[2], parts[3]
	if op == "patch" {
		e.patches.Add(1)
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (2<<20)+1))
	if err != nil {
		http.Error(w, "read failed", 400)
		return
	}
	var input struct {
		ID     string          `json:"id"`
		Schema int             `json:"schema"`
		Body   json.RawMessage `json:"body"`
		Epoch  string          `json:"epoch"`
		Rev    int64           `json:"ifRev"`
		Target string          `json:"target"`
		Hash   string          `json:"ifHash"`
		Text   string          `json:"text"`
	}
	if json.Unmarshal(raw, &input) != nil {
		http.Error(w, "object required", 400)
		return
	}
	call := func(operation string, body any) (map[string]any, int, error) {
		return e.call(r.Context(), r.Header.Get("Authorization"), r.Header.Get("X-Workspace-ID"), page, operation, body)
	}
	reply := func(value map[string]any, status int, err error) {
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
	}
	fingerprint := humanBinaryHash(op + ":" + string(raw))
	if op == "initialize" {
		receipt, status, err := call("receipt", map[string]any{"id": input.ID, "fingerprint": fingerprint})
		if err != nil || status >= 400 {
			reply(receipt, status, err)
			return
		}
		if receipt["found"] == true {
			reply(receipt, 200, nil)
			return
		}
		doc, err := humanBinaryDecode(input.Body)
		if err != nil {
			reply(nil, 400, err)
			return
		}
		snapshot, _ := json.Marshal(doc)
		state, status, err := call("initialize", map[string]any{"id": input.ID, "fingerprint": fingerprint, "schema": input.Schema, "snapshot": snapshot, "blocks": humanBinaryProjection(doc)})
		reply(state, status, err)
		return
	}
	state, status, err := call("state", map[string]any{})
	if err != nil || status >= 400 {
		reply(state, status, err)
		return
	}
	encoded, _ := state["snapshot"].(string)
	snapshot, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		reply(nil, 502, err)
		return
	}
	doc, err := humanBinaryDecode(snapshot)
	if err != nil {
		reply(nil, 502, err)
		return
	}
	// Apply the fixture's synthetic text updates from the actual durable log.
	// This deliberately performs no Yjs/model decoding.
	if updates, ok := state["updates"].([]any); ok {
		for _, encoded := range updates {
			text, decodeErr := base64.StdEncoding.DecodeString(encoded.(string))
			if decodeErr != nil {
				reply(nil, 502, decodeErr)
				return
			}
			doc.Content[0].Content[0].Text = string(text)
		}
	}
	epoch := fmt.Sprintf("%s:controlled-human-engine:%v", state["incarnation"], state["generation"])
	if op == "read" {
		reply(map[string]any{"schema": state["schema"], "rev": state["rev"], "epoch": epoch, "content": doc, "outline": humanBinaryOutline(doc)}, 200, nil)
		return
	}
	if op != "patch" {
		http.NotFound(w, r)
		return
	}
	receipt, status, err := call("receipt", map[string]any{"id": input.ID, "fingerprint": fingerprint})
	if err != nil || status >= 400 {
		reply(receipt, status, err)
		return
	}
	if receipt["found"] == true {
		receipt["epoch"] = epoch
		reply(receipt, 200, nil)
		return
	}
	if input.Epoch != epoch {
		reply(map[string]any{"error": "old epoch; reload document"}, 409, nil)
		return
	}
	if input.Target != doc.Content[0].Attrs.ID {
		reply(map[string]any{"error": "unknown target"}, 409, nil)
		return
	}
	lease, status, err := call("lease", map[string]any{"owner": "controlled-human-engine"})
	if err != nil || status >= 400 {
		reply(lease, status, err)
		return
	}
	defer func() {
		_, _, _ = call("release", map[string]any{"owner": lease["owner"], "token": lease["token"], "epoch": lease["epoch"]})
	}()
	doc.Content[0].Content[0].Text = input.Text
	snapshot, _ = json.Marshal(doc)
	receipt, status, err = call("commit", map[string]any{"id": input.ID, "fingerprint": fingerprint, "kind": "human", "schema": input.Schema, "generation": state["generation"], "ifRev": input.Rev, "ifHashes": map[string]string{input.Target: input.Hash}, "epoch": lease["epoch"], "leaseOwner": lease["owner"], "leaseToken": lease["token"], "update": []byte(input.Text), "snapshot": snapshot, "blocks": humanBinaryProjection(doc)})
	if err == nil && status < 400 {
		receipt["epoch"] = epoch
	}
	reply(receipt, status, err)
}

type humanBinaryDocument struct {
	Type    string `json:"type"`
	Content []struct {
		Type  string `json:"type"`
		Attrs struct {
			ID string `json:"id"`
		} `json:"attrs"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"content"`
}

func humanBinaryDecode(raw []byte) (humanBinaryDocument, error) {
	var doc humanBinaryDocument
	err := json.Unmarshal(raw, &doc)
	if err == nil && (len(doc.Content) != 1 || len(doc.Content[0].Content) != 1 || doc.Content[0].Attrs.ID == "") {
		err = fmt.Errorf("controlled fixture requires one paragraph/text leaf")
	}
	return doc, err
}
func humanBinaryProjection(doc humanBinaryDocument) []map[string]string {
	return []map[string]string{{"id": doc.Content[0].Attrs.ID, "hash": humanBinaryHash(doc.Content[0].Content[0].Text)}}
}
func humanBinaryHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func humanBinaryOutline(doc humanBinaryDocument) []map[string]string {
	outline := humanBinaryProjection(doc)
	outline[0]["text"] = doc.Content[0].Content[0].Text
	return outline
}
