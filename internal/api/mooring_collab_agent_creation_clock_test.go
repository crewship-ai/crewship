package api

import (
	"context"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/provider"
	"testing"
)

// Actual FULL SQLite and original captured human fixture; provider, private
// environment and executor remain synthetic and no process is started here.
func TestP3AgentRequestedTransitionRechecksImmutableWorkAndAttempt(t *testing.T) {
	for _, mode := range []string{"input", "scope", "attempt-lease"} {
		t.Run(mode, func(t *testing.T) {
			x, d, h, runner, _, a := collaborationRuntimeFixture(t)
			published := false
			rt, err := newCollaborationAgentTaskRuntime(d, h, collaborationRuntimeEnvironment{}, func(context.Context, collaborationAgentConfirmed) error { published = true; return nil })
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.Authorize(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			mutation := `UPDATE work_items SET input_json='{}',input_sha256='0000000000000000000000000000000000000000000000000000000000000000' WHERE id=NEW.work_id;`
			if mode == "scope" {
				mutation = `UPDATE work_items SET authorized_scope='pages:read' WHERE id=NEW.work_id;`
			}
			if mode == "attempt-lease" {
				mutation = `UPDATE work_attempts SET lease_expires_at='2000-01-01T00:00:00Z' WHERE run_id=NEW.run_id;`
			}
			mustExec(t, x.f.rig.db, `CREATE TRIGGER p3_creation_work_drift AFTER UPDATE OF runtime_phase ON work_attempts WHEN NEW.runtime_phase='requested' AND OLD.runtime_phase='starting' BEGIN `+mutation+` END`)
			d.mu.Lock()
			entry := *d.entries[a.Item.ID]
			d.mu.Unlock()
			if err = rt.requestOriginalCreation(t.Context(), a, entry); err == nil {
				t.Fatal("changed original work/attempt committed requested phase")
			}
			var phase string
			if err = x.f.rig.db.QueryRow(`SELECT runtime_phase FROM work_attempts WHERE run_id=?`, a.RunID).Scan(&phase); err != nil || phase != "starting" {
				t.Fatal("failed requested phase survived", phase, err)
			}
			var principals int
			if err = x.f.rig.db.QueryRow(`SELECT count(*) FROM artifact_run_principals WHERE run_id=?`, a.RunID).Scan(&principals); err != nil || principals != 0 {
				t.Fatal("principal survived rollback", principals, err)
			}
			if published || runner.request != nil {
				t.Fatal("failed writer published or reached executor")
			}
		})
	}
}

type p3AgentClockEnvironment struct{ qualify func() }

func (p3AgentClockEnvironment) BeforeStart(context.Context) error { return nil }
func (e p3AgentClockEnvironment) Qualify(context.Context, chatbridge.ChatInfo, *provider.RuntimeUse) error {
	e.qualify()
	return nil
}

// Synthetic executor calls beforeStarted ONLY AFTER the real request ExecGate
// succeeds. This is a creation-admission probe, not actual provider evidence.
func TestP3AgentPendingClockInvalidationCannotPassExecCreationGate(t *testing.T) {
	for _, mode := range []string{"clock-invalid", "pending-expired", "boot-changed"} {
		t.Run(mode, func(t *testing.T) {
			x, d, h, runner, _, a := collaborationRuntimeFixture(t)
			creationAdmitted, published, started := false, false, false
			runner.beforeStarted = func() { creationAdmitted = true }
			environment := p3AgentClockEnvironment{qualify: func() {
				x.clock.mu.Lock()
				defer x.clock.mu.Unlock()
				switch mode {
				case "clock-invalid":
					x.clock.boot = ""
				case "pending-expired":
					x.clock.tick += x.store.ttl
				case "boot-changed":
					x.clock.boot = "replacement-boot"
				}
			}}
			rt, err := newCollaborationAgentTaskRuntime(d, h, environment, func(context.Context, collaborationAgentConfirmed) error { published = true; return nil })
			if err != nil {
				t.Fatal(err)
			}
			_ = rt.Run(t.Context(), a, func() { started = true })
			if creationAdmitted {
				t.Error("unqualified BOOTTIME pending capability passed actual request ExecGate before synthetic provider start")
			}
			if started || published {
				t.Error("unqualified pending capability reached started/publication callback")
			}
			var phase string
			if err = x.f.rig.db.QueryRow(`SELECT runtime_phase FROM work_attempts WHERE run_id=?`, a.RunID).Scan(&phase); err != nil || phase != "starting" {
				t.Error("invalid pending creation phase persisted", phase, err)
			}
		})
	}
}
