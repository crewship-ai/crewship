// connector_test.go covers the eight test categories required by
// SPEC-2 §13 plus the kind-specific edge cases that fall out of the
// "reference kind" treatment: missing required-credential mapping,
// best-effort uninstall, and the credential-existence check at install
// time. Tests use an in-memory fakeConnectorClient so the entire suite
// runs without httptest — keeps the iteration loop fast and avoids
// flaky port bind-ups when run under -race in parallel with the rest
// of the manifest suite.
package kinds

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"

	yaml "gopkg.in/yaml.v3"
)

// ── test fixture ──────────────────────────────────────────────────────────

// fakeConnectorClient implements internalapi.Client with enough state
// to drive the Connector kind's HTTP calls. Each test instantiates a
// fresh one so cross-test pollution is impossible.
//
// The map shapes mirror what GET /api/v1/connectors/{id} and
// GET /api/v1/credentials actually return on the wire — the kind
// tolerates both flat-array and wrapped envelope shapes for the
// credentials list, but the handler currently emits the flat shape
// and we exercise that path in the happy-case tests.
type fakeConnectorClient struct {
	t           *testing.T
	connectors  map[string]ConnectorRemote
	credentials map[string]bool // name → exists
	calls       []connectorCall

	// installEndpointStatus lets tests force an alternate status code
	// on the POST install path (used for the error-propagation case).
	// Zero value (0) means "200 OK, empty body".
	installEndpointStatus int

	// deleteEndpointStatus drives the uninstall branch. Defaults to
	// 200; a 404/405/501 triggers the "endpoint not implemented"
	// degraded path.
	deleteEndpointStatus int
}

type connectorCall struct {
	Method string
	Path   string
	Body   any
}

func newFakeConnectorClient(t *testing.T) *fakeConnectorClient {
	return &fakeConnectorClient{
		t:           t,
		connectors:  map[string]ConnectorRemote{},
		credentials: map[string]bool{},
	}
}

func (f *fakeConnectorClient) WorkspaceID() string { return "ws_test" }

func (f *fakeConnectorClient) record(method, path string, body any) {
	f.calls = append(f.calls, connectorCall{Method: method, Path: path, Body: body})
}

func (f *fakeConnectorClient) Get(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("GET", path, nil)
	switch {
	case path == "/api/v1/connectors":
		// Catalog list — emit every known entry.
		list := make([]ConnectorRemote, 0, len(f.connectors))
		for _, v := range f.connectors {
			list = append(list, v)
		}
		return connectorJSONResp(200, list), nil
	case strings.HasPrefix(path, "/api/v1/connectors/"):
		slug := strings.TrimPrefix(path, "/api/v1/connectors/")
		c, ok := f.connectors[slug]
		if !ok {
			return connectorJSONResp(404, map[string]any{"error": "not found"}), nil
		}
		return connectorJSONResp(200, c), nil
	case path == "/api/v1/credentials":
		out := make([]map[string]any, 0, len(f.credentials))
		for name := range f.credentials {
			out = append(out, map[string]any{"name": name})
		}
		return connectorJSONResp(200, out), nil
	}
	return connectorJSONResp(404, map[string]any{"error": "not found"}), nil
}

func (f *fakeConnectorClient) Post(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("POST", path, body)
	if strings.HasPrefix(path, "/api/v1/connectors/") && strings.HasSuffix(path, "/install") {
		status := f.installEndpointStatus
		if status == 0 {
			status = 200
		}
		// Flip the in-memory state so a subsequent Plan sees the
		// installed=true outcome (idempotency tests rely on this).
		if status >= 200 && status < 300 {
			slug := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/connectors/"), "/install")
			if entry, ok := f.connectors[slug]; ok {
				entry.Installed = true
				f.connectors[slug] = entry
			}
		}
		return connectorJSONResp(status, map[string]any{"ok": true}), nil
	}
	return connectorJSONResp(404, map[string]any{"error": "not found"}), nil
}

func (f *fakeConnectorClient) Patch(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PATCH", path, body)
	return connectorJSONResp(404, map[string]any{"error": "not found"}), nil
}

func (f *fakeConnectorClient) Put(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PUT", path, body)
	return connectorJSONResp(404, map[string]any{"error": "not found"}), nil
}

func (f *fakeConnectorClient) Delete(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("DELETE", path, nil)
	if strings.HasPrefix(path, "/api/v1/connectors/") && strings.HasSuffix(path, "/install") {
		status := f.deleteEndpointStatus
		if status == 0 {
			status = 200
		}
		if status >= 200 && status < 300 {
			slug := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/connectors/"), "/install")
			if entry, ok := f.connectors[slug]; ok {
				entry.Installed = false
				f.connectors[slug] = entry
			}
		}
		return connectorJSONResp(status, map[string]any{"ok": true}), nil
	}
	return connectorJSONResp(404, map[string]any{"error": "not found"}), nil
}

// jsonResp builds an internalapi.Response with a JSON body so the
// kind's decoder paths are exercised end-to-end.
func connectorJSONResp(status int, payload any) *internalapi.Response {
	data, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("connectorJSONResp marshal: %v", err))
	}
	return &internalapi.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(data)),
	}
}

// findCall returns the first recorded call matching (method, pathSuffix),
// or nil. Tests use this instead of indexed comparisons because the
// fetch order (GET catalog vs GET credentials vs POST install) is not
// stable across refactors.
func (f *fakeConnectorClient) findCall(method, pathSuffix string) *connectorCall {
	for i := range f.calls {
		c := &f.calls[i]
		if c.Method == method && strings.HasSuffix(c.Path, pathSuffix) {
			return c
		}
	}
	return nil
}

// ── 1. Parse round-trip ──────────────────────────────────────────────────

func TestConnector_ParseRoundTrip(t *testing.T) {
	in := `apiVersion: crewship/v1
kind: Connector
metadata:
  name: Linear
  slug: linear
  description: Project management
spec:
  install: true
  credentials:
    LINEAR_API_KEY: LINEAR_PROD_KEY
`
	var doc ConnectorDocument
	if err := yaml.Unmarshal([]byte(in), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Metadata.Slug != "linear" {
		t.Errorf("slug: want linear, got %q", doc.Metadata.Slug)
	}
	if !doc.Spec.Install {
		t.Error("install should parse as true")
	}
	if got := doc.Spec.Credentials["LINEAR_API_KEY"]; got != "LINEAR_PROD_KEY" {
		t.Errorf("credentials[LINEAR_API_KEY]: want LINEAR_PROD_KEY, got %q", got)
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundTripped ConnectorDocument
	if err := yaml.Unmarshal(out, &roundTripped); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if roundTripped.Spec.Install != doc.Spec.Install {
		t.Error("round-trip changed install flag")
	}
	if roundTripped.Spec.Credentials["LINEAR_API_KEY"] != doc.Spec.Credentials["LINEAR_API_KEY"] {
		t.Error("round-trip changed credential mapping")
	}
}

// ── 2. Validate happy path ───────────────────────────────────────────────

func TestConnector_Validate_HappyPath(t *testing.T) {
	doc := &ConnectorDocument{
		APIVersion: "crewship/v1",
		Kind:       "Connector",
		Metadata: internalapi.Metadata{
			Name: "Linear",
			Slug: "linear",
		},
		Spec: ConnectorSpec{
			Install: true,
			Credentials: map[string]string{
				"LINEAR_API_KEY": "LINEAR_PROD_KEY",
			},
		},
	}
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate: unexpected error: %v", err)
	}
}

func TestConnector_Validate_NoCredentialsIsFine(t *testing.T) {
	// A connector that genuinely has no env-var deps (e.g. a future
	// no-auth catalog entry) must validate even with credentials
	// omitted. Plan's required-credentials check handles the case
	// where the catalog *does* require them.
	doc := &ConnectorDocument{
		APIVersion: "crewship/v1",
		Kind:       "Connector",
		Metadata: internalapi.Metadata{
			Name: "Public",
			Slug: "public-feed",
		},
		Spec: ConnectorSpec{Install: true},
	}
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate: unexpected error: %v", err)
	}
}

// ── 3. Validate error paths ──────────────────────────────────────────────

func TestConnector_Validate_Errors(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(d *ConnectorDocument)
		wantSubstr string
	}{
		{
			name:       "missing name",
			mutate:     func(d *ConnectorDocument) { d.Metadata.Name = "" },
			wantSubstr: "metadata.name is required",
		},
		{
			name:       "missing slug",
			mutate:     func(d *ConnectorDocument) { d.Metadata.Slug = "" },
			wantSubstr: "metadata.slug is required",
		},
		{
			name: "credential key invalid",
			mutate: func(d *ConnectorDocument) {
				d.Spec.Credentials = map[string]string{"linear api key": "X"}
			},
			wantSubstr: "is not a valid env var name",
		},
		{
			name: "credential value invalid",
			mutate: func(d *ConnectorDocument) {
				d.Spec.Credentials = map[string]string{"LINEAR_API_KEY": "not a name"}
			},
			wantSubstr: "is not a valid env var name",
		},
		{
			name: "credential value empty",
			mutate: func(d *ConnectorDocument) {
				d.Spec.Credentials = map[string]string{"LINEAR_API_KEY": "  "}
			},
			wantSubstr: "is empty",
		},
		{
			name: "credential key starts with digit",
			mutate: func(d *ConnectorDocument) {
				d.Spec.Credentials = map[string]string{"1API": "X"}
			},
			wantSubstr: "is not a valid env var name",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := &ConnectorDocument{
				APIVersion: "crewship/v1",
				Kind:       "Connector",
				Metadata:   internalapi.Metadata{Name: "Linear", Slug: "linear"},
				Spec: ConnectorSpec{
					Install:     true,
					Credentials: map[string]string{"LINEAR_API_KEY": "LINEAR_PROD_KEY"},
				},
			}
			tc.mutate(d)
			err := d.Validate(internalapi.WorkspaceContext{})
			if err == nil {
				t.Fatalf("Validate: expected error containing %q, got nil", tc.wantSubstr)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("Validate: error %q does not contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

// ── 4. Plan: install fresh ───────────────────────────────────────────────

func TestConnector_Plan_InstallFresh(t *testing.T) {
	fake := newFakeConnectorClient(t)
	fake.connectors["linear"] = ConnectorRemote{
		ID:                  "linear",
		Installed:           false,
		RequiredCredentials: []string{"LINEAR_API_KEY"},
	}
	fake.credentials["LINEAR_PROD_KEY"] = true

	doc := &ConnectorDocument{
		APIVersion: "crewship/v1",
		Kind:       "Connector",
		Metadata:   internalapi.Metadata{Name: "Linear", Slug: "linear"},
		Spec: ConnectorSpec{
			Install:     true,
			Credentials: map[string]string{"LINEAR_API_KEY": "LINEAR_PROD_KEY"},
		},
	}
	items, err := doc.Plan(context.Background(), fake, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("Plan: want 1 item, got %d", len(items))
	}
	if items[0].Action != internalapi.ActionCreate {
		t.Errorf("Plan: want ActionCreate, got %s", items[0].Action)
	}
	if items[0].Exec == nil {
		t.Fatal("Plan: Exec is nil for Create")
	}

	// Run the exec to verify the POST body shape + credential check.
	if err := items[0].Exec(context.Background(), fake); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	call := fake.findCall("POST", "/api/v1/connectors/linear/install")
	if call == nil {
		t.Fatal("Exec did not POST to install endpoint")
	}
	body, ok := call.Body.(map[string]any)
	if !ok {
		t.Fatalf("install body: want map[string]any, got %T", call.Body)
	}
	creds, ok := body["credentials"].(map[string]string)
	if !ok {
		t.Fatalf("install body.credentials: want map[string]string, got %T", body["credentials"])
	}
	if creds["LINEAR_API_KEY"] != "LINEAR_PROD_KEY" {
		t.Errorf("install body.credentials[LINEAR_API_KEY]: want LINEAR_PROD_KEY, got %q", creds["LINEAR_API_KEY"])
	}
}

// ── 5. Plan: already installed ───────────────────────────────────────────

func TestConnector_Plan_AlreadyInstalled(t *testing.T) {
	fake := newFakeConnectorClient(t)
	fake.connectors["linear"] = ConnectorRemote{
		ID:                  "linear",
		Installed:           true,
		RequiredCredentials: []string{"LINEAR_API_KEY"},
	}

	doc := &ConnectorDocument{
		APIVersion: "crewship/v1",
		Kind:       "Connector",
		Metadata:   internalapi.Metadata{Name: "Linear", Slug: "linear"},
		Spec: ConnectorSpec{
			Install:     true,
			Credentials: map[string]string{"LINEAR_API_KEY": "LINEAR_PROD_KEY"},
		},
	}
	items, err := doc.Plan(context.Background(), fake, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUnchanged {
		t.Fatalf("Plan: want one Unchanged item, got %v", items)
	}
	if items[0].Exec != nil {
		t.Error("Unchanged item should have nil Exec")
	}
}

// ── 6. Plan: missing required credential mapping → error PlanItem ────────

func TestConnector_Plan_MissingRequiredCredentialMapping(t *testing.T) {
	fake := newFakeConnectorClient(t)
	// The catalog requires TWO env vars but the manifest only maps one.
	fake.connectors["linear"] = ConnectorRemote{
		ID:                  "linear",
		Installed:           false,
		RequiredCredentials: []string{"LINEAR_API_KEY", "LINEAR_WEBHOOK_SECRET"},
	}

	doc := &ConnectorDocument{
		APIVersion: "crewship/v1",
		Kind:       "Connector",
		Metadata:   internalapi.Metadata{Name: "Linear", Slug: "linear"},
		Spec: ConnectorSpec{
			Install: true,
			Credentials: map[string]string{
				"LINEAR_API_KEY": "LINEAR_PROD_KEY",
				// LINEAR_WEBHOOK_SECRET intentionally missing.
			},
		},
	}
	items, err := doc.Plan(context.Background(), fake, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("Plan: want 1 item, got %d", len(items))
	}
	// Spec says: emit Action=Create with an Exec that returns the
	// error before any HTTP. Verify both halves.
	if items[0].Action != internalapi.ActionCreate {
		t.Errorf("Action: want ActionCreate, got %s", items[0].Action)
	}
	if items[0].Exec == nil {
		t.Fatal("Exec must not be nil for the missing-mapping case")
	}
	preCallLen := len(fake.calls)
	err = items[0].Exec(context.Background(), fake)
	if err == nil {
		t.Fatal("Exec: expected error for missing mapping")
	}
	if !strings.Contains(err.Error(), "LINEAR_WEBHOOK_SECRET") {
		t.Errorf("Exec error %q does not name the missing env var", err.Error())
	}
	if !strings.Contains(err.Error(), "missing credential mapping") {
		t.Errorf("Exec error %q does not mention missing credential mapping", err.Error())
	}
	// The Exec MUST short-circuit before any HTTP call.
	postCallLen := len(fake.calls)
	if postCallLen != preCallLen {
		t.Errorf("Exec made %d HTTP call(s) before returning error; expected zero",
			postCallLen-preCallLen)
	}
}

// ── 7. Plan: workspace credential missing at install time ───────────────

func TestConnector_Plan_WorkspaceCredentialMissing(t *testing.T) {
	fake := newFakeConnectorClient(t)
	fake.connectors["linear"] = ConnectorRemote{
		ID:                  "linear",
		Installed:           false,
		RequiredCredentials: []string{"LINEAR_API_KEY"},
	}
	// Note: NO entry in fake.credentials — the workspace credential
	// referenced by spec.credentials does not exist on the server.

	doc := &ConnectorDocument{
		APIVersion: "crewship/v1",
		Kind:       "Connector",
		Metadata:   internalapi.Metadata{Name: "Linear", Slug: "linear"},
		Spec: ConnectorSpec{
			Install:     true,
			Credentials: map[string]string{"LINEAR_API_KEY": "LINEAR_PROD_KEY"},
		},
	}
	items, err := doc.Plan(context.Background(), fake, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionCreate {
		t.Fatalf("want one Create item, got %v", items)
	}
	err = items[0].Exec(context.Background(), fake)
	if err == nil {
		t.Fatal("Exec: expected error for missing workspace credential")
	}
	if !strings.Contains(err.Error(), "LINEAR_PROD_KEY") {
		t.Errorf("Exec error %q does not name the missing workspace credential", err.Error())
	}
	if fake.findCall("POST", "/install") != nil {
		t.Error("Exec must not POST install when the workspace credential is missing")
	}
}

// ── 8. Plan: uninstall path (best-effort) ────────────────────────────────

func TestConnector_Plan_Uninstall_Success(t *testing.T) {
	fake := newFakeConnectorClient(t)
	fake.connectors["linear"] = ConnectorRemote{
		ID:        "linear",
		Installed: true,
	}

	doc := &ConnectorDocument{
		APIVersion: "crewship/v1",
		Kind:       "Connector",
		Metadata:   internalapi.Metadata{Name: "Linear", Slug: "linear"},
		Spec:       ConnectorSpec{Install: false},
	}
	items, err := doc.Plan(context.Background(), fake, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionDelete {
		t.Fatalf("want one Delete item, got %v", items)
	}
	if err := items[0].Exec(context.Background(), fake); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if fake.findCall("DELETE", "/install") == nil {
		t.Error("Exec did not issue DELETE")
	}
}

func TestConnector_Plan_Uninstall_EndpointMissing(t *testing.T) {
	// When the catalog entry doesn't expose a DELETE verb the kind
	// degrades to a no-op rather than blowing up the whole apply.
	for _, status := range []int{404, 405, 501} {
		t.Run(fmt.Sprintf("status=%d", status), func(t *testing.T) {
			fake := newFakeConnectorClient(t)
			fake.connectors["linear"] = ConnectorRemote{
				ID:        "linear",
				Installed: true,
			}
			fake.deleteEndpointStatus = status

			doc := &ConnectorDocument{
				APIVersion: "crewship/v1",
				Kind:       "Connector",
				Metadata:   internalapi.Metadata{Name: "Linear", Slug: "linear"},
				Spec:       ConnectorSpec{Install: false},
			}
			items, err := doc.Plan(context.Background(), fake, nil)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if err := items[0].Exec(context.Background(), fake); err != nil {
				t.Errorf("Exec: want nil (degraded no-op) for status %d, got %v", status, err)
			}
		})
	}
}

func TestConnector_Plan_AlreadyUninstalled(t *testing.T) {
	fake := newFakeConnectorClient(t)
	fake.connectors["linear"] = ConnectorRemote{
		ID:        "linear",
		Installed: false,
	}
	doc := &ConnectorDocument{
		APIVersion: "crewship/v1",
		Kind:       "Connector",
		Metadata:   internalapi.Metadata{Name: "Linear", Slug: "linear"},
		Spec:       ConnectorSpec{Install: false},
	}
	items, err := doc.Plan(context.Background(), fake, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if items[0].Action != internalapi.ActionUnchanged {
		t.Errorf("want Unchanged, got %s", items[0].Action)
	}
	if items[0].Exec != nil {
		t.Error("Unchanged item should not have an Exec")
	}
}

// ── Plan: connector slug not in catalog ──────────────────────────────────

func TestConnector_Plan_SlugNotInCatalog(t *testing.T) {
	fake := newFakeConnectorClient(t)
	// No catalog entries at all — GET returns 404.
	doc := &ConnectorDocument{
		APIVersion: "crewship/v1",
		Kind:       "Connector",
		Metadata:   internalapi.Metadata{Name: "Bogus", Slug: "does-not-exist"},
		Spec:       ConnectorSpec{Install: true},
	}
	_, err := doc.Plan(context.Background(), fake, nil)
	if err == nil {
		t.Fatal("Plan: expected error for unknown catalog slug")
	}
	if !strings.Contains(err.Error(), "not in the catalog") {
		t.Errorf("Plan: error %q does not flag the catalog miss", err.Error())
	}
}

// ── Plan: caller-supplied remote skips the GET ───────────────────────────

func TestConnector_Plan_RemoteSuppliedSkipsCatalogGET(t *testing.T) {
	fake := newFakeConnectorClient(t)
	fake.credentials["LINEAR_PROD_KEY"] = true
	remote := &ConnectorRemote{
		ID:                  "linear",
		Installed:           false,
		RequiredCredentials: []string{"LINEAR_API_KEY"},
	}
	doc := &ConnectorDocument{
		APIVersion: "crewship/v1",
		Kind:       "Connector",
		Metadata:   internalapi.Metadata{Name: "Linear", Slug: "linear"},
		Spec: ConnectorSpec{
			Install:     true,
			Credentials: map[string]string{"LINEAR_API_KEY": "LINEAR_PROD_KEY"},
		},
	}
	if _, err := doc.Plan(context.Background(), fake, remote); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, c := range fake.calls {
		if c.Method == "GET" && strings.HasPrefix(c.Path, "/api/v1/connectors/") {
			t.Errorf("Plan should not GET catalog detail when remote was supplied; got %s %s", c.Method, c.Path)
		}
	}
}

// ── 9. Export round-trip ─────────────────────────────────────────────────

func TestConnector_Export_OnlyInstalled(t *testing.T) {
	fake := newFakeConnectorClient(t)
	fake.connectors["linear"] = ConnectorRemote{ID: "linear", Installed: true}
	fake.connectors["github"] = ConnectorRemote{ID: "github", Installed: false}
	fake.connectors["slack"] = ConnectorRemote{ID: "slack", Installed: true}

	docs, err := ExportConnectors(context.Background(), fake)
	if err != nil {
		t.Fatalf("ExportConnectors: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("want 2 installed docs, got %d", len(docs))
	}
	gotSlugs := map[string]bool{}
	for _, d := range docs {
		gotSlugs[d.Metadata.Slug] = true
		if !d.Spec.Install {
			t.Errorf("doc %q: Install should be true on export", d.Metadata.Slug)
		}
		if d.APIVersion != "crewship/v1" {
			t.Errorf("doc %q: apiVersion %q != crewship/v1", d.Metadata.Slug, d.APIVersion)
		}
		if d.Kind != "Connector" {
			t.Errorf("doc %q: kind %q != Connector", d.Metadata.Slug, d.Kind)
		}
	}
	if !gotSlugs["linear"] || !gotSlugs["slack"] {
		t.Errorf("export missed slugs: %v", gotSlugs)
	}
	if gotSlugs["github"] {
		t.Error("export emitted an uninstalled connector (github)")
	}
}

func TestConnector_Export_EmptyCatalog(t *testing.T) {
	fake := newFakeConnectorClient(t)
	docs, err := ExportConnectors(context.Background(), fake)
	if err != nil {
		t.Fatalf("ExportConnectors: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("want 0 docs from empty catalog, got %d", len(docs))
	}
}

// ── helpers under test ───────────────────────────────────────────────────

func TestMissingRequiredCredentials(t *testing.T) {
	cases := []struct {
		name     string
		required []string
		mapping  map[string]string
		want     []string
	}{
		{name: "all present", required: []string{"A", "B"}, mapping: map[string]string{"A": "a", "B": "b"}, want: nil},
		{name: "one missing", required: []string{"A", "B"}, mapping: map[string]string{"A": "a"}, want: []string{"B"}},
		{name: "sorted output", required: []string{"Z", "A", "M"}, mapping: nil, want: []string{"A", "M", "Z"}},
		{name: "empty required", required: nil, mapping: map[string]string{"X": "y"}, want: nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := missingRequiredCredentials(tc.required, tc.mapping)
			if len(got) != len(tc.want) {
				t.Fatalf("len mismatch: want %v got %v", tc.want, got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("at %d: want %q got %q", i, tc.want[i], got[i])
				}
			}
		})
	}
}
