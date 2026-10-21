//go:build linux && (docs_foundation || conformance)

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDuplicatePersonalDocumentRealNodeBinaryLostACK(t *testing.T) {
	if os.Getenv("CREWSHIP_DOCUMENT_CREATE") != "1" {
		t.Fatal("requires explicit owned Node24/frozen Docs opt-in")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if v, e := exec.Command("node", "--version").Output(); e != nil || !strings.HasPrefix(string(v), "v24.") {
		t.Fatalf("Node24 required: %s %v", v, e)
	}
	h, workspace, user, saved := documentResultsFixture(t)
	mustExec(t, h.db, `UPDATE workspaces SET slug='duplicate-private-cli' WHERE id=?`, workspace)
	mustExec(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, workspace, user)
	// A real Yjs snapshot plus a later persisted update. Copying SQL snapshot
	// alone would lose the latest marker; real Node materialization must apply it.
	materialize := exec.Command("node", "--input-type=module", "-e", `import * as Y from 'yjs';import {create,snapshot,patch,outline} from './schema.mjs';const d=create({type:'doc',content:[{type:'paragraph',attrs:{id:'target'},content:[{type:'text',text:'Earlier snapshot'}]},{type:'paragraph',attrs:{id:'link'},content:[{type:'text',text:'Keep link',marks:[{type:'link',attrs:{href:'https://example.invalid/'}}]}]}]},3);const before=snapshot(d),v=Y.encodeStateVector(d);patch(d,{op:'replaceText',id:'target',text:'Latest persisted update marker'},3);console.log(JSON.stringify({snapshot:before,update:Buffer.from(Y.encodeStateAsUpdate(d,v)).toString('base64'),blocks:(await outline(d,3)).map(({id,hash})=>({id,hash}))}));d.destroy();`)
	materialize.Dir = filepath.Join(root, "tools/docs-service")
	raw, err := materialize.CombinedOutput()
	if err != nil {
		t.Fatalf("real source materialization: %v %s", err, raw)
	}
	var source struct {
		Snapshot, Update []byte
		Blocks           json.RawMessage
	}
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	mustExec(t, h.db, `UPDATE documents SET snapshot=?,snapshot_rev=0,head_rev=1,projection=? WHERE page_id=?`, source.Snapshot, []byte(source.Blocks), saved.PageID)
	mustExec(t, h.db, `INSERT INTO document_updates(workspace_id,page_id,rev,payload) VALUES(?,?,1,?)`, workspace, saved.PageID, source.Update)
	const token = "crewship_cli_actual_document_duplicate"
	mustExec(t, h.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('actual-copy-token',?,'actual copy',?,'STANDARD','["pages:read","pages:write"]')`, user, hashStandard(token))
	dir, err := os.MkdirTemp("", "copy-live-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	secretFile := filepath.Join(dir, "secret")
	const secret = "synthetic-owned-copy-service-secret-at-least-32"
	if err = os.WriteFile(secretFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	authority, nodeSocket := filepath.Join(dir, "a.sock"), filepath.Join(dir, "n.sock")
	router, err := NewRouter(h.db, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithDocumentAuthority(authority, secretFile), WithDocumentService(nodeSocket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	node := exec.Command("node", filepath.Join(root, "tools/docs-service/service.mjs"))
	node.Env = append(os.Environ(), "CREWSHIP_DOCS_SOCKET="+nodeSocket, "CREWSHIP_DOCS_AUTHORITY_SOCKET="+authority, "CREWSHIP_DOCS_SERVICE_TOKEN_FILE="+secretFile)
	node.Stderr = os.Stderr
	if err = node.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Process.Kill(); _ = node.Wait() })
	documentsWaitSocket(t, nodeSocket)
	documentsWaitReady(t, nodeSocket, secret)
	var dropped atomic.Bool
	var dropACK atomic.Bool
	dropACK.Store(true)
	var copyRequests atomic.Int64
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/duplicate") && r.Header.Get("Idempotency-Key") == "lost-copy-ack" && dropACK.Load() {
			committed := httptest.NewRecorder()
			router.ServeHTTP(committed, r)
			attempt := copyRequests.Add(1)
			want := 201
			if attempt > 1 {
				want = 200
			}
			if committed.Code != want {
				t.Errorf("fault must follow actual commit: %d %s", committed.Code, committed.Body)
			}
			dropped.Store(true)
			c, _, e := w.(http.Hijacker).Hijack()
			if e != nil {
				t.Error(e)
				return
			}
			_ = c.Close()
			return
		}
		router.ServeHTTP(w, r)
	}))
	t.Cleanup(host.Close)
	binary := filepath.Join(dir, "crewship")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/crewship")
	build.Dir = root
	if raw, e := build.CombinedOutput(); e != nil {
		t.Fatalf("actual CLI build: %v %s", e, raw)
	}
	cfg := filepath.Join(dir, "cli.yaml")
	if err = os.WriteFile(cfg, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: %s\nformat: json\n", host.URL, token, "duplicate-private-cli")), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(key string, extra ...string) ([]byte, error) {
		args := append([]string{"page", "duplicate", saved.PageID, "--mutation-id", key}, extra...)
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Dir = dir
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "CREWSHIP_") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		return cmd.CombinedOutput()
	}
	if raw, e := run("lost-copy-ack"); e == nil || !dropped.Load() {
		t.Fatalf("lost HTTP ACK must be observed after actual commit: %v %s", e, raw)
	}
	dropACK.Store(false)
	raw, err = run("lost-copy-ack")
	if err != nil {
		t.Fatalf("actual CLI exact retry: %v %s", err, raw)
	}
	var copy struct {
		Page struct{ ID, Slug string } `json:"page"`
	}
	if err = json.Unmarshal(raw, &copy); err != nil || copy.Page.ID == "" || copy.Page.ID == saved.PageID {
		t.Fatalf("copy identity %v %s", err, raw)
	}
	if raw, e := run("lost-copy-ack", "--pages-version", "999"); e == nil || !strings.Contains(string(raw), "409") {
		t.Fatalf("changed copy packet: %v %s", e, raw)
	}
	read := httptest.NewRequest("GET", "/api/v1/pages/@"+copy.Page.ID+"/document?workspace_id="+workspace, nil)
	read.Header.Set("Authorization", "Bearer "+token)
	result := httptest.NewRecorder()
	router.ServeHTTP(result, read)
	creationExpect(t, result, 200)
	if !strings.Contains(result.Body.String(), "Latest persisted update marker") || !strings.Contains(result.Body.String(), "https://example.invalid/") {
		t.Fatalf("canonical tail/marks lost: %s", result.Body)
	}
	var count int
	if err = h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE id<>?`, saved.PageID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("lost ACK made duplicate: %d %v", count, err)
	}
	// Interpose only after the real Node preparation. The actual Router must
	// reject a changed source generation and a revoked credential at commit.
	realCall := router.pages.documentCall
	for _, change := range []string{"generation", "credential"} {
		router.pages.documentCall = func(ctx context.Context, ws, bearer, page, op string, body any) ([]byte, int, error) {
			raw, status, e := realCall(ctx, ws, bearer, page, op, body)
			if op == "prepare-copy" && e == nil && status == 200 {
				if change == "generation" {
					mustExec(t, h.db, `UPDATE documents SET generation=generation+1 WHERE page_id=?`, saved.PageID)
				} else {
					mustExec(t, h.db, `UPDATE cli_tokens SET revoked_at=datetime('now') WHERE id='actual-copy-token'`)
				}
			}
			return raw, status, e
		}
		want := "409"
		if change == "credential" {
			want = "401"
		}
		if raw, e := run("late-" + change); e == nil || !strings.Contains(string(raw), want) {
			t.Fatalf("real preparation %s race: %v %s", change, e, raw)
		}
		if err = h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE id<>?`, saved.PageID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("race created partial copy: %d %v", count, err)
		}
	}
}
