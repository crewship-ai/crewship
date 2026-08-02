package api

// PortExposeRegistry.HasContainer — the occupancy check the idle-crew reaper
// runs before stopping a crew container (#1662).
//
// A stop kills the process the agent exposed the port from. The reverse-proxy
// entry survives, so the capability URL would 502 for the rest of its TTL
// with nothing left to restart. The exposure must therefore keep the
// container up.

import (
	"testing"
	"time"
)

func TestHasContainer_LiveEntry(t *testing.T) {
	r := NewPortExposeRegistry(nil, newTestLogger())
	r.Add(&ExposeEntry{
		Token:         "tok-live",
		ContainerID:   "ct-a",
		ContainerIP:   "10.0.0.5",
		ContainerPort: 3000,
		ExpiresAt:     time.Now().UTC().Add(time.Hour),
	})
	if !r.HasContainer("ct-a") {
		t.Error("HasContainer(ct-a) = false with a live exposure pointing at it")
	}
}

func TestHasContainer_OtherContainersExposureDoesNotCount(t *testing.T) {
	// The whole point is per-container: one crew's exposure must not pin
	// every other crew's container on the host.
	r := NewPortExposeRegistry(nil, newTestLogger())
	r.Add(&ExposeEntry{
		Token:       "tok-other",
		ContainerID: "ct-b",
		ExpiresAt:   time.Now().UTC().Add(time.Hour),
	})
	if r.HasContainer("ct-a") {
		t.Error("HasContainer(ct-a) = true on the strength of ct-b's exposure")
	}
}

func TestHasContainer_ExpiredEntryDoesNotPinTheContainer(t *testing.T) {
	// The purger runs on a 30s cadence, so an expired row can still be in the
	// map when the sweep asks. An exposure past its TTL is not a reason to
	// keep a container running.
	r := NewPortExposeRegistry(nil, newTestLogger())
	r.Add(&ExposeEntry{
		Token:       "tok-dead",
		ContainerID: "ct-a",
		ExpiresAt:   time.Now().UTC().Add(-time.Minute),
	})
	if r.HasContainer("ct-a") {
		t.Error("HasContainer(ct-a) = true for an expired exposure")
	}
}

func TestHasContainer_EmptyRegistryAndEmptyID(t *testing.T) {
	r := NewPortExposeRegistry(nil, newTestLogger())
	if r.HasContainer("ct-a") {
		t.Error("HasContainer on an empty registry = true")
	}
	r.Add(&ExposeEntry{
		Token:       "tok",
		ContainerID: "ct-a",
		ExpiresAt:   time.Now().UTC().Add(time.Hour),
	})
	if r.HasContainer("") {
		t.Error("HasContainer(\"\") = true; an unknown container id must never pin anything")
	}
}
