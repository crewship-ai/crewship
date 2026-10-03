package orchestrator

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

type replayContextFixture struct {
	values         []string
	workspace, run string
	err            error
}

func (s *replayContextFixture) Save(_ context.Context, workspace, run string, values []string) error {
	s.workspace, s.run, s.values = workspace, run, slices.Clone(values)
	return s.err
}
func (s *replayContextFixture) Load(context.Context, string, string) ([]string, error) {
	return nil, s.err
}

func TestDurableReplayContextCommittedBeforeExecGate(t *testing.T) {
	for _, scenario := range []string{"stored", "failed", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			o := New(covNewRunContainer(covRunOpts{}), newMemState(), covQuietLogger())
			store := &replayContextFixture{}
			secret := "synthetic-historical-model-token"
			if scenario == "failed" {
				store.err = errors.New(secret)
			}
			if scenario != "missing" {
				o.SetReplayContextStore(store)
			}
			req := covRunReq()
			req.DurableOutputDir = "/persistent/runs"
			req.LocalModelAPIKey = secret
			reached := false
			stop := errors.New("test stopped at creation boundary")
			req.ExecGate = func(context.Context) error {
				reached = true
				if store.workspace != req.WorkspaceID || store.run != req.RunID || !slices.Contains(store.values, secret) {
					t.Error("gate reached without original scoped scrub literals")
				}
				return stop
			}
			err := o.RunAgent(t.Context(), req, nil)
			if scenario == "stored" {
				if !reached {
					t.Fatalf("gate not reached after persistence: %v", err)
				}
			} else if reached || !errors.Is(err, ErrReplayContextUnavailable) {
				t.Fatalf("unrecoverable run admitted: gate=%v err=%v", reached, err)
			}
			if err != nil && strings.Contains(err.Error(), secret) {
				t.Fatal("store failure leaked scrub literal")
			}
		})
	}
}
