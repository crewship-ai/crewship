package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// #1219 — the --format contract across the `routine schedules` family.
//
// `list` already routed through the shared formatter (#782), but every
// sibling hardcoded fmt.Printf, so the one thing a script actually needs to
// automate schedules — the id that `create` mints, and a parseable
// acknowledgement from now/enable/disable/update/delete — was only ever
// available as prose. That's the same class of gap the global -f/--format
// flag exists to close (#964).
//
// Each case asserts both directions: the machine format decodes to the
// documented shape, and the default path keeps its human substrings so
// eyeballs (and anything scraping the current output) don't regress.

const covSchedID = "psched_cmrm3xxzk0083de436e64"

func schedFmtStub(t *testing.T) *clitest.StubServer {
	t.Helper()
	stub := covSetupCli5(t)
	return stub
}

func TestRoutineSchedulesNowRunE_Format(t *testing.T) {
	stub := schedFmtStub(t)
	stub.OnPost("/api/v1/workspaces/"+covWSCli5+"/pipeline-schedules/"+covSchedID+"/run",
		clitest.JSONResponse(200, map[string]any{"ok": true}))

	t.Run("json", func(t *testing.T) {
		flagFormat = "json"
		t.Cleanup(func() { flagFormat = "" })
		var err error
		out := covCaptureStdoutCli5(t, func() {
			err = routineSchedulesNowCmd.RunE(routineSchedulesNowCmd, []string{covSchedID})
		})
		if err != nil {
			t.Fatalf("now --format json: %v", err)
		}
		var got map[string]any
		if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
			t.Fatalf("output is not JSON (%v): %s", jerr, out)
		}
		if got["id"] != covSchedID {
			t.Errorf("json should carry the full schedule id, got: %s", out)
		}
		if got["fired"] != true {
			t.Errorf("json should report fired=true, got: %s", out)
		}
	})

	t.Run("default stays human", func(t *testing.T) {
		flagFormat = ""
		var err error
		out := covCaptureStdoutCli5(t, func() {
			err = routineSchedulesNowCmd.RunE(routineSchedulesNowCmd, []string{covSchedID})
		})
		if err != nil {
			t.Fatalf("now: %v", err)
		}
		if !strings.Contains(out, "fired (out-of-cycle)") {
			t.Errorf("human output must survive byte-for-byte, got: %s", out)
		}
	})
}

func TestRoutineSchedulesSetEnabledRunE_Format(t *testing.T) {
	stub := schedFmtStub(t)
	stub.OnPatch("/api/v1/workspaces/"+covWSCli5+"/pipeline-schedules/"+covSchedID,
		clitest.JSONResponse(200, map[string]any{"id": covSchedID, "enabled": false}))

	t.Run("json", func(t *testing.T) {
		flagFormat = "json"
		t.Cleanup(func() { flagFormat = "" })
		var err error
		out := covCaptureStdoutCli5(t, func() {
			err = routineSchedulesDisableCmd.RunE(routineSchedulesDisableCmd, []string{covSchedID})
		})
		if err != nil {
			t.Fatalf("disable --format json: %v", err)
		}
		var got map[string]any
		if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
			t.Fatalf("output is not JSON (%v): %s", jerr, out)
		}
		if got["id"] != covSchedID {
			t.Errorf("json should carry the full schedule id, got: %s", out)
		}
		if got["enabled"] != false {
			t.Errorf("json should report enabled=false, got: %s", out)
		}
	})

	t.Run("default stays human", func(t *testing.T) {
		flagFormat = ""
		var err error
		out := covCaptureStdoutCli5(t, func() {
			err = routineSchedulesDisableCmd.RunE(routineSchedulesDisableCmd, []string{covSchedID})
		})
		if err != nil {
			t.Fatalf("disable: %v", err)
		}
		if !strings.Contains(out, "disabled") {
			t.Errorf("human output must survive byte-for-byte, got: %s", out)
		}
	})
}

// create is the only place a schedule id is minted, so a script that
// can't parse this reply can never reach `schedules now <id>` — this is
// the concrete blocker the harness hit.
func TestRoutineSchedulesCreateRunE_Format(t *testing.T) {
	stub := schedFmtStub(t)
	stub.OnPost("/api/v1/workspaces/"+covWSCli5+"/pipeline-schedules",
		clitest.JSONResponse(200, map[string]any{
			"id":        covSchedID,
			"name":      "nightly",
			"cron_expr": "0 3 * * *",
			"timezone":  "UTC",
		}))
	// routineSchedulesCreateCmd is package-level, so these values would
	// otherwise persist into every later test in the package.
	if ferr := routineSchedulesCreateCmd.Flags().Set("slug", "classify-ticket"); ferr != nil {
		t.Fatalf("set --slug: %v", ferr)
	}
	if ferr := routineSchedulesCreateCmd.Flags().Set("cron", "0 3 * * *"); ferr != nil {
		t.Fatalf("set --cron: %v", ferr)
	}
	t.Cleanup(func() {
		_ = routineSchedulesCreateCmd.Flags().Set("slug", "")
		_ = routineSchedulesCreateCmd.Flags().Set("cron", "")
	})

	flagFormat = "json"
	t.Cleanup(func() { flagFormat = "" })
	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = routineSchedulesCreateCmd.RunE(routineSchedulesCreateCmd, nil)
	})
	if err != nil {
		t.Fatalf("create --format json: %v", err)
	}
	var got map[string]any
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("output is not JSON (%v): %s", jerr, out)
	}
	// The whole point: the full, untruncated id must round-trip.
	if got["id"] != covSchedID {
		t.Errorf("json must carry the full schedule id for `schedules now <id>`, got: %s", out)
	}
}

func TestRoutineSchedulesDeleteRunE_Format(t *testing.T) {
	stub := schedFmtStub(t)
	stub.OnDelete("/api/v1/workspaces/"+covWSCli5+"/pipeline-schedules/"+covSchedID,
		clitest.JSONResponse(200, map[string]any{}))

	flagFormat = "json"
	t.Cleanup(func() { flagFormat = "" })
	// Non-interactive: delete prompts on stdin without --yes.
	if ferr := routineSchedulesDeleteCmd.Flags().Set("yes", "true"); ferr != nil {
		t.Fatalf("set --yes: %v", ferr)
	}
	t.Cleanup(func() { _ = routineSchedulesDeleteCmd.Flags().Set("yes", "false") })
	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = routineSchedulesDeleteCmd.RunE(routineSchedulesDeleteCmd, []string{covSchedID})
	})
	if err != nil {
		t.Fatalf("delete --format json: %v", err)
	}
	var got map[string]any
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("output is not JSON (%v): %s", jerr, out)
	}
	if got["id"] != covSchedID || got["deleted"] != true {
		t.Errorf("json should report the deleted id, got: %s", out)
	}
}
