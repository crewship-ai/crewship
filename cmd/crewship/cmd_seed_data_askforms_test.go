package main

// The seed's create-drops-columns trap, pinned.
//
// POST /api/v1/agents models a fixed set of columns (createAgentRequest in
// internal/api/agents_create.go) and readJSON ignores everything else, so
// `suggested_prompts` and `ask_forms` are accepted with a 201 and silently
// dropped. Only PATCH /api/v1/agents/{id} writes them.
//
// The stub below is that server: it keeps ONLY the create-modelled keys on a
// create and applies the two update-only ones on a PATCH. A seeder that sends
// the columns in the create body and stops there passes every status check and
// still produces the workspace this change exists to fix — agents whose
// suggested questions and ask forms are unset. The assertion is on the RECORD,
// not on the request.
//
// Same shape as internal/manifest's fix (buildAgentPostCreateBody in
// internal/manifest/plan.go), which the manifest importer covers with
// agent_suggested_prompts_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/askforms"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// createModelledKeys is createAgentRequest's json tag set — the keys a create
// actually persists. Anything else in a create body goes nowhere.
var createModelledKeys = map[string]bool{
	"name": true, "slug": true, "crew_id": true, "description": true,
	"role_title": true, "agent_role": true, "lead_mode": true,
	"cli_adapter": true, "llm_provider": true, "llm_model": true,
	"system_prompt": true, "avatar_seed": true, "avatar_style": true,
	"timeout_seconds": true, "tool_profile": true, "memory_enabled": true,
}

// agentRecord is one row as the stub server stores it.
type agentRecord map[string]any

// seedAgentAPIStub models the real agent endpoints closely enough for the
// trap: create drops unknown keys, PATCH applies the two update-only columns.
// Returns the stub and the record store, keyed by agent slug.
func seedAgentAPIStub(t *testing.T) (*clitest.StubServer, map[string]agentRecord) {
	t.Helper()
	stub := clitest.NewStubServer()
	records := map[string]agentRecord{}
	idToSlug := map[string]string{}

	stub.SetFallback(func(r *http.Request, body []byte) (int, []byte, string) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents":
			var in map[string]any
			if err := json.Unmarshal(body, &in); err != nil {
				return 400, []byte(`{"error":"bad json"}`), "application/json"
			}
			slug, _ := in["slug"].(string)
			rec := agentRecord{}
			for k, v := range in {
				// readJSON's behaviour: an unmodelled key is not an error and
				// is not stored either.
				if createModelledKeys[k] {
					rec[k] = v
				}
			}
			id := fmt.Sprintf("cagent%014d", len(records)+1)
			rec["id"] = id
			records[slug] = rec
			idToSlug[id] = slug
			return 201, []byte(fmt.Sprintf(`{"id":%q}`, id)), "application/json"

		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v1/agents/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/v1/agents/")
			slug, ok := idToSlug[id]
			if !ok {
				return 404, []byte(`{"error":"no such agent"}`), "application/json"
			}
			var in map[string]any
			if err := json.Unmarshal(body, &in); err != nil {
				return 400, []byte(`{"error":"bad json"}`), "application/json"
			}
			for k, v := range in {
				// The update handler validates these two before writing them,
				// exactly as the server does — a seed that ships an invalid
				// form must fail here, loudly.
				switch k {
				case "ask_forms":
					s, _ := v.(string)
					canonical, err := askforms.Normalize(s)
					if err != nil {
						return 400, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())), "application/json"
					}
					records[slug][k] = canonical
				default:
					records[slug][k] = v
				}
			}
			return 200, []byte(`{"id":"` + id + `"}`), "application/json"
		}
		return 404, []byte(fmt.Sprintf(`{"error":"no stub for %s %s"}`, r.Method, r.URL.Path)), "application/json"
	})
	return stub, records
}

func seedAllCrewIDs() map[string]string {
	crewIDs := map[string]string{}
	for _, c := range seeddata.ActiveCrews() {
		crewIDs[c.Slug] = covCrewIDCli2
	}
	return crewIDs
}

// The assertion the trap makes necessary: after seedAgents, an agent that
// declares suggested prompts and an ask form has BOTH set on its record.
func TestSeedAgents_UpdateOnlyColumnsLandOnTheRecord(t *testing.T) {
	stub, records := seedAgentAPIStub(t)
	defer stub.Close()

	_ = captureStdoutCovCli2(t, func() {
		if _, err := seedAgents(context.Background(), newSeedClient(stub), seedAllCrewIDs()); err != nil {
			t.Errorf("seedAgents: %v", err)
		}
	})

	checkedPrompts, checkedForms := 0, 0
	for _, a := range seeddata.ActiveAgents() {
		rec, ok := records[a.Slug]
		if !ok {
			t.Fatalf("agent %s was never created", a.Slug)
		}
		if want := strings.TrimSpace(a.SuggestedPrompts); want != "" {
			checkedPrompts++
			got, _ := rec["suggested_prompts"].(string)
			if strings.TrimSpace(got) != want {
				t.Errorf("agent %s: suggested_prompts on the record = %q, want the seeded "+
					"list — POST drops the key, so only a follow-up PATCH can set it", a.Slug, got)
			}
		} else if _, set := rec["suggested_prompts"]; set {
			t.Errorf("agent %s: suggested_prompts written for an agent that declares none — "+
				"an empty value would overwrite whatever a re-seeded workspace already had", a.Slug)
		}
		if a.AskFormsSlug != "" {
			checkedForms++
			got, _ := rec["ask_forms"].(string)
			if strings.TrimSpace(got) == "" {
				t.Errorf("agent %s: ask_forms is unset on the record — the questionnaire "+
					"never reaches the workspace", a.Slug)
			}
			forms, err := askforms.Parse(got)
			if err != nil {
				t.Errorf("agent %s: stored ask_forms does not parse: %v", a.Slug, err)
			} else if len(forms) == 0 {
				t.Errorf("agent %s: stored ask_forms holds no form", a.Slug)
			}
		} else if _, set := rec["ask_forms"]; set {
			t.Errorf("agent %s: ask_forms written for an agent that declares none", a.Slug)
		}
	}
	if checkedPrompts == 0 || checkedForms == 0 {
		t.Fatalf("nothing to check: %d agents with prompts, %d with forms — the seed data "+
			"is what makes this test meaningful", checkedPrompts, checkedForms)
	}

	// One PATCH per agent that needs one, and none for the rest: the seed
	// talks to the API the smallest number of times that does the job.
	patches := 0
	for _, c := range stub.Calls() {
		if c.Method == http.MethodPatch {
			patches++
		}
	}
	want := 0
	for _, a := range seeddata.ActiveAgents() {
		if strings.TrimSpace(a.SuggestedPrompts) != "" || a.AskFormsSlug != "" {
			want++
		}
	}
	if patches != want {
		t.Errorf("PATCH calls = %d, want %d (one per agent with update-only columns)", patches, want)
	}
}

// Re-seeding an existing workspace goes down the 409 → resolve-by-slug path.
// The columns must still be applied there: `crewship seed` on a workspace
// seeded before this change is exactly how an existing dev clone gets them.
func TestSeedAgents_UpdateOnlyColumnsAppliedOnReSeed(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()

	const existingID = "cagentexisting000001"
	patched := map[string]any{}
	existing := []map[string]string{}
	for _, a := range seeddata.ActiveAgents() {
		existing = append(existing, map[string]string{"id": existingID, "slug": a.Slug})
	}
	stub.OnPost("/api/v1/agents", clitest.JSONResponse(http.StatusConflict, map[string]string{"error": "slug taken"}))
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, existing))
	stub.OnPatch("/api/v1/agents/"+existingID, func(_ *http.Request, body []byte) (int, []byte, string) {
		var in map[string]any
		if err := json.Unmarshal(body, &in); err != nil {
			return 400, []byte(`{"error":"bad json"}`), "application/json"
		}
		for k, v := range in {
			patched[k] = v
		}
		return 200, []byte(`{"id":"` + existingID + `"}`), "application/json"
	})

	_ = captureStdoutCovCli2(t, func() {
		if _, err := seedAgents(context.Background(), newSeedClient(stub), seedAllCrewIDs()); err != nil {
			t.Errorf("seedAgents: %v", err)
		}
	})

	if _, ok := patched["suggested_prompts"]; !ok {
		t.Error("re-seed never PATCHed suggested_prompts — an existing workspace would " +
			"keep showing the generic fallback chips forever")
	}
	if _, ok := patched["ask_forms"]; !ok {
		t.Error("re-seed never PATCHed ask_forms")
	}
}

// A form the server would refuse must fail the seed loudly rather than leaving
// an agent half-configured: the seeder surfaces the API's own sentence.
func TestSeedAgents_InvalidAskFormsFailsTheSeed(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/agents", clitest.JSONResponse(201, map[string]string{"id": covAgentIDCli2}))
	stub.OnPatch("/api/v1/agents/"+covAgentIDCli2, clitest.ErrorResponse(400,
		`form "bug-report": template names {{typo}}, which is not a field on that form`))

	var err error
	_ = captureStdoutCovCli2(t, func() {
		_, err = seedAgents(context.Background(), newSeedClient(stub), seedAllCrewIDs())
	})
	if err == nil {
		t.Fatal("a rejected PATCH left the seed green — the agent is then created " +
			"with neither column and nobody is told")
	}
	if !strings.Contains(err.Error(), "not a field on that form") {
		t.Errorf("error does not carry the server's explanation: %v", err)
	}
}
