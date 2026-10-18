package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ODCH-RT-06: GET /pipelines/{slug}/checkup answers with the checks the CLI's
// doctor composes plus what only the server knows.

func checkupOf(t *testing.T, h *PipelineHandler, slug string) (int, checkupResponse) {
	t.Helper()
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/x", nil), "ws_smoke")
	req.SetPathValue("slug", slug)
	rr := httptest.NewRecorder()
	h.GetCheckup(rr, req)
	var resp checkupResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	return rr.Code, resp
}

func (r checkupResponse) level(key string) string {
	for _, c := range r.Checks {
		if c.Key == key {
			return c.Level
		}
	}
	return ""
}

func TestGetCheckup(t *testing.T) {
	db := openSmokeDB(t)
	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%q: %v", q, err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// A routine whose agent exists, with a cost cap and a clean history.
	seedSmokePipelineDef(t, db, "healthy", `{"name":"healthy","max_cost_usd":1,"estimated_cost_usd":0.1,"steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`)
	var healthyID string
	_ = db.QueryRow(`SELECT id FROM pipelines WHERE slug='healthy'`).Scan(&healthyID)
	mustExec(`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, mode, started_at, created_at, updated_at) VALUES ('ck_ok','ws_smoke',?, 'healthy','completed','run',?,?,?)`, healthyID, now, now, now)

	// A routine with a missing agent, a missing credential, no cap, two failures, a tripped breaker.
	seedSmokePipelineDef(t, db, "broken", `{"name":"broken","credentials_required":[{"type":"LINEAR"}],"steps":[{"id":"a","type":"agent_run","agent_slug":"ghost","prompt":"hi"}]}`)
	var brokenID string
	_ = db.QueryRow(`SELECT id FROM pipelines WHERE slug='broken'`).Scan(&brokenID)
	for i, id := range []string{"ck_f1", "ck_f2"} {
		ts := time.Now().UTC().Add(-time.Duration(2-i) * time.Hour).Format(time.RFC3339Nano)
		mustExec(`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, mode, error_message, started_at, created_at, updated_at) VALUES (?,'ws_smoke',?, 'broken','failed','run','Linear credential rejected (401)',?,?,?)`, id, brokenID, ts, ts, ts)
	}
	mustExec(`INSERT INTO pipeline_schedules (id, workspace_id, name, target_pipeline_id, cron_expr, timezone, inputs_json, enabled, consecutive_failures, max_consecutive_failures, disabled_reason, created_at, updated_at)
		VALUES ('ck_s','ws_smoke','Nightly',?, '0 2 * * *','UTC','{}',0,5,5,'circuit_breaker',?,?)`, brokenID, now, now)

	// Off and proposed routines.
	seedSmokePipelineDef(t, db, "off", `{"name":"off","steps":[]}`)
	mustExec(`UPDATE pipelines SET status='disabled' WHERE slug='off'`)
	seedSmokePipelineDef(t, db, "proposed", `{"name":"proposed","steps":[]}`)
	mustExec(`UPDATE pipelines SET status='proposed' WHERE slug='proposed'`)

	cases := []struct {
		slug string
		want map[string]string // check key -> level; "" = must be absent
	}{
		{"healthy", map[string]string{"runnable": "ok", "agents": "ok", "cost": "ok", "failures-in-a-row": "ok", "credentials": "", "untested-changes": ""}},
		{"broken", map[string]string{"agents": "bad", "credentials": "bad", "cost": "warn", "failures-in-a-row": "warn", "missing-credential": "bad", "breaker:Nightly": "bad"}},
		{"off", map[string]string{"off": "bad", "runnable": ""}},
		{"proposed", map[string]string{"proposed": "bad"}},
	}
	for _, tc := range cases {
		t.Run(tc.slug, func(t *testing.T) {
			code, resp := checkupOf(t, h, tc.slug)
			if code != http.StatusOK {
				t.Fatalf("status %d", code)
			}
			for key, level := range tc.want {
				if got := resp.level(key); got != level {
					t.Errorf("%s = %q, want %q (checks: %+v)", key, got, level, resp.Checks)
				}
			}
			if resp.Total != len(resp.Checks) || resp.Passed+resp.Warned+resp.Failed != resp.Total {
				t.Errorf("counts do not add up: %+v", resp)
			}
			// Worst first.
			rank := map[string]int{"bad": 0, "warn": 1, "ok": 2}
			for i := 1; i < len(resp.Checks); i++ {
				if rank[resp.Checks[i-1].Level] > rank[resp.Checks[i].Level] {
					t.Errorf("checks are not worst first: %+v", resp.Checks)
					break
				}
			}
		})
	}

	if code, _ := checkupOf(t, h, "nope"); code != http.StatusNotFound {
		t.Errorf("unknown routine = %d, want 404", code)
	}
}

func TestGetCheckup_UntestedChanges(t *testing.T) {
	db := openSmokeDB(t)
	h := NewPipelineHandler(db, slog.Default(), &stubRunner{output: "x"}, nil)
	h.SetSaveTokenSecret([]byte("test-secret-32-bytes-long-padxxx"))
	seedSmokePipelineDef(t, db, "tr", testResultDef)
	doc, _ := json.Marshal(map[string]any{"slug": "tr", "name": "tr", "definition": json.RawMessage(testResultDef)})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO pipeline_drafts (id, workspace_id, slug, revision, document_json, updated_by, created_at, updated_at) VALUES ('d1','ws_smoke','tr',1,?, 'user_tr',?,?)`, string(doc), now, now); err != nil {
		t.Fatal(err)
	}
	if _, resp := checkupOf(t, h, "tr"); resp.level("untested-changes") != "warn" {
		t.Fatalf("untested draft: %+v", resp.Checks)
	}
	testRunOnce(t, h, testResultDef)
	if _, resp := checkupOf(t, h, "tr"); resp.level("untested-changes") != "ok" {
		t.Fatalf("tested draft: %+v", resp.Checks)
	}
}
