package server

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

type trackedContainer struct {
	ContainerID string
	CrewID      string
	WorkspaceID string
}

type StatsCollector struct {
	container provider.ContainerProvider
	hub       *ws.Hub
	logger    *slog.Logger
	interval  time.Duration
	mu        sync.RWMutex
	tracked   map[string]trackedContainer
	latestMu  sync.RWMutex
	latest    map[string]*provider.ContainerMetrics
}

func NewStatsCollector(ctr provider.ContainerProvider, hub *ws.Hub, logger *slog.Logger, interval time.Duration) *StatsCollector {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &StatsCollector{
		container: ctr, hub: hub, logger: logger, interval: interval,
		tracked: make(map[string]trackedContainer),
		latest:  make(map[string]*provider.ContainerMetrics),
	}
}

func (sc *StatsCollector) Register(containerID, crewID, workspaceID string) {
	sc.mu.Lock()
	sc.tracked[containerID] = trackedContainer{ContainerID: containerID, CrewID: crewID, WorkspaceID: workspaceID}
	sc.mu.Unlock()
}

func (sc *StatsCollector) Unregister(containerID string) {
	sc.mu.Lock()
	delete(sc.tracked, containerID)
	sc.mu.Unlock()
	sc.latestMu.Lock()
	delete(sc.latest, containerID)
	sc.latestMu.Unlock()
}

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
			if sc.hub != nil {
				channel := "workspace:" + tc.WorkspaceID
				sc.hub.Broadcast(channel, ws.ServerMessage{
					Type: "container.stats", Channel: channel,
					Payload: map[string]interface{}{
						"container_id": tc.ContainerID, "crew_id": tc.CrewID,
						"cpu_percent": metrics.CPUPercent, "memory_used": metrics.MemoryUsed,
						"memory_limit": metrics.MemoryLimit, "memory_percent": metrics.MemoryPct,
						"net_rx_bytes": metrics.NetRx, "net_tx_bytes": metrics.NetTx,
						"pids": metrics.PIDs, "timestamp": metrics.Timestamp,
					},
				})
			}
		}(t)
	}
	wg.Wait()
}
