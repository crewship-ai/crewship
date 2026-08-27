package main

// A tabwriter buffers: nothing a list command prints reaches stdout until
// Flush, so Flush is where the whole table succeeds or fails. `return
// w.Flush()` used to carry that failure — a redirect to a full disk (ENOSPC),
// a closed pipe (EPIPE) — out as the command's exit status.
//
// Moving the human rendering inside AutoHuman's `func()` renderer took the
// return path away, and six commands became `_ = w.Flush()`: the table is
// dropped, the error is discarded, and the command exits 0 claiming it
// printed. A caller that pipes `routine active` into a file and checks `$?`
// gets a green light over an empty file.
//
// These tests drive each command with an unwritable stdout and require a
// non-nil error back. The routine commands build their tabwriter over
// os.Stdout directly, so stdout is swapped for a read-only file descriptor —
// every write to it fails with EBADF, and unlike a closed pipe it cannot
// deliver SIGPIPE to the test binary.

import (
	"errors"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// withUnwritableStdout points os.Stdout at a read-only descriptor for the
// duration of the test. Writes to it fail; the buffered tabwriter only
// attempts one at Flush.
func withUnwritableStdout(t *testing.T) {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	orig := os.Stdout
	os.Stdout = f
	t.Cleanup(func() {
		os.Stdout = orig
		_ = f.Close()
	})
}

// Sanity: the mechanism the tests below rely on actually reports an error.
// Without this, every case would pass just as well against a stdout that
// works, and the suite would prove nothing.
func TestUnwritableStdoutReallyFails(t *testing.T) {
	withUnwritableStdout(t)
	if _, err := os.Stdout.Write([]byte("x")); err == nil {
		t.Fatal("write to the read-only stdout succeeded — the harness is not exercising a write failure")
	}
}

func TestTabwriterFlushErrorsReachTheExitStatus(t *testing.T) {
	wsPath := "/api/v1/workspaces/" + covWorkspaceID

	cases := []struct {
		name    string
		command string
		arrange func(stub *clitest.StubServer)
		run     func() error
	}{
		{
			name:    "routine versions",
			command: "crewship routine versions <slug>",
			arrange: func(stub *clitest.StubServer) {
				stub.OnGet(wsPath+"/pipelines/email-fetch/versions", clitest.JSONResponse(200, []map[string]any{
					{"version": 2, "is_head": true, "definition_hash": "abc123", "author_type": "agent",
						"author_id": "ag_1", "created_at": "2026-01-01T00:00:00Z", "change_summary": "tweak"},
				}))
			},
			run: func() error { return routineVersionsCmd.RunE(routineVersionsCmd, []string{"email-fetch"}) },
		},
		{
			name:    "routine active",
			command: "crewship routine active",
			arrange: func(stub *clitest.StubServer) {
				stub.OnGet(wsPath+"/pipelines/runs/active", clitest.JSONResponse(200, []map[string]any{
					{"run_id": "run_1", "pipeline_slug": "email-fetch", "started_at": "2026-01-01T00:00:00Z"},
				}))
			},
			run: func() error { return routineActiveCmd.RunE(routineActiveCmd, nil) },
		},
		{
			name:    "routine tree",
			command: "crewship routine tree <run-id>",
			arrange: func(stub *clitest.StubServer) {
				stub.OnGet(wsPath+"/pipeline-runs/run_1/tree", clitest.JSONResponse(200, map[string]any{
					"nodes": []map[string]any{
						{"id": "run_1", "parent_id": "", "pipeline_slug": "email-fetch", "status": "RUNNING"},
					},
				}))
			},
			run: func() error { return routineTreeCmd.RunE(routineTreeCmd, []string{"run_1"}) },
		},
		{
			name:    "routine pending list",
			command: "crewship routine pending list",
			arrange: func(stub *clitest.StubServer) {
				stub.OnGet(wsPath+"/pipelines/pending", clitest.JSONResponse(200, []map[string]any{
					{"id": "pend_1", "pipeline_slug": "email-fetch", "priority": 5,
						"debounce_key": "k", "fire_at": "2026-01-01T00:00:00Z"},
				}))
			},
			run: func() error { return routinePendingListCmd.RunE(routinePendingListCmd, nil) },
		},
		{
			name:    "routine step-override list",
			command: "crewship routine step-override list <slug>",
			arrange: func(stub *clitest.StubServer) {
				stub.OnGet(wsPath+"/pipelines/email-fetch/overrides", clitest.JSONResponse(200, map[string]any{
					"overrides": []map[string]any{
						{"step_id": "s1", "model_override": "haiku", "prompt": "do the thing"},
					},
				}))
			},
			run: func() error {
				return routineStepOverrideListCmd.RunE(routineStepOverrideListCmd, []string{"email-fetch"})
			},
		},
		{
			name:    "routine errors",
			command: "crewship routine errors",
			arrange: func(stub *clitest.StubServer) {
				stub.OnGet(wsPath+"/pipelines/runs/errors", clitest.JSONResponse(200, map[string]any{
					"groups": []map[string]any{
						{"fingerprint": "fp1", "count": 3, "pipeline_slug": "email-fetch",
							"failed_at_step": "s1", "sample_error": "boom"},
					},
				}))
			},
			run: func() error { return routineErrorsCmd.RunE(routineErrorsCmd, nil) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := clitest.NewStubServer()
			defer stub.Close()
			setupStubCLICov(t, stub)
			setFormatCov(t, "")
			tc.arrange(stub)
			withUnwritableStdout(t)

			if err := tc.run(); err == nil {
				t.Errorf("%s exited 0 with an unwritable stdout — the tabwriter Flush error "+
					"is being discarded, so a truncated or absent table reports success", tc.command)
			}
		})
	}
}

// The admin forensic read builds its tabwriter over cmd.OutOrStdout(), so its
// failure is injected through the command's own writer rather than the process
// stdout. Same defect, same fix, different seam.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("no space left on device") }

func TestAdminSessionsListFlushErrorReachesTheExitStatus(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)
	execAdminSQL(t, dbURL,
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES ('u1', 'a@b.c', 'A', 'x')`,
	)
	execAdminSQL(t, dbURL,
		`INSERT INTO user_sessions (id, user_id, created_at, expires_at, last_used_at)
		 VALUES ('s1', 'u1', '2026-01-01T00:00:00Z', '2099-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)

	cmd, _ := newAdminCovCmd(runAdminSessionsList)
	cmd.SetOut(failingWriter{})
	cmd.SetArgs([]string{"--email=a@b.c"})
	if err := cmd.Execute(); err == nil {
		t.Error("admin sessions list exited 0 with a failing output writer — the tabwriter " +
			"Flush error is being discarded")
	}
}
