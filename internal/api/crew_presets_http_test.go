package api

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestCrewPresetHTTPConsentIsolationAndAtomicity(t *testing.T) {
	for _, failAudit := range []bool{false, true} {
		t.Run(map[bool]string{false: "apply", true: "audit-failure"}[failAudit], func(t *testing.T) {
			f := newPresetApplyFixture(t)
			execOrFatal(t, f.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('preset-crew',?,'Preset crew','preset-crew')`, f.workspace)
			seedAgentRow(t, f.db, "preset-agent", f.workspace, "preset-crew", "Agent", "preset-agent", "AGENT")
			f.request.Resource = access.PresetResource{Kind: "crew", ID: "preset-crew"}
			f.request.PresetID, f.request.Level = "crew-levels", "use"
			router, err := NewRouter(f.db, "crew-preset-http-synthetic-secret-32chars", newTestLogger())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			tokens := map[string]string{f.owner: mintTokenFor(t, f.db, f.owner, "crew-preset-owner"), "apply-subject": mintTokenFor(t, f.db, "apply-subject", "crew-preset-guest")}
			base := "/api/v1/workspaces/" + f.workspace
			path := base + "/members/" + f.request.MembershipID + "/access/presets/"
			call := func(actor, method, path string, body any, want int) *httptest.ResponseRecorder {
				t.Helper()
				data, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(method, path, strings.NewReader(string(data)))
				req.Header.Set("Authorization", "Bearer "+tokens[actor])
				req.Header.Set("X-Workspace-ID", f.workspace)
				req.Header.Set("Content-Type", "application/json")
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				if rr.Code != want {
					t.Fatalf("%s %s: %d != %d: %s", method, path, rr.Code, want, rr.Body.String())
				}
				return rr
			}
			call("apply-subject", "GET", base+"/access/presets", nil, 404)
			call("apply-subject", "POST", path+"preview", f.request, 404)
			catalog := call(f.owner, "GET", base+"/access/presets", nil, 200)
			if !strings.Contains(catalog.Body.String(), `"preset_id":"crew-levels"`) {
				t.Fatal("crew preset absent")
			}
			bad := f.request
			bad.Resource.ID = "ungranted-foreign-crew"
			call(f.owner, "POST", path+"preview", bad, 404)
			before, err := f.store.Policy(t.Context(), f.owner, "apply-subject", f.workspace)
			if err != nil {
				t.Fatal(err)
			}
			rr := call(f.owner, "POST", path+"preview", f.request, 200)
			var preview access.PresetPreview
			if err := json.Unmarshal(rr.Body.Bytes(), &preview); err != nil {
				t.Fatal(err)
			}
			if len(preview.Warnings) != 1 || preview.Warnings[0].Code != "CREW_DYNAMIC_AUDIENCE" {
				t.Fatalf("audience: %+v", preview.Warnings)
			}
			want := 200
			if failAudit {
				execOrFatal(t, f.db, `CREATE TRIGGER reject_crew_preset_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type='member.access_changed' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`)
				want = 500
			}
			input := presetApplyRequest{Selection: preview.Selection, Digest: preview.Digest}
			call(f.owner, "POST", path+"apply", input, want)
			after, err := f.store.Policy(t.Context(), f.owner, "apply-subject", f.workspace)
			if err != nil {
				t.Fatal(err)
			}
			if failAudit {
				if !reflect.DeepEqual(before, after) {
					t.Fatal("audit failure partially committed")
				}
				return
			}
			if after.Mode != "restricted" || len(after.Rights) != 2 || len(*after.PresetSelections) != 1 || (*after.PresetSelections)[0].State != "selected" {
				t.Fatalf("policy: %+v", after)
			}
			call(f.owner, "POST", path+"apply", input, 409)
			for _, op := range []string{"discover", "chat", "run"} {
				if err := f.store.Check(t.Context(), "apply-subject", f.workspace, access.Right{Kind: "agent", ID: "preset-agent", Operation: op}); err != nil {
					t.Fatal(op, err)
				}
			}
			if err := f.store.Check(t.Context(), "apply-subject", f.workspace, access.Right{Kind: "agent", ID: "preset-agent", Operation: "delegate"}); err == nil {
				t.Fatal("preset granted Delegate")
			}
		})
	}
}
