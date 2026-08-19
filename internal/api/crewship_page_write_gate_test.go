package api

// #1945 — the autonomy gate on `page.write`, from the dispatcher's side.
//
// pages_internal_test.go proves what the internal route does once a call
// arrives. This proves which calls arrive at all: policy.ActionPageWrite is
// resolved against the ROUTINE'S AUTHOR CREW before anything is sent, so the
// four cells decided in internal/policy/types.go are what a routine actually
// experiences.

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/policy"
)

// The four cells, as behaviour. strict HOLDS — and on the unattended path a
// held decision is a refusal, because there is nobody attached to a 03:00 run
// to approve anything — while guided, trusted and full dispatch.
//
// The refusal half asserts that NOTHING was sent, not merely that an error came
// back: a gate that refuses after the write has already landed is not a gate.
func TestCrewshipActions_PageWriteAutonomyMatrix(t *testing.T) {
	args := map[string]any{
		"page":  "flotila-201",
		"panel": "sluzby",
		"data":  map[string]any{"items": []any{map[string]any{"name": "api", "state": "ok"}}},
	}

	for _, tc := range []struct {
		level      string
		wantCalls  int
		wantErrHas string
	}{
		{"strict", 0, "autonomy_level=strict"},
		{"guided", 1, ""},
		{"trusted", 1, ""},
		{"full", 1, ""},
	} {
		t.Run(tc.level, func(t *testing.T) {
			var calls []capturedCall
			srv := fakeInternalAPI(t, &calls)
			crew := "crew_" + tc.level
			db := crewshipPolicyDB(t, crew, tc.level)

			actions := newCrewshipActions(srv.URL, "master-token", policy.NewResolver(db), db, slog.Default())
			out, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
				Verb:        "page.write",
				Args:        args,
				WorkspaceID: crewshipWorkspace,
				CrewID:      crew,
				AgentID:     "agent_real",
				RunID:       "run_real",
			})

			if tc.wantErrHas != "" {
				if err == nil {
					t.Fatalf("autonomy_level=%s must not write a panel unattended (got %q)", tc.level, out)
				}
				if !strings.Contains(err.Error(), tc.wantErrHas) {
					t.Errorf("the refusal must name the level that caused it, got %q", err)
				}
				if !strings.Contains(err.Error(), "crewship policy set") {
					t.Errorf("the refusal must name the fix, got %q", err)
				}
			} else if err != nil {
				t.Fatalf("autonomy_level=%s must write a panel unattended: %v", tc.level, err)
			}

			if len(calls) != tc.wantCalls {
				t.Fatalf("made %d calls at autonomy_level=%s, want %d — the gate runs BEFORE the write",
					len(calls), tc.level, tc.wantCalls)
			}
			if tc.wantCalls == 0 {
				return
			}

			call := calls[0]
			if call.method != "PUT" || call.path != "/api/v1/internal/pages/flotila-201/data" {
				t.Errorf("dispatched %s %s, want PUT /api/v1/internal/pages/flotila-201/data",
					call.method, call.path)
			}
			// The panel travels in the body (one path placeholder, see the
			// registry), and the payload travels untouched.
			if got, _ := call.body["panel"].(string); got != "sluzby" {
				t.Errorf("body panel = %v, want sluzby", call.body["panel"])
			}
			if _, ok := call.body["data"].(map[string]any); !ok {
				t.Errorf("body data = %#v, want the authored payload as an object", call.body["data"])
			}
			// Identity is the dispatcher's, not the author's — the internal route
			// has no other way to know which tenant and which run this is.
			for field, want := range map[string]string{
				"workspace_id":  crewshipWorkspace,
				"crew_id":       crew,
				"author_run_id": "run_real",
			} {
				if got, _ := call.body[field].(string); got != want {
					t.Errorf("body %s = %v, want %q", field, call.body[field], want)
				}
			}
		})
	}
}

// The seam check, for this verb specifically: PolicyAction is a plain string on
// the pipeline side (it must not import internal/policy), so a typo compiles and
// then refuses forever on DecideAction's defensive default. The package-wide
// version of this is TestCrewshipVerbs_EveryPolicyActionIsDeclared; this one
// names page_write so the failure says which verb.
func TestCrewshipVerbs_PageWritePolicyActionIsDeclared(t *testing.T) {
	name := pipeline.CrewshipVerbPolicyAction("page.write")
	if name == "" {
		t.Fatal("page.write has no policy action — it is refused at save (ErrCrewshipVerbUngoverned)")
	}
	if !policy.IsKnownAction(policy.Action(name)) {
		t.Fatalf("page.write is gated on %q, which internal/policy does not declare", name)
	}
	if policy.Action(name) != policy.ActionPageWrite {
		t.Errorf("page.write is gated on %q, want %q", name, policy.ActionPageWrite)
	}
}
