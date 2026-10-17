package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/journal"
)

func TestHumanDocumentEmptyPageCreation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"staged", `{"slug":"private-doc","name":"Secret document title","panels":[],"allow_empty":true}`, 201},
		{"strict default", `{"slug":"private-doc","name":"Secret document title","panels":[]}`, 400},
		{"crew owner", `{"slug":"private-doc","name":"Secret document title","panels":[],"allow_empty":true,"owner":"crew/lookout"}`, 400},
		{"panels", strings.TrimSuffix(pagesSpecBody("private-doc"), "}") + `,"allow_empty":true}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, spy, _, ws, user := newPagesFixture(t)
			w := httptest.NewRecorder()
			req := pagesRequest(t, "POST", "/api/v1/pages", ws, user, "OWNER", tc.body)
			req = req.WithContext(context.WithValue(req.Context(), ctxAuthKind, AuthKindSession))
			h.Create(w, req)
			if w.Code != tc.status {
				t.Fatalf("create=%d want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if tc.status != 201 {
				return
			}
			var page map[string]any
			if json.Unmarshal(w.Body.Bytes(), &page) != nil || page["owner"] != "user/"+user {
				t.Fatalf("human Page: %s", w.Body.String())
			}
			e := spy.firstOfType(journal.EntryPageSpecChanged)
			if e == nil {
				t.Fatal("missing creation audit")
			}
			raw, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "private-doc") || strings.Contains(string(raw), "Secret document title") || strings.Contains(string(raw), "fingerprint") {
				t.Fatalf("private metadata leaked: %s", raw)
			}
		})
	}
}

func TestHumanDocumentPageMetadata(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	if _, err := h.db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('human-doc-page',?,'private-doc','Private document',?,'{}')`, ws, user); err != nil {
		t.Fatal(err)
	}
	auth := func(context.Context, *sql.Tx) (string, error) { return "user:" + user, nil }
	if _, err := documents.New(h.db, documents.Options{}).Init(t.Context(), documents.Key{WorkspaceID: ws, PageID: "human-doc-page"}, 1, []byte("synthetic engine snapshot"), []byte(`[]`), auth); err != nil {
		t.Fatal(err)
	}
	for _, list := range []bool{false, true} {
		w := httptest.NewRecorder()
		r := pagesRequest(t, http.MethodGet, "/api/v1/pages/private-doc", ws, user, "OWNER", "")
		r.SetPathValue("slug", "private-doc")
		if list {
			h.List(w, r)
		} else {
			h.Get(w, r)
		}
		if w.Code != 200 {
			t.Fatalf("metadata=%d: %s", w.Code, w.Body.String())
		}
		var page map[string]any
		if list {
			var rows []map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 1 {
				t.Fatalf("list: %s %v", w.Body.String(), err)
			}
			page = rows[0]
		} else if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page["has_document"] != true {
			t.Fatalf("missing document metadata: %s", w.Body.String())
		}
	}
}

func humanDocumentTransportFixture(t *testing.T) (*PageHandler, string, string) {
	t.Helper()
	h, _, _, ws, user := newPagesFixture(t)
	if _, err := h.db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('human-doc-page',?,'private-doc','Private document',?,'{}')`, ws, user); err != nil {
		t.Fatal(err)
	}
	return h, ws, user
}

func humanDocumentRequest(t *testing.T, method, path, ws, user, body string) *http.Request {
	t.Helper()
	r := pagesRequest(t, method, path, ws, user, "OWNER", body)
	r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindSession))
	r.SetPathValue("slug", "private-doc")
	r.Header.Set("Authorization", "Bearer original-human-credential")
	return r
}

// These unit tests isolate the public transport boundary. The engine callback
// does not stand in for Go authority authorization or model validation.
func TestHumanDocumentPayloadObjectBoundary(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `"text"`, `123`, `{} {}`, strings.Repeat(" ", 2<<20) + `{}`} {
		t.Run(body[:min(len(body), 12)], func(t *testing.T) {
			h, ws, user := humanDocumentTransportFixture(t)
			calls := 0
			h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
				calls++
				return []byte(`{}`), 200, nil
			}
			w := httptest.NewRecorder()
			h.MutateDocument(w, humanDocumentRequest(t, "POST", "/api/v1/pages/private-doc/document/patch", ws, user, body))
			if w.Code != 400 || calls != 0 {
				t.Fatalf("invalid body reached engine: status=%d calls=%d response=%s", w.Code, calls, w.Body.String())
			}
		})
	}
}

func TestHumanDocumentEngineResponseObjectBoundary(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `"text"`, `not-json`} {
		t.Run(body, func(t *testing.T) {
			h, ws, user := humanDocumentTransportFixture(t)
			h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
				return []byte(body), 200, nil
			}
			w := httptest.NewRecorder()
			var panicValue any
			func() {
				defer func() { panicValue = recover() }()
				h.GetDocument(w, humanDocumentRequest(t, "GET", "/api/v1/pages/private-doc/document", ws, user, ""))
			}()
			if panicValue != nil || w.Code != 502 {
				t.Fatalf("invalid engine response: panic=%v status=%d body=%s", panicValue, w.Code, w.Body.String())
			}
		})
	}
}

func TestHumanDocumentTransportPreservesHumanRequest(t *testing.T) {
	for _, operation := range []string{"initialize", "patch", "read"} {
		t.Run(operation, func(t *testing.T) {
			h, ws, user := humanDocumentTransportFixture(t)
			method, path, body := "POST", "/api/v1/pages/private-doc/document/"+operation, `{"id":"exact-retry","schema":2,"epoch":"opaque","ifRev":3,"ifHash":"old-hash","target":"block","text":"changed"}`
			if operation == "initialize" {
				body = `{"id":"exact-initialize","schema":2,"body":{"type":"doc","content":[{"type":"paragraph","attrs":{"id":"block"}}]}}`
			}
			if operation == "read" {
				method, path, body = "GET", "/api/v1/pages/private-doc/document", ""
			}
			calls := 0
			h.documentCall = func(ctx context.Context, workspace, bearer, page, op string, payload any) ([]byte, int, error) {
				calls++
				if workspace != ws || bearer != "Bearer original-human-credential" || page != "human-doc-page" || op != operation {
					t.Fatalf("changed dispatch %q %q %q %q", workspace, bearer, page, op)
				}
				if operation != "read" {
					raw, err := json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					var got, want any
					if json.Unmarshal(raw, &got) != nil || json.Unmarshal([]byte(body), &want) != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("changed request: %s", raw)
					}
				}
				return []byte(`{"id":"exact-retry","rev":4,"epoch":"opaque","outline":[]}`), 200, nil
			}
			w := httptest.NewRecorder()
			h.serveDocument(w, humanDocumentRequest(t, method, path, ws, user, body), operation != "read")
			if w.Code != 200 || calls != 1 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("dispatch %d/%d %s", w.Code, calls, w.Body.String())
			}
			var got map[string]any
			if json.Unmarshal(w.Body.Bytes(), &got) != nil || got["epoch"] != "opaque" || got["rev"] != float64(4) {
				t.Fatalf("changed receipt/state: %s", w.Body.String())
			}
			if operation == "read" && (got["page_id"] != "human-doc-page" || got["can_write"] != true || got["can_comment"] != false || got["can_resolve"] != false) {
				t.Fatalf("read metadata: %s", w.Body.String())
			}
		})
	}
}

func TestHumanDocumentTransportPropagatesRefusalWithoutRetry(t *testing.T) {
	for _, status := range []int{403, 409, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			h, ws, user := humanDocumentTransportFixture(t)
			calls := 0
			refusal := []byte(`{"error":"confirmed content preserved","code":"document_conflict"}`)
			h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
				calls++
				return refusal, status, nil
			}
			w := httptest.NewRecorder()
			h.MutateDocument(w, humanDocumentRequest(t, "POST", "/api/v1/pages/private-doc/document/patch", ws, user, `{"id":"same-id"}`))
			if w.Code != status || calls != 1 || !bytes.Equal(w.Body.Bytes(), refusal) || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("refusal changed or retried: status=%d calls=%d %s", w.Code, calls, w.Body.String())
			}
		})
	}
}

func TestHumanDocumentTransportFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, path, kind      string
		disabled, unavailable bool
		status                int
	}{
		{name: "disabled", path: "/api/v1/pages/private-doc/document", kind: AuthKindSession, disabled: true, status: 503},
		{name: "engine unavailable", path: "/api/v1/pages/private-doc/document", kind: AuthKindSession, unavailable: true, status: 503},
		{name: "unknown realm", path: "/api/v1/pages/private-doc/document", kind: "future-agent", status: 403},
		{name: "selected block deferred", path: "/api/v1/pages/private-doc/document?block_id=secret", kind: AuthKindSession, status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, ws, user := humanDocumentTransportFixture(t)
			calls := 0
			if !tc.disabled {
				h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
					calls++
					if tc.unavailable {
						return nil, 503, io.ErrUnexpectedEOF
					}
					return []byte(`{}`), 200, nil
				}
			}
			r := humanDocumentRequest(t, "GET", tc.path, ws, user, "")
			r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, tc.kind))
			w := httptest.NewRecorder()
			h.GetDocument(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if !tc.unavailable && calls != 0 {
				t.Fatal("rejected request reached engine")
			}
		})
	}
}

type humanDocumentRoundTrip func(*http.Request) (*http.Response, error)

func (f humanDocumentRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHumanDocumentServiceTransportCredentialsAndResponseBound(t *testing.T) {
	for _, size := range []int{32, 4 << 20, (4 << 20) + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			r := &Router{documentsServiceSecret: "private-service-secret"}
			calls := 0
			r.documentsClient = &http.Client{Transport: humanDocumentRoundTrip(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "POST" || req.URL.Path != "/v1/documents/page-id/patch" || req.Header.Get("Authorization") != "Bearer original-human" || req.Header.Get("X-Workspace-ID") != "workspace-id" || req.Header.Get("X-Crewship-Docs-Service") != "private-service-secret" {
					t.Fatalf("service dispatch: %s %s %#v", req.Method, req.URL.Path, req.Header)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", size)))}, nil
			})}
			raw, status, err := r.documentServiceCall(t.Context(), "workspace-id", "Bearer original-human", "page-id", "patch", map[string]any{"id": "same-id", "ifRev": 3})
			if calls != 1 {
				t.Fatal("transport retried")
			}
			if size > 4<<20 {
				if err == nil || status != 502 || raw != nil {
					t.Fatalf("oversize response: %d %v", status, err)
				}
			} else if err != nil || status != 200 || len(raw) != size {
				t.Fatalf("bounded response: %d %d %v", status, len(raw), err)
			}
		})
	}
}

func TestHumanDocumentMetadataEditPreservesContentType(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	spec := `{"apiVersion":"crewship/v1","kind":"Page","metadata":{"name":"Private document","slug":"private-doc"},"spec":{"panels":[]}}`
	if _, err := h.db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('human-doc-page',?,'private-doc','Private document',?,?)`, ws, user, spec); err != nil {
		t.Fatal(err)
	}
	auth := func(context.Context, *sql.Tx) (string, error) { return "user:" + user, nil }
	if _, err := documents.New(h.db, documents.Options{}).Init(t.Context(), documents.Key{WorkspaceID: ws, PageID: "human-doc-page"}, 1, []byte("synthetic engine snapshot"), []byte(`[]`), auth); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"rename", `{"name":"Renamed private document"}`, 200},
		{"avatar", `{"color":"blue"}`, 200},
		{"empty panel replacement", `{"panels":[]}`, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.Update(w, humanDocumentRequest(t, "PATCH", "/api/v1/pages/private-doc", ws, user, tc.body))
			if w.Code != tc.status {
				t.Fatalf("metadata update=%d want %d %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
	var snapshot []byte
	if err := h.db.QueryRow(`SELECT snapshot FROM documents WHERE page_id='human-doc-page'`).Scan(&snapshot); err != nil || string(snapshot) != "synthetic engine snapshot" {
		t.Fatalf("content changed: %q %v", snapshot, err)
	}
}
