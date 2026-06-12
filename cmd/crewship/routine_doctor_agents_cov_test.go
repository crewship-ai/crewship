package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// fakeDoctorGetter implements doctorHTTPGetter with canned responses
// keyed by path prefix. Used by both routine_doctor_*_cov_test files.
type fakeDoctorGetter struct {
	status int
	body   string
	err    error
	calls  []string
}

func (f *fakeDoctorGetter) Get(path string) (*http.Response, error) {
	f.calls = append(f.calls, path)
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(bytes.NewReader([]byte(f.body))),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func stepDef(steps ...map[string]interface{}) map[string]interface{} {
	raw := make([]interface{}, len(steps))
	for i, s := range steps {
		raw[i] = s
	}
	return map[string]interface{}{"steps": raw}
}

func TestCheckAgentSlugs_NoStepsWarns(t *testing.T) {
	got := checkAgentSlugs(&fakeDoctorGetter{}, "crew-1", map[string]interface{}{})
	if len(got) != 1 || got[0].Level != doctorWarn || !strings.Contains(got[0].Message, "no steps") {
		t.Errorf("got %+v", got)
	}
}

func TestCheckAgentSlugs_EmptyCrewIDWarns(t *testing.T) {
	def := stepDef(map[string]interface{}{"id": "a", "agent_slug": "viktor"})
	got := checkAgentSlugs(&fakeDoctorGetter{}, "", def)
	if len(got) != 1 || got[0].Level != doctorWarn || !strings.Contains(got[0].Message, "author_crew_id is empty") {
		t.Errorf("got %+v", got)
	}
}

func TestCheckAgentSlugs_FetchFailureWarns(t *testing.T) {
	def := stepDef(map[string]interface{}{"id": "a", "agent_slug": "viktor"})
	got := checkAgentSlugs(&fakeDoctorGetter{err: fmt.Errorf("conn refused")}, "crew-1", def)
	if len(got) != 1 || got[0].Level != doctorWarn || !strings.Contains(got[0].Message, "could not fetch crew agents") {
		t.Errorf("got %+v", got)
	}
}

func TestCheckAgentSlugs_AllResolveOK(t *testing.T) {
	def := stepDef(
		map[string]interface{}{"id": "a", "agent_slug": "viktor"},
		map[string]interface{}{"id": "b", "outcomes": map[string]interface{}{"grader_agent_slug": "eva"}},
	)
	g := &fakeDoctorGetter{status: 200, body: `[{"slug":"viktor"},{"slug":"eva"}]`}
	got := checkAgentSlugs(g, "crew-1", def)
	if len(got) != 1 || got[0].Level != doctorOK {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(got[0].Message, "2 agents available") {
		t.Errorf("message: %q", got[0].Message)
	}
	if len(g.calls) != 1 || !strings.Contains(g.calls[0], "crew_id=crew-1") {
		t.Errorf("crew filter not in request: %v", g.calls)
	}
}

func TestCheckAgentSlugs_MissingSlugAndGraderFail(t *testing.T) {
	def := stepDef(
		map[string]interface{}{"id": "step1", "agent_slug": "ghost"},
		map[string]interface{}{"id": "step2", "outcomes": map[string]interface{}{"grader_agent_slug": "phantom"}},
	)
	g := &fakeDoctorGetter{status: 200, body: `[{"slug":"viktor"}]`}
	got := checkAgentSlugs(g, "crew-1", def)
	if len(got) != 2 {
		t.Fatalf("want 2 failures, got %+v", got)
	}
	byName := map[string]doctorCheck{}
	for _, c := range got {
		byName[c.Name] = c
		if c.Level != doctorFail {
			t.Errorf("level for %s = %s, want FAIL", c.Name, c.Level)
		}
		if !strings.Contains(c.Hint, "viktor") {
			t.Errorf("hint should list available slugs: %q", c.Hint)
		}
	}
	if _, ok := byName["agent_slug:ghost"]; !ok {
		t.Errorf("missing ghost failure: %v", byName)
	}
	if c, ok := byName["agent_slug:phantom"]; !ok || !strings.Contains(c.Message, "step2/outcomes") {
		t.Errorf("grader failure should reference outcomes path: %+v", c)
	}
}

func TestFetchAgentSlugsForCrew_Non200ReturnsNil(t *testing.T) {
	g := &fakeDoctorGetter{status: 500, body: `{"error":"x"}`}
	if got := fetchAgentSlugsForCrew(g, "crew-1"); got != nil {
		t.Errorf("expected nil on 500, got %v", got)
	}
}

func TestFetchAgentSlugsForCrew_BadJSONReturnsNil(t *testing.T) {
	g := &fakeDoctorGetter{status: 200, body: `{not json`}
	if got := fetchAgentSlugsForCrew(g, "crew-1"); got != nil {
		t.Errorf("expected nil on decode failure, got %v", got)
	}
}

func TestFetchAgentSlugsForCrew_SkipsEmptySlugs(t *testing.T) {
	g := &fakeDoctorGetter{status: 200, body: `[{"slug":"viktor"},{"slug":""}]`}
	got := fetchAgentSlugsForCrew(g, "crew-1")
	if len(got) != 1 {
		t.Fatalf("want 1 slug, got %v", got)
	}
	if _, ok := got["viktor"]; !ok {
		t.Errorf("viktor missing: %v", got)
	}
}
