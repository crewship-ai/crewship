package kinds

// Second coverage pass for routine.go (routine_cov_test.go already
// exists and owns the shared covClient fake). This file pins the
// Validate rule matrix and the schedule/webhook list helpers' error
// and filter branches.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

func routineCov2Doc() *RoutineDocument {
	return &RoutineDocument{
		APIVersion: "crewship/v1",
		Kind:       "Routine",
		Metadata: internalapi.Metadata{
			Name:   "Sync",
			Slug:   "sync",
			Labels: map[string]string{"crew": "eng"},
		},
		Spec: RoutineSpec{
			DSLVersion: "routine.v1",
			Steps:      []RoutineStep{{ID: "s1", Type: "agent_run", AgentSlug: "eva"}},
		},
	}
}

func routineCov2Ctx() internalapi.WorkspaceContext {
	return internalapi.WorkspaceContext{
		DeclaredCrews:  []internalapi.SlugLookup{{Slug: "eng", Name: "Eng"}},
		DeclaredAgents: []internalapi.SlugLookup{{Slug: "eva", Name: "Eva"}},
	}
}

func TestRoutineCov2_Validate(t *testing.T) {
	t.Parallel()

	t.Run("valid document passes", func(t *testing.T) {
		t.Parallel()
		enabled := true
		doc := routineCov2Doc()
		doc.Spec.Schedules = []RoutineSchedule{
			{Name: "hourly", Cron: "0 * * * *", Timezone: "UTC", Enabled: &enabled},
		}
		if err := doc.Validate(routineCov2Ctx()); err != nil {
			t.Fatalf("valid doc rejected: %v", err)
		}
	})

	cases := []struct {
		name    string
		mutate  func(*RoutineDocument)
		wantErr string
	}{
		{
			name:    "missing slug",
			mutate:  func(d *RoutineDocument) { d.Metadata.Slug = " " },
			wantErr: "metadata.slug required",
		},
		{
			name:    "missing name",
			mutate:  func(d *RoutineDocument) { d.Metadata.Name = "" },
			wantErr: "metadata.name required",
		},
		{
			name:    "missing crew label",
			mutate:  func(d *RoutineDocument) { d.Metadata.Labels = nil },
			wantErr: "metadata.labels.crew required",
		},
		{
			name:    "unknown crew",
			mutate:  func(d *RoutineDocument) { d.Metadata.Labels["crew"] = "ghost" },
			wantErr: `metadata.labels.crew "ghost" not found`,
		},
		{
			name:    "missing dsl version",
			mutate:  func(d *RoutineDocument) { d.Spec.DSLVersion = "" },
			wantErr: "spec.dsl_version required",
		},
		{
			name:    "no steps",
			mutate:  func(d *RoutineDocument) { d.Spec.Steps = nil },
			wantErr: "spec.steps must have at least one step",
		},
		{
			name:    "unknown agent slug on agent_run step",
			mutate:  func(d *RoutineDocument) { d.Spec.Steps[0].AgentSlug = "ghost" },
			wantErr: `spec.steps[0].agent_slug "ghost" not found`,
		},
		{
			name: "schedule name required",
			mutate: func(d *RoutineDocument) {
				d.Spec.Schedules = []RoutineSchedule{{Cron: "0 * * * *", Timezone: "UTC"}}
			},
			wantErr: "spec.schedules[0].name required",
		},
		{
			name: "duplicate schedule names",
			mutate: func(d *RoutineDocument) {
				d.Spec.Schedules = []RoutineSchedule{
					{Name: "x", Cron: "0 * * * *", Timezone: "UTC"},
					{Name: "x", Cron: "5 * * * *", Timezone: "UTC"},
				}
			},
			wantErr: "spec.schedules[1].name duplicates schedules[0]",
		},
		{
			name: "cron required",
			mutate: func(d *RoutineDocument) {
				d.Spec.Schedules = []RoutineSchedule{{Name: "x", Timezone: "UTC"}}
			},
			wantErr: "spec.schedules[0].cron required",
		},
		{
			name: "cron invalid",
			mutate: func(d *RoutineDocument) {
				d.Spec.Schedules = []RoutineSchedule{{Name: "x", Cron: "not a cron", Timezone: "UTC"}}
			},
			wantErr: "spec.schedules[0].cron invalid",
		},
		{
			name: "timezone required",
			mutate: func(d *RoutineDocument) {
				d.Spec.Schedules = []RoutineSchedule{{Name: "x", Cron: "0 * * * *"}}
			},
			wantErr: "spec.schedules[0].timezone required",
		},
		{
			name: "timezone invalid",
			mutate: func(d *RoutineDocument) {
				d.Spec.Schedules = []RoutineSchedule{{Name: "x", Cron: "0 * * * *", Timezone: "Mars/Olympus"}}
			},
			wantErr: "spec.schedules[0].timezone invalid",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := routineCov2Doc()
			tc.mutate(doc)
			err := doc.Validate(routineCov2Ctx())
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// ── listRoutineSchedules ────────────────────────────────────────────

func TestRoutineCov2_ListRoutineSchedules(t *testing.T) {
	t.Parallel()

	const path = "GET /api/v1/workspaces/ws1/pipeline-schedules"

	t.Run("transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{path: {err: errors.New("down")}})
		if _, err := listRoutineSchedules(context.Background(), c, "ws1", "sync"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("404 treated as empty", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{path: {status: 404, body: "not found"}})
		rows, err := listRoutineSchedules(context.Background(), c, "ws1", "sync")
		if rows != nil || err != nil {
			t.Fatalf("got (%v,%v)", rows, err)
		}
	})
	t.Run("500 propagates", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{path: {status: 500, body: "boom"}})
		_, err := listRoutineSchedules(context.Background(), c, "ws1", "sync")
		if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("empty body", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{path: {body: ""}})
		rows, err := listRoutineSchedules(context.Background(), c, "ws1", "sync")
		if rows != nil || err != nil {
			t.Fatalf("got (%v,%v)", rows, err)
		}
	})
	t.Run("decode error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{path: {body: "{bad"}})
		_, err := listRoutineSchedules(context.Background(), c, "ws1", "sync")
		if err == nil || !strings.Contains(err.Error(), "decode schedules") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("filters by pipeline slug", func(t *testing.T) {
		t.Parallel()
		body := `[
			{"id":"s1","name":"hourly","target_pipeline_slug":"sync","cron_expr":"0 * * * *","timezone":"UTC","enabled":true},
			{"id":"s2","name":"other","target_pipeline_slug":"different","cron_expr":"0 0 * * *","timezone":"UTC","enabled":true}
		]`
		c := newCovClient(map[string]covRoute{path: {body: body}})
		rows, err := listRoutineSchedules(context.Background(), c, "ws1", "sync")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(rows) != 1 || rows[0].ID != "s1" {
			t.Fatalf("rows = %v", rows)
		}
	})
}

// ── getRoutineWebhook ───────────────────────────────────────────────

func TestRoutineCov2_GetRoutineWebhook(t *testing.T) {
	t.Parallel()

	const path = "GET /api/v1/workspaces/ws1/pipeline-webhooks"

	t.Run("transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{path: {err: errors.New("down")}})
		if _, err := getRoutineWebhook(context.Background(), c, "ws1", "sync"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("404 treated as none", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{path: {status: 404, body: "nope"}})
		wh, err := getRoutineWebhook(context.Background(), c, "ws1", "sync")
		if wh != nil || err != nil {
			t.Fatalf("got (%v,%v)", wh, err)
		}
	})
	t.Run("500 propagates", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{path: {status: 500, body: "boom"}})
		_, err := getRoutineWebhook(context.Background(), c, "ws1", "sync")
		if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("empty body", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{path: {body: ""}})
		wh, err := getRoutineWebhook(context.Background(), c, "ws1", "sync")
		if wh != nil || err != nil {
			t.Fatalf("got (%v,%v)", wh, err)
		}
	})
	t.Run("decode error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{path: {body: "nope"}})
		_, err := getRoutineWebhook(context.Background(), c, "ws1", "sync")
		if err == nil || !strings.Contains(err.Error(), "decode webhooks") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("match found", func(t *testing.T) {
		t.Parallel()
		body := `[
			{"id":"w0","target_pipeline_slug":"other","enabled":true},
			{"id":"w1","target_pipeline_slug":"sync","enabled":true}
		]`
		c := newCovClient(map[string]covRoute{path: {body: body}})
		wh, err := getRoutineWebhook(context.Background(), c, "ws1", "sync")
		if err != nil || wh == nil || wh.ID != "w1" {
			t.Fatalf("got (%v,%v)", wh, err)
		}
	})
	t.Run("no match returns nil", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{path: {body: `[{"id":"w0","target_pipeline_slug":"other"}]`}})
		wh, err := getRoutineWebhook(context.Background(), c, "ws1", "sync")
		if wh != nil || err != nil {
			t.Fatalf("got (%v,%v)", wh, err)
		}
	})
}

// ── small defaults ──────────────────────────────────────────────────

func TestRoutineCov2_RequireTokenOrDefault(t *testing.T) {
	t.Parallel()

	w := &RoutineWebhook{}
	if !w.RequireTokenOrDefault() {
		t.Fatal("nil RequireToken must default to true")
	}
	f := false
	w.RequireToken = &f
	if w.RequireTokenOrDefault() {
		t.Fatal("explicit false must be honored")
	}
}
