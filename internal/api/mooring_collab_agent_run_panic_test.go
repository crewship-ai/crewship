package api

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/work"
)

type collaborationPanicEnvironment struct{ stage string }

func (e collaborationPanicEnvironment) BeforeStart(context.Context) error {
	if e.stage == "before" {
		panic("synthetic-private-before-start-diagnostic")
	}
	return nil
}
func (e collaborationPanicEnvironment) Qualify(context.Context, chatbridge.ChatInfo, *provider.RuntimeUse) error {
	if e.stage == "qualify" {
		panic("synthetic-private-qualification-diagnostic")
	}
	return nil
}

type collaborationStartPanicContainer struct{ collaborationRuntimeContainer }

func (*collaborationStartPanicContainer) EnsureCrewRuntime(context.Context, provider.CrewConfig) (string, error) {
	panic("synthetic-private-provider-start-diagnostic")
}

// Actual SQL/host launch code with explicit synthetic provider and callbacks.
// No private environment, actual Docker or agent completion is qualified here.
func TestCollaborationAgentRuntimePanicBeforeOrchestratorCannotPublish(t *testing.T) {
	for _, stage := range []string{"before", "qualify", "provider"} {
		t.Run(stage, func(t *testing.T) {
			x, d, h, runner, _, a := collaborationRuntimeFixture(t)
			if stage == "provider" {
				h.container = &collaborationStartPanicContainer{}
			}
			published := false
			rt, e := newCollaborationAgentTaskRuntime(d, h, collaborationPanicEnvironment{stage: stage}, func(context.Context, collaborationAgentConfirmed) error { published = true; return nil })
			if e != nil {
				t.Fatal(e)
			}
			e = rt.Run(t.Context(), a, func() { t.Fatal("pre-orchestrator panic confirmed attempt") })
			if !errors.Is(e, orchestrator.ErrPrivateRunUnqualified) || published || runner.request != nil {
				t.Fatal("pre-orchestrator panic escaped or published", e)
			}
			var phase string
			if e = x.f.rig.db.QueryRow(`SELECT runtime_phase FROM work_attempts WHERE run_id=?`, a.RunID).Scan(&phase); e != nil || phase != "starting" {
				t.Fatal("panic fabricated requested/confirmed phase", phase, e)
			}
			if stage != "before" {
				alive, e := rt.Alive(t.Context(), rt.Locator(a))
				if e != nil || alive {
					t.Fatal("creation gate was reached despite pre-orchestrator panic", alive, e)
				}
			}
		})
	}
}

func TestCollaborationAgentRuntimeConfirmedCallbackPanicKeepsOriginalExec(t *testing.T) {
	x, d, h, _, _, a := collaborationRuntimeFixture(t)
	h.container = &collaborationCaptureProbeContainer{}
	rt, e := newCollaborationAgentTaskRuntime(d, h, collaborationRuntimeEnvironment{}, func(context.Context, collaborationAgentConfirmed) error {
		panic("synthetic-private-confirmed-callback-diagnostic")
	})
	if e != nil {
		t.Fatal(e)
	}
	e = rt.Run(t.Context(), a, func() {
		if err := work.NewStore(x.f.rig.db).StartRunning(t.Context(), a.Item.ID, a.RunID, a.Generation, rt.Locator(a)); err != nil {
			t.Fatal(err)
		}
	})
	if !errors.Is(e, orchestrator.ErrPrivateRunUnqualified) {
		t.Fatal("confirmed callback diagnostic escaped", e)
	}
	alive, e := rt.Alive(t.Context(), rt.Locator(a))
	if e != nil || !alive {
		t.Fatal("confirmed callback panic lost original live exec", alive, e)
	}
	if rt.Classify(a, e) != dispatch.OutcomeUnclear {
		t.Fatal("live probe invented a completion")
	}
}
