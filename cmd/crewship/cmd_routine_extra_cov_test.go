package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// ─── pure helpers ───────────────────────────────────────────────────────

func TestMustJSON(t *testing.T) {
	if got := string(mustJSON(map[string]any{"a": 1})); got != `{"a":1}` {
		t.Errorf("mustJSON: got %q", got)
	}
	defer func() {
		if r := recover(); r == nil {
			t.Error("mustJSON should panic on unmarshalable input")
		}
	}()
	mustJSON(make(chan int))
}

func TestTruncIDForCLI(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly-ten", 11, "exactly-ten"},
		{"abcdefghijklmno", 6, "abcdef…"},
		{"abcd_-xyz", 6, "abcd…"}, // trailing separators trimmed before ellipsis
	}
	for _, tc := range cases {
		if got := truncIDForCLI(tc.in, tc.n); got != tc.want {
			t.Errorf("truncIDForCLI(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

// ─── versions ───────────────────────────────────────────────────────────

func TestRoutineVersionsCmd(t *testing.T) {
	versionsPath := "/api/v1/workspaces/" + covWSCli3 + "/pipelines/my-routine/versions"

	t.Run("table with head marker and parent", func(t *testing.T) {
		stub := covStub(t)
		parent := 2
		stub.OnGet(versionsPath, clitest.JSONResponse(200, []pipelineVersionRow{
			{Version: 3, ParentVersion: &parent, DefinitionHash: "abcdef1234567890", AuthorType: "user",
				AuthorID: "u1", ChangeSummary: strings.Repeat("long summary ", 10), CreatedAt: "2026-06-01"},
			{Version: 2, DefinitionHash: "ffff", AuthorType: "agent", AuthorID: "a1", CreatedAt: "2026-05-01"},
		}))
		out := covCaptureStdoutCli3(t, func() {
			if err := routineVersionsCmd.RunE(routineVersionsCmd, []string{"my-routine"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "v3") || !strings.Contains(out, "v2") {
			t.Errorf("version rows missing: %q", out)
		}
		if !strings.Contains(out, "*") {
			t.Errorf("head marker missing: %q", out)
		}
		if !strings.Contains(out, "...") {
			t.Errorf("long summary should be truncated with ...: %q", out)
		}
		if !strings.Contains(out, "—") {
			t.Errorf("missing parent should render as —: %q", out)
		}
	})

	t.Run("empty history", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(versionsPath, clitest.JSONResponse(200, []pipelineVersionRow{}))
		out := covCaptureStdoutCli3(t, func() {
			if err := routineVersionsCmd.RunE(routineVersionsCmd, []string{"my-routine"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "No version history yet.") {
			t.Errorf("expected empty message: %q", out)
		}
	})

	t.Run("api error", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(versionsPath, clitest.ErrorResponse(404, "routine not found"))
		err := routineVersionsCmd.RunE(routineVersionsCmd, []string{"my-routine"})
		if err == nil || !strings.Contains(err.Error(), "routine not found") {
			t.Fatalf("expected 404, got %v", err)
		}
	})

	t.Run("not logged in", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{}
		if err := routineVersionsCmd.RunE(routineVersionsCmd, []string{"x"}); err == nil {
			t.Fatal("expected auth error")
		}
	})
}

func TestRoutineVersionsShowCmd(t *testing.T) {
	showPath := "/api/v1/workspaces/" + covWSCli3 + "/pipelines/my-routine/versions/3"

	t.Run("requires positive version", func(t *testing.T) {
		covStub(t)
		covResetFlags(t, routineVersionsShowCmd)
		err := routineVersionsShowCmd.RunE(routineVersionsShowCmd, []string{"my-routine"})
		if err == nil || !strings.Contains(err.Error(), "--version <n> is required") {
			t.Fatalf("expected version-required error, got %v", err)
		}
	})

	t.Run("pretty prints json", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, routineVersionsShowCmd)
		stub.OnGet(showPath, clitest.JSONResponse(200, map[string]any{"version": 3, "definition": map[string]any{"steps": []string{"a"}}}))
		covSetFlags(t, routineVersionsShowCmd, map[string]string{"version": "3"})
		out := covCaptureStdoutCli3(t, func() {
			if err := routineVersionsShowCmd.RunE(routineVersionsShowCmd, []string{"my-routine"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, `"version": 3`) || !strings.Contains(out, `"steps"`) {
			t.Errorf("pretty json missing: %q", out)
		}
	})

	t.Run("non-json body printed raw", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, routineVersionsShowCmd)
		stub.OnGet(showPath, clitest.TextResponse(200, "not json payload"))
		covSetFlags(t, routineVersionsShowCmd, map[string]string{"version": "3"})
		out := covCaptureStdoutCli3(t, func() {
			if err := routineVersionsShowCmd.RunE(routineVersionsShowCmd, []string{"my-routine"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "not json payload") {
			t.Errorf("raw body missing: %q", out)
		}
	})

	t.Run("api error", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, routineVersionsShowCmd)
		stub.OnGet(showPath, clitest.ErrorResponse(404, "version not found"))
		covSetFlags(t, routineVersionsShowCmd, map[string]string{"version": "3"})
		err := routineVersionsShowCmd.RunE(routineVersionsShowCmd, []string{"my-routine"})
		if err == nil || !strings.Contains(err.Error(), "version not found") {
			t.Fatalf("expected 404, got %v", err)
		}
	})
}

// ─── active ─────────────────────────────────────────────────────────────

func TestRoutineActiveCmd(t *testing.T) {
	activePath := "/api/v1/workspaces/" + covWSCli3 + "/pipelines/runs/active"

	t.Run("empty", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(activePath, clitest.JSONResponse(200, []any{}))
		out := covCaptureStdoutCli3(t, func() {
			if err := routineActiveCmd.RunE(routineActiveCmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "No active runs.") {
			t.Errorf("expected empty message: %q", out)
		}
	})

	t.Run("rows with cancel marker", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(activePath, clitest.JSONResponse(200, []map[string]any{
			{"run_id": "run-1", "pipeline_slug": "nightly", "concurrency_key": "k1",
				"started_at": "2026-06-01T10:00:00Z", "cancel_requested": true},
			{"run_id": "run-2", "pipeline_slug": "hourly", "started_at": "2026-06-01T11:00:00Z"},
		}))
		out := covCaptureStdoutCli3(t, func() {
			if err := routineActiveCmd.RunE(routineActiveCmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "nightly") || !strings.Contains(out, "hourly") {
			t.Errorf("run rows missing: %q", out)
		}
		if !strings.Contains(out, "yes") {
			t.Errorf("cancel_requested marker missing: %q", out)
		}
	})

	t.Run("api error", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(activePath, clitest.ErrorResponse(500, "scheduler down"))
		if err := routineActiveCmd.RunE(routineActiveCmd, nil); err == nil {
			t.Fatal("expected error")
		}
	})
}

// ─── rollback ───────────────────────────────────────────────────────────

func TestRoutineRollbackCmd(t *testing.T) {
	rollbackPath := "/api/v1/workspaces/" + covWSCli3 + "/pipelines/my-routine/rollback"

	t.Run("requires --to", func(t *testing.T) {
		covStub(t)
		covResetFlags(t, routineRollbackCmd)
		err := routineRollbackCmd.RunE(routineRollbackCmd, []string{"my-routine"})
		if err == nil || !strings.Contains(err.Error(), "--to <version> is required") {
			t.Fatalf("expected --to error, got %v", err)
		}
	})

	t.Run("happy", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, routineRollbackCmd)
		stub.OnPost(rollbackPath, clitest.JSONResponse(200, map[string]any{"version": 4}))
		covSetFlags(t, routineRollbackCmd, map[string]string{"to": "2"})
		out := covCaptureStdoutCli3(t, func() {
			if err := routineRollbackCmd.RunE(routineRollbackCmd, []string{"my-routine"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "Rolled back my-routine to v2.") {
			t.Errorf("rollback message wrong: %q", out)
		}
		calls := stub.CallsFor("POST", rollbackPath)
		if len(calls) != 1 {
			t.Fatalf("expected 1 POST, got %d", len(calls))
		}
		var body map[string]any
		clitest.MustDecodeJSONBody(calls[0].Body, &body)
		if tv, ok := body["target_version"].(float64); !ok || tv != 2 {
			t.Errorf("target_version wrong: %v", body)
		}
	})

	t.Run("api error", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, routineRollbackCmd)
		stub.OnPost(rollbackPath, clitest.ErrorResponse(404, "target version missing"))
		covSetFlags(t, routineRollbackCmd, map[string]string{"to": "99"})
		err := routineRollbackCmd.RunE(routineRollbackCmd, []string{"my-routine"})
		if err == nil || !strings.Contains(err.Error(), "target version missing") {
			t.Fatalf("expected 404, got %v", err)
		}
	})
}

// ─── export ─────────────────────────────────────────────────────────────

func TestRoutineExportCmd(t *testing.T) {
	exportPath := "/api/v1/workspaces/" + covWSCli3 + "/pipelines/my-routine/export"

	t.Run("streams bundle to stdout", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, routineExportCmd)
		stub.OnGet(exportPath, clitest.TextResponse(200, `{"slug":"my-routine"}`))
		out := covCaptureStdoutCli3(t, func() {
			if err := routineExportCmd.RunE(routineExportCmd, []string{"my-routine"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if out != `{"slug":"my-routine"}` {
			t.Errorf("export output: %q", out)
		}
		calls := stub.CallsFor("GET", exportPath)
		if len(calls) != 1 || strings.Contains(calls[0].Query, "include_history") {
			t.Errorf("include_history must be absent by default: %+v", calls)
		}
	})

	t.Run("include-history query param", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, routineExportCmd)
		stub.OnGet(exportPath, clitest.TextResponse(200, "{}"))
		covSetFlags(t, routineExportCmd, map[string]string{"include-history": "true"})
		_ = covCaptureStdoutCli3(t, func() {
			if err := routineExportCmd.RunE(routineExportCmd, []string{"my-routine"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		calls := stub.CallsFor("GET", exportPath)
		if len(calls) != 1 || !strings.Contains(calls[0].Query, "include_history=1") {
			t.Errorf("include_history=1 missing: %+v", calls)
		}
	})

	t.Run("api error", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, routineExportCmd)
		stub.OnGet(exportPath, clitest.ErrorResponse(404, "routine not found"))
		err := routineExportCmd.RunE(routineExportCmd, []string{"my-routine"})
		if err == nil || !strings.Contains(err.Error(), "routine not found") {
			t.Fatalf("expected 404, got %v", err)
		}
	})
}

// ─── import ─────────────────────────────────────────────────────────────

func TestRoutineImportCmd(t *testing.T) {
	importPath := "/api/v1/workspaces/" + covWSCli3 + "/pipelines/import"
	bundle := `{"slug":"imported","definition":{}}`

	t.Run("from file", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost(importPath, clitest.JSONResponse(200, map[string]string{"slug": "imported", "id": "p1"}))
		file := filepath.Join(t.TempDir(), "bundle.json")
		if err := os.WriteFile(file, []byte(bundle), 0o644); err != nil {
			t.Fatal(err)
		}
		out := covCaptureStdoutCli3(t, func() {
			if err := routineImportCmd.RunE(routineImportCmd, []string{file}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, `Imported routine "imported" (id=p1).`) {
			t.Errorf("import message wrong: %q", out)
		}
		calls := stub.CallsFor("POST", importPath)
		if len(calls) != 1 || string(calls[0].Body) != bundle {
			t.Errorf("bundle not forwarded verbatim: %+v", calls)
		}
	})

	t.Run("from stdin", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost(importPath, clitest.JSONResponse(200, map[string]string{"slug": "imported", "id": "p2"}))
		covWithStdin(t, bundle)
		out := covCaptureStdoutCli3(t, func() {
			if err := routineImportCmd.RunE(routineImportCmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "p2") {
			t.Errorf("import message wrong: %q", out)
		}
	})

	t.Run("empty stdin", func(t *testing.T) {
		covStub(t)
		covWithStdin(t, "")
		err := routineImportCmd.RunE(routineImportCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "empty bundle") {
			t.Fatalf("expected empty-bundle error, got %v", err)
		}
	})

	t.Run("invalid json rejected locally", func(t *testing.T) {
		stub := covStub(t)
		covWithStdin(t, "{not json")
		err := routineImportCmd.RunE(routineImportCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "bundle is not valid JSON") {
			t.Fatalf("expected invalid-json error, got %v", err)
		}
		if len(stub.Calls()) != 0 {
			t.Error("invalid bundle must not reach the server")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		covStub(t)
		err := routineImportCmd.RunE(routineImportCmd, []string{filepath.Join(t.TempDir(), "ghost.json")})
		if err == nil || !strings.Contains(err.Error(), "read bundle file") {
			t.Fatalf("expected read error, got %v", err)
		}
	})

	t.Run("response without slug prints raw body", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost(importPath, clitest.JSONResponse(200, map[string]int{"imported_count": 2}))
		covWithStdin(t, bundle)
		out := covCaptureStdoutCli3(t, func() {
			if err := routineImportCmd.RunE(routineImportCmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "imported_count") {
			t.Errorf("raw body missing: %q", out)
		}
	})

	t.Run("server error", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost(importPath, clitest.ErrorResponse(422, "bundle schema invalid"))
		covWithStdin(t, bundle)
		err := routineImportCmd.RunE(routineImportCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "bundle schema invalid") {
			t.Fatalf("expected 422, got %v", err)
		}
	})
}

// ─── cancel ─────────────────────────────────────────────────────────────

func TestRoutineCancelCmd(t *testing.T) {
	cancelPath := "/api/v1/workspaces/" + covWSCli3 + "/pipelines/runs/run-1/cancel"

	t.Run("happy", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost(cancelPath, clitest.JSONResponse(200, map[string]string{"status": "cancelling"}))
		out := covCaptureStdoutCli3(t, func() {
			if err := routineCancelCmd.RunE(routineCancelCmd, []string{"run-1"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "Cancellation signaled for run run-1.") {
			t.Errorf("cancel message wrong: %q", out)
		}
		if len(stub.CallsFor("POST", cancelPath)) != 1 {
			t.Error("expected 1 cancel POST")
		}
	})

	t.Run("already finished returns 409", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost(cancelPath, clitest.ErrorResponse(409, "run already completed"))
		err := routineCancelCmd.RunE(routineCancelCmd, []string{"run-1"})
		if err == nil || !strings.Contains(err.Error(), "run already completed") {
			t.Fatalf("expected 409, got %v", err)
		}
	})
}
