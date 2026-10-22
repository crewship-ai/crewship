package server

import (
	"fmt"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/appengine"
)

// startGridCraftTestBarrier activates the workbook test barrier when this
// binary was built with the crewship_testbarrier tag and the environment asks
// for it. Any invalid request (for example a stage or prod slot) stops startup.
// Normal builds compile a no-op and never fail here.
func startGridCraftTestBarrier(logger *slog.Logger) error {
	active, err := appengine.ConfigureTestBarrier()
	if err != nil {
		return fmt.Errorf("GridCraft test barrier: %w", err)
	}
	if active && logger != nil {
		logger.Warn("GridCraft TEST BARRIER ACTIVE: barrier- workbook operations can be parked; never run this build outside a development slot")
	}
	return nil
}

// addGridCraftTestBarrierHealth marks /healthz only while a barrier is active,
// so a healthy normal body is unchanged.
func addGridCraftTestBarrierHealth(body map[string]interface{}) {
	if appengine.TestBarrierActive() {
		body["test_barrier"] = true
	}
}
