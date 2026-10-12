package automation

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

type lifecycleLoader struct {
	calls  atomic.Int32
	loaded chan struct{}
	rules  []Resolved
}

func (l *lifecycleLoader) ListActive(context.Context) ([]Resolved, error) {
	call := l.calls.Add(1)
	if call == 1 {
		return nil, errors.New("temporary storage outage")
	}
	if call == 2 {
		close(l.loaded)
	}
	return l.rules, nil
}

func TestRegistryRecoversRefreshAndFlushesUntilStopped(t *testing.T) {
	enq := &recordingEnqueuer{}
	rule := rule("rule", "ws", "mission.status_change")
	rule.DebounceSeconds = 0
	loader := &lifecycleLoader{loaded: make(chan struct{}), rules: []Resolved{rule}}
	reg := NewRegistry(loader, enq, Options{RefreshInterval: 5 * time.Millisecond, FlushInterval: 5 * time.Millisecond})
	reg.Start(t.Context())
	t.Cleanup(reg.Stop)
	select {
	case <-loader.loaded:
	case <-time.After(2 * time.Second):
		t.Fatal("registry never retried failed refresh")
	}
	// Refresh completion is signalled by observing the actual rule index, not
	// merely the loader returning: the first event must be routed after Load.
	deadline := time.Now().Add(2 * time.Second)
	for {
		reg.mu.Lock()
		loaded := len(reg.byEvent) > 0
		reg.mu.Unlock()
		if loaded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refreshed rules never installed")
		}
		time.Sleep(time.Millisecond)
	}
	reg.Observer([]journal.Entry{entry("ws", "mission.status_change", "mission")})
	for enq.n() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("background flush never enqueued matching event")
		}
		time.Sleep(time.Millisecond)
	}
	reg.Stop()
	reg.Stop()
	if enq.n() != 1 {
		t.Fatalf("event enqueued %d times", enq.n())
	}
	if got := enq.at(0); got.WorkspaceID != "ws" || got.PipelineSlug != "triage" {
		t.Fatalf("wrong background target: %+v", got)
	}
}

func TestRegistryCancellationDrainsPendingIntent(t *testing.T) {
	enq := &recordingEnqueuer{}
	reg := NewRegistry(nil, enq, Options{RefreshInterval: time.Hour, FlushInterval: time.Hour})
	rule := rule("rule", "ws", "mission.status_change")
	rule.DebounceSeconds = 0
	reg.Load([]Resolved{rule})
	if err := reg.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	reg.Observer([]journal.Entry{entry("ws", "mission.status_change", "mission")})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reg.Start(ctx)
	done := make(chan struct{})
	go func() { reg.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		reg.Stop()
		t.Fatal("cancelled registry did not drain and exit")
	}
	reg.Stop()
	if enq.n() != 1 || reg.PendingIntents() != 0 {
		t.Fatalf("shutdown lost pending event: enqueues=%d pending=%d", enq.n(), reg.PendingIntents())
	}
}

func TestPayloadValidationNamesDeterministicUnknownKeys(t *testing.T) {
	if !ValidPayloadKey(journal.EntryType("future.event"), "new_field") {
		t.Fatal("incomplete catalog rejected an undocumented emitter")
	}
	if err := validatePayloadEqualsKeys(journal.EntryMissionStatus, map[string]any{"action": "changed", "to": "DONE"}); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		err := validatePayloadEqualsKeys(journal.EntryMissionStatus, map[string]any{"z_unknown": true, "a_unknown": true})
		if err == nil || !strings.Contains(err.Error(), `"a_unknown"`) || !strings.Contains(err.Error(), "known keys:") {
			t.Fatalf("unstable or unactionable payload failure: %v", err)
		}
	}
	if hint := eventTypeHint("nonexistent.event"); !strings.Contains(hint, "no registered type") || !strings.Contains(hint, "crewship journal") {
		t.Fatalf("unknown namespace lacks diagnostic: %s", hint)
	}
	if hint := eventTypeHint("pipeline.typo"); !strings.Contains(hint, "pipeline.") || !strings.Contains(hint, "…") {
		t.Fatalf("large namespace not bounded: %s", hint)
	}
}
