package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// A held run has to be distinguishable from a slow one at a glance. That means
// the summary line says HELD and names the count — not "0 problems" with the
// detail hidden behind another command.
func TestCapacitySummary_HeldStartsAreCalledOut(t *testing.T) {
	rc := runtimeCapacity{
		Enabled:             true,
		InFlightStarts:      4,
		HostSignalAvailable: true,
		Host:                runtimeCapacityHost{AvailableMB: 900, TotalMB: 16000, SomeStallPct: 2.5},
		Held: []runtimeCapacityHold{
			{CrewID: "c1", CrewSlug: "alpha", Reason: "host_memory", Detail: "host has 900 MiB available, 3072 MiB needed", WaitedMs: 42000},
			{CrewID: "c2", CrewSlug: "beta", Reason: "concurrency", Detail: "4 container starts already in flight, limit 4", WaitedMs: 1200},
		},
	}
	got := capacitySummary(rc)
	if !strings.Contains(strings.ToLower(got), "held") {
		t.Errorf("summary %q never says anything is held", got)
	}
	// The COUNT has to be the held count, not a stray digit from the memory
	// or pressure figures further along the line.
	if !strings.Contains(got, "2 container start(s) held") {
		t.Errorf("summary %q does not report 2 held starts", got)
	}
	if strings.Contains(got, "0 held") {
		t.Errorf("summary %q reports nothing held while 2 crews are waiting", got)
	}
}

func TestCapacityHeldLines_NameTheCrewReasonAndWait(t *testing.T) {
	rc := runtimeCapacity{
		Enabled: true,
		Held: []runtimeCapacityHold{{
			CrewID: "c1", CrewSlug: "alpha", Reason: "host_memory",
			Detail: "host has 900 MiB available, 3072 MiB needed", WaitedMs: 42000,
		}},
	}
	lines := capacityHeldLines(rc)
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want one", lines)
	}
	for _, want := range []string{"alpha", "host_memory", "42", "900 MiB"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("held line %q missing %q", lines[0], want)
		}
	}
}

// Nothing held is a specific, reassuring statement, not an empty section that
// reads like a failed fetch.
func TestCapacitySummary_NothingHeldSaysSo(t *testing.T) {
	got := capacitySummary(runtimeCapacity{Enabled: true, HostSignalAvailable: true,
		Host: runtimeCapacityHost{AvailableMB: 12000, TotalMB: 16000}})
	if strings.Contains(strings.ToLower(got), "held") && !strings.Contains(got, "0") {
		t.Errorf("summary %q is ambiguous about nothing being held", got)
	}
	if !strings.Contains(got, "12000") && !strings.Contains(got, "12,000") {
		t.Errorf("summary %q does not report available host memory", got)
	}
}

// macOS. The operator has to be able to see that the host-memory gate is
// inactive, or they will read "nothing held" as "the gate is watching".
func TestCapacitySummary_UnreadableHostSignalIsStated(t *testing.T) {
	got := capacitySummary(runtimeCapacity{
		Enabled:         true,
		HostSignalError: "host memory signal unavailable on this platform: /proc/meminfo: no such file or directory",
	})
	low := strings.ToLower(got)
	if !strings.Contains(low, "unavailable") && !strings.Contains(low, "inactive") {
		t.Errorf("summary %q does not say the host-memory gate is inactive", got)
	}
}

func TestCapacitySummary_DisabledIsDistinctFromIdle(t *testing.T) {
	got := strings.ToLower(capacitySummary(runtimeCapacity{Enabled: false}))
	if !strings.Contains(got, "not configured") && !strings.Contains(got, "disabled") {
		t.Errorf("summary %q does not distinguish an unwired gate from an idle one", got)
	}
}

// `crewship now` must surface a capacity hold. This is the requirement that a
// held run is distinguishable from a slow one on the screen an operator
// actually looks at when something is not happening.
func TestRenderNow_ShowsHeldContainerStarts(t *testing.T) {
	covSetupCli5(t)
	flagFormat = "table"

	capacity := runtimeCapacity{
		Enabled: true,
		Held: []runtimeCapacityHold{{
			CrewID: "c1", CrewSlug: "alpha", Reason: "host_memory",
			Detail: "host has 900 MiB available, 3072 MiB needed", WaitedMs: 42000,
		}},
	}
	out := covCaptureStdoutCli5(t, func() {
		if err := renderNow(nil, nil, nil, capacity, nil); err != nil {
			t.Errorf("renderNow: %v", err)
		}
	})
	for _, want := range []string{"Held for capacity", "alpha", "host_memory", "not hung"} {
		if !strings.Contains(out, want) {
			t.Errorf("`crewship now` output missing %q; got:\n%s", want, out)
		}
	}
}

// And it must stay quiet when nothing is held — a section that is always
// there teaches people to stop reading it.
func TestRenderNow_NothingHeldPrintsNoCapacitySection(t *testing.T) {
	covSetupCli5(t)
	flagFormat = "table"

	out := covCaptureStdoutCli5(t, func() {
		if err := renderNow(nil, nil, nil, runtimeCapacity{Enabled: true}, nil); err != nil {
			t.Errorf("renderNow: %v", err)
		}
	})
	if strings.Contains(out, "Held for capacity") {
		t.Errorf("capacity section printed with nothing held:\n%s", out)
	}
}

// The capacity fetch is additive on a composite screen, so its failures must
// not become permanent furniture. A server older than the endpoint answers 404
// forever; a "[partial] capacity: 404" line under every `crewship now` is how
// people learn to stop reading the partial-error lines that do matter.
func TestCapacityFetchNoise_SuppressesTheExpectedFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"success", nil, ""},
		{"server predates the endpoint", &cli.APIError{Status: 404, Detail: "not found"}, ""},
		{"session expired (reported by the caller already)", &cli.APIError{Status: 401, Detail: "session_invalid"}, ""},
		{"a real failure", &cli.APIError{Status: 500, Detail: "boom"}, "capacity: "},
		{"a transport failure", errors.New("dial tcp: connection refused"), "capacity: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := capacityFetchNoise(tc.err)
			if tc.want == "" {
				if got != "" {
					t.Errorf("reported %q, want silence", got)
				}
				return
			}
			if !strings.HasPrefix(got, tc.want) {
				t.Errorf("reported %q, want a message starting %q", got, tc.want)
			}
		})
	}
}
