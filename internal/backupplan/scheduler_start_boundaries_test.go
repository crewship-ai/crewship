package backupplan

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestStartedBackupSchedulerDispatchesManualWorkOnKick(t *testing.T) {
	h := newHarness(t, "2026-09-30T12:00:00Z")
	directory := t.TempDir()
	h.svc.BackupsDir = func() (string, error) { return directory, nil }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h.svc.Start(ctx)
	t.Cleanup(func() { cancel(); h.svc.Wait() })
	ids, err := h.svc.StartManual(ctx, ManualRequest{Scope: ScopeWorkspaces, WorkspaceIDs: []string{"ws_a"}, Recipients: []string{h.key}}, "u1")
	if err != nil || len(ids) != 1 {
		t.Fatalf("manual admission: %v %v", ids, err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("started scheduler never dispatched queued manual run")
		case <-poll.C:
			run, err := GetRun(ctx, h.db, ids[0])
			if err != nil {
				t.Fatal(err)
			}
			if run.Status == StatusDone {
				if len(h.exec.calls()) != 1 {
					t.Fatalf("manual run executed more than once: %d", len(h.exec.calls()))
				}
				cancel()
				h.svc.Wait()
				return
			}
			if run.Status == StatusFailed {
				t.Fatalf("manual run failed: %s", run.Error)
			}
		}
	}
}

func TestBackupSchedulerLoopStopsWhenCanceled(t *testing.T) {
	db := busyFixtureDB(t)
	service := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.kick = make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan struct{})
	go func() { defer close(done); service.loop(ctx) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled scheduler did not stop")
	}
}

func TestBackupContentsPreviewRejectsUnsupportedRequests(t *testing.T) {
	for _, tc := range []struct {
		preset   string
		contents []string
		mode     string
	}{
		{"unknown", nil, ""},
		{backup.PresetWorkspace, nil, "unsafe"},
		{backup.PresetCustom, []string{"unknown-category"}, backup.EnvModeFiles},
	} {
		if _, err := PreviewContents(tc.preset, tc.contents, tc.mode); !IsValidation(err) {
			t.Fatalf("invalid preview accepted: %#v %v", tc, err)
		}
	}
	if _, err := PreviewContents(backup.PresetWorkspace, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewContents(backup.PresetComplete, nil, backup.EnvModeComplete); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewContents(backup.PresetCustom, []string{"memory"}, backup.EnvModeFiles); err != nil {
		t.Fatal(err)
	}
}
