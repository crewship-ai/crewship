package kinds

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// ---------------------------------------------------------------------------
// Test scaffolding
//
// Every helper in this file is `ff`-prefixed because multiple
// parallel agents are dropping `_test.go` files into this package and
// the unprefixed names (recordedCall, fakeClient, writeJSON, jsonResp)
// otherwise collide at compile time. Keep new helpers prefixed.
// ---------------------------------------------------------------------------

// ffRecordedCall captures one HTTP request the kind made through the
// internalapi.Client. Tests assert on this slice instead of touching
// the httptest.Server response writers directly.
type ffRecordedCall struct {
	Method string
	Path   string
	Body   map[string]any
}

// ffHTTPClient adapts an *httptest.Server URL into the
// internalapi.Client interface so the kind code under test exercises
// the real JSON-over-HTTP path (encoding, status decoding, error
// handling) instead of a hand-rolled mock.
type ffHTTPClient struct {
	t       *testing.T
	baseURL string
	wsID    string
	calls   *[]ffRecordedCall
}

func ffNewHTTPClient(t *testing.T, ts *httptest.Server) (*ffHTTPClient, *[]ffRecordedCall) {
	t.Helper()
	calls := []ffRecordedCall{}
	return &ffHTTPClient{
		t:       t,
		baseURL: ts.URL,
		wsID:    "ws_test",
		calls:   &calls,
	}, &calls
}

func (c *ffHTTPClient) WorkspaceID() string { return c.wsID }

func (c *ffHTTPClient) do(ctx context.Context, method, path string, body any) (*internalapi.Response, error) {
	var rdr io.Reader
	var captured map[string]any
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(data)
		_ = json.Unmarshal(data, &captured)
	}
	*c.calls = append(*c.calls, ffRecordedCall{Method: method, Path: path, Body: captured})

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &internalapi.Response{
		StatusCode: resp.StatusCode,
		Body:       bytes.NewReader(buf),
	}, nil
}

func (c *ffHTTPClient) Get(ctx context.Context, path string) (*internalapi.Response, error) {
	return c.do(ctx, "GET", path, nil)
}
func (c *ffHTTPClient) Post(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	return c.do(ctx, "POST", path, body)
}
func (c *ffHTTPClient) Patch(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	return c.do(ctx, "PATCH", path, body)
}
func (c *ffHTTPClient) Put(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	return c.do(ctx, "PUT", path, body)
}
func (c *ffHTTPClient) Delete(ctx context.Context, path string) (*internalapi.Response, error) {
	return c.do(ctx, "DELETE", path, nil)
}

// ffFakeServer stands in for the backend handler being built in
// parallel. It serves all six feature-flag endpoints from the spec.
type ffFakeServer struct {
	flags map[string]*FeatureFlagRemote // key -> row
}

func ffNewFakeServer() *ffFakeServer {
	return &ffFakeServer{flags: map[string]*FeatureFlagRemote{}}
}

func (s *ffFakeServer) handler() http.Handler {
	mux := http.NewServeMux()

	// GET list
	mux.HandleFunc("GET /api/v1/feature-flags", func(w http.ResponseWriter, _ *http.Request) {
		out := make([]FeatureFlagRemote, 0, len(s.flags))
		for _, f := range s.flags {
			out = append(out, *f)
		}
		ffWriteJSON(w, http.StatusOK, out)
	})

	// POST create
	mux.HandleFunc("POST /api/v1/feature-flags", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Key               string `json:"key"`
			Description       string `json:"description"`
			DefaultEnabled    bool   `json:"default_enabled"`
			DefaultPercentage int    `json:"default_percentage"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if _, exists := s.flags[body.Key]; exists {
			http.Error(w, "exists", http.StatusConflict)
			return
		}
		s.flags[body.Key] = &FeatureFlagRemote{
			Key:               body.Key,
			Description:       body.Description,
			DefaultEnabled:    body.DefaultEnabled,
			DefaultPercentage: body.DefaultPercentage,
		}
		ffWriteJSON(w, http.StatusCreated, s.flags[body.Key])
	})

	// PATCH update
	mux.HandleFunc("PATCH /api/v1/feature-flags/{key}", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		flag, ok := s.flags[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		var patch map[string]any
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if v, ok := patch["description"].(string); ok {
			flag.Description = v
		}
		if v, ok := patch["default_enabled"].(bool); ok {
			flag.DefaultEnabled = v
		}
		ffWriteJSON(w, http.StatusOK, flag)
	})

	// DELETE flag definition
	mux.HandleFunc("DELETE /api/v1/feature-flags/{key}", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		if _, ok := s.flags[key]; !ok {
			http.NotFound(w, r)
			return
		}
		delete(s.flags, key)
		w.WriteHeader(http.StatusNoContent)
	})

	// PUT workspace override
	mux.HandleFunc("PUT /api/v1/feature-flags/{key}/override", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		flag, ok := s.flags[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		v := body.Enabled
		flag.WorkspaceOverride = &v
		ffWriteJSON(w, http.StatusOK, flag)
	})

	// DELETE workspace override
	mux.HandleFunc("DELETE /api/v1/feature-flags/{key}/override", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		flag, ok := s.flags[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		flag.WorkspaceOverride = nil
		w.WriteHeader(http.StatusNoContent)
	})

	return mux
}

func ffWriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ffBoolPtr(b bool) *bool { return &b }

// ---------------------------------------------------------------------------
// 1. Validate — happy path
// ---------------------------------------------------------------------------

func TestFeatureFlag_Validate_HappyPath(t *testing.T) {
	doc := &FeatureFlagDocument{
		APIVersion: "crewship/v1",
		Kind:       "FeatureFlag",
		Metadata:   internalapi.Metadata{Name: "exp", Slug: "exp"},
		Spec: FeatureFlagSpec{
			Description:       "Experimental thing",
			DefaultEnabled:    false,
			DefaultPercentage: 25,
		},
	}
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 2. Validate — error paths (slug missing, percentage out of range)
// ---------------------------------------------------------------------------

func TestFeatureFlag_Validate_Errors(t *testing.T) {
	cases := []struct {
		name    string
		spec    FeatureFlagSpec
		meta    internalapi.Metadata
		wantSub string
	}{
		{
			name:    "slug-missing",
			meta:    internalapi.Metadata{Name: "exp"},
			spec:    FeatureFlagSpec{DefaultPercentage: 0},
			wantSub: "metadata.slug is required",
		},
		{
			name:    "percentage-negative",
			meta:    internalapi.Metadata{Name: "exp", Slug: "exp"},
			spec:    FeatureFlagSpec{DefaultPercentage: -1},
			wantSub: "default_percentage must be in [0,100]",
		},
		{
			name:    "percentage-over-100",
			meta:    internalapi.Metadata{Name: "exp", Slug: "exp"},
			spec:    FeatureFlagSpec{DefaultPercentage: 101},
			wantSub: "default_percentage must be in [0,100]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := &FeatureFlagDocument{Metadata: tc.meta, Spec: tc.spec}
			err := doc.Validate(internalapi.WorkspaceContext{})
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 3. Plan: flag does not exist remotely → 1 PlanItem (POST create)
// ---------------------------------------------------------------------------

func TestFeatureFlag_Plan_CreateOnly(t *testing.T) {
	srv := ffNewFakeServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()
	client, calls := ffNewHTTPClient(t, ts)

	doc := &FeatureFlagDocument{
		Metadata: internalapi.Metadata{Slug: "new-flag", Name: "new-flag"},
		Spec: FeatureFlagSpec{
			Description:       "A new flag",
			DefaultEnabled:    true,
			DefaultPercentage: 0,
		},
	}
	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 PlanItem, got %d (%v)", len(items), ffDescribeItems(items))
	}
	if items[0].Action != internalapi.ActionCreate {
		t.Errorf("want ActionCreate, got %v", items[0].Action)
	}
	// Exec the item; confirm the server saw the POST with the right body.
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	ffMustHaveCall(t, *calls, "POST", "/api/v1/feature-flags")
	if got := srv.flags["new-flag"]; got == nil {
		t.Fatal("server did not store new flag")
	} else if !got.DefaultEnabled || got.Description != "A new flag" {
		t.Errorf("server flag mismatch: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// 4. Plan: flag exists but description drifted → PATCH update
// ---------------------------------------------------------------------------

func TestFeatureFlag_Plan_UpdateDefinition(t *testing.T) {
	srv := ffNewFakeServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()
	client, calls := ffNewHTTPClient(t, ts)

	srv.flags["exp"] = &FeatureFlagRemote{
		Key:               "exp",
		Description:       "old description",
		DefaultEnabled:    false,
		DefaultPercentage: 0,
	}
	remote := *srv.flags["exp"]
	doc := &FeatureFlagDocument{
		Metadata: internalapi.Metadata{Slug: "exp", Name: "exp"},
		Spec: FeatureFlagSpec{
			Description:       "new description",
			DefaultEnabled:    true,
			DefaultPercentage: 0,
		},
	}
	items, err := doc.Plan(context.Background(), client, &remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 PlanItem, got %d (%v)", len(items), ffDescribeItems(items))
	}
	if items[0].Action != internalapi.ActionUpdate {
		t.Errorf("want ActionUpdate, got %v", items[0].Action)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	ffMustHaveCall(t, *calls, "PATCH", "/api/v1/feature-flags/exp")
	got := srv.flags["exp"]
	if got.Description != "new description" || !got.DefaultEnabled {
		t.Errorf("server did not apply patch: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// 5. Plan: identical remote → no items
// ---------------------------------------------------------------------------

func TestFeatureFlag_Plan_Unchanged(t *testing.T) {
	srv := ffNewFakeServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()
	client, _ := ffNewHTTPClient(t, ts)

	remote := FeatureFlagRemote{
		Key:               "exp",
		Description:       "same",
		DefaultEnabled:    true,
		DefaultPercentage: 0,
	}
	doc := &FeatureFlagDocument{
		Metadata: internalapi.Metadata{Slug: "exp", Name: "exp"},
		Spec: FeatureFlagSpec{
			Description:       "same",
			DefaultEnabled:    true,
			DefaultPercentage: 0,
		},
	}
	items, err := doc.Plan(context.Background(), client, &remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("want 0 PlanItems, got %d (%v)", len(items), ffDescribeItems(items))
	}
}

// ---------------------------------------------------------------------------
// 6. Plan: flag-and-override both new → 2 PlanItems (POST create + PUT override)
// ---------------------------------------------------------------------------

func TestFeatureFlag_Plan_CreateFlagAndOverride(t *testing.T) {
	srv := ffNewFakeServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()
	client, calls := ffNewHTTPClient(t, ts)

	doc := &FeatureFlagDocument{
		Metadata: internalapi.Metadata{Slug: "new-flag", Name: "new-flag"},
		Spec: FeatureFlagSpec{
			Description:       "new",
			DefaultEnabled:    false,
			DefaultPercentage: 0,
			WorkspaceOverride: ffBoolPtr(true),
		},
	}
	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 PlanItems, got %d (%v)", len(items), ffDescribeItems(items))
	}
	if items[0].Action != internalapi.ActionCreate {
		t.Errorf("item[0] want ActionCreate, got %v", items[0].Action)
	}
	if items[1].Action != internalapi.ActionUpdate {
		t.Errorf("item[1] want ActionUpdate (override PUT), got %v", items[1].Action)
	}
	// Exec both in declaration order; the second needs the first to
	// have created the row.
	for i, it := range items {
		if err := it.Exec(context.Background(), client); err != nil {
			t.Fatalf("Exec[%d]: %v", i, err)
		}
	}
	ffMustHaveCall(t, *calls, "POST", "/api/v1/feature-flags")
	ffMustHaveCall(t, *calls, "PUT", "/api/v1/feature-flags/new-flag/override")
	if got := srv.flags["new-flag"]; got == nil || got.WorkspaceOverride == nil || !*got.WorkspaceOverride {
		t.Errorf("override not stored: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// 7. Plan: only-override-changed → 1 PlanItem (PUT override)
// ---------------------------------------------------------------------------

func TestFeatureFlag_Plan_OverrideChangedOnly(t *testing.T) {
	srv := ffNewFakeServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()
	client, calls := ffNewHTTPClient(t, ts)

	// Definition is identical; override flips false → true.
	srv.flags["exp"] = &FeatureFlagRemote{
		Key:               "exp",
		Description:       "stable",
		DefaultEnabled:    false,
		DefaultPercentage: 0,
		WorkspaceOverride: ffBoolPtr(false),
	}
	remote := *srv.flags["exp"]
	doc := &FeatureFlagDocument{
		Metadata: internalapi.Metadata{Slug: "exp", Name: "exp"},
		Spec: FeatureFlagSpec{
			Description:       "stable",
			DefaultEnabled:    false,
			DefaultPercentage: 0,
			WorkspaceOverride: ffBoolPtr(true),
		},
	}
	items, err := doc.Plan(context.Background(), client, &remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 PlanItem, got %d (%v)", len(items), ffDescribeItems(items))
	}
	if items[0].Action != internalapi.ActionUpdate {
		t.Errorf("want ActionUpdate, got %v", items[0].Action)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	ffMustHaveCall(t, *calls, "PUT", "/api/v1/feature-flags/exp/override")
	if got := srv.flags["exp"]; got.WorkspaceOverride == nil || !*got.WorkspaceOverride {
		t.Errorf("override not flipped: %+v", got.WorkspaceOverride)
	}
}

// ---------------------------------------------------------------------------
// 8. Plan: override-removed → 1 PlanItem (DELETE override)
// ---------------------------------------------------------------------------

func TestFeatureFlag_Plan_OverrideRemoved(t *testing.T) {
	srv := ffNewFakeServer()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()
	client, calls := ffNewHTTPClient(t, ts)

	// Definition stable; manifest omits workspace_override but server
	// still has one — Plan must DELETE the override.
	srv.flags["exp"] = &FeatureFlagRemote{
		Key:               "exp",
		Description:       "stable",
		DefaultEnabled:    false,
		DefaultPercentage: 0,
		WorkspaceOverride: ffBoolPtr(true),
	}
	remote := *srv.flags["exp"]
	doc := &FeatureFlagDocument{
		Metadata: internalapi.Metadata{Slug: "exp", Name: "exp"},
		Spec: FeatureFlagSpec{
			Description:       "stable",
			DefaultEnabled:    false,
			DefaultPercentage: 0,
			// WorkspaceOverride intentionally nil
		},
	}
	items, err := doc.Plan(context.Background(), client, &remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 PlanItem, got %d (%v)", len(items), ffDescribeItems(items))
	}
	if items[0].Action != internalapi.ActionDelete {
		t.Errorf("want ActionDelete, got %v", items[0].Action)
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	ffMustHaveCall(t, *calls, "DELETE", "/api/v1/feature-flags/exp/override")
	if got := srv.flags["exp"]; got.WorkspaceOverride != nil {
		t.Errorf("override still present after delete: %+v", got.WorkspaceOverride)
	}
}

// ---------------------------------------------------------------------------
// Export round-trip
// ---------------------------------------------------------------------------

func TestFeatureFlag_Export_RoundTrip(t *testing.T) {
	srv := ffNewFakeServer()
	srv.flags["a"] = &FeatureFlagRemote{
		Key: "a", Description: "alpha", DefaultEnabled: true, DefaultPercentage: 50,
	}
	srv.flags["b"] = &FeatureFlagRemote{
		Key: "b", Description: "beta", DefaultEnabled: false, DefaultPercentage: 0,
		WorkspaceOverride: ffBoolPtr(true),
	}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()
	client, _ := ffNewHTTPClient(t, ts)

	docs, err := ExportFeatureFlags(context.Background(), client)
	if err != nil {
		t.Fatalf("ExportFeatureFlags: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("want 2 docs, got %d", len(docs))
	}
	byKey := map[string]*FeatureFlagDocument{}
	for _, d := range docs {
		byKey[d.Metadata.Slug] = d
	}
	if a := byKey["a"]; a == nil {
		t.Fatal("missing doc for flag a")
	} else {
		if a.Spec.Description != "alpha" || !a.Spec.DefaultEnabled || a.Spec.DefaultPercentage != 50 {
			t.Errorf("flag a wrong: %+v", a.Spec)
		}
		if a.Spec.WorkspaceOverride != nil {
			t.Errorf("flag a should have no override, got %v", *a.Spec.WorkspaceOverride)
		}
	}
	if b := byKey["b"]; b == nil {
		t.Fatal("missing doc for flag b")
	} else if b.Spec.WorkspaceOverride == nil || !*b.Spec.WorkspaceOverride {
		t.Errorf("flag b override missing or wrong: %v", b.Spec.WorkspaceOverride)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func ffDescribeItems(items []internalapi.PlanItem) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("{%s %s}", it.Action, it.Description))
	}
	return strings.Join(parts, ", ")
}

func ffMustHaveCall(t *testing.T, calls []ffRecordedCall, method, path string) {
	t.Helper()
	for _, c := range calls {
		if c.Method == method && c.Path == path {
			return
		}
	}
	got := make([]string, 0, len(calls))
	for _, c := range calls {
		got = append(got, c.Method+" "+c.Path)
	}
	t.Fatalf("expected call %s %s; recorded: %v", method, path, got)
}
