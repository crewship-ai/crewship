//go:build unix

package orchestrator

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider/bbolt"
	"github.com/crewship-ai/crewship/internal/runoutput"
)

func retainedAnalysisFixture(t *testing.T, adapter, stdout, stderr string, code int, reasons ...string) (*Orchestrator, RunState) {
	t.Helper()
	requireProtectedHostTestUID(t)
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
	dir := filepath.Join(root, "retained-analysis")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Hour)
	identity, _ := json.Marshal(runoutput.HostIdentity{Version: 1, ContainerID: "container", ExecID: "exec", Deadline: deadline})
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), identity, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := runoutput.Create(filepath.Join(dir, "output"), runoutput.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// Deliberately split every source chunk across JSON framing boundaries.
	for _, stream := range []struct{ name, data string }{{"stdout", stdout}, {"stderr", stderr}} {
		half := len(stream.data) / 2
		for _, chunk := range []string{stream.data[:half], stream.data[half:]} {
			if chunk != "" {
				if _, err := store.Write(stream.name, []byte(chunk)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	reason := "exited"
	if len(reasons) != 0 {
		reason = reasons[0]
	}
	if err := store.Finish(code, reason); err != nil {
		t.Fatal(err)
	}
	state, err := bbolt.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	run := RunState{ID: "retained-analysis", Status: "running", ContainerID: "container", ExecID: "exec", Output: &RunOutputState{Version: 2, Directory: filepath.Join(dir, "output"), Deadline: deadline, Adapter: adapter}}
	raw, _ := json.Marshal(run)
	if err := state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
		t.Fatal(err)
	}
	o := &Orchestrator{state: state}
	o.SetHostOutputRoot(root)
	return o, run
}

func TestRetainedAnalysisUsesSemanticFailureInsteadOfZeroExit(t *testing.T) {
	for _, tc := range []struct{ name, adapter, line string }{
		{"claude", "CLAUDE_CODE", `{"type":"result","subtype":"success","is_error":true,"result":"failed","usage":{"input_tokens":31,"output_tokens":7}}`},
		{"codex", "CODEX_CLI", `{"type":"turn.failed","error":{"message":"usage limit reached"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, run := retainedAnalysisFixture(t, tc.adapter, tc.line, "", 0)
			analysis, err := o.AnalyzeRetainedRun(t.Context(), run)
			if err != nil || !analysis.Failed || analysis.Unknown || !analysis.TerminalObserved {
				t.Fatalf("analysis=%+v %v", analysis, err)
			}
			snapshot, err := o.ReadRetainedRunResult(t.Context(), run)
			if err != nil {
				t.Fatal(err)
			}
			if err = o.AcknowledgeRetainedRunResult(t.Context(), run, snapshot); err != nil {
				t.Fatal(err)
			}
			raw, err := o.state.Get(t.Context(), "agent_runs", run.ID)
			if err != nil {
				t.Fatal(err)
			}
			var got RunState
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.Status != "error" {
				t.Fatalf("semantic failure acknowledged as %s", got.Status)
			}
		})
	}
}

func TestRetainedAnalysisIgnoresStderrAndRequiresStructuredTerminal(t *testing.T) {
	o, run := retainedAnalysisFixture(t, "CLAUDE_CODE", `{"type":"system","subtype":"init","session_id":"session-one","model":"model-one"}`+"\n", `{"type":"result","subtype":"success","is_error":false}`, 0)
	analysis, err := o.AnalyzeRetainedRun(t.Context(), run)
	if err != nil || !analysis.Unknown || analysis.TerminalObserved || analysis.Metadata["session_id"] != "session-one" {
		t.Fatalf("analysis=%+v %v", analysis, err)
	}
	snapshot, err := o.ReadRetainedRunResult(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.AcknowledgeRetainedRunResult(t.Context(), run, snapshot); !errors.Is(err, ErrHostCaptureUnconfirmed) {
		t.Fatalf("unknown success accepted: %v", err)
	}
}

func TestRetainedAcknowledgementRefusesActiveOwnerAndForeignRoot(t *testing.T) {
	o, run := retainedAnalysisFixture(t, "CLAUDE_CODE", `{"type":"result","subtype":"success","is_error":false}`, "", 0)
	snapshot, err := o.ReadRetainedRunResult(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	o.agentRuns.Store(run.ID, struct{}{})
	if err := o.AcknowledgeRetainedRunResult(t.Context(), run, snapshot); !errors.Is(err, ErrHostCaptureUnconfirmed) {
		t.Fatalf("active outcome accepted: %v", err)
	}
	o.SetHostOutputRoot(t.TempDir())
	if _, err := o.AnalyzeRetainedRun(t.Context(), run); err == nil {
		t.Fatal("foreign root accepted")
	}
}

func TestRetainedAcknowledgementRespectsAuthoritativeNonSuccess(t *testing.T) {
	for _, tc := range []struct{ status, want string }{{"CANCELLED", "cancelled"}, {"FAILED", "error"}, {"TIMEOUT", "error"}} {
		t.Run(tc.status, func(t *testing.T) {
			o, run := retainedAnalysisFixture(t, "CLAUDE_CODE", "unstructured output", "", 0)
			snapshot, err := o.ReadRetainedRunResult(t.Context(), run)
			if err != nil {
				t.Fatal(err)
			}
			if err := o.AcknowledgeRetainedRunResult(t.Context(), run, snapshot, tc.status); err != nil {
				t.Fatal(err)
			}
			raw, err := o.state.Get(t.Context(), "agent_runs", run.ID)
			if err != nil {
				t.Fatal(err)
			}
			var current RunState
			if err := json.Unmarshal(raw, &current); err != nil {
				t.Fatal(err)
			}
			if current.Status != tc.want {
				t.Fatalf("authoritative %s became %s", tc.status, current.Status)
			}
		})
	}
}

func TestRetainedAuthoritativeStatusMirrorsOverLocalTerminalObservation(t *testing.T) {
	for _, supplied := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "authoritative"}[supplied], func(t *testing.T) {
			o, run := retainedAnalysisFixture(t, "CLAUDE_CODE", `{"type":"result","subtype":"success","is_error":false}`, "", 0)
			current := run
			current.Status = "completed"
			raw, _ := json.Marshal(current)
			if err := o.state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
				t.Fatal(err)
			}
			snapshot, err := o.ReadRetainedRunResult(t.Context(), run)
			if err != nil {
				t.Fatal(err)
			}
			statuses := []string{}
			want := "completed"
			if supplied {
				statuses = append(statuses, "CANCELLED")
				want = "cancelled"
			}
			if err := o.AcknowledgeRetainedRunResult(t.Context(), run, snapshot, statuses...); err != nil {
				t.Fatal(err)
			}
			raw, err = o.state.Get(t.Context(), "agent_runs", run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &current); err != nil {
				t.Fatal(err)
			}
			if current.Status != want {
				t.Fatalf("status=%s want=%s", current.Status, want)
			}
		})
	}
}

func requireProtectedHostTestUID(t *testing.T) {
	t.Helper()
	if uid := os.Geteuid(); os.Getenv("CREWSHIP_HOST_CAPTURE_REQUIRE_UNPRIVILEGED") == "1" && (uid == 0 || uid == 1001 || uid == 1002) {
		t.Fatal("required capture qualification needs distinct unprivileged host UID")
	}
	if uid := os.Geteuid(); uid == 1001 || uid == 1002 {
		// SKIP-WAIVER(#3055): workload identities cannot own protected spools;
		// every guarded fixture runs in the required distinct-UID lane.
		t.Skip("protected capture requires host UID distinct from workload/sidecar; required qualification uses distinct unprivileged UID")
	}
}
