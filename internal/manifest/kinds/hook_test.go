package kinds

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
	"gopkg.in/yaml.v3"
)

// hookFakeClient is a minimal in-memory internalapi.Client used by
// every Hook test. It records every call so a test can assert what
// the manifest layer would have sent over the wire, and keeps a map
// of registered hooks (keyed by id) that GET /api/v1/hooks reads
// from. Toggle endpoints flip the row's Enabled boolean so subsequent
// reads observe the new state.
//
// The fake never emits the description column (hooks_config has none
// in the real schema) — Plan and Export synthesise descriptions from
// event + handler_kind, both of which are recorded on the row.
type hookFakeClient struct {
	t        *testing.T
	wsID     string
	rows     map[string]hookFakeRow // keyed by id (== slug in the manifest)
	calls    []hookFakeCall
	listErr  error // when set, GET /api/v1/hooks returns this error
	listCode int   // when non-zero, GET /api/v1/hooks returns this status
	listBody []byte
}

type hookFakeRow struct {
	ID          string
	Event       string
	HandlerKind string
	Enabled     bool
}

type hookFakeCall struct {
	Method string
	Path   string
	Body   any
}

func newHookFakeClient(t *testing.T) *hookFakeClient {
	t.Helper()
	return &hookFakeClient{
		t:    t,
		wsID: "ws_test",
		rows: map[string]hookFakeRow{},
	}
}

func (f *hookFakeClient) WorkspaceID() string { return f.wsID }

func (f *hookFakeClient) record(method, path string, body any) {
	f.calls = append(f.calls, hookFakeCall{Method: method, Path: path, Body: body})
}

func (f *hookFakeClient) respond(status int, v any) *internalapi.Response {
	data, _ := json.Marshal(v)
	return &internalapi.Response{
		StatusCode: status,
		Body:       bytes.NewReader(data),
	}
}

func (f *hookFakeClient) Get(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("GET", path, nil)
	if f.listErr != nil {
		return nil, f.listErr
	}
	if path == "/api/v1/hooks" {
		if f.listCode != 0 {
			return &internalapi.Response{
				StatusCode: f.listCode,
				Body:       bytes.NewReader(f.listBody),
			}, nil
		}
		rows := make([]map[string]any, 0, len(f.rows))
		for _, r := range f.rows {
			rows = append(rows, map[string]any{
				"id":           r.ID,
				"event":        r.Event,
				"handler_kind": r.HandlerKind,
				"enabled":      r.Enabled,
			})
		}
		return f.respond(200, map[string]any{
			"rows":  rows,
			"count": len(rows),
		}), nil
	}
	return f.respond(404, map[string]any{"error": "not found"}), nil
}

func (f *hookFakeClient) Post(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("POST", path, body)
	// Match /api/v1/hooks/{id}/enable or /disable.
	const prefix = "/api/v1/hooks/"
	if strings.HasPrefix(path, prefix) {
		rest := strings.TrimPrefix(path, prefix)
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) == 2 {
			id, verb := parts[0], parts[1]
			row, ok := f.rows[id]
			if !ok {
				return f.respond(404, map[string]any{"error": "hook not found"}), nil
			}
			switch verb {
			case "enable":
				row.Enabled = true
				f.rows[id] = row
				return f.respond(200, map[string]any{"id": id, "enabled": true}), nil
			case "disable":
				row.Enabled = false
				f.rows[id] = row
				return f.respond(200, map[string]any{"id": id, "enabled": false}), nil
			}
		}
	}
	return f.respond(404, map[string]any{"error": "not found"}), nil
}

func (f *hookFakeClient) Patch(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PATCH", path, body)
	return f.respond(200, body), nil
}

func (f *hookFakeClient) Put(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PUT", path, body)
	return f.respond(200, body), nil
}

func (f *hookFakeClient) Delete(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("DELETE", path, nil)
	return f.respond(204, nil), nil
}

// hookSampleDoc returns a fully-specified HookDocument every test can
// shallow-copy and tweak. Keeping construction in one place means a
// future schema addition doesn't ripple across every test body.
func hookSampleDoc() HookDocument {
	return HookDocument{
		APIVersion: "crewship/v1",
		Kind:       "Hook",
		Metadata: internalapi.Metadata{
			Name: "pre-run-cost-gate",
			Slug: "pre-run-cost-gate",
		},
		Spec: HookSpec{Enabled: true},
	}
}

// hookFindCall is a small assertion helper: returns the first call
// matching method+path or nil.
func hookFindCall(calls []hookFakeCall, method, path string) *hookFakeCall {
	for i := range calls {
		if calls[i].Method == method && calls[i].Path == path {
			return &calls[i]
		}
	}
	return nil
}

// ── 1. Parse round-trip ─────────────────────────────────────────────────────

// TestHook_ParseRoundTrip asserts that a Hook YAML document survives a
// Marshal → Unmarshal cycle byte-for-byte (modulo whitespace). This is
// the load-bearing property for `crewship export | crewship apply`.
func TestHook_ParseRoundTrip(t *testing.T) {
	yamlIn := `apiVersion: crewship/v1
kind: Hook
metadata:
  name: pre-run-cost-gate
  slug: pre-run-cost-gate
spec:
  enabled: true
`
	var doc HookDocument
	if err := yaml.Unmarshal([]byte(yamlIn), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Metadata.Slug != "pre-run-cost-gate" {
		t.Errorf("slug = %q, want pre-run-cost-gate", doc.Metadata.Slug)
	}
	if !doc.Spec.Enabled {
		t.Errorf("Enabled = false, want true")
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var rt HookDocument
	if err := yaml.Unmarshal(out, &rt); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if rt.APIVersion != doc.APIVersion ||
		rt.Kind != doc.Kind ||
		rt.Metadata.Slug != doc.Metadata.Slug ||
		rt.Metadata.Name != doc.Metadata.Name ||
		rt.Spec.Enabled != doc.Spec.Enabled {
		t.Errorf("round-trip mismatch:\n  before=%+v\n  after =%+v", doc, rt)
	}
}

// ── 2. Validate happy path ──────────────────────────────────────────────────

// TestHook_ValidateHappy confirms that a well-formed document passes
// Validate without error. WorkspaceContext is unused — hooks have no
// FK references — but we pass a populated one to make sure the
// signature handles non-zero values too.
func TestHook_ValidateHappy(t *testing.T) {
	doc := hookSampleDoc()
	ctx := internalapi.WorkspaceContext{
		DeclaredCrews: []internalapi.SlugLookup{{Slug: "unused"}},
	}
	if err := doc.Validate(ctx); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// ── 3. Validate error paths ─────────────────────────────────────────────────

// TestHook_ValidateErrors covers every structural rejection: bad
// apiVersion, bad kind, missing slug, missing name. Each sub-case
// asserts the error message contains a recognisable substring so the
// CLI surface stays predictable.
func TestHook_ValidateErrors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*HookDocument)
		wantSub string
	}{
		{
			name: "wrong apiVersion",
			mutate: func(d *HookDocument) {
				d.APIVersion = "crewship/v2"
			},
			wantSub: "unsupported apiVersion",
		},
		{
			name: "wrong kind",
			mutate: func(d *HookDocument) {
				d.Kind = "Webhook"
			},
			wantSub: `kind must be "Hook"`,
		},
		{
			name: "missing slug",
			mutate: func(d *HookDocument) {
				d.Metadata.Slug = ""
			},
			wantSub: "metadata.slug is required",
		},
		{
			name: "blank slug",
			mutate: func(d *HookDocument) {
				d.Metadata.Slug = "   "
			},
			wantSub: "metadata.slug is required",
		},
		{
			name: "missing name",
			mutate: func(d *HookDocument) {
				d.Metadata.Name = ""
			},
			wantSub: "metadata.name is required",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := hookSampleDoc()
			tc.mutate(&doc)
			err := doc.Validate(internalapi.WorkspaceContext{})
			if err == nil {
				t.Fatalf("Validate succeeded; want error containing %q", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// ── 4. Plan: hook not registered → error PlanItem ──────────────────────────

// TestHook_PlanNotRegistered confirms the SPEC-2 §14 rule that a hook
// missing from the live registry surfaces as an erroring PlanItem (not
// a Plan-time abort). Dry-run callers rely on this to print every
// missing hook in one pass.
func TestHook_PlanNotRegistered(t *testing.T) {
	doc := hookSampleDoc()
	fake := newHookFakeClient(t)

	items, err := doc.Plan(context.Background(), fake, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 plan item, got %d", len(items))
	}
	item := items[0]
	if item.Action != internalapi.ActionUpdate {
		t.Errorf("Action = %v, want ActionUpdate (missing-hook is reported via exec error)", item.Action)
	}
	if !strings.Contains(item.Description, "not registered") {
		t.Errorf("Description = %q, want 'not registered' substring", item.Description)
	}
	if !strings.Contains(item.Description, "register it in code first") {
		t.Errorf("Description = %q, want 'register it in code first' substring", item.Description)
	}
	if item.Exec == nil {
		t.Fatal("Exec is nil; want an erroring closure so Apply (non-dry-run) fails")
	}
	if execErr := item.Exec(context.Background(), fake); execErr == nil {
		t.Error("Exec succeeded; want error reporting the missing hook")
	} else if !strings.Contains(execErr.Error(), "not registered") {
		t.Errorf("Exec error = %q, want 'not registered' substring", execErr.Error())
	}
}

// ── 5. Plan: enable when disabled ──────────────────────────────────────────

// TestHook_PlanEnableWhenDisabled confirms that a declared `enabled:
// true` against a server row with enabled=false emits one ActionUpdate
// whose Exec hits /enable.
func TestHook_PlanEnableWhenDisabled(t *testing.T) {
	doc := hookSampleDoc()
	doc.Spec.Enabled = true

	remote := &HookRemote{
		ID:          "pre-run-cost-gate",
		Slug:        "pre-run-cost-gate",
		Enabled:     false,
		Description: "pre_run shell hook",
	}
	fake := newHookFakeClient(t)
	fake.rows["pre-run-cost-gate"] = hookFakeRow{
		ID: "pre-run-cost-gate", Event: "pre_run", HandlerKind: "shell", Enabled: false,
	}

	items, err := doc.Plan(context.Background(), fake, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 plan item, got %d", len(items))
	}
	item := items[0]
	if item.Action != internalapi.ActionUpdate {
		t.Errorf("Action = %v, want ActionUpdate", item.Action)
	}
	if !strings.Contains(item.Description, "enable") {
		t.Errorf("Description = %q, want 'enable' substring", item.Description)
	}

	// Run the Exec and confirm it POSTed to the enable path.
	if err := item.Exec(context.Background(), fake); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if call := hookFindCall(fake.calls, "POST", "/api/v1/hooks/pre-run-cost-gate/enable"); call == nil {
		t.Errorf("missing POST .../enable call; got calls: %+v", fake.calls)
	}
	// State should now reflect the toggle.
	if !fake.rows["pre-run-cost-gate"].Enabled {
		t.Error("after Exec, row.Enabled should be true")
	}
}

// ── 6. Plan: disable when enabled ──────────────────────────────────────────

// TestHook_PlanDisableWhenEnabled is the mirror of the previous test:
// declared `enabled: false` against a server row with enabled=true
// emits ActionUpdate against the /disable endpoint.
func TestHook_PlanDisableWhenEnabled(t *testing.T) {
	doc := hookSampleDoc()
	doc.Spec.Enabled = false

	remote := &HookRemote{
		ID:          "pre-run-cost-gate",
		Slug:        "pre-run-cost-gate",
		Enabled:     true,
		Description: "pre_run shell hook",
	}
	fake := newHookFakeClient(t)
	fake.rows["pre-run-cost-gate"] = hookFakeRow{
		ID: "pre-run-cost-gate", Event: "pre_run", HandlerKind: "shell", Enabled: true,
	}

	items, err := doc.Plan(context.Background(), fake, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 plan item, got %d", len(items))
	}
	item := items[0]
	if item.Action != internalapi.ActionUpdate {
		t.Errorf("Action = %v, want ActionUpdate", item.Action)
	}
	if !strings.Contains(item.Description, "disable") {
		t.Errorf("Description = %q, want 'disable' substring", item.Description)
	}

	if err := item.Exec(context.Background(), fake); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if call := hookFindCall(fake.calls, "POST", "/api/v1/hooks/pre-run-cost-gate/disable"); call == nil {
		t.Errorf("missing POST .../disable call; got calls: %+v", fake.calls)
	}
	if fake.rows["pre-run-cost-gate"].Enabled {
		t.Error("after Exec, row.Enabled should be false")
	}
}

// ── 7. Plan: already in desired state → Unchanged ──────────────────────────

// TestHook_PlanUnchanged confirms that a declared state matching the
// server emits Action=Unchanged with a nil Exec (the apply loop relies
// on nil-Exec to short-circuit dry-run reporting and not consume a
// network round-trip).
func TestHook_PlanUnchanged(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
	}{
		{"enabled stays enabled", true},
		{"disabled stays disabled", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := hookSampleDoc()
			doc.Spec.Enabled = tc.enabled
			remote := &HookRemote{
				ID:          "pre-run-cost-gate",
				Slug:        "pre-run-cost-gate",
				Enabled:     tc.enabled,
				Description: "pre_run shell hook",
			}
			fake := newHookFakeClient(t)
			items, err := doc.Plan(context.Background(), fake, remote)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if len(items) != 1 {
				t.Fatalf("want 1 plan item, got %d", len(items))
			}
			item := items[0]
			if item.Action != internalapi.ActionUnchanged {
				t.Errorf("Action = %v, want ActionUnchanged", item.Action)
			}
			if item.Exec != nil {
				t.Error("Exec should be nil for Unchanged items")
			}
			if !strings.Contains(item.Description, "already") {
				t.Errorf("Description = %q, want 'already' substring", item.Description)
			}
			// No network call should have been made.
			for _, c := range fake.calls {
				t.Errorf("unexpected network call during Unchanged plan: %s %s", c.Method, c.Path)
			}
		})
	}
}

// ── 8. Export: round-trip via ExportHooks + ListHooks ──────────────────────

// TestHook_ExportRoundTrip confirms that ExportHooks reads the
// registry and produces HookDocuments whose slug matches the row id
// and whose spec.Enabled reflects the live state. The test seeds the
// fake with two rows in different states and asserts both come back.
func TestHook_ExportRoundTrip(t *testing.T) {
	fake := newHookFakeClient(t)
	fake.rows["pre-run-cost-gate"] = hookFakeRow{
		ID: "pre-run-cost-gate", Event: "pre_run", HandlerKind: "shell", Enabled: true,
	}
	fake.rows["post-run-notify"] = hookFakeRow{
		ID: "post-run-notify", Event: "post_run", HandlerKind: "http", Enabled: false,
	}

	docs, err := ExportHooks(context.Background(), fake)
	if err != nil {
		t.Fatalf("ExportHooks: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("want 2 docs, got %d", len(docs))
	}

	bySlug := map[string]*HookDocument{}
	for _, d := range docs {
		if d.APIVersion != "crewship/v1" {
			t.Errorf("doc %q apiVersion = %q, want crewship/v1", d.Metadata.Slug, d.APIVersion)
		}
		if d.Kind != "Hook" {
			t.Errorf("doc %q kind = %q, want Hook", d.Metadata.Slug, d.Kind)
		}
		bySlug[d.Metadata.Slug] = d
	}

	gate := bySlug["pre-run-cost-gate"]
	if gate == nil {
		t.Fatal("missing pre-run-cost-gate export")
	}
	if !gate.Spec.Enabled {
		t.Error("pre-run-cost-gate should export with Enabled=true")
	}
	if !strings.Contains(gate.Metadata.Description, "pre_run") || !strings.Contains(gate.Metadata.Description, "shell") {
		t.Errorf("description = %q, want 'pre_run' and 'shell' substrings", gate.Metadata.Description)
	}

	notify := bySlug["post-run-notify"]
	if notify == nil {
		t.Fatal("missing post-run-notify export")
	}
	if notify.Spec.Enabled {
		t.Error("post-run-notify should export with Enabled=false")
	}

	// Verify FindHookBySlug semantics: existing slug returns the row,
	// missing slug returns (nil, nil) so callers can feed it to Plan as
	// the "missing hook" signal.
	got, err := FindHookBySlug(context.Background(), fake, "pre-run-cost-gate")
	if err != nil {
		t.Fatalf("FindHookBySlug: %v", err)
	}
	if got == nil || got.ID != "pre-run-cost-gate" {
		t.Errorf("FindHookBySlug returned %+v, want id=pre-run-cost-gate", got)
	}
	missing, err := FindHookBySlug(context.Background(), fake, "no-such-hook")
	if err != nil {
		t.Fatalf("FindHookBySlug (missing): %v", err)
	}
	if missing != nil {
		t.Errorf("FindHookBySlug(missing) = %+v, want nil", missing)
	}
}

// TestHook_ListHooksTransportErrors exercises the two failure shapes
// ListHooks can surface: a Get() error from the underlying client (eg
// network blip) and a non-2xx response (eg the server returned a
// Problem Details body). Both must propagate so apply.go can abort
// with an actionable message rather than silently treating every hook
// as missing.
func TestHook_ListHooksTransportErrors(t *testing.T) {
	// Network-level error.
	fakeNet := newHookFakeClient(t)
	fakeNet.listErr = errors.New("connection refused")
	if _, err := ListHooks(context.Background(), fakeNet); err == nil {
		t.Error("ListHooks succeeded against listErr; want propagated error")
	} else if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error = %q, want 'connection refused' substring", err.Error())
	}

	// HTTP-level error.
	fakeHTTP := newHookFakeClient(t)
	fakeHTTP.listCode = 503
	fakeHTTP.listBody = []byte(`{"type":"about:blank","title":"Service Unavailable"}`)
	if _, err := ListHooks(context.Background(), fakeHTTP); err == nil {
		t.Error("ListHooks succeeded against 503; want propagated error")
	} else if !strings.Contains(err.Error(), "503") {
		t.Errorf("error = %q, want '503' substring", err.Error())
	}
}
