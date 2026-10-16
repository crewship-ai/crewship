//go:build linux && (docs_foundation || conformance)

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
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
	"github.com/crewship-ai/crewship/internal/documents"
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
	oldEncoded := bytes.Replace(encoded, []byte(`"adapter_version":2`), []byte(`"adapter_version":1`), 1)
	oldDigest := sha256.Sum256(oldEncoded)
	execOrFatal(t, h.db, `UPDATE page_search_projections SET adapter_version=1,encoded=?,encoded_sha256=? WHERE page_id=?`, oldEncoded, hex.EncodeToString(oldDigest[:]), pageID)
	call("GET", "/api/v1/pages/search/content?q=needle", nil, 200)
	var cacheVersion int
	if err = h.db.QueryRow(`SELECT adapter_version FROM page_search_projections WHERE page_id=?`, pageID).Scan(&cacheVersion); err != nil || cacheVersion != pagesearch.AdapterVersion {
		t.Fatalf("old derived adapter not canonically rebuilt: %d %v", cacheVersion, err)
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
		} else if mode.CompareAndSwap(4, 0) {
			var observed map[string]any
			if e = json.Unmarshal(raw, &observed); e != nil {
				return nil, 500, e
			}
			_, status, e = request("POST", endpoint+"/upgrade", map[string]any{"id": "search-upgrade-after-read", "schema": 2, "target_schema": 3, "page_id": pageID, "epoch": observed["epoch"], "ifRev": observed["rev"]})
			if e != nil || status != 200 {
				e = fmt.Errorf("actual upgrade after read status%d error%v", status, e)
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
	// Legacy initialization is a trusted fixture made by the real version1
	// Yjs encoder/Store. All subsequent reads/materialization/query are actual
	// HTTP managed operations with the requesting human's current credential.
	nativeSearchSeedLegacyDocument(t, h, root, workspace, owner)
	const richPage = "search-rich-page"
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,'search-rich','Rich search',?,'{}')`, richPage, workspace, owner)
	richBody := map[string]any{"type": "doc", "content": []any{
		map[string]any{"type": "paragraph", "attrs": map[string]any{"id": "rich-inline"}, "content": []any{
			map[string]any{"type": "text", "text": "linked needle", "marks": []any{map[string]any{"type": "link", "attrs": map[string]any{"href": "https://example.test/secret-href-canary"}}}},
			map[string]any{"type": "text", "text": " inline-code", "marks": []any{map[string]any{"type": "code", "attrs": map[string]any{}}}},
		}},
		map[string]any{"type": "codeBlock", "attrs": map[string]any{"id": "rich-code"}, "content": []any{map[string]any{"type": "text", "text": "code-leaf second needle <literal>"}}},
	}}
	call("POST", "/api/v1/pages/search-rich/document/initialize", map[string]any{"id": "rich-search-init", "schema": 3, "page_id": richPage, "body": richBody}, 200)
	mixed := call("GET", "/api/v1/pages/search/content?q=needle", nil, 200)
	versions := map[int]bool{}
	for _, item := range mixed["matches"].([]any) {
		versions[int(item.(map[string]any)["schema_version"].(float64))] = true
	}
	if len(mixed["matches"].([]any)) != 3 || !versions[1] || !versions[2] || !versions[3] {
		t.Fatalf("mixed actual schema coverage: %+v", mixed)
	}
	codeMatch := call("GET", "/api/v1/pages/search/content?q=code-leaf%20second", nil, 200)
	if results := codeMatch["matches"].([]any); len(results) != 1 || results[0].(map[string]any)["page_id"] != richPage {
		t.Fatalf("rich code leaf missing: %+v", codeMatch)
	}
	noHref := call("GET", "/api/v1/pages/search/content?q=secret-href-canary", nil, 200)
	if len(noHref["matches"].([]any)) != 0 {
		t.Fatalf("link href entered index: %+v", noHref)
	}
	execOrFatal(t, h.db, `UPDATE documents SET schema_version=4 WHERE page_id=?`, richPage)
	unknownRaw, unknownStatus, unknownErr := request("GET", "/api/v1/pages/search/content?q=needle", nil)
	if unknownErr != nil || unknownStatus != 422 || bytes.Contains(unknownRaw, []byte(`"matches"`)) {
		t.Fatalf("unknown4 returned partial matches: %d %v %s", unknownStatus, unknownErr, unknownRaw)
	}
	execOrFatal(t, h.db, `UPDATE documents SET schema_version=3 WHERE page_id=?`, richPage)
	// A real human schema2→3 upgrade after managed read invalidates the old
	// source stamp just like a content commit; a source-schema change is not
	// silently relabeled current. Restore returns to its retained schema2.
	execOrFatal(t, h.db, `DELETE FROM page_search_projections WHERE page_id=?`, pageID)
	mode.Store(4)
	call("POST", "/fixture-materialize", nil, 409)
	if err = h.db.QueryRow(`SELECT count(*) FROM page_search_projections WHERE page_id=?`, pageID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("old schema published after upgrade: %d %v", rows, err)
	}
	state = call("GET", endpoint, nil, 200)
	if state["schema"] != float64(3) {
		t.Fatalf("actual schema upgrade missing: %+v", state)
	}
	call("POST", "/fixture-materialize", nil, 204)
	call("POST", endpoint+"/restore", map[string]any{"id": "search-rich-return", "schema": 3, "page_id": pageID, "checkpoint": "search-before-human", "epoch": state["epoch"], "ifRev": state["rev"]}, 200)
	call("POST", "/fixture-materialize", nil, 204)
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
	if matches, ok := search["matches"].([]any); !ok || len(matches) != 4 {
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
	t.Log("actual Router + managed Node24: initial rev0, nested leaf dedup, human raw CRDT save after read rejected by final CAS, fresh rebuild, checkpoint restore, mixed actual schemas1/2/3 and code leaf, href exclusion, old-cache rebuild, real schema upgrade CAS, unknown4 no partial response, exact session revocation zero publication")
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

func nativeSearchSeedLegacyDocument(t *testing.T, h *PageHandler, root, workspace, owner string) {
	t.Helper()
	const page = "search-legacy-page"
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,'search-legacy','Legacy search',?,'{}')`, page, workspace, owner)
	script := `import {create,snapshot,outline} from './tools/docs-service/schema.mjs';const doc=create({type:'doc',content:[{type:'paragraph',attrs:{id:'legacy-search-leaf'},content:[{type:'text',text:'legacy needle second'}]}]},1);try{console.log(JSON.stringify({snapshot:snapshot(doc),blocks:(await outline(doc,1)).map(({id,hash})=>({id,hash}))}));}finally{doc.destroy();}`
	command := exec.CommandContext(t.Context(), "node", "--input-type=module", "-e", script)
	command.Dir = root
	raw, err := command.Output()
	if err != nil {
		t.Fatalf("trusted legacy encoder fixture: %v", err)
	}
	var fixture struct {
		Snapshot string          `json:"snapshot"`
		Blocks   json.RawMessage `json:"blocks"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	snapshot, err := base64.StdEncoding.DecodeString(fixture.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	authorize := func(ctx context.Context, tx *sql.Tx) (string, error) {
		var current string
		err := tx.QueryRowContext(ctx, `SELECT owner_user_id FROM pages WHERE workspace_id=? AND id=?`, workspace, page).Scan(&current)
		return current, err
	}
	if _, err = documents.New(h.db, documents.Options{}).InitRequest(t.Context(), documents.Key{WorkspaceID: workspace, PageID: page}, "legacy-search-fixture", "synthetic-legacy-search-source", 1, snapshot, fixture.Blocks, authorize); err != nil {
		t.Fatal(err)
	}
}
