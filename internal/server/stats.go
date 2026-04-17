package server

import (
	"context"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

type trackedContainer struct {
	ContainerID string
	CrewID      string
	WorkspaceID string
}

// lastEmitted records what the Crow's Nest journal last saw for a container.
// Used to throttle container.metrics entries: emit only when CPU or RAM
// changed by >10% relative to the last recorded sample, or when the minimum
// interval has elapsed since the last emit. This keeps the journal from
// getting one container.metrics row every 5s per container (which would
// drown out higher-signal events in the Crow's Nest UI).
type lastEmitted struct {
	CPU       float64
	MemoryPct float64
	At        time.Time
}

// StatsCollector periodically polls container metrics and broadcasts them
// to WebSocket clients subscribed to workspace channels. When a journal
// emitter is configured via SetJournal, it also writes container.metrics
// entries to the Crew Journal so Crow's Nest has a persisted resource
// timeline.
type StatsCollector struct {
	container provider.ContainerProvider
	hub       *ws.Hub
	logger    *slog.Logger
	interval  time.Duration
	mu        sync.RWMutex
	tracked   map[string]trackedContainer
	latestMu  sync.RWMutex
	latest    map[string]*provider.ContainerMetrics

	// journal, journalEmitInterval, and lastEmit together gate writes to
	// the Crew Journal. journal is nil when the server starts without a
	// live DB (tests, --dry-run), so all emit attempts become no-ops.
	journalMu            sync.RWMutex
	journal              journal.Emitter
	journalEmitInterval  time.Duration
	lastEmit             map[string]lastEmitted
}

// NewStatsCollector creates a StatsCollector that polls container metrics at
// the given interval and broadcasts them via the WebSocket hub.
func NewStatsCollector(ctr provider.ContainerProvider, hub *ws.Hub, logger *slog.Logger, interval time.Duration) *StatsCollector {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &StatsCollector{
		container: ctr, hub: hub, logger: logger, interval: interval,
		tracked:             make(map[string]trackedContainer),
		latest:              make(map[string]*provider.ContainerMetrics),
		lastEmit:            make(map[string]lastEmitted),
		journalEmitInterval: 30 * time.Second,
	}
}

// SetJournal wires the journal emitter. When set, poll() writes one
// container.metrics entry per tracked container every journalEmitInterval
// (30s by default) or sooner if CPU/RAM changed by >10% since the last
// emit. Passing nil disables journal writes without affecting the live
// WebSocket broadcast.
func (sc *StatsCollector) SetJournal(j journal.Emitter) {
	sc.journalMu.Lock()
	sc.journal = j
	sc.journalMu.Unlock()
}

func (sc *StatsCollector) getJournal() journal.Emitter {
	sc.journalMu.RLock()
	defer sc.journalMu.RUnlock()
	return sc.journal
}

// Register adds a container to the polling set.
func (sc *StatsCollector) Register(containerID, crewID, workspaceID string) {
	sc.mu.Lock()
	sc.tracked[containerID] = trackedContainer{ContainerID: containerID, CrewID: crewID, WorkspaceID: workspaceID}
	sc.mu.Unlock()
}

// Unregister removes a container from the polling set.
func (sc *StatsCollector) Unregister(containerID string) {
	sc.mu.Lock()
	delete(sc.tracked, containerID)
	sc.mu.Unlock()
	sc.latestMu.Lock()
	delete(sc.latest, containerID)
	sc.latestMu.Unlock()
}

// Latest returns the most recent metrics for a container, or nil if unavailable.
func (sc *StatsCollector) Latest(containerID string) *provider.ContainerMetrics {
	sc.latestMu.RLock()
	defer sc.latestMu.RUnlock()
	return sc.latest[containerID]
}

// LatestByCrewID looks up the container registered for the given crewID and
// returns its latest metrics along with the container ID. This avoids trusting
// a client-supplied container_id parameter.
func (sc *StatsCollector) LatestByCrewID(crewID string) (string, *provider.ContainerMetrics) {
	sc.mu.RLock()
	var containerID string
	for _, tc := range sc.tracked {
		if tc.CrewID == crewID {
			containerID = tc.ContainerID
			break
		}
	}
	sc.mu.RUnlock()
	if containerID == "" {
		return "", nil
	}
	sc.latestMu.RLock()
	m := sc.latest[containerID]
	sc.latestMu.RUnlock()
	return containerID, m
}

// Run starts the polling loop, blocking until ctx is cancelled.
func (sc *StatsCollector) Run(ctx context.Context) {
	ticker := time.NewTicker(sc.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sc.poll(ctx)
		}
	}
}

func (sc *StatsCollector) poll(ctx context.Context) {
	sc.mu.RLock()
	targets := make([]trackedContainer, 0, len(sc.tracked))
	for _, t := range sc.tracked {
		targets = append(targets, t)
	}
	sc.mu.RUnlock()
	if len(targets) == 0 {
		return
	}
	const maxWorkers = 10
	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(tc trackedContainer) {
			defer wg.Done()
			defer func() { <-sem }()
			pollCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			metrics, err := sc.container.ContainerStats(pollCtx, tc.ContainerID)
			if err != nil {
				// Don't unregister on transient errors — just skip this cycle
				return
			}
			sc.latestMu.Lock()
			sc.latest[tc.ContainerID] = metrics
			sc.latestMu.Unlock()
			sc.hub.BroadcastWorkspace(tc.WorkspaceID, "container.stats",
				map[string]interface{}{
					"container_id": tc.ContainerID, "crew_id": tc.CrewID,
					"cpu_percent": metrics.CPUPercent, "memory_used": metrics.MemoryUsed,
					"memory_limit": metrics.MemoryLimit, "memory_percent": metrics.MemoryPct,
					"net_rx_bytes": metrics.NetRx, "net_tx_bytes": metrics.NetTx,
					"pids": metrics.PIDs, "timestamp": metrics.Timestamp,
				})

			// Crow's Nest: persist a container.metrics journal entry when
			// thresholds are crossed. The WebSocket broadcast above is the
			// live/ephemeral signal (dashboard sparkline); the journal entry
			// is the replayable/auditable one. Tracked separately from the
			// latestMu lock so a slow journal write never blocks the live
			// stats fan-out.
			sc.maybeEmitJournalMetrics(ctx, tc, metrics)
		}(t)
	}
	wg.Wait()
}

// maybeEmitJournalMetrics writes a container.metrics journal entry for the
// given container if either (a) more than journalEmitInterval has passed
// since the last emit, or (b) CPU or memory-percent changed by more than
// 10 percentage points since the last emit. The 10pp threshold is coarse
// on purpose — absolute deltas keep the filter cheap and intuitive for
// small containers where relative deltas swing wildly.
//
// The journal Emitter is called in the caller's goroutine (already inside
// poll()'s worker pool) so a slow DB write back-pressures the next poll
// cycle instead of spawning runaway goroutines. The write uses the
// caller's ctx so Run's cancellation (server shutdown) propagates.
func (sc *StatsCollector) maybeEmitJournalMetrics(ctx context.Context, tc trackedContainer, m *provider.ContainerMetrics) {
	j := sc.getJournal()
	if j == nil || m == nil {
		return
	}

	sc.journalMu.Lock()
	prev, had := sc.lastEmit[tc.ContainerID]
	interval := sc.journalEmitInterval
	sc.journalMu.Unlock()

	now := time.Now()
	shouldEmit := !had
	if !shouldEmit && now.Sub(prev.At) >= interval {
		shouldEmit = true
	}
	if !shouldEmit {
		if math.Abs(m.CPUPercent-prev.CPU) > 10 || math.Abs(m.MemoryPct-prev.MemoryPct) > 10 {
			shouldEmit = true
		}
	}
	if !shouldEmit {
		return
	}

	// MemoryUsed is bytes on disk in the provider struct; expose MB in the
	// payload so Crow's Nest sparklines can render without per-row math.
	// disk_mb is left out — ContainerMetrics doesn't expose a disk field,
	// and inventing one here would either (a) require an extra exec or
	// (b) lie. A future ContainerMetrics.DiskUsedBytes can be added later.
	memMB := float64(m.MemoryUsed) / (1024 * 1024)
	payload := map[string]any{
		"cpu_pct": m.CPUPercent,
		"ram_mb":  memMB,
		"ram_pct": m.MemoryPct,
		"net_rx":  m.NetRx,
		"net_tx":  m.NetTx,
		"pids":    m.PIDs,
	}

	// WorkspaceID is required by journal.Entry.Validate; a tracked container
	// without a workspace scope indicates a caller bug (direct-run path
	// passing empty), so skip silently rather than surfacing the validation
	// error. The WS broadcast above already covers that case.
	if tc.WorkspaceID == "" {
		return
	}

	entry := journal.Entry{
		WorkspaceID: tc.WorkspaceID,
		CrewID:      tc.CrewID,
		Type:        journal.EntryContainerMetrics,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorSystem,
		Summary: formatMetricsSummary(tc.CrewID, m.CPUPercent, memMB),
		Payload: payload,
		Refs:    map[string]any{"container_id": tc.ContainerID},
	}
	if _, err := j.Emit(ctx, entry); err != nil {
		sc.logger.Debug("container.metrics journal emit failed", "err", err, "container_id", tc.ContainerID)
		return
	}

	sc.journalMu.Lock()
	sc.lastEmit[tc.ContainerID] = lastEmitted{CPU: m.CPUPercent, MemoryPct: m.MemoryPct, At: now}
	sc.journalMu.Unlock()
}

// formatMetricsSummary builds a short human-readable summary. Kept separate
// from the emit path so tests can assert stability of the wire format
// without exercising the full poll loop.
func formatMetricsSummary(crewID string, cpuPct, ramMB float64) string {
	// e.g. "crew-42: 12.3% CPU, 512 MB RAM"
	if crewID == "" {
		crewID = "container"
	}
	return crewID + ": " +
		strconv.FormatFloat(cpuPct, 'f', 1, 64) + "% CPU, " +
		strconv.FormatFloat(ramMB, 'f', 0, 64) + " MB RAM"
}
