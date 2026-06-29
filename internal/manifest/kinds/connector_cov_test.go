package kinds

// Coverage-focused tests for connector.go: fetchConnector branches,
// credential listing shape tolerance, export error paths, and the
// install/uninstall Exec closures.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

func connectorCovDoc(install bool) ConnectorDocument {
	return ConnectorDocument{
		APIVersion: connectorAPIVersion,
		Kind:       connectorKind,
		Metadata:   internalapi.Metadata{Name: "Linear", Slug: "linear"},
		Spec: ConnectorSpec{
			Install:     install,
			Credentials: map[string]string{"LINEAR_API_KEY": "LINEAR_PROD_KEY"},
		},
	}
}

func TestConnectorCov_FetchConnector(t *testing.T) {
	t.Parallel()
	path := "/api/v1/connectors/linear"

	t.Run("transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {err: errors.New("down")}})
		if _, err := fetchConnector(context.Background(), c, "linear"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("nil response", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {nilResp: true}})
		if _, err := fetchConnector(context.Background(), c, "linear"); err == nil || !strings.Contains(err.Error(), "nil response") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("404 → not in catalog", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {status: 404, body: `{}`}})
		if _, err := fetchConnector(context.Background(), c, "linear"); err == nil || !strings.Contains(err.Error(), "not in the catalog") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("non-2xx with body", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {status: 500, body: "boom"}})
		_, err := fetchConnector(context.Background(), c, "linear")
		if err == nil || !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("body read failure", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {badBody: true}})
		if _, err := fetchConnector(context.Background(), c, "linear"); err == nil || !strings.Contains(err.Error(), "read") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("decode error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: "not json"}})
		if _, err := fetchConnector(context.Background(), c, "linear"); err == nil || !strings.Contains(err.Error(), "decode") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("slug normalised from id", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `{"id":"linear","installed":true,"required_credentials":["LINEAR_API_KEY"]}`}})
		out, err := fetchConnector(context.Background(), c, "linear")
		if err != nil || out == nil {
			t.Fatalf("out=%v err=%v", out, err)
		}
		if out.Slug != "linear" || !out.Installed || len(out.RequiredCredentials) != 1 {
			t.Errorf("out = %+v", out)
		}
	})
}

func TestConnectorCov_ListCredentialNames(t *testing.T) {
	t.Parallel()
	path := "/api/v1/credentials"

	t.Run("transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {err: errors.New("down")}})
		if _, err := listCredentialNames(context.Background(), c); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("non-2xx", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {status: 500, body: "x"}})
		if _, err := listCredentialNames(context.Background(), c); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("bad body", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {badBody: true}})
		if _, err := listCredentialNames(context.Background(), c); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("flat array", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `[{"name":"LINEAR_PROD_KEY"}]`}})
		out, err := listCredentialNames(context.Background(), c)
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if _, ok := out["LINEAR_PROD_KEY"]; !ok {
			t.Errorf("out = %v", out)
		}
	})
	t.Run("wrapped object", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `{"credentials":[{"name":"LINEAR_PROD_KEY"}]}`}})
		out, err := listCredentialNames(context.Background(), c)
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if _, ok := out["LINEAR_PROD_KEY"]; !ok {
			t.Errorf("out = %v", out)
		}
	})
	t.Run("undecodable", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `42`}})
		if _, err := listCredentialNames(context.Background(), c); err == nil || !strings.Contains(err.Error(), "decode /api/v1/credentials") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestConnectorCov_AssertCredentialsExist(t *testing.T) {
	t.Parallel()
	mapping := map[string]string{"LINEAR_API_KEY": "LINEAR_PROD_KEY"}

	t.Run("empty mapping makes no calls", func(t *testing.T) {
		c := newCovClient(nil)
		if err := assertCredentialsExist(context.Background(), c, nil); err != nil {
			t.Fatalf("got %v", err)
		}
		if len(c.calls) != 0 {
			t.Errorf("calls = %v", c.calls)
		}
	})
	t.Run("list error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET /api/v1/credentials": {err: errors.New("down")}})
		if err := assertCredentialsExist(context.Background(), c, mapping); err == nil || !strings.Contains(err.Error(), "list workspace credentials") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("missing credential", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET /api/v1/credentials": {body: `[{"name":"OTHER"}]`}})
		err := assertCredentialsExist(context.Background(), c, mapping)
		if err == nil || !strings.Contains(err.Error(), `"LINEAR_PROD_KEY"`) || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("all present", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET /api/v1/credentials": {body: `[{"name":"LINEAR_PROD_KEY"}]`}})
		if err := assertCredentialsExist(context.Background(), c, mapping); err != nil {
			t.Fatalf("got %v", err)
		}
	})
}

func TestConnectorCov_CheckStatusAndIDForSlug(t *testing.T) {
	t.Parallel()
	if err := connectorCheckStatus(nil, "op"); err == nil || !strings.Contains(err.Error(), "nil response") {
		t.Errorf("nil: %v", err)
	}
	err := connectorCheckStatus(&internalapi.Response{StatusCode: 500, Body: strings.NewReader("why")}, "op")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "why") {
		t.Errorf("500: %v", err)
	}
	if err := connectorCheckStatus(&internalapi.Response{StatusCode: 200}, "op"); err != nil {
		t.Errorf("200: %v", err)
	}

	if got := idForSlug(ConnectorRemote{ID: "id", Slug: "slug"}); got != "slug" {
		t.Errorf("slug pref: %q", got)
	}
	if got := idForSlug(ConnectorRemote{ID: "id"}); got != "id" {
		t.Errorf("id fallback: %q", got)
	}
	if b, err := readAll(nil); b != nil || err != nil {
		t.Errorf("readAll(nil) = %v, %v", b, err)
	}
}

func TestConnectorCov_ExportConnectors(t *testing.T) {
	t.Parallel()
	list := "/api/v1/connectors"

	t.Run("transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + list: {err: errors.New("down")}})
		if _, err := ExportConnectors(context.Background(), c); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("non-2xx", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + list: {status: 500, body: "x"}})
		if _, err := ExportConnectors(context.Background(), c); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("undecodable both shapes", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + list: {body: `42`}})
		if _, err := ExportConnectors(context.Background(), c); err == nil || !strings.Contains(err.Error(), "decode connectors") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("wrapped shape, uninstalled rows skipped", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET " + list: {body: `{"connectors":[
				{"id":"linear","installed":true},
				{"id":"github","installed":false}
			]}`},
			"GET /api/v1/connectors/linear": {body: `{"id":"linear","installed":true}`},
		})
		docs, err := ExportConnectors(context.Background(), c)
		if err != nil || len(docs) != 1 {
			t.Fatalf("docs=%v err=%v", docs, err)
		}
		d := docs[0]
		if d.Metadata.Slug != "linear" || !d.Spec.Install {
			t.Errorf("doc = %+v", d)
		}
	})
	t.Run("detail fetch failure → shell doc", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET " + list:                   {body: `[{"id":"linear","installed":true}]`},
			"GET /api/v1/connectors/linear": {status: 500, body: "x"},
		})
		docs, err := ExportConnectors(context.Background(), c)
		if err != nil || len(docs) != 1 || docs[0].Metadata.Slug != "linear" {
			t.Fatalf("docs=%v err=%v", docs, err)
		}
	})
}

func TestConnectorCov_Plan_InstallExec(t *testing.T) {
	t.Parallel()
	remote := &ConnectorRemote{ID: "linear", Slug: "linear", Installed: false,
		RequiredCredentials: []string{"LINEAR_API_KEY"}}

	t.Run("missing credential aborts before POST", func(t *testing.T) {
		d := connectorCovDoc(true)
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/credentials": {body: `[{"name":"OTHER"}]`},
		})
		items, err := d.Plan(context.Background(), c, remote)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionCreate {
			t.Fatalf("items=%+v err=%v", items, err)
		}
		if err := items[0].Exec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("got %v", err)
		}
		if c.sawCall("POST /api/v1/connectors/linear/install") {
			t.Error("install POST should not have run")
		}
	})
	t.Run("install POST 500", func(t *testing.T) {
		d := connectorCovDoc(true)
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/credentials":                {body: `[{"name":"LINEAR_PROD_KEY"}]`},
			"POST /api/v1/connectors/linear/install": {status: 500, body: "boom"},
		})
		items, err := d.Plan(context.Background(), c, remote)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if err := items[0].Exec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("unmapped required credential plans an erroring item", func(t *testing.T) {
		d := connectorCovDoc(true)
		d.Spec.Credentials = nil
		c := newCovClient(nil)
		items, err := d.Plan(context.Background(), c, remote)
		if err != nil || len(items) != 1 {
			t.Fatalf("items=%+v err=%v", items, err)
		}
		if err := items[0].Exec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "missing credential mapping") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestConnectorCov_Plan_UninstallExec(t *testing.T) {
	t.Parallel()
	remote := &ConnectorRemote{ID: "linear", Slug: "linear", Installed: true}
	path := "DELETE /api/v1/connectors/linear/install"

	planDelete := func(t *testing.T, c *covClient) internalapi.PlanItem {
		t.Helper()
		d := connectorCovDoc(false)
		items, err := d.Plan(context.Background(), c, remote)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionDelete {
			t.Fatalf("items=%+v err=%v", items, err)
		}
		return items[0]
	}

	t.Run("404 tolerated as not-implemented", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{path: {status: 404, body: `{}`}})
		if err := planDelete(t, c).Exec(context.Background(), c); err != nil {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("405 tolerated", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{path: {status: 405, body: `{}`}})
		if err := planDelete(t, c).Exec(context.Background(), c); err != nil {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("500 fails", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{path: {status: 500, body: "boom"}})
		if err := planDelete(t, c).Exec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{path: {err: errors.New("down")}})
		if err := planDelete(t, c).Exec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "down") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("nil response", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{path: {nilResp: true}})
		if err := planDelete(t, c).Exec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "nil response") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestConnectorCov_Plan_FetchRemoteWhenNil(t *testing.T) {
	t.Parallel()
	d := connectorCovDoc(true)
	c := newCovClient(map[string]covRoute{
		"GET /api/v1/connectors/linear": {status: 404, body: `{}`},
	})
	if _, err := d.Plan(context.Background(), c, nil); err == nil || !strings.Contains(err.Error(), "fetch catalog entry") {
		t.Fatalf("got %v", err)
	}
}
