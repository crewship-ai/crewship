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
)

const hostResourceInterval = 15 * time.Second

type hostResourceSample struct {
	SampledAt     time.Time
	CPUPercent    float64
	MemoryPercent float64
	MemoryUsedMB  int64
	MemoryTotalMB int64
}

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

// runHostResourceSampler refreshes in-memory host gauges for /metrics. Scrape
// history belongs in Prometheus; this path performs no SQLite writes.
func (s *Server) runHostResourceSampler(ctx context.Context) {
	ticker := time.NewTicker(hostResourceInterval)
	defer ticker.Stop()
	collect := func() {
		cpuPct, memoryPct, usedMB, totalMB, err := measureHostResources(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Debug("host resource measurement unavailable", "error", err)
			}
			return
		}
		s.hostResourceLatest.Store(&hostResourceSample{
			SampledAt: time.Now().UTC(), CPUPercent: cpuPct,
			MemoryPercent: memoryPct, MemoryUsedMB: usedMB, MemoryTotalMB: totalMB,
		})
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
