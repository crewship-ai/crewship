package main

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// covDoctorDefinition is a healthy routine definition every doctor
// check passes on (given the stubs in covDoctorHappyStubs).
func covDoctorDefinition() map[string]any {
	return map[string]any{
		"steps": []any{
			map[string]any{
				"id":         "s1",
				"agent_slug": "viktor",
				"validation": map[string]any{"min_length": 1, "max_length": 100},
			},
		},
		"credentials_required": []any{map[string]any{"type": "github"}},
		"egress_targets":       []any{"api.example.com"},
		"max_cost_usd":         1.0,
		"estimated_cost_usd":   0.1,
	}
}

func covDoctorHappyStubs(t *testing.T, s *clitest.StubServer, slug string) {
	t.Helper()
	s.OnGet("/api/v1/workspaces/"+covWSCli9+"/pipelines/"+slug, clitest.JSONResponse(200, map[string]any{
		"slug":           slug,
		"author_crew_id": covCrew,
		"definition":     covDoctorDefinition(),
	}))
	s.OnGet("/api/v1/crews/"+covCrew+"/provision", clitest.JSONResponse(200, map[string]any{
		"status":              "completed",
		"devcontainer_config": "{}",
		"cached_image":        "crewship-cached:latest",
	}))
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{{"slug": "viktor"}}))
	s.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{
		{"provider": "GITHUB", "type": "GITHUB", "status": "ACTIVE"},
	}))
}

func TestRunRoutineDoctor_AllChecksPass(t *testing.T) {
	s := covStubCli9(t)
	covDoctorHappyStubs(t, s, "eval-x")

	var buf bytes.Buffer
	routineDoctorCmd.SetOut(&buf)
	t.Cleanup(func() { routineDoctorCmd.SetOut(nil) })

	if err := runRoutineDoctor(routineDoctorCmd, []string{"eval-x"}); err != nil {
		t.Fatalf("doctor should pass: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Doctor: eval-x", "routine_exists", "author_crew", "agent_slugs", "credential:GITHUB", "egress_allowlist", "cost_cap", "0 failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor table missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "✗") {
		t.Errorf("no check should fail:\n%s", out)
	}
}

func TestRunRoutineDoctor_RoutineNotFound(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/workspaces/"+covWSCli9+"/pipelines/nope", clitest.ErrorResponse(404, "not found"))
	// Suggestion pass: empty routine list → canned "no routines" hint.
	s.OnGet("/api/v1/workspaces/"+covWSCli9+"/pipelines", clitest.JSONResponse(200, []map[string]string{}))

	var buf bytes.Buffer
	routineDoctorCmd.SetOut(&buf)
	t.Cleanup(func() { routineDoctorCmd.SetOut(nil) })

	err := runRoutineDoctor(routineDoctorCmd, []string{"nope"})
	if err == nil || !strings.Contains(err.Error(), "1 check(s) failed") {
		t.Fatalf("expected '1 check(s) failed'; got %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `routine "nope" not found in workspace`) {
		t.Errorf("missing not-found message:\n%s", out)
	}
	if !strings.Contains(out, "no routines registered yet") {
		t.Errorf("missing empty-workspace hint:\n%s", out)
	}
	if !strings.Contains(out, "1 failed") {
		t.Errorf("summary should count the failure:\n%s", out)
	}
}

func TestRunRoutineDoctor_JSONFormat(t *testing.T) {
	s := covStubCli9(t)
	covDoctorHappyStubs(t, s, "eval-json")
	flagFormat = "json"

	out := covCaptureStdoutCli9(t, func() {
		if err := runRoutineDoctor(routineDoctorCmd, []string{"eval-json"}); err != nil {
			t.Errorf("doctor should pass: %v", err)
		}
	})
	if !strings.Contains(out, `"slug": "eval-json"`) && !strings.Contains(out, `"slug":"eval-json"`) {
		t.Errorf("json output missing slug:\n%s", out)
	}
	if !strings.Contains(out, `"failed": 0`) && !strings.Contains(out, `"failed":0`) {
		t.Errorf("json output missing failed tally:\n%s", out)
	}
}

func TestRunRoutineDoctor_AuthGates(t *testing.T) {
	covSaveState(t)

	cliCfg = &cli.CLIConfig{}
	err := runRoutineDoctor(routineDoctorCmd, []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}

	cliCfg.Token = "tok"
	err = runRoutineDoctor(routineDoctorCmd, []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

func TestFinishDoctorReport_TalliesAndFails(t *testing.T) {
	covSaveState(t)
	report := doctorReport{
		Slug: "r",
		Checks: []doctorCheck{
			{Name: "a", Level: doctorOK, Message: "fine"},
			{Name: "b", Level: doctorWarn, Message: "meh", Hint: "tune it"},
			{Name: "c", Level: doctorFail, Message: "broken"},
			{Name: "d", Level: doctorFail, Message: "also broken"},
		},
	}
	var buf bytes.Buffer
	routineDoctorCmd.SetOut(&buf)
	t.Cleanup(func() { routineDoctorCmd.SetOut(nil) })

	err := finishDoctorReport(routineDoctorCmd, report)
	if err == nil || !strings.Contains(err.Error(), "2 check(s) failed") {
		t.Fatalf("expected 2 failures; got %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "1 passed, 1 warning(s), 2 failed") {
		t.Errorf("summary tallies wrong:\n%s", out)
	}
	if !strings.Contains(out, "→ tune it") {
		t.Errorf("hint continuation line missing:\n%s", out)
	}
	for _, sym := range []string{"✓", "⚠", "✗"} {
		if !strings.Contains(out, sym) {
			t.Errorf("missing symbol %q:\n%s", sym, out)
		}
	}
}

func TestFetchRoutineForDoctor_Shapes(t *testing.T) {
	t.Parallel()

	get := func(body string, status int) covGetterFunc {
		return func(string) (*http.Response, error) { return covHTTPResp(status, body), nil }
	}

	t.Run("transport error", func(t *testing.T) {
		t.Parallel()
		g := covGetterFunc(func(string) (*http.Response, error) { return nil, errors.New("boom") })
		if _, ok := fetchRoutineForDoctor(g, "ws", "s"); ok {
			t.Error("transport error should report not-found")
		}
	})
	t.Run("non-200", func(t *testing.T) {
		t.Parallel()
		if _, ok := fetchRoutineForDoctor(get(`{}`, 404), "ws", "s"); ok {
			t.Error("404 should report not-found")
		}
	})
	t.Run("bad json", func(t *testing.T) {
		t.Parallel()
		if _, ok := fetchRoutineForDoctor(get(`{nope`, 200), "ws", "s"); ok {
			t.Error("decode failure should report not-found")
		}
	})
	t.Run("definition_parsed variant", func(t *testing.T) {
		t.Parallel()
		out, ok := fetchRoutineForDoctor(get(`{"slug":"a","author_crew_id":"c1","definition_parsed":{"steps":[]}}`, 200), "ws", "a")
		if !ok || out.Slug != "a" || out.AuthorCrewID != "c1" || out.Definition == nil {
			t.Errorf("definition_parsed not decoded: %+v ok=%v", out, ok)
		}
	})
	t.Run("definition_json string variant", func(t *testing.T) {
		t.Parallel()
		out, ok := fetchRoutineForDoctor(get(`{"slug":"b","definition_json":"{\"max_cost_usd\":2}"}`, 200), "ws", "b")
		if !ok || out.Definition["max_cost_usd"] != 2.0 {
			t.Errorf("definition_json not decoded: %+v ok=%v", out, ok)
		}
	})
	t.Run("path escapes ws and slug", func(t *testing.T) {
		t.Parallel()
		var gotPath string
		g := covGetterFunc(func(p string) (*http.Response, error) {
			gotPath = p
			return covHTTPResp(200, `{"slug":"x"}`), nil
		})
		_, _ = fetchRoutineForDoctor(g, "ws/1", "slug#2")
		if !strings.Contains(gotPath, "ws%2F1") || !strings.Contains(gotPath, "slug%232") {
			t.Errorf("path not escaped: %q", gotPath)
		}
	})
}

func TestOptionalInt(t *testing.T) {
	t.Parallel()
	m := map[string]any{"f": 7.9, "i": 3, "s": "nope"}
	if v := optionalInt(m, "missing"); v != nil {
		t.Errorf("missing key should be nil, got %v", *v)
	}
	if v := optionalInt(m, "f"); v == nil || *v != 7 {
		t.Errorf("float64 7.9 should coerce to 7, got %v", v)
	}
	if v := optionalInt(m, "i"); v == nil || *v != 3 {
		t.Errorf("int 3 should pass through, got %v", v)
	}
	if v := optionalInt(m, "s"); v != nil {
		t.Errorf("string value should be nil, got %v", *v)
	}
}

func TestTruncCrewID(t *testing.T) {
	t.Parallel()
	if got := truncCrewID("short"); got != "short" {
		t.Errorf("short id should pass through, got %q", got)
	}
	if got := truncCrewID("123456789012"); got != "123456789012" {
		t.Errorf("exactly 12 runes should pass through, got %q", got)
	}
	if got := truncCrewID("1234567890123"); got != "123456789012…" {
		t.Errorf("13 runes should truncate to 12+ellipsis, got %q", got)
	}
	// Multi-byte safety: 13 two-byte runes must cut on rune boundary.
	if got := truncCrewID(strings.Repeat("é", 13)); got != strings.Repeat("é", 12)+"…" {
		t.Errorf("rune-safe truncation broken: %q", got)
	}
}
