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

// healthSeq returns a HealthChecker that yields the given results in order,
// then nil once exhausted. Lets a test model "new binary unhealthy, rolled-back
// old binary healthy" as [err, nil].
func healthSeq(results ...error) (HealthChecker, *int) {
	calls := 0
	hc := func(_ context.Context) error {
		r := error(nil)
		if calls < len(results) {
			r = results[calls]
		}
		calls++
		return r
	}
	return hc, &calls
}

// baseDeps builds a ServerUpdateDeps whose prepare + commit succeed, health
// passes, and rollback records that it ran. Individual tests override fields.
func baseDeps(m *mockService, swapped []string) (*ServerUpdateDeps, *bool) {
	rolledBack := false
	deps := &ServerUpdateDeps{
		Manager: m,
		Prepare: func(_ context.Context) (any, error) { return "prepared", nil },
		Commit: func(_ context.Context, prepared any) (*SelfUpdateResult, error) {
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

func TestRunServerUpdate_PrepareFailsBeforeStop(t *testing.T) {
	m := &mockService{}
	deps, _ := baseDeps(m, nil)
	deps.Prepare = func(_ context.Context) (any, error) {
		return nil, errors.New("checksum mismatch")
	}
	commitCalled := false
	deps.Commit = func(_ context.Context, _ any) (*SelfUpdateResult, error) {
		commitCalled = true
		return nil, nil
	}

	out, err := RunServerUpdate(context.Background(), *deps)
	if err == nil {
		t.Fatal("expected error when prepare (download/verify) fails")
	}
	// Pre-fetch failure must never stop the service or swap anything.
	if len(m.events) != 0 {
		t.Errorf("service was touched despite a prepare failure: %v", m.events)
	}
	if commitCalled {
		t.Error("commit ran despite a prepare failure")
	}
	if out.RolledBack {
		t.Error("nothing was swapped; RolledBack should be false")
	}
	if !strings.Contains(err.Error(), "untouched") {
		t.Errorf("prepare-failure error should say the server is untouched; got: %v", err)
	}
}

func TestRunServerUpdate_StopFailsBeforeSwap(t *testing.T) {
	m := &mockService{stopErr: []error{errors.New("unit is masked")}}
	deps, _ := baseDeps(m, []string{"/usr/local/bin/crewship"})
	commitCalled := false
	inner := deps.Commit
	deps.Commit = func(ctx context.Context, p any) (*SelfUpdateResult, error) {
		commitCalled = true
		return inner(ctx, p)
	}

	out, err := RunServerUpdate(context.Background(), *deps)
	if err == nil {
		t.Fatal("expected error when the service can't be stopped")
	}
	if commitCalled {
		t.Error("commit must not run if the service could not be stopped")
	}
	if out.RolledBack {
		t.Error("nothing was swapped; RolledBack should be false")
	}
}

func TestRunServerUpdate_CommitFailsRestartsOldBinary(t *testing.T) {
	m := &mockService{}
	deps, rolledBack := baseDeps(m, nil)
	deps.Commit = func(_ context.Context, _ any) (*SelfUpdateResult, error) {
		return nil, errors.New("cross-device rename")
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
	// Start #1 (new binary) fails; after rollback Start #2 (old binary) succeeds
	// and its post-rollback health probe passes.
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
	// A failed START (not health) means migrations never ran, so no
	// restore-snapshot guidance should appear.
	if strings.Contains(err.Error(), "restore-snapshot") {
		t.Errorf("a failed-to-start rollback should not mention restore-snapshot: %v", err)
	}
}

func TestRunServerUpdate_UnhealthyRollsBack_OldRecovers(t *testing.T) {
	// New binary unhealthy; rolled-back old binary comes back healthy. The
	// message carries the soft snapshot note (migrations ran) but not the
	// urgent NOW escalation.
	m := &mockService{}
	deps, _ := baseDeps(m, []string{"/usr/local/bin/crewship"})
	hc, _ := healthSeq(errors.New("health probe timed out"), nil) // new: unhealthy, old: healthy
	deps.Health = hc

	out, err := RunServerUpdate(context.Background(), *deps)
	if err == nil {
		t.Fatal("expected error when the new binary is unhealthy")
	}
	if !out.RolledBack {
		t.Error("an unhealthy new binary must be rolled back")
	}
	if !strings.Contains(err.Error(), "restore-snapshot") {
		t.Errorf("post-migration rollback should mention restore-snapshot (soft): %v", err)
	}
	if strings.Contains(err.Error(), "NOW") {
		t.Errorf("old binary recovered, so the urgent NOW escalation should NOT fire: %v", err)
	}
	// stop(old) -> start(new) -> stop(new, unhealthy) -> start(old)
	if got := strings.Join(m.events, ","); got != "stop,start,stop,start" {
		t.Errorf("call sequence = %q, want stop,start,stop,start", got)
	}
}

func TestRunServerUpdate_UnhealthyRollsBack_OldAlsoBroken_UrgentSnapshot(t *testing.T) {
	// New binary unhealthy AND the rolled-back old binary is ALSO unhealthy —
	// the classic post-migration skew-guard crash loop. The operator must be
	// told to restore the snapshot NOW.
	m := &mockService{}
	deps, _ := baseDeps(m, []string{"/usr/local/bin/crewship"})
	hc, calls := healthSeq(errors.New("new: 503"), errors.New("old: skew guard refuses to boot"))
	deps.Health = hc

	out, err := RunServerUpdate(context.Background(), *deps)
	if err == nil {
		t.Fatal("expected error when both new and rolled-back binaries are unhealthy")
	}
	if !out.RolledBack {
		t.Error("the binary was restored, so RolledBack should be true even though it's unhealthy")
	}
	if *calls != 2 {
		t.Errorf("expected 2 health probes (new + post-rollback old), got %d", *calls)
	}
	if !strings.Contains(err.Error(), "snapshot NOW") {
		t.Errorf("a broken rolled-back binary after migrations must demand restore-snapshot NOW; got: %v", err)
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
	// recovery and, since migrations ran, the snapshot.
	if !strings.Contains(err.Error(), "manual") {
		t.Errorf("failed-rollback error should call for manual recovery; got: %v", err)
	}
}
