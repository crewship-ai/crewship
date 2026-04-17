package server

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/provider"
)

func newMetricsCollector(t *testing.T) (*StatsCollector, *captureEmitter) {
	t.Helper()
	sc := NewStatsCollector(nil, nil, newSilentLogger(), 5*time.Second)
	em := &captureEmitter{}
	sc.SetJournal(em)
	return sc, em
}

func TestMaybeEmitJournalMetrics_FirstEmitAlways(t *testing.T) {
	t.Parallel()
	sc, em := newMetricsCollector(t)
	tc := trackedContainer{ContainerID: "cid", CrewID: "c1", WorkspaceID: "ws"}
	m := &provider.ContainerMetrics{CPUPercent: 5, MemoryUsed: 1024 * 1024, MemoryPct: 10}

	sc.maybeEmitJournalMetrics(context.Background(), tc, m)

	if len(em.entries) != 1 {
		t.Fatalf("expected one emit on first call, got %d", len(em.entries))
	}
	if em.entries[0].Type != journal.EntryContainerMetrics {
		t.Errorf("expected container.metrics, got %s", em.entries[0].Type)
	}
}

func TestMaybeEmitJournalMetrics_SuppressesSmallDelta(t *testing.T) {
	t.Parallel()
	sc, em := newMetricsCollector(t)
	tc := trackedContainer{ContainerID: "cid", CrewID: "c1", WorkspaceID: "ws"}

	// First emit establishes baseline
	sc.maybeEmitJournalMetrics(context.Background(), tc,
		&provider.ContainerMetrics{CPUPercent: 50, MemoryPct: 30})

	// +5pp CPU, +5pp mem — below the 10pp threshold — should not emit
	sc.maybeEmitJournalMetrics(context.Background(), tc,
		&provider.ContainerMetrics{CPUPercent: 55, MemoryPct: 35})

	if len(em.entries) != 1 {
		t.Errorf("expected throttled second emit, got %d entries", len(em.entries))
	}
}

func TestMaybeEmitJournalMetrics_EmitOnBigDelta(t *testing.T) {
	t.Parallel()
	sc, em := newMetricsCollector(t)
	tc := trackedContainer{ContainerID: "cid", CrewID: "c1", WorkspaceID: "ws"}

	sc.maybeEmitJournalMetrics(context.Background(), tc,
		&provider.ContainerMetrics{CPUPercent: 10, MemoryPct: 10})

	// +20pp CPU — above threshold
	sc.maybeEmitJournalMetrics(context.Background(), tc,
		&provider.ContainerMetrics{CPUPercent: 30, MemoryPct: 12})

	if len(em.entries) != 2 {
		t.Errorf("expected emit on >10pp delta, got %d entries", len(em.entries))
	}
}

func TestMaybeEmitJournalMetrics_SkipsWithoutWorkspace(t *testing.T) {
	t.Parallel()
	sc, em := newMetricsCollector(t)
	tc := trackedContainer{ContainerID: "cid", CrewID: "c1"} // no WorkspaceID
	m := &provider.ContainerMetrics{CPUPercent: 5}

	sc.maybeEmitJournalMetrics(context.Background(), tc, m)

	if len(em.entries) != 0 {
		t.Errorf("expected no emit without workspace, got %d", len(em.entries))
	}
}

func TestMaybeEmitJournalMetrics_NoOpWithoutJournal(t *testing.T) {
	t.Parallel()
	sc := NewStatsCollector(nil, nil, newSilentLogger(), 5*time.Second)
	// No SetJournal call → nil emitter → must not panic.
	sc.maybeEmitJournalMetrics(context.Background(),
		trackedContainer{ContainerID: "cid", WorkspaceID: "ws"},
		&provider.ContainerMetrics{CPUPercent: 5})
}

func TestFormatMetricsSummary(t *testing.T) {
	t.Parallel()
	got := formatMetricsSummary("crew-42", 12.345, 512.7)
	want := "crew-42: 12.3% CPU, 513 MB RAM"
	if got != want {
		t.Errorf("formatMetricsSummary = %q, want %q", got, want)
	}
	if got := formatMetricsSummary("", 0.0, 0.0); got != "container: 0.0% CPU, 0 MB RAM" {
		t.Errorf("empty crew ID fallback got %q", got)
	}
}
