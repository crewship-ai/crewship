package kinds

// Coverage-focused tests for project.go. Reuses the scriptable covClient
// fake from routine_cov_test.go. project_test.go owns the validation and
// Plan happy paths; this file pins PlanReplace, the diffPatch field
// matrix, fetch/decode failure modes, and FetchProjectBySlug pointer
// unboxing.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

func projectCovDoc() *ProjectDocument {
	return &ProjectDocument{
		APIVersion: "crewship/v1",
		Kind:       "Project",
		Metadata:   internalapi.Metadata{Name: "Roadmap", Slug: "roadmap"},
		Spec:       ProjectSpec{Status: "backlog"},
	}
}

// ── PlanReplace ─────────────────────────────────────────────────────

func TestProjectCov_PlanReplace(t *testing.T) {
	t.Parallel()

	t.Run("lead resolve fails", func(t *testing.T) {
		t.Parallel()
		doc := projectCovDoc()
		doc.Spec.LeadAgentSlug = "ghost"
		c := newCovClient(map[string]covRoute{"GET /api/v1/agents": {body: `[]`}})
		_, err := doc.PlanReplace(context.Background(), c, nil)
		if err == nil || !strings.Contains(err.Error(), "resolve lead_agent_slug") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("no remote emits create only", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"POST /api/v1/projects": {status: 201, body: "{}"}})
		items, err := projectCovDoc().PlanReplace(context.Background(), c, nil)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionCreate {
			t.Fatalf("got items=%v err=%v", items, err)
		}
		if err := items[0].Exec(context.Background(), c); err != nil {
			t.Fatalf("create exec: %v", err)
		}
		if !c.sawCall("POST /api/v1/projects") {
			t.Fatalf("calls = %v", c.calls)
		}
	})
	t.Run("remote emits delete then create", func(t *testing.T) {
		t.Parallel()
		doc := projectCovDoc()
		doc.Spec.LeadAgentSlug = "eva"
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/agents":         {body: `[{"id":"a1","slug":"eva"}]`},
			"DELETE /api/v1/projects/p1": {status: 200, body: "{}"},
			"POST /api/v1/projects":      {status: 201, body: "{}"},
		})
		items, err := doc.PlanReplace(context.Background(), c, &ProjectRemote{ID: "p1", Slug: "roadmap"})
		if err != nil || len(items) != 2 {
			t.Fatalf("got items=%v err=%v", items, err)
		}
		if items[0].Action != internalapi.ActionDelete || items[1].Action != internalapi.ActionCreate {
			t.Fatalf("actions = %v / %v", items[0].Action, items[1].Action)
		}
		if err := items[0].Exec(context.Background(), c); err != nil {
			t.Fatalf("delete exec: %v", err)
		}
		if err := items[1].Exec(context.Background(), c); err != nil {
			t.Fatalf("create exec: %v", err)
		}
		if !c.sawCall("DELETE /api/v1/projects/p1") {
			t.Fatalf("calls = %v", c.calls)
		}
	})
	t.Run("delete exec transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"DELETE /api/v1/projects/p1": {err: errors.New("down")}})
		items, err := projectCovDoc().PlanReplace(context.Background(), c, &ProjectRemote{ID: "p1"})
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "DELETE project") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
	t.Run("delete exec bad status", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"DELETE /api/v1/projects/p1": {status: 409, body: "has issues"}})
		items, _ := projectCovDoc().PlanReplace(context.Background(), c, &ProjectRemote{ID: "p1"})
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "status 409") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
	t.Run("create exec transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"POST /api/v1/projects": {err: errors.New("down")}})
		items, _ := projectCovDoc().PlanReplace(context.Background(), c, nil)
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "POST project") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
}

// ── diffPatch ───────────────────────────────────────────────────────

func TestProjectCov_DiffPatch(t *testing.T) {
	t.Parallel()

	remote := &ProjectRemote{
		Name:        "Old",
		Description: "old desc",
		Color:       "blue",
		Status:      "backlog",
		Priority:    "none",
		Health:      "on_track",
		TargetDate:  "2026-01-01",
		LeadType:    "user",
		LeadID:      "u1",
	}

	t.Run("everything drifts", func(t *testing.T) {
		t.Parallel()
		doc := &ProjectDocument{
			Metadata: internalapi.Metadata{Name: "New", Slug: "roadmap", Description: "new desc"},
			Spec: ProjectSpec{
				Color:         "red",
				Status:        "active",
				Priority:      "high",
				Health:        "at_risk",
				TargetDate:    "2026-12-31",
				LeadAgentSlug: "eva",
			},
		}
		patch := doc.diffPatch(remote, "a1")
		want := map[string]any{
			"name": "New", "description": "new desc", "color": "red",
			"status": "active", "priority": "high", "health": "at_risk",
			"target_date": "2026-12-31", "lead_id": "a1", "lead_type": "agent",
		}
		for k, v := range want {
			if patch[k] != v {
				t.Errorf("patch[%q] = %v, want %v", k, patch[k], v)
			}
		}
		if len(patch) != len(want) {
			t.Errorf("patch keys = %d, want %d: %v", len(patch), len(want), patch)
		}
	})
	t.Run("lead id matches but lead type wrong still patches", func(t *testing.T) {
		t.Parallel()
		doc := &ProjectDocument{
			Metadata: internalapi.Metadata{Slug: "roadmap"},
			Spec:     ProjectSpec{LeadAgentSlug: "eva"},
		}
		patch := doc.diffPatch(remote, "u1") // same id, but remote lead_type=user
		if patch["lead_id"] != "u1" || patch["lead_type"] != "agent" {
			t.Fatalf("patch = %v", patch)
		}
	})
	t.Run("no drift empty patch", func(t *testing.T) {
		t.Parallel()
		doc := &ProjectDocument{
			Metadata: internalapi.Metadata{Name: "Old", Slug: "roadmap", Description: "old desc"},
			Spec: ProjectSpec{
				Color: "blue", Status: "backlog", Priority: "none",
				Health: "on_track", TargetDate: "2026-01-01",
			},
		}
		patch := doc.diffPatch(remote, "")
		if len(patch) != 0 {
			t.Fatalf("want empty, got %v", patch)
		}
	})
}

// ── fetch helpers ───────────────────────────────────────────────────

func TestProjectCov_FetchProjects_Errors(t *testing.T) {
	t.Parallel()

	t.Run("transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/projects": {err: errors.New("down")}})
		_, err := projectFetchProjects(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "GET /api/v1/projects") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("bad status", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/projects": {status: 500}})
		_, err := projectFetchProjects(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "status 500") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("decode error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/projects": {body: "{bad"}})
		_, err := projectFetchProjects(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "decode projects") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestProjectCov_FetchAgents_Errors(t *testing.T) {
	t.Parallel()

	t.Run("transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/agents": {err: errors.New("down")}})
		_, err := projectFetchAgents(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "GET /api/v1/agents") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("bad status", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/agents": {status: 403}})
		_, err := projectFetchAgents(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "status 403") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("decode error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/agents": {body: "nope"}})
		_, err := projectFetchAgents(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "decode agents") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestProjectCov_FetchProjectBySlug(t *testing.T) {
	t.Parallel()

	t.Run("fetch error propagates", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/projects": {status: 500}})
		if _, err := FetchProjectBySlug(context.Background(), c, "roadmap"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("not found returns nil nil", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/projects": {body: `[{"id":"p1","slug":"other"}]`}})
		remote, err := FetchProjectBySlug(context.Background(), c, "roadmap")
		if remote != nil || err != nil {
			t.Fatalf("got (%v,%v)", remote, err)
		}
	})
	t.Run("found with all nullable fields", func(t *testing.T) {
		t.Parallel()
		body := `[{
			"id":"p1","workspace_id":"w1","slug":"roadmap","name":"Roadmap",
			"description":"desc","color":"red","status":"active","priority":"high",
			"health":"on_track","target_date":"2026-12-31","lead_type":"agent","lead_id":"a1"
		}]`
		c := newCovClient(map[string]covRoute{"GET /api/v1/projects": {body: body}})
		remote, err := FetchProjectBySlug(context.Background(), c, "roadmap")
		if err != nil || remote == nil {
			t.Fatalf("got (%v,%v)", remote, err)
		}
		if remote.Description != "desc" || remote.TargetDate != "2026-12-31" ||
			remote.LeadType != "agent" || remote.LeadID != "a1" {
			t.Fatalf("pointer fields not unboxed: %+v", remote)
		}
	})
	t.Run("found with null pointers", func(t *testing.T) {
		t.Parallel()
		body := `[{"id":"p1","slug":"roadmap","name":"Roadmap","description":null,"target_date":null,"lead_type":null,"lead_id":null}]`
		c := newCovClient(map[string]covRoute{"GET /api/v1/projects": {body: body}})
		remote, err := FetchProjectBySlug(context.Background(), c, "roadmap")
		if err != nil || remote == nil {
			t.Fatalf("got (%v,%v)", remote, err)
		}
		if remote.Description != "" || remote.LeadID != "" {
			t.Fatalf("nulls must unbox to empty: %+v", remote)
		}
	})
}

func TestProjectCov_ResolveAgentSlugToID(t *testing.T) {
	t.Parallel()

	t.Run("fetch error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/agents": {status: 500}})
		if _, err := projectResolveAgentSlugToID(context.Background(), c, "eva"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/agents": {body: `[]`}})
		_, err := projectResolveAgentSlugToID(context.Background(), c, "eva")
		if err == nil || !strings.Contains(err.Error(), `agent with slug "eva" not found`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("found", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/agents": {body: `[{"id":"a1","slug":"eva"}]`}})
		id, err := projectResolveAgentSlugToID(context.Background(), c, "eva")
		if err != nil || id != "a1" {
			t.Fatalf("got (%q,%v)", id, err)
		}
	})
}

// ── small helpers ───────────────────────────────────────────────────

func TestProjectCov_ExpectSuccess(t *testing.T) {
	t.Parallel()

	if err := projectExpectSuccess(nil, "op"); err == nil || !strings.Contains(err.Error(), "nil response") {
		t.Fatalf("nil: got %v", err)
	}
	if err := projectExpectSuccess(&internalapi.Response{StatusCode: 200}, "op"); err != nil {
		t.Fatalf("2xx: got %v", err)
	}
	if err := projectExpectSuccess(&internalapi.Response{StatusCode: 500}, "op"); err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("500 no body: got %v", err)
	}
	err := projectExpectSuccess(&internalapi.Response{StatusCode: 400, Body: strings.NewReader(" detail ")}, "op")
	if err == nil || !strings.Contains(err.Error(), "status 400: detail") {
		t.Fatalf("400 with body: got %v", err)
	}
}

func TestProjectCov_DecodeJSON(t *testing.T) {
	t.Parallel()

	var v []projectRow
	if err := projectDecodeJSON(nil, &v); err != nil || v != nil {
		t.Fatalf("nil reader: got (%v,%v)", v, err)
	}
	if err := projectDecodeJSON(strings.NewReader(`[{"id":"p1"}]`), &v); err != nil || len(v) != 1 || v[0].ID != "p1" {
		t.Fatalf("decode: got (%v,%v)", v, err)
	}
}
