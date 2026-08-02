package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/admission"
)

type fakeSnapshotter struct{ snap admission.Snapshot }

func (f fakeSnapshotter) Snapshot(context.Context) admission.Snapshot { return f.snap }

func decodeCapacity(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rr.Body.String())
	}
	return out
}

// The whole point of the endpoint: a held start is legible — which crew, why,
// and for how long.
func TestRuntimeCapacity_ReportsHeldStartsWithReasonAndAge(t *testing.T) {
	h := NewRuntimeCapacityHandler(fakeSnapshotter{snap: admission.Snapshot{
		Limits:              admission.Limits{RequiredFreeMB: 3072, MaxConcurrentStarts: 4},
		InFlightStarts:      4,
		HostSignalAvailable: true,
		Host:                admission.HostMemory{AvailableMB: 900, TotalMB: 16000, SomeStallPct: 3.2},
		HeldTotal:           7,
		Held: []admission.Hold{{
			CrewID:   "crew-a",
			CrewSlug: "alpha",
			Reason:   admission.ReasonHostMemory,
			Detail:   "host has 900 MiB available, 3072 MiB needed for one more agent container",
			Since:    time.Now().Add(-42 * time.Second),
			WaitedMs: 42000,
		}},
	}})

	rr := httptest.NewRecorder()
	h.Get(rr, httptest.NewRequest(http.MethodGet, "/api/v1/runtime/capacity", nil))
	out := decodeCapacity(t, rr)

	if out["enabled"] != true {
		t.Error("enabled = false with a controller wired")
	}
	held, _ := out["held"].([]any)
	if len(held) != 1 {
		t.Fatalf("held = %v, want one entry", out["held"])
	}
	entry, _ := held[0].(map[string]any)
	if entry["crew_id"] != "crew-a" {
		t.Errorf("crew_id = %v, want crew-a", entry["crew_id"])
	}
	if entry["reason"] != admission.ReasonHostMemory {
		t.Errorf("reason = %v, want %q", entry["reason"], admission.ReasonHostMemory)
	}
	if entry["detail"] == "" || entry["detail"] == nil {
		t.Error("detail missing; the operator cannot see the numbers")
	}
	if entry["waited_ms"] == nil {
		t.Error("waited_ms missing; a hold with no age is indistinguishable from a hang")
	}
	if out["host_signal_available"] != true {
		t.Error("host_signal_available = false with a readable signal")
	}
}

// macOS, and any host without /proc: the endpoint must say the memory gate is
// inactive rather than presenting a gate that is quietly doing nothing.
func TestRuntimeCapacity_ReportsAnUnreadableHostSignal(t *testing.T) {
	h := NewRuntimeCapacityHandler(fakeSnapshotter{snap: admission.Snapshot{
		HostSignalError: "host memory signal unavailable on this platform: /proc/meminfo: no such file or directory",
	}})
	rr := httptest.NewRecorder()
	h.Get(rr, httptest.NewRequest(http.MethodGet, "/api/v1/runtime/capacity", nil))
	out := decodeCapacity(t, rr)

	if out["host_signal_available"] == true {
		t.Error("host_signal_available = true on a host with no /proc/meminfo")
	}
	if out["host_signal_error"] == nil || out["host_signal_error"] == "" {
		t.Error("host_signal_error empty; the inactive gate is invisible")
	}
}

// "Not wired" and "wired with every leg off" are different situations.
func TestRuntimeCapacity_NoControllerReportsDisabledNotEmpty(t *testing.T) {
	h := NewRuntimeCapacityHandler(nil)
	rr := httptest.NewRecorder()
	h.Get(rr, httptest.NewRequest(http.MethodGet, "/api/v1/runtime/capacity", nil))
	out := decodeCapacity(t, rr)

	if out["enabled"] != false {
		t.Errorf("enabled = %v with no controller, want false", out["enabled"])
	}
	if out["host_signal_error"] == nil {
		t.Error("no explanation for a disabled gate")
	}
}

// Nothing held must serialise as [], not null — a client rendering "nothing
// is held" should not have to special-case the empty case.
func TestRuntimeCapacity_EmptyHeldIsAnArray(t *testing.T) {
	h := NewRuntimeCapacityHandler(fakeSnapshotter{snap: admission.Snapshot{HostSignalAvailable: true}})
	rr := httptest.NewRecorder()
	h.Get(rr, httptest.NewRequest(http.MethodGet, "/api/v1/runtime/capacity", nil))

	var raw struct {
		Held *[]admission.Hold `json:"held"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if raw.Held == nil {
		t.Fatalf("held serialised as null: %s", rr.Body.String())
	}
	if len(*raw.Held) != 0 {
		t.Fatalf("held = %v, want empty", *raw.Held)
	}
}
