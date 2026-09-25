package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/admission"
	goapi "github.com/crewship-ai/crewship/internal/api"
)

const (
	hostResourceInterval        = 15 * time.Second
	hostResourcePersistInterval = time.Minute
)

// Fixed-width UTC fractions keep SQLite's TEXT range comparisons ordered.
const hostResourceTimeFormat = "2006-01-02T15:04:05.000000000Z"

type cpuCounters struct {
	total uint64
	idle  uint64
}

// /proc/stat's guest fields are already included in user/nice, so only the
// first eight counters belong in the total. idle includes iowait.
func parseHostCPUCounters(raw []byte) (cpuCounters, error) {
	line, _, _ := strings.Cut(string(raw), "\n")
	fields := strings.Fields(line)
	if len(fields) < 9 || fields[0] != "cpu" {
		return cpuCounters{}, errors.New("missing aggregate cpu counters")
	}
	var out cpuCounters
	for i := 1; i <= 8; i++ {
		value, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			return cpuCounters{}, fmt.Errorf("invalid cpu counter: %w", err)
		}
		out.total += value
		if i == 4 || i == 5 {
			out.idle += value
		}
	}
	return out, nil
}

func hostCPUPercent(before, after cpuCounters) (float64, error) {
	if after.total <= before.total || after.idle < before.idle {
		return 0, errors.New("cpu counters did not advance")
	}
	total := after.total - before.total
	idle := after.idle - before.idle
	if idle > total {
		return 0, errors.New("idle cpu counter exceeded total")
	}
	return 100 * float64(total-idle) / float64(total), nil
}

func readHostCPUCounters() (cpuCounters, error) {
	raw, err := os.ReadFile("/proc/stat") // #nosec G304 -- fixed kernel path
	if err != nil {
		return cpuCounters{}, err
	}
	return parseHostCPUCounters(raw)
}

func measureHostResources(ctx context.Context) (cpuPct, memoryPct float64, usedMB, totalMB int64, err error) {
	before, err := readHostCPUCounters()
	if err != nil {
		return 0, 0, 0, 0, err
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return 0, 0, 0, 0, ctx.Err()
	case <-timer.C:
	}
	after, err := readHostCPUCounters()
	if err != nil {
		return 0, 0, 0, 0, err
	}
	cpuPct, err = hostCPUPercent(before, after)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	memory, err := admission.ReadHostMemory()
	if err != nil {
		return 0, 0, 0, 0, err
	}
	if memory.TotalMB <= 0 || memory.AvailableMB < 0 || memory.AvailableMB > memory.TotalMB {
		return 0, 0, 0, 0, errors.New("invalid host memory reading")
	}
	usedMB = memory.TotalMB - memory.AvailableMB
	totalMB = memory.TotalMB
	memoryPct = 100 * float64(usedMB) / float64(totalMB)
	return cpuPct, memoryPct, usedMB, totalMB, nil
}

// runHostResourceSampler refreshes the in-memory reading every 15 seconds for
// the dashboard and /metrics, but persists at most one sample per minute for
// the bounded 30-day chart history. It runs even without dashboard viewers.
func (s *Server) runHostResourceSampler(ctx context.Context) {
	ticker := time.NewTicker(hostResourceInterval)
	defer ticker.Stop()
	var lastPrune time.Time
	var lastPersist time.Time
	collect := func() {
		cpuPct, memoryPct, usedMB, totalMB, err := measureHostResources(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Debug("host resource measurement unavailable", "error", err)
			}
			return
		}
		now := time.Now().UTC()
		s.hostResourceLatest.Store(&goapi.HostResourceSample{
			SampledAt: now.Format(hostResourceTimeFormat), CPUPercent: cpuPct,
			MemoryPercent: memoryPct, MemoryUsedMB: usedMB, MemoryTotalMB: totalMB,
		})
		if now.Sub(lastPersist) < hostResourcePersistInterval {
			return
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO host_resource_samples(ts, cpu_percent, memory_percent, memory_used_mb, memory_total_mb) VALUES(?, ?, ?, ?, ?)`,
			now.Format(hostResourceTimeFormat), cpuPct, memoryPct, usedMB, totalMB); err != nil {
			s.logger.Warn("host resource sample write failed", "error", err)
			return
		}
		lastPersist = now
		if now.Sub(lastPrune) >= 24*time.Hour {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM host_resource_samples WHERE ts < ?`, now.Add(-30*24*time.Hour).Format(hostResourceTimeFormat)); err != nil {
				s.logger.Warn("host resource sample pruning failed", "error", err)
			} else {
				lastPrune = now
			}
		}
	}
	collect()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collect()
		}
	}
}
