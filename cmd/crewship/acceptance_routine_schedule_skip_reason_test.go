package main

// #4490: recorded skip telemetry survives real API → binary CLI formatting.
// Seed only last-fire telemetry after creating a real routine and schedule;
// scheduler admission itself is covered by the scheduler's boundary tests.
import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAcceptanceRoutineSchedules_LastSkipReason(t *testing.T) {
	ownerCfg, _, db := startRoutineSchedulesAcceptanceServer(t)
	saveRoutineSchedulesTarget(t, ownerCfg)
	out, stderr, err := runRoutineSchedulesCLI(t, ownerCfg, "-f", "json", "routine", "schedules", "create",
		"--slug", "acceptance-sched", "--name", "recorded-outcome", "--cron", "0 9 * * *")
	if err != nil {
		t.Fatalf("create schedule: %v\n%s%s", err, out, stderr)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == "" {
		t.Fatalf("create receipt: %v\n%s", err, out)
	}
	// Today's disabled routine cannot turn its earlier busy skip into an
	// inactive skip. All cases must use the recorded last-fire metadata.
	if _, err := db.Exec(`UPDATE pipelines SET status='disabled' WHERE workspace_id=? AND slug='acceptance-sched'`, routineSchedulesAcceptanceWorkspaceID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, status, reason, label string
	}{
		{"capacity", "SKIPPED", "capacity", "SKIPPED (capacity)"},
		{"catchup", "SKIPPED", "catchup", "SKIPPED (catch-up)"},
		{"inactive", "SKIPPED", "inactive", "SKIPPED (routine inactive)"},
		{"busy before disable", "SKIPPED", "busy", "SKIPPED (previous run busy)"},
		{"legacy missing reason", "SKIPPED", "", "SKIPPED (reason unavailable)"},
		{"other status ignores stale reason", "COMPLETED", "busy", "COMPLETED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(`UPDATE pipeline_schedules SET last_status=?,last_skip_reason=?,last_run_at='2026-11-16T10:00:00Z',last_missed_count=4 WHERE id=?`, tc.status, tc.reason, created.ID); err != nil {
				t.Fatal(err)
			}
			table, stderr, err := runRoutineSchedulesCLI(t, ownerCfg, "routine", "schedules", "list")
			if err != nil || !strings.Contains(table, "LAST STATUS") || !strings.Contains(table, tc.label) {
				t.Fatalf("table lost recorded reason: %v\n%s%s", err, table, stderr)
			}
			if tc.status != "SKIPPED" && strings.Contains(table, "previous run busy") {
				t.Fatalf("stale skip metadata changed the latest outcome:\n%s", table)
			}
			for _, format := range []string{"json", "yaml", "ndjson"} {
				t.Run(format, func(t *testing.T) {
					out, stderr, err := runRoutineSchedulesCLI(t, ownerCfg, "-f", format, "routine", "schedules", "list")
					if err != nil {
						t.Fatalf("list: %v\n%s%s", err, out, stderr)
					}
					var rows []map[string]any
					if format == "ndjson" {
						var row map[string]any
						err = json.Unmarshal([]byte(out), &row)
						rows = []map[string]any{row}
					} else if format == "json" {
						err = json.Unmarshal([]byte(out), &rows)
					} else {
						err = yaml.Unmarshal([]byte(out), &rows)
					}
					if err != nil || len(rows) != 1 {
						t.Fatalf("decode rows: %v\n%s", err, out)
					}
					row := rows[0]
					if row["last_status"] != tc.status || row["id"] != created.ID {
						t.Fatalf("lost API identity/status: %v", row)
					}
					if tc.reason == "" {
						if _, exists := row["last_skip_reason"]; exists {
							t.Fatalf("invented a legacy reason: %v", row)
						}
					} else if row["last_skip_reason"] != tc.reason {
						t.Fatalf("lost API reason: %v", row)
					}
				})
			}
		})
	}
}

// A newer server may add a reason that this binary does not yet name. Its
// document must remain parseable and lossless, while the table stays neutral.
func TestAcceptanceRoutineSchedules_UnknownSkipReason(t *testing.T) {
	stub := &settingsStub{routes: map[string]func(map[string]any) (int, string){
		"GET /api/v1/workspaces/ws_test/pipeline-schedules": func(map[string]any) (int, string) {
			return 200, `[{"id":"future-schedule","name":"future","target_pipeline_slug":"future","last_status":"SKIPPED","last_skip_reason":"future_reason","last_missed_count":4}]`
		},
	}}
	cfg := profileStubConfig(t, stub.start(t).URL)
	out, stderr, err := runRoutineSchedulesCLI(t, cfg, "routine", "schedules", "list")
	if err != nil || !strings.Contains(out, "SKIPPED (reason unavailable)") || strings.Contains(out, "SKIPPED (future_reason)") {
		t.Fatalf("unknown cause was inferred: %v\n%s%s", err, out, stderr)
	}
	for _, format := range []string{"json", "yaml", "ndjson"} {
		out, stderr, err := runRoutineSchedulesCLI(t, cfg, "-f", format, "routine", "schedules", "list")
		if err != nil {
			t.Fatalf("%s list: %v\n%s%s", format, err, out, stderr)
		}
		var row map[string]any
		if format == "ndjson" {
			err = json.Unmarshal([]byte(out), &row)
		} else {
			var rows []map[string]any
			if format == "json" {
				err = json.Unmarshal([]byte(out), &rows)
			} else {
				err = yaml.Unmarshal([]byte(out), &rows)
			}
			if len(rows) == 1 {
				row = rows[0]
			}
		}
		if err != nil || row["last_skip_reason"] != "future_reason" || row["last_status"] != "SKIPPED" {
			t.Fatalf("%s dropped unknown history: %v\n%s", format, err, out)
		}
	}
}
