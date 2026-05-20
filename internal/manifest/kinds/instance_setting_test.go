package kinds

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// httptestClient adapts an *httptest.Server to internalapi.Client.
// We use a real round-trip (rather than a fake-only stub) because
// the InstanceSetting handler under test in the backend is in
// active development by a parallel agent — exercising the same
// HTTP surface here pins the manifest side to a stable contract
// that the backend can be measured against.
type httptestClient struct {
	t     *testing.T
	srv   *httptest.Server
	wsID  string
	hc    *http.Client
	calls []recordedCall
	mu    sync.Mutex
}

type recordedCall struct {
	Method string
	Path   string
	Body   string
}

func newHTTPClient(t *testing.T, handler http.Handler) *httptestClient {
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &httptestClient{
		t:    t,
		srv:  srv,
		wsID: "ws_test",
		hc:   srv.Client(),
	}
}

func (h *httptestClient) record(method, path string, body any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rec := recordedCall{Method: method, Path: path}
	if body != nil {
		b, _ := json.Marshal(body)
		rec.Body = string(b)
	}
	h.calls = append(h.calls, rec)
}

func (h *httptestClient) Calls() []recordedCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]recordedCall, len(h.calls))
	copy(out, h.calls)
	return out
}

func (h *httptestClient) WorkspaceID() string { return h.wsID }

func (h *httptestClient) Get(ctx context.Context, path string) (*internalapi.Response, error) {
	h.record("GET", path, nil)
	return h.do(ctx, http.MethodGet, path, nil)
}
func (h *httptestClient) Post(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	h.record("POST", path, body)
	return h.do(ctx, http.MethodPost, path, body)
}
func (h *httptestClient) Patch(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	h.record("PATCH", path, body)
	return h.do(ctx, http.MethodPatch, path, body)
}
func (h *httptestClient) Put(ctx context.Context, path string, body any) (*internalapi.Response, error) {
	h.record("PUT", path, body)
	return h.do(ctx, http.MethodPut, path, body)
}
func (h *httptestClient) Delete(ctx context.Context, path string) (*internalapi.Response, error) {
	h.record("DELETE", path, nil)
	return h.do(ctx, http.MethodDelete, path, nil)
}

func (h *httptestClient) do(ctx context.Context, method, path string, body any) (*internalapi.Response, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.srv.URL+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.hc.Do(req)
	if err != nil {
		return nil, err
	}
	// Buffer the body so the caller can read freely without
	// worrying about connection reuse.
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return &internalapi.Response{
		StatusCode: resp.StatusCode,
		Body:       bytes.NewReader(data),
	}, nil
}

// fakeBackend is a minimal in-memory stand-in for the
// InstanceSetting backend handler. It implements just enough of the
// SPEC-2 endpoint contract to exercise Plan/Apply round-trips.
type fakeBackend struct {
	mu       sync.Mutex
	settings map[string]string
	// maskKeys are returned as "***" by GET; mirrors the backend's
	// sensitive-value redaction.
	maskKeys map[string]struct{}
	// protectedDelete refuses to DELETE these keys, mirroring the
	// backend's protected-key whitelist. Plan should never even
	// attempt to delete one, but the backend stays as the ultimate
	// authority.
	protected map[string]struct{}
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		settings:  map[string]string{},
		maskKeys:  map[string]struct{}{},
		protected: map[string]struct{}{},
	}
}

func (b *fakeBackend) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/instance/settings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		b.mu.Lock()
		out := make(map[string]string, len(b.settings))
		for k, v := range b.settings {
			if _, mask := b.maskKeys[k]; mask {
				out[k] = "***"
			} else {
				out[k] = v
			}
		}
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/api/v1/instance/settings/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/api/v1/instance/settings/")
		if key == "" {
			http.Error(w, "missing key", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodGet:
			b.mu.Lock()
			v, ok := b.settings[key]
			if _, mask := b.maskKeys[key]; mask {
				v = "***"
			}
			b.mu.Unlock()
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"key": key, "value": v})
		case http.MethodPut:
			var body struct {
				Value string `json:"value"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			b.mu.Lock()
			b.settings[key] = body.Value
			b.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			b.mu.Lock()
			defer b.mu.Unlock()
			if _, prot := b.protected[key]; prot {
				http.Error(w, "protected", http.StatusForbidden)
				return
			}
			if _, ok := b.settings[key]; !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			delete(b.settings, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	return mux
}

// ---------- 1. Validate ----------

func TestInstanceSetting_Validate_OK(t *testing.T) {
	doc := &InstanceSettingDocument{
		APIVersion: "crewship/v1",
		Kind:       "InstanceSetting",
		Metadata:   internalapi.Metadata{Name: "settings", Slug: "settings"},
		Spec: InstanceSettingSpec{Settings: map[string]string{
			"branding.product_name": "Crewship",
			"smtp.host":             "smtp.gmail.com",
		}},
	}
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestInstanceSetting_Validate_RejectsEmptyKey(t *testing.T) {
	doc := &InstanceSettingDocument{
		Spec: InstanceSettingSpec{Settings: map[string]string{"": "x"}},
	}
	if err := doc.Validate(internalapi.WorkspaceContext{}); err == nil {
		t.Fatal("want error for empty key, got nil")
	}
}

func TestInstanceSetting_Validate_RejectsBadAPIVersion(t *testing.T) {
	doc := &InstanceSettingDocument{
		APIVersion: "crewship/v2",
		Kind:       "InstanceSetting",
	}
	if err := doc.Validate(internalapi.WorkspaceContext{}); err == nil {
		t.Fatal("want error for bad apiVersion, got nil")
	}
}

// ---------- 2. Warnings (sensitive-value detection) ----------

func TestInstanceSetting_Warnings_SensitiveLiteral(t *testing.T) {
	doc := &InstanceSettingDocument{
		Spec: InstanceSettingSpec{Settings: map[string]string{
			"smtp.password":              "hunter2", // literal — should warn
			"oauth.github.client_secret": "ghs_xxx", // literal — should warn
			"webhook.foo.secret":         "${WH}",   // env ref — no warn
			"branding.product_name":      "Crewship",
		}},
	}
	ws := doc.Warnings()
	if len(ws) != 2 {
		t.Fatalf("want 2 warnings, got %d: %+v", len(ws), ws)
	}
	got := map[string]bool{}
	for _, w := range ws {
		got[w.Key] = true
	}
	if !got["smtp.password"] || !got["oauth.github.client_secret"] {
		t.Errorf("missing expected warning keys: %+v", ws)
	}
}

// ---------- 3. Env interpolation: success ----------

func TestInstanceSetting_Plan_EnvInterpolation_Resolves(t *testing.T) {
	be := newFakeBackend()
	hc := newHTTPClient(t, be.Handler())
	doc := &InstanceSettingDocument{
		Spec: InstanceSettingSpec{Settings: map[string]string{
			"smtp.password": "${SMTP_PASSWORD}",
		}},
	}
	opts := PlanInstanceSettingsOptions{
		EnvLookup: func(name string) (string, bool) {
			if name == "SMTP_PASSWORD" {
				return "s3cret", true
			}
			return "", false
		},
	}
	items, err := doc.Plan(context.Background(), hc, nil, opts)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want one update, got %+v", items)
	}
	// Run the exec; verify the PUT body carried the resolved value.
	if err := items[0].Exec(context.Background(), hc); err != nil {
		t.Fatalf("exec: %v", err)
	}
	be.mu.Lock()
	got := be.settings["smtp.password"]
	be.mu.Unlock()
	if got != "s3cret" {
		t.Errorf("backend stored %q, want %q", got, "s3cret")
	}
}

// ---------- 4. Env interpolation: missing var → clear error ----------

func TestInstanceSetting_Plan_EnvInterpolation_MissingVarErrors(t *testing.T) {
	be := newFakeBackend()
	hc := newHTTPClient(t, be.Handler())
	doc := &InstanceSettingDocument{
		Spec: InstanceSettingSpec{Settings: map[string]string{
			"smtp.password": "${SMTP_PASSWORD_MISSING}",
		}},
	}
	opts := PlanInstanceSettingsOptions{
		EnvLookup: func(name string) (string, bool) {
			return "", false // every lookup misses
		},
	}
	_, err := doc.Plan(context.Background(), hc, nil, opts)
	if err == nil {
		t.Fatal("want error for missing env var, got nil")
	}
	if !strings.Contains(err.Error(), "SMTP_PASSWORD_MISSING") {
		t.Errorf("error should name the missing var; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "not set") {
		t.Errorf("error should explain the failure; got %q", err.Error())
	}
}

// ---------- 5. Plan: create + unchanged classification ----------

func TestInstanceSetting_Plan_CreateAndUnchanged(t *testing.T) {
	be := newFakeBackend()
	be.settings["branding.product_name"] = "Crewship" // matches
	be.settings["branding.primary_color"] = "#000000" // differs
	hc := newHTTPClient(t, be.Handler())

	doc := &InstanceSettingDocument{
		Spec: InstanceSettingSpec{Settings: map[string]string{
			"branding.product_name":  "Crewship",
			"branding.primary_color": "#3B82F6",
			"branding.tagline":       "Ship faster", // missing remotely
		}},
	}
	items, err := doc.Plan(context.Background(), hc, nil, PlanInstanceSettingsOptions{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	byKey := map[string]internalapi.PlanAction{}
	for _, it := range items {
		byKey[it.Slug] = it.Action
	}
	if byKey["branding.product_name"] != internalapi.ActionUnchanged {
		t.Errorf("product_name should be Unchanged, got %v", byKey["branding.product_name"])
	}
	if byKey["branding.primary_color"] != internalapi.ActionUpdate {
		t.Errorf("primary_color should be Update, got %v", byKey["branding.primary_color"])
	}
	if byKey["branding.tagline"] != internalapi.ActionUpdate {
		t.Errorf("tagline should be Update, got %v", byKey["branding.tagline"])
	}
}

// ---------- 6. Plan: masked sensitive value always triggers update ----------

func TestInstanceSetting_Plan_MaskedValueAlwaysUpdates(t *testing.T) {
	be := newFakeBackend()
	be.settings["smtp.password"] = "real-secret"
	be.maskKeys["smtp.password"] = struct{}{}
	hc := newHTTPClient(t, be.Handler())

	doc := &InstanceSettingDocument{
		Spec: InstanceSettingSpec{Settings: map[string]string{
			"smtp.password": "real-secret", // same as stored, but read is masked
		}},
	}
	items, err := doc.Plan(context.Background(), hc, nil, PlanInstanceSettingsOptions{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("masked read should produce Update, got %+v", items)
	}
}

// ---------- 7. ApplyReplace prunes undeclared keys, skips protected ----------

func TestInstanceSetting_Plan_ApplyReplaceDeletesUndeclared(t *testing.T) {
	be := newFakeBackend()
	be.settings["branding.product_name"] = "Old"
	be.settings["legacy.feature_flag"] = "true" // not declared → delete
	be.settings["another.dead.key"] = "yes"     // not declared → delete
	hc := newHTTPClient(t, be.Handler())

	doc := &InstanceSettingDocument{
		Spec: InstanceSettingSpec{Settings: map[string]string{
			"branding.product_name": "New",
		}},
	}
	items, err := doc.Plan(context.Background(), hc, nil, PlanInstanceSettingsOptions{Replace: true})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	deletes := map[string]bool{}
	var updates int
	for _, it := range items {
		switch it.Action {
		case internalapi.ActionUpdate:
			updates++
		case internalapi.ActionDelete:
			deletes[it.Slug] = true
		}
	}
	if updates != 1 {
		t.Errorf("want 1 update, got %d", updates)
	}
	if !deletes["legacy.feature_flag"] || !deletes["another.dead.key"] {
		t.Errorf("expected deletes for undeclared keys, got %v", deletes)
	}
	if len(deletes) != 2 {
		t.Errorf("want exactly 2 delete items, got %d (%v)", len(deletes), deletes)
	}
}

// ---------- 8. ApplyReplace must NOT delete protected keys ----------

func TestInstanceSetting_Plan_ApplyReplaceSkipsProtected(t *testing.T) {
	be := newFakeBackend()
	be.settings["instance.bootstrap_at"] = "2026-01-01T00:00:00Z"
	be.settings["instance.first_user_id"] = "usr_abc"
	be.settings["schema.version"] = "44"
	be.settings["random.thing"] = "delete-me"
	hc := newHTTPClient(t, be.Handler())

	doc := &InstanceSettingDocument{
		// Empty spec — every remote key is "undeclared", so without
		// protection ApplyReplace would wipe the instance.
		Spec: InstanceSettingSpec{Settings: map[string]string{}},
	}
	items, err := doc.Plan(context.Background(), hc, nil, PlanInstanceSettingsOptions{Replace: true})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	for _, it := range items {
		if it.Action == internalapi.ActionDelete {
			if IsProtectedInstanceKey(it.Slug) {
				t.Errorf("protected key %q must never appear as a Delete item", it.Slug)
			}
		}
	}

	// And we should still see the non-protected deletion.
	var sawRandomDelete bool
	for _, it := range items {
		if it.Slug == "random.thing" && it.Action == internalapi.ActionDelete {
			sawRandomDelete = true
		}
	}
	if !sawRandomDelete {
		t.Error("non-protected key should still be deleted in ApplyReplace")
	}

	// Protected keys must surface as Unchanged so users see them.
	protectedSeen := map[string]bool{}
	for _, it := range items {
		if IsProtectedInstanceKey(it.Slug) && it.Action == internalapi.ActionUnchanged {
			protectedSeen[it.Slug] = true
		}
	}
	for _, k := range []string{"instance.bootstrap_at", "instance.first_user_id", "schema.version"} {
		if !protectedSeen[k] {
			t.Errorf("protected key %q should appear as Unchanged in plan", k)
		}
	}
}

// ---------- 9. Export ----------

func TestInstanceSetting_Export(t *testing.T) {
	be := newFakeBackend()
	be.settings["smtp.host"] = "smtp.gmail.com"
	be.settings["smtp.password"] = "stored-secret"
	be.maskKeys["smtp.password"] = struct{}{}
	be.settings["branding.product_name"] = "Crewship"
	hc := newHTTPClient(t, be.Handler())

	docs, err := ExportInstanceSettings(context.Background(), hc)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("want 1 document, got %d", len(docs))
	}
	doc := docs[0]
	if doc.APIVersion != "crewship/v1" || doc.Kind != "InstanceSetting" {
		t.Errorf("envelope wrong: %+v", doc)
	}
	if doc.Spec.Settings["smtp.host"] != "smtp.gmail.com" {
		t.Errorf("host wrong: %q", doc.Spec.Settings["smtp.host"])
	}
	if doc.Spec.Settings["smtp.password"] != "***" {
		t.Errorf("sensitive value should export as '***', got %q", doc.Spec.Settings["smtp.password"])
	}
	if doc.Spec.Settings["branding.product_name"] != "Crewship" {
		t.Errorf("branding wrong: %q", doc.Spec.Settings["branding.product_name"])
	}
}

func TestInstanceSetting_Export_EmptyServerReturnsNoDoc(t *testing.T) {
	be := newFakeBackend()
	hc := newHTTPClient(t, be.Handler())
	docs, err := ExportInstanceSettings(context.Background(), hc)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("want no document for empty server, got %+v", docs)
	}
}

// ---------- Bonus: handler-not-yet-deployed gracefully degrades ----------

func TestInstanceSetting_Plan_HandlerMissing_GracefulPlan(t *testing.T) {
	// Empty mux — every path 404s, simulating the backend handler
	// not being deployed yet. Plan should still produce updates for
	// every declared key (treating remote state as empty).
	hc := newHTTPClient(t, http.NewServeMux())

	doc := &InstanceSettingDocument{
		Spec: InstanceSettingSpec{Settings: map[string]string{
			"branding.product_name": "Crewship",
		}},
	}
	items, err := doc.Plan(context.Background(), hc, nil, PlanInstanceSettingsOptions{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want one Update item, got %+v", items)
	}
}

// Compile-time guard: ensure the fake satisfies the manifest
// Client surface. If internalapi.Client gains a method, this won't
// compile — surfacing the obligation immediately.
var _ internalapi.Client = (*httptestClient)(nil)

// ensure helper used to fail a test if a recorded body is missing.
//
//nolint:unused
func bodyOrFail(t *testing.T, body string) map[string]any {
	t.Helper()
	if body == "" {
		t.Fatal("expected non-empty body")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return m
}

// ensure errors.Is is usable for typed assertions in the future.
var _ = errors.Is
var _ = fmt.Sprintf
