package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// #5372: JSON struct decoding accepts mixed-case keys. Presence checks must
// make the same decision, including for disabled schedules whose presets are
// not checked by the store until they are enabled.
func TestSchedulePatch_MixedCaseVersionPin(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{name: "unpin", body: `{"Target_Pipeline_Version":null,"inputs":{"zone":"eu"}}`, status: http.StatusOK},
		{name: "unicode folded unpin", body: `{"Target_Pipeline_Verſion":null,"inputs":{"zone":"eu"}}`, status: http.StatusOK},
		{name: "last mixed-case duplicate wins", body: `{"target_pipeline_version":1,"Target_Pipeline_Version":null,"inputs":{"zone":"eu"}}`, status: http.StatusOK},
		{name: "last exact duplicate wins", body: `{"Target_Pipeline_Version":1,"target_pipeline_version":null,"inputs":{"zone":"eu"}}`, status: http.StatusOK},
		{name: "repin invalid preset", body: `{"TARGET_PIPELINE_VERSION":2}`, status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, user, ws := presetRig(t)
			_, id := pinnedPlanOnV1(t, h, user, ws)
			rr := patchPlan(t, h, user, ws, id, tc.body)
			if rr.Code != tc.status {
				t.Fatalf("PATCH: %d %s, want %d", rr.Code, rr.Body, tc.status)
			}
			_, pin := storedPlan(t, h, id)
			if tc.status == http.StatusOK && pin != nil {
				t.Fatalf("explicit mixed-case null did not unpin: %d", *pin)
			}
			if tc.status != http.StatusOK && (pin == nil || *pin != 1) {
				t.Fatalf("refused repin changed pin: %v", pin)
			}
		})
	}
}

func TestSchedulePatch_MixedCaseWakeReferenceChecksDisabledPreset(t *testing.T) {
	for _, key := range []string{"Wake_Pipeline_ID", "WAKE_PIPELINE_SLUG"} {
		t.Run(key, func(t *testing.T) {
			h, user, ws := presetRig(t)
			target := seedRoutineForPreset(t, h, ws, "target", legacyUntypedDef)
			wake := seedRoutineForPreset(t, h, ws, "wake", strings.Replace(presetValidationDef, `"name":"planned"`, `"name":"planned","agentless":true`, 1))
			plan, err := h.schedules.Save(t.Context(), pipeline.SaveScheduleInput{WorkspaceID: ws, Name: "disabled", TargetPipelineID: target.ID, CronExpr: "0 9 * * *", Timezone: "UTC", Enabled: false})
			if err != nil {
				t.Fatal(err)
			}
			value := wake.ID
			if key == "WAKE_PIPELINE_SLUG" {
				value = wake.Slug
			}
			body, _ := json.Marshal(map[string]any{key: value})
			rr := patchPlan(t, h, user, ws, plan.ID, string(body))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("invalid disabled wake preset accepted: %d %s", rr.Code, rr.Body)
			}
			saved, err := h.schedules.GetByID(t.Context(), plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Enabled || saved.WakePipelineID != "" {
				t.Fatalf("refused request changed row: %+v", saved)
			}
			body = []byte(fmt.Sprintf(`{%q:%q,"wake_inputs":{"region":"eu"}}`, key, value))
			rr = patchPlan(t, h, user, ws, plan.ID, string(body))
			if rr.Code != http.StatusOK {
				t.Fatalf("valid disabled wake preset refused: %d %s", rr.Code, rr.Body)
			}
		})
	}
}
