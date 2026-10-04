package server

import (
	"testing"

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
