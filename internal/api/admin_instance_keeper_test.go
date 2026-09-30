package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

// Admin › Security across workspaces. The instance admin ("boss") belongs to
// ws-old only; ws-new is carol's. Everything here must reach ws-new anyway —
// Admin does not depend on the workspace its user happens to sit in.

type govListBody struct {
	Defaults struct {
		Configured bool `json:"configured"`
		Enabled    bool `json:"enabled"`
	} `json:"defaults"`
	Workspaces []struct {
		WorkspaceID   string `json:"workspace_id"`
		WorkspaceName string `json:"workspace_name"`
		Configured    bool   `json:"configured"`
		Enabled       bool   `json:"enabled"`
		DenyMinRisk   int    `json:"deny_notify_min_risk"`
	} `json:"workspaces"`
}

type govPutResp struct {
	Applied         bool `json:"applied"`
	Changed         int  `json:"changed"`
	DefaultsUpdated bool `json:"defaults_updated"`
	Workspaces      []struct {
		WorkspaceID string `json:"workspace_id"`
		Changes     []struct {
			Field  string `json:"field"`
			Before any    `json:"before"`
			After  any    `json:"after"`
		} `json:"changes"`
	} `json:"workspaces"`
}

func (f *instanceFixture) gov(ws string) (governance.Settings, bool) {
	f.t.Helper()
	s, found, err := governance.Get(context.Background(), f.db, ws)
	if err != nil {
		f.t.Fatalf("governance.Get: %v", err)
	}
	return s, found
}

func decodeAs[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	return v
}

func TestInstanceKeeperGovernanceIsForInstanceAdminsOnly(t *testing.T) {
	f := newInstanceFixture(t)
	wantCode(t, f.do(f.wsAdmin, "GET", "/api/v1/admin/instance/keeper/governance", ""), http.StatusForbidden, "workspace ADMIN reads")
	wantCode(t, f.do(f.wsAdmin, "PUT", "/api/v1/admin/instance/keeper/governance", `{"all":true,"set":{"enabled":true}}`), http.StatusForbidden, "workspace ADMIN writes")
	if _, found := f.gov("ws-old"); found {
		t.Fatal("a refused write left a row")
	}
}

func TestInstanceKeeperGovernanceListsEveryWorkspace(t *testing.T) {
	f := newInstanceFixture(t)
	if err := governance.Upsert(context.Background(), f.db, "ws-new", governance.Settings{Enabled: true, DenyNotifyMinRisk: 4}, ""); err != nil {
		t.Fatal(err)
	}
	rr := f.do(f.boss, "GET", "/api/v1/admin/instance/keeper/governance", "")
	wantCode(t, rr, http.StatusOK, "list")
	got := decodeAs[govListBody](t, rr.Body.Bytes())
	seen := map[string]bool{}
	for _, w := range got.Workspaces {
		seen[w.WorkspaceID] = true
		if w.WorkspaceID == "ws-new" && (!w.Configured || !w.Enabled || w.DenyMinRisk != 4 || w.WorkspaceName == "") {
			t.Fatalf("ws-new row = %+v, want its own settings and name", w)
		}
		if w.WorkspaceID == "ws-old" && (w.Configured || w.Enabled) {
			t.Fatalf("ws-old row = %+v, want unconfigured and off", w)
		}
	}
	if !seen["ws-old"] || !seen["ws-new"] {
		t.Fatalf("workspaces = %v, want both, including one the admin is not a member of", seen)
	}
	if got.Defaults.Configured {
		t.Fatal("defaults configured on a fresh instance")
	}
}

func TestInstanceKeeperGovernanceEditsOneWorkspaceTheAdminIsNotIn(t *testing.T) {
	f := newInstanceFixture(t)
	rr := f.do(f.boss, "PUT", "/api/v1/admin/instance/keeper/governance", `{"workspaces":["ws-new"],"set":{"enabled":true,"behavior_sample_every":10}}`)
	wantCode(t, rr, http.StatusOK, "single write")
	resp := decodeAs[govPutResp](t, rr.Body.Bytes())
	if !resp.Applied || resp.Changed != 1 || resp.DefaultsUpdated {
		t.Fatalf("resp = %+v, want applied to one workspace and defaults untouched", resp)
	}
	s, found := f.gov("ws-new")
	if !found || !s.Enabled || s.BehaviorSampleEvery != 10 {
		t.Fatalf("ws-new = %+v (found %v)", s, found)
	}
	if _, found := f.gov("ws-old"); found {
		t.Fatal("a one-workspace save wrote another workspace")
	}
	if !f.audited("instance.keeper_governance_updated") {
		t.Fatal("no instance audit entry")
	}
}

func TestInstanceKeeperGovernanceDryRunWritesNothing(t *testing.T) {
	f := newInstanceFixture(t)
	rr := f.do(f.boss, "PUT", "/api/v1/admin/instance/keeper/governance", `{"all":true,"dry_run":true,"set":{"enabled":true}}`)
	wantCode(t, rr, http.StatusOK, "dry run")
	resp := decodeAs[govPutResp](t, rr.Body.Bytes())
	if resp.Applied || resp.Changed < 2 {
		t.Fatalf("resp = %+v, want a preview naming every workspace that would change", resp)
	}
	for _, w := range resp.Workspaces {
		if len(w.Changes) != 1 || w.Changes[0].Field != "enabled" || w.Changes[0].Before != false || w.Changes[0].After != true {
			t.Fatalf("%s changes = %+v, want enabled false → true", w.WorkspaceID, w.Changes)
		}
	}
	if _, found := f.gov("ws-old"); found {
		t.Fatal("a dry run wrote a row")
	}
	if _, found, _ := governance.Defaults(context.Background(), f.db); found {
		t.Fatal("a dry run wrote the defaults")
	}
	if f.audited("instance.keeper_governance_updated") {
		t.Fatal("a dry run was audited as a change")
	}
}

func TestInstanceKeeperGovernanceAllOverwritesEveryWorkspaceAndSeedsNewOnes(t *testing.T) {
	f := newInstanceFixture(t)
	if err := governance.Upsert(context.Background(), f.db, "ws-new", governance.Settings{Enabled: false, DenyNotifyMinRisk: 9}, ""); err != nil {
		t.Fatal(err)
	}
	rr := f.do(f.boss, "PUT", "/api/v1/admin/instance/keeper/governance", `{"all":true,"set":{"enabled":true,"deny_notify_min_risk":5}}`)
	wantCode(t, rr, http.StatusOK, "all")
	resp := decodeAs[govPutResp](t, rr.Body.Bytes())
	if !resp.Applied || !resp.DefaultsUpdated {
		t.Fatalf("resp = %+v, want applied and defaults updated", resp)
	}
	for _, ws := range []string{"ws-old", "ws-new"} {
		if s, found := f.gov(ws); !found || !s.Enabled || s.DenyNotifyMinRisk != 5 {
			t.Fatalf("%s = %+v (found %v), want overwritten", ws, s, found)
		}
	}
	seedInstanceWorkspace(t, f.db, "ws-later", "2026-09-01 00:00:00")
	if s, found := f.gov("ws-later"); found || !s.Enabled || s.DenyNotifyMinRisk != 5 {
		t.Fatalf("new workspace = %+v (found %v), want it to start from what was saved for all", s, found)
	}
}

func TestInstanceKeeperGovernanceIsAllOrNothing(t *testing.T) {
	f := newInstanceFixture(t)
	ctx := context.Background()
	// ws-new runs a hosted judge; clearing the model id is fine for ws-old
	// (no provider) and incoherent for ws-new. Nothing may be written.
	if err := governance.Upsert(ctx, f.db, "ws-new", governance.Settings{DenyNotifyMinRisk: 7, GovModelProvider: "anthropic", GovModelID: "claude-haiku-4-5"}, ""); err != nil {
		t.Fatal(err)
	}
	rr := f.do(f.boss, "PUT", "/api/v1/admin/instance/keeper/governance", `{"all":true,"set":{"enabled":true,"gov_model_id":""}}`)
	wantCode(t, rr, http.StatusBadRequest, "incoherent in one workspace")
	if b := rr.Body.String(); !strings.Contains(b, "ws-new") || !strings.Contains(b, "gov_model_id") {
		t.Fatalf("body = %s, want the workspace and the field named", rr.Body.String())
	}
	if _, found := f.gov("ws-old"); found {
		t.Fatal("ws-old was written although the save failed in ws-new")
	}
	if s, _ := f.gov("ws-new"); s.Enabled {
		t.Fatal("ws-new changed although the save was refused")
	}
}

func TestInstanceKeeperGovernanceRejects(t *testing.T) {
	cases := []struct{ name, body string }{
		{"nothing selected", `{"set":{"enabled":true}}`},
		{"both all and a list", `{"all":true,"workspaces":["ws-old"],"set":{"enabled":true}}`},
		{"nothing to change", `{"all":true,"set":{}}`},
		{"a person across workspaces", `{"workspaces":["ws-old","ws-new"],"set":{"security_contact_user_id":"boss"}}`},
		{"a vault credential across workspaces", `{"all":true,"set":{"gov_model_credential_id":"c1"}}`},
		{"a value out of bounds", `{"all":true,"set":{"deny_notify_min_risk":11}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newInstanceFixture(t)
			wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/keeper/governance", c.body), http.StatusBadRequest, c.name)
			if _, found := f.gov("ws-old"); found {
				t.Fatal("a refused save wrote a row")
			}
		})
	}
	f := newInstanceFixture(t)
	wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/keeper/governance", `{"workspaces":["nope"],"set":{"enabled":true}}`), http.StatusNotFound, "unknown workspace")
}

func TestInstanceKeeperGovernanceAcceptsSlugsAndSkipsUnchanged(t *testing.T) {
	f := newInstanceFixture(t)
	if err := governance.Upsert(context.Background(), f.db, "ws-new", governance.Settings{Enabled: true, DenyNotifyMinRisk: 7}, ""); err != nil {
		t.Fatal(err)
	}
	// seedInstanceWorkspace uses the id as the slug; resolve by slug all the same.
	rr := f.do(f.boss, "PUT", "/api/v1/admin/instance/keeper/governance", `{"workspaces":["ws-old","ws-new"],"set":{"enabled":true}}`)
	wantCode(t, rr, http.StatusOK, "two by slug")
	resp := decodeAs[govPutResp](t, rr.Body.Bytes())
	if resp.Changed != 1 {
		t.Fatalf("changed = %d, want 1 — ws-new already had it on", resp.Changed)
	}
}

func TestInstanceKeeperRequestsSpanWorkspaces(t *testing.T) {
	f := newInstanceFixture(t)
	mustExec(t, f.db, `INSERT INTO agents (id, workspace_id, name, slug) VALUES ('ag-o', 'ws-old', 'Old agent', 'oa'), ('ag-n', 'ws-new', 'New agent', 'na')`)
	mustExec(t, f.db, `INSERT INTO keeper_requests (id, requesting_agent_id, intent, decision, request_type) VALUES
		('k1','ag-o','x','ALLOW','access'), ('k2','ag-n','x','DENY','access'), ('k3','ag-n','x','ALLOW','behavior')`)

	type reqList struct {
		Items []struct {
			ID            string `json:"id"`
			WorkspaceID   string `json:"workspace_id"`
			WorkspaceName string `json:"workspace_name"`
		} `json:"items"`
		Total  int `json:"total"`
		Counts struct {
			Allow int `json:"allow"`
			Deny  int `json:"deny"`
		} `json:"counts"`
		ByWorkspace []struct {
			WorkspaceID string `json:"workspace_id"`
			Count       int    `json:"count"`
		} `json:"by_workspace"`
	}

	rr := f.do(f.boss, "GET", "/api/v1/admin/instance/keeper/requests", "")
	wantCode(t, rr, http.StatusOK, "all")
	all := decodeAs[reqList](t, rr.Body.Bytes())
	if all.Total != 3 || len(all.Items) != 3 || all.Counts.Allow != 2 || all.Counts.Deny != 1 {
		t.Fatalf("all = %+v", all)
	}
	for _, it := range all.Items {
		if it.WorkspaceID == "" || it.WorkspaceName == "" {
			t.Fatalf("row %s carries no workspace", it.ID)
		}
	}
	by := map[string]int{}
	for _, b := range all.ByWorkspace {
		by[b.WorkspaceID] = b.Count
	}
	if by["ws-old"] != 1 || by["ws-new"] != 2 {
		t.Fatalf("by_workspace = %v", by)
	}

	rr = f.do(f.boss, "GET", "/api/v1/admin/instance/keeper/requests?workspace=ws-new&request_type=access", "")
	wantCode(t, rr, http.StatusOK, "filtered")
	one := decodeAs[reqList](t, rr.Body.Bytes())
	if one.Total != 1 || one.Items[0].ID != "k2" {
		t.Fatalf("filtered = %+v, want only k2", one)
	}
	// The panel's counts stay instance-wide, so a filtered view still shows
	// how much every workspace holds.
	if len(one.ByWorkspace) < 2 {
		t.Fatalf("by_workspace narrowed with the filter: %+v", one.ByWorkspace)
	}

	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/instance/keeper/requests?workspace=nope", ""), http.StatusNotFound, "unknown workspace")
	wantCode(t, f.do(f.wsAdmin, "GET", "/api/v1/admin/instance/keeper/requests", ""), http.StatusForbidden, "workspace ADMIN")
}

func TestInstanceKeeperHealthCoversEveryWorkspace(t *testing.T) {
	f := newInstanceFixture(t)
	rr := f.do(f.boss, "GET", "/api/v1/admin/instance/keeper/health", "")
	wantCode(t, rr, http.StatusOK, "health")
	got := decodeAs[struct {
		Workspaces []struct {
			WorkspaceID   string `json:"workspace_id"`
			WorkspaceName string `json:"workspace_name"`
			MinSamples    int    `json:"min_samples"`
		} `json:"workspaces"`
	}](t, rr.Body.Bytes())
	seen := map[string]bool{}
	for _, w := range got.Workspaces {
		seen[w.WorkspaceID] = w.WorkspaceName != "" && w.MinSamples > 0
	}
	if !seen["ws-old"] || !seen["ws-new"] {
		t.Fatalf("health = %+v, want a named window per workspace", got.Workspaces)
	}
}

// An instance admin runs Admin › Security whatever their role where they
// stand. These reads used to re-check the workspace role inside the handler
// and refused a named instance admin who is only a MEMBER of their current
// workspace — the page then said "requires an admin role" to the one person
// it exists for.
func TestInstanceAdminWhoIsAMemberReadsTheInstanceCards(t *testing.T) {
	f := newInstanceFixture(t)
	seedInstanceUser(t, f.db, "ann", "ann@ex.com", "ws-new", "MEMBER")
	mustExec(t, f.db, `UPDATE users SET instance_role = 'ADMIN' WHERE id = 'ann'`)
	ann := mintTokenFor(t, f.db, "ann", "instfixann00000000000000000")
	wantCode(t, f.do(ann, "GET", "/api/v1/admin/security-posture?workspace_id=ws-new", ""), http.StatusOK, "posture")
	// The test router has no keeper settings store, so these answer 503 once
	// past the gate; what matters here is that the gate lets her through.
	for _, path := range []string{"/api/v1/admin/keeper/config", "/api/v1/admin/keeper/aux"} {
		if rr := f.do(ann, "GET", path+"?workspace_id=ws-new", ""); rr.Code == http.StatusForbidden {
			t.Fatalf("%s = 403 for an instance admin: %s", path, rr.Body.String())
		}
	}
	// A MEMBER who is not an instance admin still gets nowhere.
	seedInstanceUser(t, f.db, "joe", "joe@ex.com", "ws-new", "MEMBER")
	joe := mintTokenFor(t, f.db, "joe", "instfixjoe00000000000000000")
	wantCode(t, f.do(joe, "GET", "/api/v1/admin/security-posture?workspace_id=ws-new", ""), http.StatusForbidden, "plain member")
}
