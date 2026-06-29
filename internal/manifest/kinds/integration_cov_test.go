package kinds

// Coverage-focused tests for integration.go. Reuses the scriptable
// covClient fake from routine_cov_test.go. integration_test.go owns the
// happy paths with its stateful fake; this file pins error branches:
// transport failures, non-2xx statuses, wrapped/flat decode shapes, the
// scope-change replace path, updatePatch field coverage, and the Export
// skip-on-failure semantics.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// ── status / read helpers ───────────────────────────────────────────

func TestIntegrationCov_CheckStatus(t *testing.T) {
	t.Parallel()

	if err := checkStatus(nil, "op"); err == nil || !strings.Contains(err.Error(), "response is nil") {
		t.Fatalf("nil response: got %v", err)
	}
	if err := checkStatus(&internalapi.Response{StatusCode: 201}, "op"); err != nil {
		t.Fatalf("2xx: got %v", err)
	}
	err := checkStatus(&internalapi.Response{StatusCode: 500, Body: strings.NewReader(" detail ")}, "op")
	if err == nil || !strings.Contains(err.Error(), "unexpected status 500") || !strings.Contains(err.Error(), "detail") {
		t.Fatalf("500: got %v", err)
	}
	if err := checkStatus(&internalapi.Response{StatusCode: 404}, "op"); err == nil || !strings.Contains(err.Error(), "unexpected status 404") {
		t.Fatalf("404 nil body: got %v", err)
	}
}

func TestIntegrationCov_ReadAll(t *testing.T) {
	t.Parallel()

	b, err := readAll(nil)
	if b != nil || err != nil {
		t.Fatalf("nil reader: got (%v,%v)", b, err)
	}
}

// ── path builders ───────────────────────────────────────────────────

func TestIntegrationCov_Paths(t *testing.T) {
	t.Parallel()

	if got := integrationPatchPath(integrationScopeCrew, "c1", "i1"); got != "/api/v1/crews/c1/integrations/i1" {
		t.Fatalf("crew patch path = %q", got)
	}
	if got := integrationPatchPath(integrationScopeWorkspace, "", "i1"); got != "/api/v1/integrations/i1" {
		t.Fatalf("ws patch path = %q", got)
	}
	if got := integrationDeletePath(integrationScopeCrew, "c1", "i1"); got != "/api/v1/crews/c1/integrations/i1" {
		t.Fatalf("crew delete path = %q", got)
	}
	if got := integrationDeletePath(integrationScopeWorkspace, "", "i1"); got != "/api/v1/integrations/i1" {
		t.Fatalf("ws delete path = %q", got)
	}
}

// ── decode helpers ──────────────────────────────────────────────────

func TestIntegrationCov_DecodeList(t *testing.T) {
	t.Parallel()

	t.Run("nil reader", func(t *testing.T) {
		t.Parallel()
		rows, err := integrationDecodeList(nil)
		if rows != nil || err != nil {
			t.Fatalf("got (%v,%v)", rows, err)
		}
	})
	t.Run("read failure", func(t *testing.T) {
		t.Parallel()
		_, err := integrationDecodeList(covErrReader{})
		if err == nil || !strings.Contains(err.Error(), "read integrations list body") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("wrapped shape", func(t *testing.T) {
		t.Parallel()
		rows, err := integrationDecodeList(strings.NewReader(`{"integrations":[{"id":"i1","name":"linear"}]}`))
		if err != nil || len(rows) != 1 || rows[0].Name != "linear" {
			t.Fatalf("got (%v,%v)", rows, err)
		}
	})
	t.Run("invalid both shapes", func(t *testing.T) {
		t.Parallel()
		_, err := integrationDecodeList(strings.NewReader(`"just a string"`))
		if err == nil || !strings.Contains(err.Error(), "decode integrations list") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestIntegrationCov_ListCrews(t *testing.T) {
	t.Parallel()

	t.Run("transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {err: errors.New("down")}})
		_, err := integrationListCrews(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "GET /api/v1/crews") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("bad status", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {status: 500}})
		_, err := integrationListCrews(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "unexpected status 500") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("read failure", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {badBody: true}})
		_, err := integrationListCrews(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "read /api/v1/crews body") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("empty body", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: ""}})
		rows, err := integrationListCrews(context.Background(), c)
		if rows != nil || err != nil {
			t.Fatalf("got (%v,%v)", rows, err)
		}
	})
	t.Run("wrapped shape", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `{"crews":[{"id":"c1","slug":"eng"}]}`}})
		rows, err := integrationListCrews(context.Background(), c)
		if err != nil || len(rows) != 1 || rows[0].Slug != "eng" {
			t.Fatalf("got (%v,%v)", rows, err)
		}
	})
	t.Run("invalid both shapes", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `42`}})
		_, err := integrationListCrews(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "decode /api/v1/crews") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestIntegrationCov_Listers_Errors(t *testing.T) {
	t.Parallel()

	t.Run("workspace transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/integrations": {err: errors.New("down")}})
		_, err := integrationListWorkspace(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "GET /api/v1/integrations") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("workspace bad status", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/integrations": {status: 503}})
		_, err := integrationListWorkspace(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "unexpected status 503") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("crew transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews/c1/integrations": {err: errors.New("down")}})
		_, err := integrationListCrew(context.Background(), c, "c1")
		if err == nil || !strings.Contains(err.Error(), "GET /api/v1/crews/c1/integrations") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("crew bad status", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews/c1/integrations": {status: 500}})
		_, err := integrationListCrew(context.Background(), c, "c1")
		if err == nil || !strings.Contains(err.Error(), "unexpected status 500") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("crew decode error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews/c1/integrations": {body: "true"}})
		_, err := integrationListCrew(context.Background(), c, "c1")
		if err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("crew stamps crew id", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews/c1/integrations": {body: `[{"id":"i1","name":"linear"},{"id":"i2","name":"jira","crew_id":"already"}]`},
		})
		rows, err := integrationListCrew(context.Background(), c, "c1")
		if err != nil || len(rows) != 2 {
			t.Fatalf("got (%v,%v)", rows, err)
		}
		if rows[0].CrewID != "c1" {
			t.Fatalf("missing crew id not stamped: %+v", rows[0])
		}
		if rows[1].CrewID != "already" {
			t.Fatalf("existing crew id overwritten: %+v", rows[1])
		}
	})
}

// ── lookups ─────────────────────────────────────────────────────────

func TestIntegrationCov_LookupRemoteBySlug(t *testing.T) {
	t.Parallel()

	t.Run("empty slug", func(t *testing.T) {
		t.Parallel()
		_, err := LookupIntegrationRemoteBySlug(context.Background(), newCovClient(nil), " ", "", "")
		if err == nil || !strings.Contains(err.Error(), "integration slug is required") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("unknown scope", func(t *testing.T) {
		t.Parallel()
		_, err := LookupIntegrationRemoteBySlug(context.Background(), newCovClient(nil), "linear", "galaxy", "")
		if err == nil || !strings.Contains(err.Error(), `unknown scope "galaxy"`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("default scope is workspace", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/integrations": {body: `[{"id":"i1","name":"linear"}]`}})
		row, err := LookupIntegrationRemoteBySlug(context.Background(), c, "linear", "", "")
		if err != nil || row == nil {
			t.Fatalf("got (%v,%v)", row, err)
		}
		if row.Scope != integrationScopeWorkspace || row.ID != "i1" {
			t.Fatalf("row = %+v", row)
		}
	})
	t.Run("workspace not found", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/integrations": {body: `[]`}})
		row, err := LookupIntegrationRemoteBySlug(context.Background(), c, "linear", integrationScopeWorkspace, "")
		if row != nil || err != nil {
			t.Fatalf("got (%v,%v)", row, err)
		}
	})
	t.Run("workspace list error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/integrations": {status: 500}})
		_, err := LookupIntegrationRemoteBySlug(context.Background(), c, "linear", integrationScopeWorkspace, "")
		if err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("crew scope requires crew slug", func(t *testing.T) {
		t.Parallel()
		_, err := LookupIntegrationRemoteBySlug(context.Background(), newCovClient(nil), "linear", integrationScopeCrew, "")
		if err == nil || !strings.Contains(err.Error(), "crew_slug is required") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("crew scope crew lookup fails", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `[]`}})
		_, err := LookupIntegrationRemoteBySlug(context.Background(), c, "linear", integrationScopeCrew, "eng")
		if err == nil || !strings.Contains(err.Error(), `crew with slug "eng" not found`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("crew scope list fails", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":                 {body: `[{"id":"c1","slug":"eng"}]`},
			"GET /api/v1/crews/c1/integrations": {status: 500},
		})
		_, err := LookupIntegrationRemoteBySlug(context.Background(), c, "linear", integrationScopeCrew, "eng")
		if err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("crew scope found with scope and crew id stamped", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":                 {body: `[{"id":"c1","slug":"eng"}]`},
			"GET /api/v1/crews/c1/integrations": {body: `[{"id":"i9","name":"linear"}]`},
		})
		row, err := LookupIntegrationRemoteBySlug(context.Background(), c, "linear", integrationScopeCrew, "eng")
		if err != nil || row == nil {
			t.Fatalf("got (%v,%v)", row, err)
		}
		if row.Scope != integrationScopeCrew || row.CrewID != "c1" || row.ID != "i9" {
			t.Fatalf("row = %+v", row)
		}
	})
	t.Run("crew scope not found", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":                 {body: `[{"id":"c1","slug":"eng"}]`},
			"GET /api/v1/crews/c1/integrations": {body: `[]`},
		})
		row, err := LookupIntegrationRemoteBySlug(context.Background(), c, "linear", integrationScopeCrew, "eng")
		if row != nil || err != nil {
			t.Fatalf("got (%v,%v)", row, err)
		}
	})
}

// ── Plan paths ──────────────────────────────────────────────────────

func integrationCovDoc() *IntegrationDocument {
	return &IntegrationDocument{
		APIVersion: integrationAPIVersion,
		Kind:       integrationKind,
		Metadata:   internalapi.Metadata{Name: "linear", Slug: "linear"},
		Spec: IntegrationSpec{
			Transport: integrationTransportHTTP,
			Endpoint:  "https://mcp.linear.app/mcp",
		},
	}
}

func TestIntegrationCov_Plan_CrewResolveFails(t *testing.T) {
	t.Parallel()

	doc := integrationCovDoc()
	doc.Spec.Scope = integrationScopeCrew
	doc.Spec.CrewSlug = "ghost"
	c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `[]`}})
	_, err := doc.Plan(context.Background(), c, nil)
	if err == nil || !strings.Contains(err.Error(), "resolve crew_slug") {
		t.Fatalf("got %v", err)
	}
}

func TestIntegrationCov_Plan_ScopeChangeReplace(t *testing.T) {
	t.Parallel()

	// Remote lives on crew scope; manifest declares workspace scope →
	// Plan must emit Delete (crew row) + Create (workspace row).
	doc := integrationCovDoc()
	remote := &IntegrationRemote{
		ID:     "i1",
		Name:   "linear",
		CrewID: "c1",
		Scope:  integrationScopeCrew,
	}
	c := newCovClient(map[string]covRoute{
		"DELETE /api/v1/crews/c1/integrations/i1": {status: 200, body: "{}"},
		"POST /api/v1/integrations":               {status: 201, body: `{"id":"i2"}`},
	})
	items, err := doc.Plan(context.Background(), c, remote)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want delete+create pair, got %d items", len(items))
	}
	if items[0].Action != internalapi.ActionDelete || !strings.Contains(items[0].Description, "scope change crew → workspace") {
		t.Fatalf("item0 = %+v", items[0])
	}
	if items[1].Action != internalapi.ActionCreate {
		t.Fatalf("item1 = %+v", items[1])
	}
	if err := items[0].Exec(context.Background(), c); err != nil {
		t.Fatalf("delete exec: %v", err)
	}
	if err := items[1].Exec(context.Background(), c); err != nil {
		t.Fatalf("create exec: %v", err)
	}
	if !c.sawCall("DELETE /api/v1/crews/c1/integrations/i1") || !c.sawCall("POST /api/v1/integrations") {
		t.Fatalf("calls = %v", c.calls)
	}
}

func TestIntegrationCov_DeleteItem_ExecErrors(t *testing.T) {
	t.Parallel()

	remote := &IntegrationRemote{ID: "i1", Name: "linear", Scope: integrationScopeWorkspace}
	item := integrationDeleteItem(remote, "test reason")
	if !strings.Contains(item.Description, "test reason") {
		t.Fatalf("description = %q", item.Description)
	}

	t.Run("transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"DELETE /api/v1/integrations/i1": {err: errors.New("down")}})
		if err := item.Exec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "DELETE /api/v1/integrations/i1") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("bad status", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"DELETE /api/v1/integrations/i1": {status: 409, body: "in use"}})
		if err := item.Exec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "unexpected status 409") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestIntegrationCov_Plan_CreateExec_CrewScope(t *testing.T) {
	t.Parallel()

	doc := integrationCovDoc()
	doc.Spec.Scope = integrationScopeCrew
	doc.Spec.CrewSlug = "eng"
	c := newCovClient(map[string]covRoute{
		"GET /api/v1/crews":                  {body: `[{"id":"c1","slug":"eng"}]`},
		"POST /api/v1/crews/c1/integrations": {err: errors.New("down")},
	})
	items, err := doc.Plan(context.Background(), c, nil)
	if err != nil || len(items) != 1 {
		t.Fatalf("plan: items=%v err=%v", items, err)
	}
	if !strings.Contains(items[0].Description, "crew=eng") {
		t.Fatalf("description = %q", items[0].Description)
	}
	if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "POST /api/v1/crews/c1/integrations") {
		t.Fatalf("exec: got %v", execErr)
	}
}

func TestIntegrationCov_Plan_UpdateExec_Errors(t *testing.T) {
	t.Parallel()

	doc := integrationCovDoc()
	doc.Spec.DisplayName = "Linear (new)"
	remote := &IntegrationRemote{
		ID:          "i1",
		Name:        "linear",
		DisplayName: "Linear",
		Transport:   integrationTransportHTTP,
		Scope:       integrationScopeWorkspace,
	}

	t.Run("patch transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"PATCH /api/v1/integrations/i1": {err: errors.New("down")}})
		items, err := doc.Plan(context.Background(), c, remote)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
			t.Fatalf("plan: items=%v err=%v", items, err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "PATCH /api/v1/integrations/i1") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
	t.Run("patch bad status", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"PATCH /api/v1/integrations/i1": {status: 400, body: "bad"}})
		items, err := doc.Plan(context.Background(), c, remote)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "unexpected status 400") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
}

// ── createBody field coverage ───────────────────────────────────────

func TestIntegrationCov_CreateBody_AllFields(t *testing.T) {
	t.Parallel()

	enabled := false
	doc := &IntegrationDocument{
		Metadata: internalapi.Metadata{Name: "tool", Slug: "tool"},
		Spec: IntegrationSpec{
			Transport:  integrationTransportStdio,
			Command:    "npx",
			Args:       []string{"-y", "@mcp/x"},
			Env:        map[string]string{"NODE_ENV": "production"},
			EnvMapping: map[string]string{"TOKEN": "GH_TOKEN", "NODE_ENV": "ignored-loses-to-env"},
			Icon:       "wrench",
			Enabled:    &enabled,
		},
	}
	body, err := doc.createBody()
	if err != nil {
		t.Fatalf("createBody: %v", err)
	}
	if body["name"] != "tool" || body["transport"] != integrationTransportStdio || body["command"] != "npx" {
		t.Fatalf("body = %v", body)
	}
	if body["display_name"] != "tool" {
		t.Fatalf("display_name must default to name: %v", body["display_name"])
	}
	if body["args_json"] != `["-y","@mcp/x"]` {
		t.Fatalf("args_json = %v", body["args_json"])
	}
	envJSON, _ := body["env_json"].(string)
	if !strings.Contains(envJSON, `"NODE_ENV":"production"`) || !strings.Contains(envJSON, `"TOKEN":"GH_TOKEN"`) {
		t.Fatalf("env_json = %q (env literal must win on collision)", envJSON)
	}
	if body["icon"] != "wrench" || body["enabled"] != false {
		t.Fatalf("icon/enabled = %v / %v", body["icon"], body["enabled"])
	}
	em, _ := body["env_mapping"].(map[string]string)
	if em == nil || em["TOKEN"] != "GH_TOKEN" {
		t.Fatalf("env_mapping = %v", body["env_mapping"])
	}
	if _, hasEndpoint := body["endpoint"]; hasEndpoint {
		t.Fatalf("stdio body must not carry endpoint: %v", body)
	}
}

func TestIntegrationCov_LookupCrewIDBySlug_ListError(t *testing.T) {
	t.Parallel()

	c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {status: 500}})
	_, err := integrationLookupCrewIDBySlug(context.Background(), c, "eng")
	if err == nil || !strings.Contains(err.Error(), "unexpected status 500") {
		t.Fatalf("got %v", err)
	}
}

// ── updatePatch field coverage ──────────────────────────────────────

func TestIntegrationCov_UpdatePatch_AllFields(t *testing.T) {
	t.Parallel()

	endpoint := "https://old.example/mcp"
	command := "old-cmd"
	argsJSON := `["--old"]`
	envJSON := `{"K":"old"}`
	icon := "old-icon"

	t.Run("stdio drift on every field", func(t *testing.T) {
		t.Parallel()
		enabled := false
		doc := &IntegrationDocument{
			Metadata: internalapi.Metadata{Name: "tool", Slug: "tool"},
			Spec: IntegrationSpec{
				Transport:   integrationTransportStdio,
				DisplayName: "Tool v2",
				Command:     "new-cmd",
				Args:        []string{"--new"},
				Env:         map[string]string{"K": "new"},
				Icon:        "new-icon",
				Enabled:     &enabled,
			},
		}
		remote := &IntegrationRemote{
			DisplayName: "Tool",
			Transport:   integrationTransportHTTP,
			Endpoint:    &endpoint,
			Command:     &command,
			ArgsJSON:    &argsJSON,
			EnvJSON:     &envJSON,
			Icon:        &icon,
			Enabled:     true,
		}
		patch, err := doc.updatePatch(remote)
		if err != nil {
			t.Fatalf("updatePatch: %v", err)
		}
		want := map[string]any{
			"display_name": "Tool v2",
			"transport":    integrationTransportStdio,
			"command":      "new-cmd",
			"args_json":    `["--new"]`,
			"env_json":     `{"K":"new"}`,
			"icon":         "new-icon",
			"enabled":      false,
		}
		for k, v := range want {
			if patch[k] != v {
				t.Errorf("patch[%q] = %v, want %v", k, patch[k], v)
			}
		}
		if len(patch) != len(want) {
			t.Errorf("patch has %d keys, want %d: %v", len(patch), len(want), patch)
		}
	})

	t.Run("http endpoint drift", func(t *testing.T) {
		t.Parallel()
		doc := &IntegrationDocument{
			Metadata: internalapi.Metadata{Name: "tool", Slug: "tool"},
			Spec: IntegrationSpec{
				Transport: integrationTransportHTTP,
				Endpoint:  "https://new.example/mcp",
			},
		}
		remote := &IntegrationRemote{
			DisplayName: "tool",
			Transport:   integrationTransportHTTP,
			Endpoint:    &endpoint,
			Enabled:     true,
		}
		patch, err := doc.updatePatch(remote)
		if err != nil {
			t.Fatalf("updatePatch: %v", err)
		}
		if patch["endpoint"] != "https://new.example/mcp" || len(patch) != 1 {
			t.Fatalf("patch = %v", patch)
		}
	})

	t.Run("no drift produces empty patch", func(t *testing.T) {
		t.Parallel()
		doc := &IntegrationDocument{
			Metadata: internalapi.Metadata{Name: "tool", Slug: "tool"},
			Spec: IntegrationSpec{
				Transport: integrationTransportStdio,
				Command:   "old-cmd",
				Args:      []string{"--old"},
				Env:       map[string]string{"K": "old"},
				Icon:      "old-icon",
			},
		}
		remote := &IntegrationRemote{
			DisplayName: "tool", // matches metadata default? display unset → skipped
			Transport:   integrationTransportStdio,
			Command:     &command,
			ArgsJSON:    &argsJSON,
			EnvJSON:     &envJSON,
			Icon:        &icon,
			Enabled:     true,
		}
		patch, err := doc.updatePatch(remote)
		if err != nil {
			t.Fatalf("updatePatch: %v", err)
		}
		if len(patch) != 0 {
			t.Fatalf("want empty patch, got %v", patch)
		}
	})
}

// ── Export ──────────────────────────────────────────────────────────

func TestIntegrationCov_Export(t *testing.T) {
	t.Parallel()

	t.Run("workspace list fails", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/integrations": {status: 500}})
		_, err := ExportIntegrations(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "export integrations: list workspace") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("crews list fails", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/integrations": {body: `[]`},
			"GET /api/v1/crews":        {status: 500},
		})
		_, err := ExportIntegrations(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "export integrations: list crews") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("per-crew failure skips crew", func(t *testing.T) {
		t.Parallel()
		args := `["-y","@mcp/x"]`
		env := `{"TOKEN":"GH_TOKEN"}`
		_ = args
		_ = env
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/integrations":           {body: `[{"id":"i1","name":"linear","display_name":"Linear","transport":"streamable-http","endpoint":"https://x","enabled":true}]`},
			"GET /api/v1/crews":                  {body: `[{"id":"bad","slug":"broken"},{"id":"c1","slug":"eng"}]`},
			"GET /api/v1/crews/bad/integrations": {status: 500},
			"GET /api/v1/crews/c1/integrations":  {body: `[{"id":"i2","name":"jira","display_name":"Jira","transport":"stdio","command":"npx","args_json":"[\"-y\",\"@mcp/x\"]","env_json":"{\"TOKEN\":\"GH_TOKEN\"}","enabled":false}]`},
		})
		docs, err := ExportIntegrations(context.Background(), c)
		if err != nil {
			t.Fatalf("export: %v", err)
		}
		if len(docs) != 2 {
			t.Fatalf("want 2 docs (broken crew skipped), got %d", len(docs))
		}
		// Sorted: crew scope ("crew") < workspace scope ("workspace").
		if docs[0].Spec.Scope != integrationScopeCrew || docs[0].Metadata.Slug != "jira" {
			t.Fatalf("docs[0] = %+v", docs[0])
		}
		if docs[0].Spec.CrewSlug != "eng" || docs[0].Spec.Command != "npx" {
			t.Fatalf("crew doc spec = %+v", docs[0].Spec)
		}
		if len(docs[0].Spec.Args) != 2 || docs[0].Spec.Args[0] != "-y" {
			t.Fatalf("args not decoded: %v", docs[0].Spec.Args)
		}
		if docs[0].Spec.Env["TOKEN"] != "GH_TOKEN" {
			t.Fatalf("env not decoded: %v", docs[0].Spec.Env)
		}
		if docs[0].Spec.Enabled == nil || *docs[0].Spec.Enabled {
			t.Fatalf("enabled pointer = %v", docs[0].Spec.Enabled)
		}
		if docs[1].Spec.Scope != integrationScopeWorkspace || docs[1].Metadata.Slug != "linear" {
			t.Fatalf("docs[1] = %+v", docs[1])
		}
		if docs[1].Spec.Endpoint != "https://x" {
			t.Fatalf("ws doc endpoint = %q", docs[1].Spec.Endpoint)
		}
	})
	t.Run("sort clusters crews and orders slugs", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/integrations": {body: `[
				{"id":"w2","name":"zeta","display_name":"Z","transport":"streamable-http","endpoint":"https://z","enabled":true},
				{"id":"w1","name":"alpha","display_name":"A","transport":"streamable-http","endpoint":"https://a","enabled":true}
			]`},
			"GET /api/v1/crews":                 {body: `[{"id":"c2","slug":"ops"},{"id":"c1","slug":"eng"}]`},
			"GET /api/v1/crews/c1/integrations": {body: `[{"id":"i1","name":"linear","display_name":"L","transport":"streamable-http","endpoint":"https://l","enabled":true}]`},
			"GET /api/v1/crews/c2/integrations": {body: `[{"id":"i2","name":"jira","display_name":"J","transport":"streamable-http","endpoint":"https://j","enabled":true}]`},
		})
		docs, err := ExportIntegrations(context.Background(), c)
		if err != nil || len(docs) != 4 {
			t.Fatalf("got (%d docs, %v)", len(docs), err)
		}
		gotOrder := make([]string, 0, 4)
		for _, d := range docs {
			gotOrder = append(gotOrder, d.Spec.Scope+"/"+d.Spec.CrewSlug+"/"+d.Metadata.Slug)
		}
		wantOrder := []string{
			"crew/eng/linear", // crew scope sorts before workspace; crews cluster by slug
			"crew/ops/jira",
			"workspace//alpha", // within workspace scope, slug order
			"workspace//zeta",
		}
		for i := range wantOrder {
			if gotOrder[i] != wantOrder[i] {
				t.Fatalf("order = %v, want %v", gotOrder, wantOrder)
			}
		}
	})
}
