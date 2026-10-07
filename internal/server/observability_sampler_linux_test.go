//go:build linux

package server

import (
	"context"
	"testing"
	"time"
)

func TestHostSamplerPublishesBoundedResourcesAndStops(t *testing.T) {
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
