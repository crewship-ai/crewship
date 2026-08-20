package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// `crewship crew start` is the command the crew-file 409 and the
// provision output both point at. It drives
// POST /api/v1/crews/{id}/container-start — API↔CLI parity, because the
// CLI is the contract agents use.
//
// Before it existed the only way to start a crew was to run an agent at
// it, which spends tokens to achieve a side effect, and the 409 that
// said "start the crew and retry" named nothing that would.

func TestCrewStartRunE_StartsAndReportsRunning(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": covCrewIDCli4, "slug": "uctarna"},
	}))
	stub.OnPost("/api/v1/crews/"+covCrewIDCli4+"/container-start", clitest.JSONResponse(200, map[string]any{
		"crew_id": covCrewIDCli4, "slug": "uctarna",
		"container_id": "abc123def456", "status": "running",
	}))

	c := covFreshCmd(crewStartCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"uctarna"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "running") {
		t.Errorf("does not report the crew running: %q", out)
	}
	if got := len(stub.CallsFor("POST", "/api/v1/crews/"+covCrewIDCli4+"/container-start")); got != 1 {
		t.Errorf("container-start calls = %d, want 1", got)
	}
}

// The "Starting crew…" progress line has to stay off stdout: a cold
// crew blocks on an image build so it must be printed before the call,
// and on stdout it lands above the JSON body and breaks
// `crewship crew start x -f json | jq` — the form an agent uses.
func TestCrewStartRunE_ProgressLineStaysOffStdoutInJSON(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnPost("/api/v1/crews/"+covCrewIDCli4+"/container-start", clitest.JSONResponse(200, map[string]any{
		"crew_id": covCrewIDCli4, "container_id": "abc", "status": "running",
	}))

	origFormat := flagFormat
	flagFormat = "json"
	t.Cleanup(func() { flagFormat = origFormat })

	c := covFreshCmd(crewStartCmd, nil)
	stdout, stderr, err := captureStreamsSeparately(t, func() error {
		return c.RunE(c, []string{covCrewIDCli4})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(stdout, "Starting crew") {
		t.Errorf("progress line landed on stdout, corrupting the JSON body:\n%s", stdout)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &body); err != nil {
		t.Fatalf("stdout is not parseable JSON (%v):\n%s", err, stdout)
	}
	if body["status"] != "running" {
		t.Errorf("status = %v, want running", body["status"])
	}
	// Still shown to a human — moved, not dropped.
	if !strings.Contains(stderr, "Starting crew") {
		t.Errorf("progress line vanished entirely; it belongs on stderr:\n%s", stderr)
	}
}

// captureStreamsSeparately splits stdout from stderr, which the shared
// covCaptureStdoutCli4 helper deliberately merges. Only a test about
// WHICH stream a line went to needs them apart.
func captureStreamsSeparately(t *testing.T, fn func() error) (string, string, error) {
	t.Helper()
	guardCLIState(t)
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW

	read := func(r *os.File) <-chan string {
		ch := make(chan string, 1)
		go func() {
			defer r.Close()
			b, _ := io.ReadAll(r)
			ch <- string(b)
		}()
		return ch
	}
	outCh, errCh := read(outR), read(errR)

	runErr := fn()
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	return <-outCh, <-errCh, runErr
}

// Degradation the start survived is the operator's business, not a log
// line: "up, but without its postgres" changes what they do next.
func TestCrewStartRunE_PrintsNotices(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnPost("/api/v1/crews/"+covCrewIDCli4+"/container-start", clitest.JSONResponse(200, map[string]any{
		"crew_id": covCrewIDCli4, "container_id": "abc", "status": "running",
		"notices": []string{"sidecar services are not supported by this container provider"},
	}))

	c := covFreshCmd(crewStartCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "sidecar services are not supported") {
		t.Errorf("notice was swallowed: %q", out)
	}
}

// A server with no container runtime answers 503. That has to be an
// error the caller can branch on, not a success with a sad message —
// a deploy script that reads exit 0 here writes files into a crew that
// is not running and gets the 409 it was trying to avoid.
func TestCrewStartRunE_NoRuntimeIsAnError(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnPost("/api/v1/crews/"+covCrewIDCli4+"/container-start",
		clitest.ErrorResponse(http.StatusServiceUnavailable,
			"no container runtime is configured on this server, so crews cannot be started here"))

	c := covFreshCmd(crewStartCmd, nil)
	_, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err == nil {
		t.Fatal("503 reported as success")
	}
	if !strings.Contains(err.Error(), "container runtime") {
		t.Errorf("error does not carry the server's reason: %v", err)
	}
}

// The command advertises "builds its image first if needed", and the
// server provisions a cold crew with a 15-minute default before starting
// it. The CLI's own default request cap is 30s, and Go's client CANCELS
// the request when it fires — which cancels the handler's context and
// tears down the very build the operator is waiting for. Left at the
// default, `crew start` would fail with `context deadline exceeded` and
// leave no container, precisely on the never-provisioned crew the restore
// remediation now sends people here to fix.
//
// Pinned as an inequality against the server's ceiling rather than an
// equality, so raising either side stays legal and lowering ours below
// theirs does not.
func TestCrewStartTimeout_ClearsTheServersProvisionCeiling(t *testing.T) {
	const serverEnsureProvisionedDefault = 15 * time.Minute
	if crewStartTimeout <= serverEnsureProvisionedDefault {
		t.Errorf("crewStartTimeout = %s, must exceed the server's %s EnsureProvisioned default — "+
			"otherwise the client cancels the build it is waiting for",
			crewStartTimeout, serverEnsureProvisionedDefault)
	}
	if crewStartCmd.Flags().Lookup("timeout") == nil {
		t.Error("no --timeout escape hatch for a slow daemon")
	}
}

func TestCrewStartCmd_IsRegisteredUnderCrew(t *testing.T) {
	var found bool
	for _, sub := range crewCmd.Commands() {
		if sub.Name() == "start" {
			found = true
		}
	}
	if !found {
		t.Error("`crewship crew start` is not registered — the 409 and the provision hint both name it")
	}
}
