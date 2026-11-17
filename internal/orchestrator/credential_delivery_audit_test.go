package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

type nativeDeliveryAudit struct {
	calls [][3]string
	err   error
}

func (*nativeDeliveryAudit) RecordAudit(context.Context, string, string, string, string, string, map[string]any) {
}
func (a *nativeDeliveryAudit) RecordNativeCredentialDelivery(_ context.Context, credential, agent, run string) error {
	a.calls = append(a.calls, [3]string{credential, agent, run})
	return a.err
}

func TestNativeDeliveryAuditAfterVerifiedPreflight(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "writer_failure"}[fail], func(t *testing.T) {
			o, container, req := preflightFixture(t)
			req.RunID = "fixture-run"
			req.Credentials = []Credential{{ID: "fixture-credential", Type: "SSH_KEY", EnvVarName: "NATIVE", PlainValue: "synthetic"}}
			audit := &nativeDeliveryAudit{}
			o.SetAuditLog(audit)
			if fail {
				container.stdout = func(_ provider.ExecConfig, stdin string) string {
					if stdin != "" {
						return preflightFailMarker + preflightStepCredentials + "\n"
					}
					return ""
				}
			}
			_, _, err := o.preparePreflightDirs(context.Background(), req, BuildEnvVarsSidecar(req, true), true, true, req.RunID)
			if fail {
				if err == nil || len(audit.calls) != 0 {
					t.Fatal("failed write was recorded as delivered")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(audit.calls) != 1 || audit.calls[0] != [3]string{"fixture-credential", req.AgentID, req.RunID} {
				t.Fatalf("incorrect delivery attribution: %v", audit.calls)
			}
		})
	}
}

func TestNativeDeliveryAuditFailureStopsPreflight(t *testing.T) {
	o, _, req := preflightFixture(t)
	req.RunID = "fixture-run"
	req.Credentials = []Credential{{ID: "fixture-credential", Type: "CLI_TOKEN", EnvVarName: "NATIVE", PlainValue: "synthetic"}}
	o.SetAuditLog(&nativeDeliveryAudit{err: errors.New("fixture audit failure")})
	state, _ := json.Marshal(RunState{ID: req.RunID, Status: "running"})
	if err := o.state.Set(context.Background(), "agent_runs", req.RunID, state); err != nil {
		t.Fatal(err)
	}

	if _, _, err := o.preparePreflightDirs(context.Background(), req, BuildEnvVarsSidecar(req, true), true, true, req.RunID); err == nil {
		t.Fatal("missing audit was ignored")
	}
	state, err := o.state.Get(context.Background(), "agent_runs", req.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var run RunState
	if err := json.Unmarshal(state, &run); err != nil {
		t.Fatal(err)
	}
	if run.Status != "error" {
		t.Fatalf("failed credential preflight left run %s", run.Status)
	}
}
