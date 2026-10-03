package server

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/scheduler"
)

func TestHostCountersRejectMalformedSamples(t *testing.T) {
	for _, raw := range []string{"", "cpu0 1 2 3 4 5 6 7 8", "cpu 1 2 3", "cpu 1 2 bad 4 5 6 7 8", "cpu 1 2 -3 4 5 6 7 8"} {
		if _, err := parseHostCPUCounters([]byte(raw)); err == nil {
			t.Fatalf("invalid counters accepted: %q", raw)
		}
	}
	if _, err := hostCPUPercent(cpuCounters{total: 100, idle: 10}, cpuCounters{total: 110, idle: 30}); err == nil {
		t.Fatal("impossible idle delta reported as utilization")
	}
}

func TestHostSamplerPublishesBoundedResourcesAndStops(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host sampler reads Linux procfs")
	}
	ctx, cancel := context.WithCancel(t.Context())
	s := &Server{logger: quietLogger()}
	done := make(chan struct{})
	go func() { defer close(done); s.runHostResourceSampler(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("sampler did not stop")
		}
	})
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("sampler published no resources")
		case <-tick.C:
			if sample := s.hostResourceLatest.Load(); sample != nil {
				if sample.CPUPercent < 0 || sample.CPUPercent > 100 || sample.MemoryPercent < 0 || sample.MemoryPercent > 100 || sample.MemoryTotalMB <= 0 || sample.MemoryUsedMB < 0 || sample.MemoryUsedMB > sample.MemoryTotalMB || sample.SampledAt.IsZero() {
					t.Fatalf("invalid host gauges: %+v", sample)
				}
				cancel()
				return
			}
		}
	}
}

type freshnessContainer struct {
	provider.ContainerProvider
	*fakeFreshness
}

func TestImageFreshnessRegistrationRequiresEveryDependency(t *testing.T) {
	db := sweepDB(t)
	sched := scheduler.New(db, nil, nil, nil, nil, nil, scheduler.Config{}, quietLogger())
	fresh := freshnessContainer{fakeFreshness: &fakeFreshness{state: behindState()}}
	for _, tc := range []struct {
		name      string
		sched     *scheduler.Scheduler
		withDB    bool
		container provider.ContainerProvider
		emitter   journal.Emitter
		want      bool
	}{
		{"missing scheduler", nil, true, fresh, &recordingEmitter{}, false},
		{"missing database", sched, false, fresh, &recordingEmitter{}, false},
		{"unsupported provider", sched, true, &mockContainer{}, &recordingEmitter{}, false},
		{"missing journal", sched, true, fresh, nil, false},
		{"complete", sched, true, fresh, &recordingEmitter{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := db
			if !tc.withDB {
				store = nil
			}
			if got := registerImageFreshnessRoutine(tc.sched, store, tc.container, tc.emitter, quietLogger()); got != tc.want {
				t.Fatalf("registered=%v, want %v", got, tc.want)
			}
		})
	}
	if fresh.calls != 0 {
		t.Fatal("registration performed unscheduled registry checks")
	}
}
