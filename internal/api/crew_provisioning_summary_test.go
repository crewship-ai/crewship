package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// The Dashboard must know which crews need a rebuild without asking once per
// crew. GET /api/v1/crews/provisioning-summary reports, for the caller's
// workspace only, the crews whose latest build failed and those that have no
// built image yet.
func TestProvisionSummary(t *testing.T) {
	type crewSeed struct {
		id, slug, cached string
		deleted, setup   bool
		job              string // in-memory job status, "" for none
		durableFailure   bool
		otherWorkspace   bool
	}
	tests := []struct {
		name       string
		crews      []crewSeed
		wantFailed []string
		wantNeeds  []string
	}{
		{name: "empty workspace"},
		{
			name:  "built image is fine",
			crews: []crewSeed{{id: "c1", slug: "built", cached: "img:1"}},
		},
		{
			name:      "no image and no job needs a provision",
			crews:     []crewSeed{{id: "c1", slug: "fresh"}},
			wantNeeds: []string{"c1"},
		},
		{
			name:  "running build is neither",
			crews: []crewSeed{{id: "c1", slug: "busy", job: "running"}},
		},
		{
			name:       "failed in-memory job",
			crews:      []crewSeed{{id: "c1", slug: "bad", job: "failed"}},
			wantFailed: []string{"c1"},
		},
		{
			name:       "durable failure beats a cached image",
			crews:      []crewSeed{{id: "c1", slug: "old", cached: "img:1", durableFailure: true}},
			wantFailed: []string{"c1"},
		},
		{
			name: "deleted, setup and foreign-workspace crews are left out",
			crews: []crewSeed{
				{id: "c1", slug: "gone", deleted: true},
				{id: "c2", slug: "setup", setup: true},
				{id: "c3", slug: "foreign", otherWorkspace: true},
				{id: "c4", slug: "mine"},
			},
			wantNeeds: []string{"c4"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
			h := NewProvisioningHandler(db, logger, nil, nil, nil, nil, "", nil)
			t.Cleanup(h.Stop)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			otherWS := "other-workspace-id"
			execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS)
			w := journal.NewWriter(db, logger, journal.WriterOptions{})
			for _, c := range tc.crews {
				ws := wsID
				if c.otherWorkspace {
					ws = otherWS
				}
				kind := "standard"
				if c.setup {
					kind = "setup"
				}
				execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug, kind, cached_image) VALUES (?, ?, ?, ?, ?, ?)`,
					c.id, ws, "Crew "+c.slug, c.slug, kind, c.cached)
				if c.deleted {
					execOrFatal(t, db, `UPDATE crews SET deleted_at = datetime('now') WHERE id = ?`, c.id)
				}
				if c.job != "" {
					h.mu.Lock()
					h.jobs[c.id] = &ProvisionJob{CrewID: c.id, Status: c.job, Error: "boom"}
					h.mu.Unlock()
				}
				if c.durableFailure {
					if _, err := w.Emit(context.Background(), journal.Entry{
						WorkspaceID: ws, CrewID: c.id, Type: journal.EntryProvisioningFailed,
						Severity: journal.SeverityWarn, ActorType: journal.ActorOrchestrator,
						Summary: "failed", Payload: map[string]any{"error": "x"},
					}); err != nil {
						t.Fatalf("emit: %v", err)
					}
				}
			}
			if err := w.Flush(context.Background()); err != nil {
				t.Fatalf("flush: %v", err)
			}

			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/v1/crews/provisioning-summary", h.ProvisionSummary)
			req := httptest.NewRequest("GET", "/api/v1/crews/provisioning-summary", nil)
			req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "VIEWER"))
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
			}
			var got struct {
				Failed         []struct{ ID, Slug, Name, Error string } `json:"failed"`
				NeedsProvision []struct{ ID, Slug, Name string }        `json:"needs_provision"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v (%s)", err, rr.Body.String())
			}
			if got.Failed == nil || got.NeedsProvision == nil {
				t.Errorf("lists must be [] not null: %s", rr.Body.String())
			}
			ids := func(n int, f func(i int) string) []string {
				out := []string{}
				for i := 0; i < n; i++ {
					out = append(out, f(i))
				}
				return out
			}
			gf := ids(len(got.Failed), func(i int) string { return got.Failed[i].ID })
			gn := ids(len(got.NeedsProvision), func(i int) string { return got.NeedsProvision[i].ID })
			eq := func(a, b []string) bool {
				if len(a) != len(b) {
					return false
				}
				for i := range a {
					if a[i] != b[i] {
						return false
					}
				}
				return true
			}
			if !eq(gf, append([]string{}, tc.wantFailed...)) {
				t.Errorf("failed = %v, want %v", gf, tc.wantFailed)
			}
			if !eq(gn, append([]string{}, tc.wantNeeds...)) {
				t.Errorf("needs_provision = %v, want %v", gn, tc.wantNeeds)
			}
		})
	}
}
