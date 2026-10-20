package api

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestPublicationGrantWritesRequireEdit(t *testing.T) {
	for _, door := range []string{"policy", "invitation", "provision"} {
		t.Run(door, func(t *testing.T) {
			t.Setenv("CREWSHIP_PUBLIC_URL", "https://fixture.invalid")
			f := newPresetApplyFixture(t)
			seedPipelineRow(t, f.db, f.workspace, "publication", "publication")
			legacy := []access.Right{{Kind: "routine", ID: "publication", Operation: "view"}, {Kind: "routine", ID: "publication", Operation: "edit"}}
			m, err := f.store.Membership(t.Context(), "apply-subject", f.workspace)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.Replace(t.Context(), f.owner, "apply-subject", f.workspace, "restricted", m, legacy); err != nil {
				t.Fatal("legacy calibration", err)
			}
			before, err := f.store.Policy(t.Context(), f.owner, "apply-subject", f.workspace)
			if err != nil {
				t.Fatal(err)
			}
			router, err := NewRouter(f.db, "publication-right-fixture-secret-long-enough", newTestLogger())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			credential := mintTokenFor(t, f.db, f.owner, "publicationrights"+strings.Repeat("0", 40))
			base := "/api/v1/workspaces/" + f.workspace
			path := base + "/members/" + before.ID + "/access"
			method := "PUT"
			invalid := []access.Right{{Kind: "routine", ID: "publication", Operation: "publish"}}
			var body any = map[string]any{"membership_id": before.ID, "revision": before.Revision, "mode": "restricted", "rights": invalid}
			if door != "policy" {
				method = "POST"
				path = base + "/invitations"
				if door == "provision" {
					path = base + "/members/provision"
				}
				body = map[string]any{"email": "new-publish@fixture.invalid", "role": "VIEWER", "access_mode": "restricted", "rights": invalid}
			}
			counts := func() []int {
				t.Helper()
				var result []int
				for _, table := range []string{"users", "workspace_members", "access_grants", "workspace_invitations", "verification_tokens", "journal_entries"} {
					var n int
					if err := f.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
						t.Fatal(err)
					}
					result = append(result, n)
				}
				return result
			}
			initial := counts()
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			request := func(raw []byte) *httptest.ResponseRecorder {
				t.Helper()
				req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
				req.Header.Set("Authorization", "Bearer "+credential)
				req.Header.Set("X-Workspace-ID", f.workspace)
				req.Header.Set("Content-Type", "application/json")
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				return rr
			}
			rr := request(raw)
			if rr.Code != 422 {
				t.Errorf("%s Publish without Edit: HTTP%d, want422", door, rr.Code)
			}
			after, err := f.store.Policy(t.Context(), f.owner, "apply-subject", f.workspace)
			if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(initial, counts()) {
				t.Fatal("rejected write changed policy or side effects", err)
			}
			valid := append(append([]access.Right{}, legacy...), invalid...)
			body.(map[string]any)["rights"] = valid
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			rr = request(raw)
			want := 201
			if door == "policy" {
				want = 200
			}
			if rr.Code != want {
				t.Fatalf("valid Edit+Publish %s HTTP%d want%d", door, rr.Code, want)
			}
			var stored int
			if door == "invitation" {
				err = f.db.QueryRow(`SELECT json_array_length(rights_json) FROM workspace_invitations WHERE email='new-publish@fixture.invalid'`).Scan(&stored)
			} else {
				subject := "apply-subject"
				if door == "provision" {
					if err = f.db.QueryRow(`SELECT id FROM users WHERE email='new-publish@fixture.invalid'`).Scan(&subject); err != nil {
						t.Fatal(err)
					}
				}
				err = f.db.QueryRow(`SELECT COUNT(*) FROM access_grants g JOIN workspace_members m ON m.id=g.member_id WHERE m.user_id=? AND m.workspace_id=? AND g.routine_id='publication'`, subject, f.workspace).Scan(&stored)
			}
			if err != nil || stored != 3 {
				t.Fatal("valid policy not stored exactly", stored, err)
			}
			if door == "policy" {
				method = "GET"
				path += "/effective"
				response := request(nil)
				var effective memberEffectiveAccess
				if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &effective) != nil {
					t.Fatal("effective policy unavailable", response.Code)
				}
				found := false
				for _, decision := range effective.Decisions {
					if decision.Operation == "publish" {
						found = true
						if decision.State != "conditional" {
							t.Fatal("Publish confused with Run eligibility", decision.State)
						}
					}
					if decision.Operation == "run" {
						t.Fatal("Publish manufactured Run")
					}
				}
				if !found {
					t.Fatal("Publish omitted from effective policy")
				}
			}
		})
	}
}

func TestPublicationPresetPreviewAndApplyKeepDependency(t *testing.T) {
	f := newPresetApplyFixture(t)
	seedPipelineRow(t, f.db, f.workspace, "publication", "publication")
	m, err := f.store.Membership(t.Context(), "apply-subject", f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	m, err = f.store.Replace(t.Context(), f.owner, "apply-subject", f.workspace, "restricted", m, []access.Right{{Kind: "routine", ID: "publication", Operation: "view"}, {Kind: "routine", ID: "publication", Operation: "edit"}})
	if err != nil {
		t.Fatal("legacy calibration", err)
	}
	// A retained canonical row may predate recognition of the operation. Preset
	// replacement must preserve it only while the resulting policy keeps Edit.
	execOrFatal(t, f.db, `INSERT INTO access_grants(id,member_id,resource_kind,routine_id,operation,created_by,created_at) VALUES('retained-publish',?,'routine','publication','publish',?,'original')`, m.ID, f.owner)
	m, err = f.store.Membership(t.Context(), "apply-subject", f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	f.request = access.PresetRequest{MembershipID: m.ID, Revision: m.Revision, Resource: access.PresetResource{Kind: "routine", ID: "publication"}, PresetID: "routine-levels", Version: 1, Level: "edit", Reissue: []access.Right{}}
	healthy := f.preview(t)
	if len(healthy.AfterRights) != 5 {
		t.Fatal("healthy preset lost orthogonal Publish")
	}
	before, err := f.store.Policy(t.Context(), f.owner, "apply-subject", f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	f.request.Level = "view"
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.PlanPresetIn(t.Context(), tx, f.owner, "apply-subject", f.workspace, f.request)
	tx.Rollback()
	if err == nil {
		t.Error("preview offered Publish without Edit")
	}
	raw, err := json.Marshal(f.request)
	if err != nil {
		t.Fatal(err)
	}
	req := withWorkspaceUser(httptest.NewRequest("POST", "/", strings.NewReader(string(raw))), f.owner, f.workspace, "OWNER")
	req.SetPathValue("workspaceId", f.workspace)
	req.SetPathValue("memberId", m.ID)
	response := httptest.NewRecorder()
	f.handler.PreviewAccessPreset(response, req)
	if response.Code != 422 {
		t.Errorf("preview HTTP%d, want422", response.Code)
	}
	// Even a fabricated well-formed confirmation cannot bypass recomputation.
	unhealthy := healthy
	unhealthy.Selection = f.request
	rr := f.apply(t, applyBody(t, unhealthy))
	if rr.Code != 422 {
		t.Errorf("apply HTTP%d, want422", rr.Code)
	}
	after, err := f.store.Policy(t.Context(), f.owner, "apply-subject", f.workspace)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("refused preset changed canonical policy", err)
	}
	for _, table := range []string{"journal_entries", "access_preset_selections"} {
		var n int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE workspace_id=?", f.workspace).Scan(&n); err != nil || n != 0 {
			t.Fatal("refused preset wrote", table, n, err)
		}
	}
}
