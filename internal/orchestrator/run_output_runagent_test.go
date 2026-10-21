//go:build linux

package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
	"github.com/crewship-ai/crewship/internal/runoutput"
)

// The production observer launches os.Executable. This test-only private mode
// executes the real detached capture engine with a synthetic provider, so the
// RunAgent test exercises admission, handoff, stream parsing and all defers.
func init() {
	if len(os.Args) != 2 || os.Args[1] != "--internal-run-capture" {
		return
	}
	input, err := runoutput.ReadHostInput(os.Stdin)
	var fixture struct {
		TestFixture    bool   `json:"test_fixture"`
		Delay          bool   `json:"delay"`
		CompletionPath string `json:"completion_path"`
	}
	parseErr := json.Unmarshal(input.Authority, &fixture)
	if err != nil || parseErr != nil || !fixture.TestFixture {
		os.Exit(125)
	}
	_, err = runoutput.CaptureHost(context.Background(), input, runAgentHostExecutor{delay: fixture.Delay})
	if err != nil {
		os.Exit(125)
	}
	// Terminal output precedes capacity settlement; signal only after all capture writes finish.
	if fixture.CompletionPath != "" {
		if err := os.WriteFile(fixture.CompletionPath, []byte("complete\n"), 0600); err != nil {
			os.Exit(125)
		}
	}
	os.Exit(0)
}

type runAgentHostExecutor struct{ delay bool }

func runAgentHostOutputRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	// Temp roots may use system aliases such as Darwin's /var and /tmp.
	// Positive fixtures admit the real directory; rejection tests add their own alias.
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func (e runAgentHostExecutor) Start(context.Context, runoutput.HostCommand) (*runoutput.HostExecution, error) {

	stdout := `{"type":"result","subtype":"success","is_error":false,"result":"complete","usage":{"input_tokens":5,"output_tokens":2}}` + "\n"
	var reader io.ReadCloser = io.NopCloser(strings.NewReader(stdout))
	if e.delay {
		r, w := io.Pipe()
		reader = r
		go func() { time.Sleep(300 * time.Millisecond); _, _ = io.WriteString(w, stdout); _ = w.Close() }()
	}
	return &runoutput.HostExecution{ID: "host-heavy-exec", Stdout: reader, Stderr: io.NopCloser(strings.NewReader(`{"type":"result","is_error":true}` + "\n"))}, nil
}
func (runAgentHostExecutor) Inspect(context.Context, string) (bool, int, error) { return false, 0, nil }
func (runAgentHostExecutor) Stop(context.Context, string) error {
	return errors.New("fixture should complete naturally")
}

type runAgentHostProvider struct {
	*covContainer
	prepared       []provider.ExecConfig
	delay          bool
	completionPath string
}

func (*runAgentHostProvider) SupportsHostCapture() bool { return true }

func (*runAgentHostProvider) QualifyHostCapture(context.Context, string) (bool, error) {
	return true, nil
}
func (p *runAgentHostProvider) PrepareHostCapture(_ context.Context, cfg provider.ExecConfig, _ string, _ []string) (string, json.RawMessage, error) {
	p.prepared = append(p.prepared, cfg)
	fixture := map[string]any{"test_fixture": true, "delay": p.delay}
	if p.completionPath != "" {
		fixture["completion_path"] = p.completionPath
	}
	authority, _ := json.Marshal(fixture)
	return "unix:///synthetic-test-only", authority, nil
}
func (*runAgentHostProvider) RestoreHostCapture(runoutput.HostInput, string) (runoutput.HostExecutor, io.Closer, error) {
	return nil, nil, errors.New("completed fixture must not restore")
}

func TestHostRunAgentCompletesThroughDetachedHelperAndPreservesLiteralLaunch(t *testing.T) {
	requireProtectedHostTestUID(t)
	state, err := bbolt.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	completionPath := filepath.Join(t.TempDir(), "capture-complete")
	p := &runAgentHostProvider{covContainer: covNewRunContainer(covRunOpts{}), completionPath: completionPath}
	o := New(p, state, covQuietLogger())
	o.ipcToken = "fixture-key"
	o.SetHostOutputRoot(runAgentHostOutputRoot(t))
	req := covRunReq()
	req.UserMessage = "literal $(never); ' quoted"
	req.RunID = "host-runagent-fixture"
	starts := 0
	req.OnExecStarted = func(id string) {
		starts++
		if id != "host-heavy-exec" {
			t.Errorf("wrong execID %q", id)
		}
	}
	if err := o.RunAgent(t.Context(), req, nil); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || len(p.prepared) != 1 {
		t.Fatalf("starts=%d captures=%d", starts, len(p.prepared))
	}
	cfg := p.prepared[0]
	if cfg.User != "1001:1001" || cfg.WorkingDir == "" || len(cfg.Cmd) == 0 || cfg.Cmd[0] != "claude" {
		t.Fatalf("launch changed: %+v", cfg)
	}
	foundPrompt, foundHome := false, false
	for _, arg := range cfg.Cmd {
		if arg == req.UserMessage {
			foundPrompt = true
		}
	}
	for _, env := range cfg.Env {
		if strings.HasPrefix(env, "HOME=") && strings.Contains(env, req.RunID) {
			foundHome = true
		}
	}
	if !foundPrompt || !foundHome {
		t.Fatalf("literal prompt/HOME lost: prompt=%v home=%v", foundPrompt, foundHome)
	}
	raw, err := state.Get(t.Context(), "agent_runs", req.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var run RunState
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" || run.ExecID != "host-heavy-exec" || run.Output == nil || run.Output.Version != 2 {
		t.Fatalf("terminal state: %+v", run)
	}
	if _, _, found := runHomeLocation(req.RunID); found {
		t.Fatal("natural completion leaked HOME registry")
	}
	if _, held := o.detachedHoldRunID(req.AgentID); held {
		t.Fatal("natural completion leaked detached hold")
	}
	if err := o.RunAgent(t.Context(), req, nil); !errors.Is(err, ErrRunAlreadyOwned) {
		t.Fatalf("duplicate identity relaunched: %v", err)
	}
	awaitRunAgentHostFixtureCompletion(t, completionPath, time.Now().Add(3*time.Second))
}

func TestHostRunAgentObserverCancellationRetainsHomeThenRecoversResult(t *testing.T) {
	requireProtectedHostTestUID(t)
	state, err := bbolt.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	outputRoot := runAgentHostOutputRoot(t)
	// Keep completion outside the spool whose TempDir teardown must await the helper.
	completionPath := filepath.Join(t.TempDir(), "capture-complete")
	p := &runAgentHostProvider{covContainer: covNewRunContainer(covRunOpts{}), delay: true, completionPath: completionPath}
	o := New(p, state, covQuietLogger())
	o.ipcToken = "fixture-key"
	o.SetHostOutputRoot(outputRoot)
	o.detachedPollInterval = time.Millisecond
	req := covRunReq()
	req.RunID = "host-runagent-detach"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req.OnExecStarted = func(string) { cancel() }
	if err := o.RunAgent(ctx, req, nil); !errors.Is(err, ErrHostCaptureUnconfirmed) {
		t.Fatalf("observer cancellation=%v", err)
	}
	if _, _, found := runHomeLocation(req.RunID); !found {
		t.Fatal("observer cancellation removed live HOME")
	}
	if _, held := o.detachedHoldRunID(req.AgentID); !held {
		t.Fatal("observer cancellation released live occupancy")
	}
	var run RunState
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := state.Get(t.Context(), "agent_runs", req.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &run); err != nil {
			t.Fatal(err)
		}
		snapshot, err := o.ReadRetainedRunResult(t.Context(), run)
		if err == nil && snapshot.Complete {
			if snapshot.Result.ExitCode != 0 || snapshot.Result.Reason != "exited" {
				t.Fatalf("detached result=%+v", snapshot)
			}
			if err := o.AcknowledgeRetainedRunResult(t.Context(), run, snapshot); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("detached helper did not finish: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	for {
		if _, held := o.detachedHoldRunID(req.AgentID); !held {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("natural capture did not release hold")
		}
		time.Sleep(time.Millisecond)
	}
	if _, _, found := runHomeLocation(req.RunID); found {
		t.Fatal("terminal host hold leaked HOME")
	}
	raw, err := state.Get(t.Context(), "agent_runs", req.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" {
		t.Fatalf("recovered state=%s", run.Status)
	}
	// Keep the existing recovery deadline, including this final fixture join.
	awaitRunAgentHostFixtureCompletion(t, completionPath, deadline)
}

// Terminal output and released holds precede the capture helper's final writes.
// This fixture marker is emitted only after CaptureHost returns.
func awaitRunAgentHostFixtureCompletion(t *testing.T, completionPath string, deadline time.Time) {
	t.Helper()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	limit := time.NewTimer(time.Until(deadline))
	defer limit.Stop()
	for {
		data, err := os.ReadFile(completionPath)
		if err == nil && string(data) == "complete\n" {
			break
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-tick.C:
		case <-limit.C:
			t.Fatal("capture helper did not finish filesystem writes")
		}
	}
}
