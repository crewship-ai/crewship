package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

// ---------------------------------------------------------------------------
// runner_orchestrator.go — NewOrchestratorRunner constructor.
//
// Validates required dependencies, defaults the logger, and returns
// a configured runner. The validation is the only safety net between
// a half-wired call site (forgot a dep) and a nil-pointer panic deep
// inside RunStep. Pinning each required-dep branch keeps the error
// message stable so a wiring miss in cmd_start.go surfaces with a
// triage-friendly "OrchestratorRunner: <field> required" line in
// startup logs.
// ---------------------------------------------------------------------------

// minimalChatResolver satisfies chatbridge.ChatResolver with all
// methods as no-ops. Used so the constructor's nil check passes.
type minimalChatResolver struct{}

func (minimalChatResolver) CreateChat(_ context.Context, _ chatbridge.CreateChatRequest) error {
	return nil
}
func (minimalChatResolver) ResolveChat(_ context.Context, _ string) (*chatbridge.ChatInfo, error) {
	return nil, nil
}
func (minimalChatResolver) ResolveAgent(_ context.Context, _, _ string) (*chatbridge.ChatInfo, error) {
	return nil, nil
}
func (minimalChatResolver) GetWebhookSecret(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
func (minimalChatResolver) CreateRun(_ context.Context, _, _, _, _, _ string, _ map[string]interface{}) error {
	return nil
}
func (minimalChatResolver) UpdateRun(_ context.Context, _, _ string, _ *int, _ *string, _ map[string]interface{}) error {
	return nil
}
func (minimalChatResolver) IncrementMessageCount(_ context.Context, _ string, _ int) error {
	return nil
}
func (minimalChatResolver) UpdateChatTitle(_ context.Context, _, _ string) error { return nil }

// minimalContainer satisfies provider.ContainerProvider with no-ops.
// Only the type-check matters for the constructor — none of the methods
// are invoked.
type minimalContainer struct{}

func (minimalContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "", nil
}
func (minimalContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (minimalContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (minimalContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return nil, nil
}
func (minimalContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (minimalContainer) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	return nil, nil
}
func (minimalContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}
func (minimalContainer) CrewContainerName(_ string, slug string) string {
	return "crewship-team-" + slug
}
func (minimalContainer) CopyToContainer(_ context.Context, _, _ string, _ io.Reader) error {
	return nil
}

// fullDeps returns a OrchestratorRunnerDeps with every required field
// set so each "missing X" subtest can blank one field at a time.
func fullDeps() OrchestratorRunnerDeps {
	return OrchestratorRunnerDeps{
		DB:        &sql.DB{}, // zero-value is fine; constructor only nil-checks
		Orch:      &orchestrator.Orchestrator{},
		Container: minimalContainer{},
		Resolver:  minimalChatResolver{},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestNewOrchestratorRunner_AllDepsPresent_Succeeds(t *testing.T) {
	// Happy path: every required field present, optional fields
	// (LogWriter, ConvStore, Journal) left nil. The constructor must
	// return a non-nil runner with nil error.
	r, err := NewOrchestratorRunner(fullDeps())
	if err != nil {
		t.Fatalf("NewOrchestratorRunner: %v", err)
	}
	if r == nil {
		t.Fatal("NewOrchestratorRunner returned nil runner without error")
	}
}

func TestNewOrchestratorRunner_RejectsMissingRequiredDeps(t *testing.T) {
	// Each required-dep branch returns a distinct error message. Pin
	// each so a wiring miss in cmd_start.go produces a triage-friendly
	// line — "OrchestratorRunner: <field> required" — that an oncall
	// can grep against the constructor source.
	cases := []struct {
		name    string
		blank   func(*OrchestratorRunnerDeps)
		wantSub string
	}{
		{"missing-DB", func(d *OrchestratorRunnerDeps) { d.DB = nil }, "DB required"},
		{"missing-Orch", func(d *OrchestratorRunnerDeps) { d.Orch = nil }, "Orchestrator required"},
		{"missing-Container", func(d *OrchestratorRunnerDeps) { d.Container = nil }, "ContainerProvider required"},
		{"missing-Resolver", func(d *OrchestratorRunnerDeps) { d.Resolver = nil }, "ChatResolver required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := fullDeps()
			tc.blank(&deps)
			r, err := NewOrchestratorRunner(deps)
			if err == nil {
				t.Fatalf("expected error when %s is blank, got runner=%+v", tc.name, r)
			}
			if r != nil {
				t.Errorf("runner = %+v, want nil on error", r)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %v, missing substring %q (triage signal for wiring miss)", err, tc.wantSub)
			}
			if !strings.HasPrefix(err.Error(), "OrchestratorRunner:") {
				t.Errorf("err = %v, missing \"OrchestratorRunner:\" prefix (operator grep target)", err)
			}
		})
	}
}

func TestNewOrchestratorRunner_DBCheckFiresFirst(t *testing.T) {
	// Pin the validation ORDER. Source has DB → Orch → Container →
	// Resolver. If all four are nil, the FIRST error reported is DB.
	// A regression that swapped the order would change which wiring
	// bug the operator sees first — and silently change the contract
	// for code that asserts errors.Is/string-matches on the first.
	r, err := NewOrchestratorRunner(OrchestratorRunnerDeps{})
	if err == nil {
		t.Fatalf("all-nil deps returned nil error, runner=%+v", r)
	}
	if !strings.Contains(err.Error(), "DB required") {
		t.Errorf("first error = %v, want \"DB required\" (DB check must fire first)", err)
	}
}

func TestNewOrchestratorRunner_OptionalDepsMayBeNil(t *testing.T) {
	// LogWriter / ConvStore / Journal are explicitly documented as
	// optional. Pin so a future refactor that added them to the
	// required list has to update this test in step.
	deps := fullDeps()
	deps.LogWriter = nil
	deps.ConvStore = nil
	deps.Journal = nil
	r, err := NewOrchestratorRunner(deps)
	if err != nil {
		t.Errorf("optional deps nil should succeed: %v", err)
	}
	if r == nil {
		t.Fatal("runner = nil with optional deps blank")
	}
}

func TestNewOrchestratorRunner_NilLoggerDefaults(t *testing.T) {
	// Source: `if deps.Logger == nil { deps.Logger = slog.Default() }`.
	// Pin that the constructor accepts nil Logger and installs a
	// non-nil default — otherwise every method that does
	// r.logger.Warn(...) would panic on a nil receiver.
	deps := fullDeps()
	deps.Logger = nil
	r, err := NewOrchestratorRunner(deps)
	if err != nil {
		t.Fatalf("nil logger: %v", err)
	}
	if r == nil {
		t.Fatal("runner = nil")
	}
	if r.logger == nil {
		t.Error("r.logger is nil after constructor; expected slog.Default() fallback")
	}
}

func TestNewOrchestratorRunner_DepFieldsCopiedToRunner(t *testing.T) {
	// All required + optional deps must reach the returned runner —
	// a regression that dropped any one of them (e.g. forgot to copy
	// journalE) would silently disable that subsystem at runtime.
	deps := fullDeps()
	deps.Resolver = minimalChatResolver{}
	r, err := NewOrchestratorRunner(deps)
	if err != nil {
		t.Fatalf("NewOrchestratorRunner: %v", err)
	}
	if r.db != deps.DB {
		t.Error("db not copied to runner")
	}
	if r.orch != deps.Orch {
		t.Error("orch not copied to runner")
	}
	if r.container != deps.Container {
		t.Error("container not copied to runner")
	}
	if r.resolver != deps.Resolver {
		t.Error("resolver not copied to runner")
	}
	if r.logger != deps.Logger {
		t.Error("logger not copied to runner")
	}
}

// errSentinel is unused by the constructor but stays in the file so
// the errors import is warm for any follow-up RunStep test that
// wants to assert on wrapped error chains. Remove if unused after
// the package is fully covered.
var _ = errors.New
