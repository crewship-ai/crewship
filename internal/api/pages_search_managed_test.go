//go:build linux && (docs_foundation || conformance)

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/pagesearch"
)

func TestNativeSearchManagedProjection(t *testing.T) {
	if os.Getenv("CREWSHIP_NATIVE_SEARCH_MANAGED") != "1" {
		t.Fatal("requires owned frozen Docs service and explicit native-search opt-in")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "tools/docs-service/node_modules/yjs")); err != nil {
		t.Fatal("requires frozen own Docs installation", err)
	}
	h, workspace, owner := pagesAccessFixture(t)
	const pageID = "search-managed-page"
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,'search-managed','Search managed',?,'{}')`, pageID, workspace, owner)
	dir, err := os.MkdirTemp("", "search-managed-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	authoritySocket, nodeSocket := filepath.Join(dir, "a.sock"), filepath.Join(dir, "n.sock")
	secretFile := filepath.Join(dir, "service-secret")
	const serviceSecret = "synthetic-search-managed-service-secret-only"
	if err = os.WriteFile(secretFile, []byte(serviceSecret), 0600); err != nil {
		t.Fatal(err)
	}
	const jwtSecret = "synthetic-search-managed-jwt-secret-long-enough"
	router, err := NewRouter(h.db, jwtSecret, newTestLogger(), WithInternalToken("synthetic-search-managed-internal"), WithDocumentAuthority(authoritySocket, secretFile), WithDocumentService(nodeSocket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	child := exec.Command("node", filepath.Join(root, "tools/docs-service/service.mjs"))
	child.Env = append(os.Environ(), "CREWSHIP_DOCS_SOCKET="+nodeSocket, "CREWSHIP_DOCS_AUTHORITY_SOCKET="+authoritySocket, "CREWSHIP_DOCS_SERVICE_TOKEN_FILE="+secretFile)
	child.Stderr = os.Stderr
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	documentsWaitSocket(t, nodeSocket)
	documentsWaitReady(t, nodeSocket, serviceSecret)
	session, err := sessions.NewDBStore(h.db).Create(t.Context(), owner, "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := auth.NewJWTValidator(jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken(owner, session.ID, "Synthetic search viewer", "search@example.test")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/fixture-materialize", router.authMw.RequireAuth(router.authMw.RequireWorkspace(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		err := router.pages.ensureNativeSearchProjection(ctx, r, pageID)
		switch {
		case err == nil:
			w.WriteHeader(204)
		case errors.Is(err, errNativeSearchAuthentication):
			replyError(w, 401, "current credential required")
		case errors.Is(err, pagesearch.ErrSourceChanged):
			replyError(w, 409, "source changed")
		default:
			replyError(w, 503, "projection unavailable")
		}
	}))))
	mux.Handle("/", router)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 15 * time.Second}
	endpoint := "/api/v1/pages/search-managed/document"
	request := func(method, path string, body any) ([]byte, int, error) {
		var data []byte
		if body != nil {
			var e error
			data, e = json.Marshal(body)
			if e != nil {
				return nil, 0, e
			}
		}
		req, e := http.NewRequestWithContext(t.Context(), method, server.URL+path+func() string {
			if strings.Contains(path, "?") {
				return "&"
			}
			return "?"
		}()+"workspace_id="+workspace, bytes.NewReader(data))
		if e != nil {
			return nil, 0, e
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response, e := client.Do(req)
		if e != nil {
			return nil, 0, e
		}
		defer response.Body.Close()
		raw, e := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
		return raw, response.StatusCode, e
	}
	call := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		raw, status, e := request(method, path, body)
		if e != nil || status != want {
			t.Fatalf("%s %s status%d want%d error%v body%s", method, path, status, want, e, raw)
		}
		result := map[string]any{}
		if len(raw) > 0 {
			if e = json.Unmarshal(raw, &result); e != nil {
				t.Fatal(e)
			}
		}
		return result
	}
	body := map[string]any{"type": "doc", "content": []any{
		map[string]any{"type": "bulletList", "attrs": map[string]any{"id": "list"}, "content": []any{map[string]any{"type": "listItem", "attrs": map[string]any{"id": "item"}, "content": []any{map[string]any{"type": "paragraph", "attrs": map[string]any{"id": "leaf"}, "content": []any{map[string]any{"type": "text", "text": "initial needle"}}}}}}},
		map[string]any{"type": "heading", "attrs": map[string]any{"id": "heading", "level": 1}, "content": []any{map[string]any{"type": "text", "text": "second term"}}},
	}}
	call("POST", endpoint+"/initialize", map[string]any{"id": "search-init", "schema": 2, "body": body}, 200)
	call("POST", "/fixture-materialize", nil, 204)
	var encoded []byte
	var encodedHash string
	if err = h.db.QueryRow(`SELECT encoded,encoded_sha256 FROM page_search_projections WHERE page_id=?`, pageID).Scan(&encoded, &encodedHash); err != nil {
		t.Fatal(err)
	}
	projection, err := pagesearch.Decode(encoded, encodedHash)
	if err != nil || projection.Source.Revision != 0 || len(projection.Segments) != 2 {
		t.Fatalf("actual initial leaf projection %+v %v", projection, err)
	}
	query, _ := pagesearch.ParseQuery("needle second")
	if _, matched, err := query.Match(projection); err != nil || !matched {
		t.Fatalf("actual managed leaf text missing: %v", err)
	}
	state := call("GET", endpoint, nil, 200)
	call("POST", endpoint+"/checkpoint", map[string]any{"id": "search-before-human", "schema": 2, "epoch": state["epoch"], "ifRev": state["rev"]}, 200)
	execOrFatal(t, h.db, `DELETE FROM page_search_projections WHERE page_id=?`, pageID)
	original := router.pages.documentCall
	var mode atomic.Int32
	mode.Store(1)
	injectionErrors := make(chan error, 2)
	router.pages.documentCall = func(ctx context.Context, ws, bearer, page, operation string, payload any) ([]byte, int, error) {
		raw, status, e := original(ctx, ws, bearer, page, operation, payload)
		if e != nil || status != 200 || operation != "read" {
			return raw, status, e
		}
		if mode.CompareAndSwap(1, 0) {
			update, e := nativeSearchHumanUpdate(ctx, root, raw)
			if e != nil {
				injectionErrors <- e
				return nil, 500, e
			}
			var observed map[string]any
			if e = json.Unmarshal(raw, &observed); e != nil {
				return nil, 500, e
			}
			patch := map[string]any{"id": "search-human-after-read", "schema": 2, "kind": "human", "epoch": observed["epoch"], "ifRev": observed["rev"], "update": update}
			_, status, e = request("POST", endpoint+"/patch", patch)
			if e != nil || status != 200 {
				e = fmt.Errorf("actual human patch status%d error%v", status, e)
				injectionErrors <- e
				return nil, 500, e
			}
		} else if mode.CompareAndSwap(3, 0) {
			_, e = h.db.ExecContext(ctx, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?, ?, ?, ?, ?, '{}')`, "search-arriving-page", workspace, "search-arriving", "Newly visible native Doc", owner)
			if e == nil {
				_, status, e = request("POST", "/api/v1/pages/search-arriving/document/initialize", map[string]any{"id": "arriving-init", "schema": 2, "body": body})
			}
			if e != nil || status != 200 {
				e = fmt.Errorf("initialize newly visible Doc status%d error%v", status, e)
				injectionErrors <- e
				return nil, 500, e
			}
		} else if mode.CompareAndSwap(2, 0) {
			if e = sessions.NewDBStore(h.db).Revoke(ctx, session.ID, "synthetic materialization revocation"); e != nil {
				injectionErrors <- e
				return nil, 500, e
			}
		}
		return raw, 200, nil
	}
	call("POST", "/fixture-materialize", nil, 409)
	select {
	case e := <-injectionErrors:
		t.Fatal(e)
	default:
	}
	var rows, head int
	if err = h.db.QueryRow(`SELECT count(*) FROM page_search_projections WHERE page_id=?`, pageID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("stale projection persisted: %d %v", rows, err)
	}
	if err = h.db.QueryRow(`SELECT head_rev FROM documents WHERE page_id=?`, pageID).Scan(&head); err != nil || head != 1 {
		t.Fatalf("real human commit missing: %d %v", head, err)
	}
	var kind string
	if err = h.db.QueryRow(`SELECT kind FROM document_receipts WHERE page_id=? AND id='search-human-after-read'`, pageID).Scan(&kind); err != nil || kind != "human" {
		t.Fatalf("not actual human save: %s %v", kind, err)
	}
	call("POST", "/fixture-materialize", nil, 204)
	state = call("GET", endpoint, nil, 200)
	call("POST", endpoint+"/restore", map[string]any{"id": "search-restore", "checkpoint": "search-before-human", "schema": 2, "epoch": state["epoch"], "ifRev": state["rev"]}, 200)
	call("POST", "/fixture-materialize", nil, 204)
	if err = h.db.QueryRow(`SELECT encoded,encoded_sha256 FROM page_search_projections WHERE page_id=?`, pageID).Scan(&encoded, &encodedHash); err != nil {
		t.Fatal(err)
	}
	projection, err = pagesearch.Decode(encoded, encodedHash)
	if err != nil || projection.Source.Revision != 2 || projection.Source.Generation != 1 || projection.Segments[0].Text != "initial needle" {
		t.Fatalf("restore did not rebuild canonical text: %+v %v", projection, err)
	}
	// A native Page created after the initial candidate snapshot cannot be
	// silently omitted from the final result. The complete retry rebuilds both.
	execOrFatal(t, h.db, `DELETE FROM page_search_projections WHERE page_id=?`, pageID)
	mode.Store(3)
	rawSearch, searchStatus, searchErr := request("GET", "/api/v1/pages/search/content?q=needle", nil)
	if searchErr != nil || searchStatus != 503 || bytes.Contains(rawSearch, []byte(`"matches"`)) {
		t.Fatalf("new final-snapshot candidate must refuse partial response: %d %v %s", searchStatus, searchErr, rawSearch)
	}
	select {
	case e := <-injectionErrors:
		t.Fatal(e)
	default:
	}
	search := call("GET", "/api/v1/pages/search/content?q=needle", nil, 200)
	if matches, ok := search["matches"].([]any); !ok || len(matches) != 2 {
		t.Fatalf("complete canonical retry omitted new candidate: %+v", search)
	}
	execOrFatal(t, h.db, `DELETE FROM page_search_projections WHERE page_id=?`, pageID)
	mode.Store(2)
	call("POST", "/fixture-materialize", nil, 401)
	select {
	case e := <-injectionErrors:
		t.Fatal(e)
	default:
	}
	if err = h.db.QueryRow(`SELECT count(*) FROM page_search_projections WHERE page_id=?`, pageID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("revoked materialization persisted: %d %v", rows, err)
	}
	if err = h.db.QueryRow(`SELECT count(*) FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspace, owner).Scan(&rows); err != nil || rows != 1 {
		t.Fatal("session race also changed membership")
	}
	t.Log("actual Router + managed Node24: initial rev0, nested leaf dedup, human raw CRDT save after read rejected by final CAS, fresh rebuild, checkpoint restore, exact session revocation zero publication")
}

func nativeSearchHumanUpdate(ctx context.Context, root string, raw []byte) (string, error) {
	script := `import * as Y from 'yjs'; import {load,decode,patch} from './schema.mjs'; let bytes=''; for await(const c of process.stdin) bytes+=c; const state=JSON.parse(bytes); const doc=load(state.snapshot); try {for(const update of state.updates||[]) Y.applyUpdate(doc,decode(typeof update==='string'?update:update.update)); const vector=Y.encodeStateVector(doc); patch(doc,{op:'replaceText',id:'leaf',text:'human replacement needle'}); process.stdout.write(Buffer.from(Y.encodeStateAsUpdate(doc,vector)).toString('base64'));}finally{doc.destroy();}`
	command := exec.CommandContext(ctx, "node", "--input-type=module", "-e", script)
	command.Dir = filepath.Join(root, "tools/docs-service")
	command.Stdin = bytes.NewReader(raw)
	data, err := command.Output()
	if err != nil {
		return "", err
	}
	if len(data) > 1400000 || strings.TrimSpace(string(data)) == "" {
		return "", errors.New("invalid synthetic human CRDT update")
	}
	return string(data), nil
}
