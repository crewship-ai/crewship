package main

// `routine state` CLI parity for the cross-run routine-state API (#1420
// follow-up). The behaviour that matters operationally and is easy to get
// wrong: --schedule must distinguish "not passed" (all buckets) from an
// explicitly empty value (the manual/webhook bucket), and every mutation must
// be bucket-scoped so repairing one schedule's cursor can't disturb another's.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func statePath(slug string) string {
	return "/api/v1/workspaces/" + covWSCli3 + "/pipelines/" + slug + "/state"
}

func TestRoutineStateList(t *testing.T) {
	t.Run("renders every bucket, naming the manual one", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(statePath("my-routine"), clitest.JSONResponse(200, routineStateResp{
			Slug: "my-routine",
			Buckets: []routineStateBucket{
				{ScheduleID: "", Entries: []routineStateEntry{
					{Key: "cursor", Value: "manual-1", UpdatedAt: "2026-07-25T09:00:00Z"},
				}},
				{ScheduleID: "psched_abc", Entries: []routineStateEntry{
					{Key: "cursor", Value: "2026-07-24", UpdatedAt: "2026-07-25T10:00:00Z"},
				}},
			},
		}))
		covResetFlags(t, routineStateListCmd)
		out := covCaptureStdoutCli3(t, func() {
			if err := routineStateListCmd.RunE(routineStateListCmd, []string{"my-routine"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		// A bare "" schedule id would read as a rendering bug, not as a bucket.
		if !strings.Contains(out, "(manual/webhook)") {
			t.Errorf("empty bucket must be labeled:\n%s", out)
		}
		if !strings.Contains(out, "psched_abc") || !strings.Contains(out, "2026-07-24") {
			t.Errorf("scheduled bucket missing:\n%s", out)
		}
		// updated_at is the tell for a frozen cursor — it has to be visible.
		if !strings.Contains(out, "2026-07-25T10:00:00Z") {
			t.Errorf("UPDATED column missing:\n%s", out)
		}
	})

	t.Run("empty state explains how state gets written", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(statePath("my-routine"), clitest.JSONResponse(200, routineStateResp{
			Slug: "my-routine", Buckets: []routineStateBucket{},
		}))
		covResetFlags(t, routineStateListCmd)
		out := covCaptureStdoutCli3(t, func() {
			if err := routineStateListCmd.RunE(routineStateListCmd, []string{"my-routine"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "no cross-run state") || !strings.Contains(out, "state_write") {
			t.Errorf("empty output should name the mechanism:\n%s", out)
		}
	})

	t.Run("omitting --schedule sends no filter", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(statePath("my-routine"), clitest.JSONResponse(200, routineStateResp{Slug: "my-routine"}))
		covResetFlags(t, routineStateListCmd)
		covCaptureStdoutCli3(t, func() {
			if err := routineStateListCmd.RunE(routineStateListCmd, []string{"my-routine"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		calls := stub.CallsFor("GET", statePath("my-routine"))
		if len(calls) != 1 {
			t.Fatalf("want 1 GET, got %d", len(calls))
		}
		if strings.Contains(calls[0].Query, "schedule_id") {
			t.Errorf("no --schedule must mean no filter (all buckets), got query %q", calls[0].Query)
		}
	})

	t.Run("explicit empty --schedule selects the manual bucket", func(t *testing.T) {
		// The distinction the server keys off Query().Has() for: "" is a real
		// selector, not "unset".
		stub := covStub(t)
		stub.OnGet(statePath("my-routine"), clitest.JSONResponse(200, routineStateResp{Slug: "my-routine"}))
		covResetFlags(t, routineStateListCmd)
		if err := routineStateListCmd.Flags().Set("schedule", ""); err != nil {
			t.Fatalf("set flag: %v", err)
		}
		covCaptureStdoutCli3(t, func() {
			if err := routineStateListCmd.RunE(routineStateListCmd, []string{"my-routine"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		calls := stub.CallsFor("GET", statePath("my-routine"))
		if len(calls) != 1 || !strings.Contains(calls[0].Query, "schedule_id") {
			t.Errorf("explicit --schedule '' must send schedule_id=, got %+v", calls)
		}
	})

	t.Run("api error propagates", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(statePath("my-routine"), clitest.ErrorResponse(404, "routine not found"))
		covResetFlags(t, routineStateListCmd)
		err := routineStateListCmd.RunE(routineStateListCmd, []string{"my-routine"})
		if err == nil || !strings.Contains(err.Error(), "routine not found") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestRoutineStateSet(t *testing.T) {
	keyPath := statePath("my-routine") + "/cursor"

	t.Run("PUTs value plus the schedule bucket", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPut(keyPath, clitest.JSONResponse(200, map[string]any{
			"slug": "my-routine", "key": "cursor", "value": "2026-07-25",
		}))
		covResetFlags(t, routineStateSetCmd)
		if err := routineStateSetCmd.Flags().Set("schedule", "psched_abc"); err != nil {
			t.Fatalf("set flag: %v", err)
		}
		covCaptureStdoutCli3(t, func() {
			if err := routineStateSetCmd.RunE(routineStateSetCmd, []string{"my-routine", "cursor", "2026-07-25"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		calls := stub.CallsFor("PUT", keyPath)
		if len(calls) != 1 {
			t.Fatalf("want 1 PUT, got %d", len(calls))
		}
		var body map[string]any
		if err := json.Unmarshal(calls[0].Body, &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["value"] != "2026-07-25" {
			t.Errorf("value = %v", body["value"])
		}
		// Without this the write silently lands in the manual bucket while the
		// schedule keeps its stale cursor — the exact bug this command fixes.
		if body["schedule_id"] != "psched_abc" {
			t.Errorf("schedule_id = %v, want psched_abc", body["schedule_id"])
		}
	})

	t.Run("403 propagates (manage-tier route)", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPut(keyPath, clitest.ErrorResponse(403, "Forbidden"))
		covResetFlags(t, routineStateSetCmd)
		err := routineStateSetCmd.RunE(routineStateSetCmd, []string{"my-routine", "cursor", "v"})
		if err == nil || !strings.Contains(err.Error(), "Forbidden") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestRoutineStateRm(t *testing.T) {
	keyPath := statePath("my-routine") + "/cursor"

	t.Run("deletes within the given bucket", func(t *testing.T) {
		stub := covStub(t)
		stub.OnDelete(keyPath, clitest.JSONResponse(200, map[string]any{"deleted": true}))
		covResetFlags(t, routineStateRmCmd)
		if err := routineStateRmCmd.Flags().Set("yes", "true"); err != nil {
			t.Fatalf("set flag: %v", err)
		}
		if err := routineStateRmCmd.Flags().Set("schedule", "psched_abc"); err != nil {
			t.Fatalf("set flag: %v", err)
		}
		covCaptureStdoutCli3(t, func() {
			if err := routineStateRmCmd.RunE(routineStateRmCmd, []string{"my-routine", "cursor"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		calls := stub.CallsFor("DELETE", keyPath)
		if len(calls) != 1 || !strings.Contains(calls[0].Query, "schedule_id=psched_abc") {
			t.Errorf("delete must be bucket-scoped, got %+v", calls)
		}
	})

	t.Run("404 on a mistyped key surfaces", func(t *testing.T) {
		stub := covStub(t)
		stub.OnDelete(statePath("my-routine")+"/curser",
			clitest.ErrorResponse(404, "no such key in this routine's state bucket"))
		covResetFlags(t, routineStateRmCmd)
		if err := routineStateRmCmd.Flags().Set("yes", "true"); err != nil {
			t.Fatalf("set flag: %v", err)
		}
		err := routineStateRmCmd.RunE(routineStateRmCmd, []string{"my-routine", "curser"})
		if err == nil || !strings.Contains(err.Error(), "no such key") {
			t.Fatalf("a typo must not read as success, got %v", err)
		}
	})
}

func TestRoutineStateClear(t *testing.T) {
	t.Run("clears one bucket and reports the count", func(t *testing.T) {
		stub := covStub(t)
		stub.OnDelete(statePath("my-routine"), clitest.JSONResponse(200, map[string]any{"removed": 3}))
		covResetFlags(t, routineStateClearCmd)
		if err := routineStateClearCmd.Flags().Set("yes", "true"); err != nil {
			t.Fatalf("set flag: %v", err)
		}
		if err := routineStateClearCmd.Flags().Set("schedule", "psched_abc"); err != nil {
			t.Fatalf("set flag: %v", err)
		}
		// cli.PrintSuccess writes to STDERR, so capture both streams — an
		// operator needs to see how many cursors just went, and asserting only
		// on stdout would silently pass even if the count were dropped.
		out := covCaptureAll(t, func() {
			if err := routineStateClearCmd.RunE(routineStateClearCmd, []string{"my-routine"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "Cleared 3 state key(s)") {
			t.Errorf("should report how many keys went:\n%s", out)
		}
		calls := stub.CallsFor("DELETE", statePath("my-routine"))
		if len(calls) != 1 || !strings.Contains(calls[0].Query, "schedule_id=psched_abc") {
			t.Errorf("clear must be bucket-scoped, got %+v", calls)
		}
	})
}
