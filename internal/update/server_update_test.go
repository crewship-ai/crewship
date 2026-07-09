package update

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// mockService records the ordered sequence of stop/start calls and can be
// programmed to fail on the Nth call of each, so the orchestration state
// machine can be driven without a live systemd.
type mockService struct {
	events        []string
	stopErr       []error
	startErr      []error
	stopN, startN int
}

func nthErr(errs []error, i int) error {
	if i < len(errs) {
		return errs[i]
	}
	return nil
}

func (m *mockService) Stop(_ context.Context) error {
	m.events = append(m.events, "stop")
	err := nthErr(m.stopErr, m.stopN)
	m.stopN++
	return err
}

func (m *mockService) Start(_ context.Context) error {
	m.events = append(m.events, "start")
	err := nthErr(m.startErr, m.startN)
	m.startN++
	return err
}

// baseDeps builds a ServerUpdateDeps whose swap succeeds, health passes, and
// rollback records that it ran. Individual tests override fields.
func baseDeps(m *mockService, swapped []string) (*ServerUpdateDeps, *bool) {
	rolledBack := false
	deps := &ServerUpdateDeps{
		Manager: m,
		Swap: func(_ context.Context) (*SelfUpdateResult, error) {
			return &SelfUpdateResult{FromVersion: "1.0.0", ToVersion: "1.1.0", Replaced: swapped, BackupPath: "/usr/local/bin/crewship.bak"}, nil
		},
		Health: func(_ context.Context) error { return nil },
		Rollback: func(paths []string) error {
			rolledBack = true
			return nil
		},
	}
	return deps, &rolledBack
}

func TestRunServerUpdate_HappyPath(t *testing.T) {
	m := &mockService{}
	deps, rolledBack := baseDeps(m, []string{"/usr/local/bin/crewship"})

	out, err := RunServerUpdate(context.Background(), *deps)
	if err != nil {
		t.Fatalf("happy path returned error: %v", err)
	}
	if !out.Healthy || out.RolledBack {
		t.Fatalf("expected Healthy=true RolledBack=false, got %+v", out)
	}
	if *rolledBack {
		t.Error("rollback ran on the happy path")
	}
	// stop the old server, then start the new one.
	if got := strings.Join(m.events, ","); got != "stop,start" {
		t.Errorf("call sequence = %q, want %q", got, "stop,start")
	}
}

func TestRunServerUpdate_StopFailsBeforeSwap(t *testing.T) {
	m := &mockService{stopErr: []error{errors.New("unit is masked")}}
	deps, _ := baseDeps(m, []string{"/usr/local/bin/crewship"})
	swapCalled := false
	inner := deps.Swap
	deps.Swap = func(ctx context.Context) (*SelfUpdateResult, error) {
		swapCalled = true
		return inner(ctx)
	}

	out, err := RunServerUpdate(context.Background(), *deps)
	if err == nil {
		t.Fatal("expected error when the service can't be stopped")
	}
	if swapCalled {
		t.Error("swap must not run if the service could not be stopped")
	}
	if out.RolledBack {
		t.Error("nothing was swapped; RolledBack should be false")
	}
}

func TestRunServerUpdate_SwapFailsRestartsOldBinary(t *testing.T) {
	m := &mockService{}
	deps, rolledBack := baseDeps(m, nil)
	deps.Swap = func(_ context.Context) (*SelfUpdateResult, error) {
		return nil, errors.New("checksum mismatch")
	}

	out, err := RunServerUpdate(context.Background(), *deps)
	if err == nil {
		t.Fatal("expected error when the swap fails")
	}
	if *rolledBack {
		t.Error("swap failed so nothing was swapped — .bak rollback must not run")
	}
	// The service was stopped for the swap; a failed swap leaves the OLD binary
	// on disk, so we must restart it rather than leave the server down.
	if got := strings.Join(m.events, ","); got != "stop,start" {
		t.Errorf("call sequence = %q, want stop,start (restart old binary)", got)
	}
	if out.RolledBack {
		t.Error("RolledBack should be false when the swap itself failed")
	}
}

func TestRunServerUpdate_NewBinaryFailsToStart_RollsBack(t *testing.T) {
	// Start #1 (new binary) fails; after rollback Start #2 (old binary) succeeds.
	m := &mockService{startErr: []error{errors.New("exec format error")}}
	swapped := []string{"/usr/local/bin/crewship", "/usr/local/bin/crewship-sidecar"}
	deps, _ := baseDeps(m, swapped)
	var rollbackPaths []string
	deps.Rollback = func(paths []string) error {
		rollbackPaths = paths
		return nil
	}

	out, err := RunServerUpdate(context.Background(), *deps)
	if err == nil {
		t.Fatal("expected error when the new binary won't start")
	}
	if !out.RolledBack {
		t.Error("RolledBack should be true after restoring the previous binary")
	}
	if len(rollbackPaths) != len(swapped) {
		t.Errorf("rollback got paths %v, want all swapped %v", rollbackPaths, swapped)
	}
	// stop(old) -> start(new,fails) -> start(old, after rollback)
	if got := strings.Join(m.events, ","); got != "stop,start,start" {
		t.Errorf("call sequence = %q, want stop,start,start", got)
	}
	if rollbackPaths == nil {
		t.Error("rollback function was not invoked")
	}
}

func TestRunServerUpdate_UnhealthyRollsBackWithSnapshotHint(t *testing.T) {
	m := &mockService{}
	deps, _ := baseDeps(m, []string{"/usr/local/bin/crewship"})
	deps.Health = func(_ context.Context) error { return errors.New("health probe timed out") }

	out, err := RunServerUpdate(context.Background(), *deps)
	if err == nil {
		t.Fatal("expected error when the new binary is unhealthy")
	}
	if !out.RolledBack {
		t.Error("an unhealthy new binary must be rolled back")
	}
	// The new binary may already have migrated the DB, so the error must point
	// the operator at restore-snapshot for the schema half of the rollback.
	if !strings.Contains(err.Error(), "restore-snapshot") {
		t.Errorf("unhealthy rollback error should mention restore-snapshot; got: %v", err)
	}
	// stop(old) -> start(new) -> stop(new, unhealthy) -> start(old)
	if got := strings.Join(m.events, ","); got != "stop,start,stop,start" {
		t.Errorf("call sequence = %q, want stop,start,stop,start", got)
	}
}

func TestRunServerUpdate_RollbackFailureIsCritical(t *testing.T) {
	m := &mockService{}
	deps, _ := baseDeps(m, []string{"/usr/local/bin/crewship"})
	deps.Health = func(_ context.Context) error { return errors.New("unhealthy") }
	deps.Rollback = func(_ []string) error { return errors.New("disk full") }

	_, err := RunServerUpdate(context.Background(), *deps)
	if err == nil {
		t.Fatal("expected error when rollback itself fails")
	}
	// A failed rollback is the worst case — the message must flag manual
	// recovery and name the backup path.
	if !strings.Contains(err.Error(), "manual") {
		t.Errorf("failed-rollback error should call for manual recovery; got: %v", err)
	}
}
