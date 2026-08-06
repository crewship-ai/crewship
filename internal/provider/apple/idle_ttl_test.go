package apple

// CrewConfig.TTLHours — idle auto-stop — on this provider.
//
// The mechanism is not provider-local on ANY provider. `container stop --time`
// has no idle trigger and neither does docker's daemon; what actually stops an
// idle crew is the orchestrator's reaper (internal/orchestrator/
// orchestrator_lifecycle.go checkTTLs), which is provider-agnostic: it tracks
// last activity per crew, refuses to stop a container with an occupant, and
// calls ContainerProvider.StopCrewRuntime. That is why the docker provider's
// capability report says nothing about TTLHours — it does not implement it
// either.
//
// The reaper reaches a crew through two doors. The live one — a crew this
// process started or ran — is already open here: it is plain
// NoteCrewActivity/RetainCrewContainer bookkeeping in the pipeline, and the
// stop lands on this provider's StopCrewRuntime. The boot one was not:
// Server.rehydrateContainers type-asserts the provider to
// provider.CrewContainerLookup and *returns immediately* when the assertion
// fails ("e.g. apple containers"), so a container that survived a crewshipd
// restart was never handed back to the reaper and was never stopped again —
// for as long as the host stayed up. That is #1662's bug, still live on this
// provider only.
//
// These tests pin the two provider-side facts that door needs:
//
//  1. FindCrewContainer — "is there a container for this crew, and is it
//     running", so rehydration runs at all here.
//  2. ContainerStatus.Uptime — the container's own start time, which
//     seedCrewReaperClock parses to date the idle clock. Seeding it with `now`
//     instead hands every restart a fresh full TTL window, so on a host that
//     redeploys more often than the TTL nothing is ever reaped: the bug would
//     survive its own fix. #1662 spells this out for docker, where
//     ContainerStatus.Uptime carries inspect.State.StartedAt; this provider
//     left the field empty, which is the same defect one layer down.
//
// What is deliberately NOT here: a provider-owned idle timer. It would need
// its own definition of "occupied", and it cannot see the two occupants the
// orchestrator checks before every stop — a live port exposure (out of
// process) and a detached tmux session (outlives the run and the daemon that
// started it). A second, weaker definition of idle that disagreed with the
// first would stop containers the first refuses to stop. "No exec in flight"
// is not available as a substitute either: the crew's egress sidecar is
// started through Exec and stays running for the container's whole life, so
// every fenced container would read as permanently busy.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// listBody is a `list` branch for the fake CLI, answering with the payload
// shape `container list --all --format json` really produces: the lifecycle
// state nested under `status`, the name under `configuration.id`. Declaring
// `status` a string is what made the whole list fail to decode in #1779, so
// the nesting here is load-bearing rather than decorative.
func listBody(entries string) string {
	return `
case "$1" in
  list) printf '%s' '` + entries + `'; exit 0;;
esac
exit 0
`
}

// Rehydration is gated on this type assertion and silently skips the whole
// pass when it fails (server_lifecycle.go:799). Without the interface no
// Apple container discovered at boot is ever handed to the reaper, so the
// TTL is unreachable through the only door a surviving container can use.
func TestProvider_IsACrewContainerLookup(t *testing.T) {
	var p provider.ContainerProvider = newTestProvider(Config{})
	if _, ok := p.(provider.CrewContainerLookup); !ok {
		t.Fatal("Provider does not implement provider.CrewContainerLookup, so Server.rehydrateContainers skips this provider entirely and a container that survives a restart is never re-registered with the idle reaper")
	}
}

func TestFindCrewContainer_ReportsARunningCrewContainer(t *testing.T) {
	p := newTestProvider(Config{})
	name := p.CrewContainerName("crew1", "eng")
	installFakeContainer(t, listBody(`[{"status":{"state":"running"},"configuration":{"id":"`+name+`"}}]`))

	id, running, err := p.FindCrewContainer(context.Background(), "crew1", "eng")
	if err != nil {
		t.Fatalf("FindCrewContainer: %v", err)
	}
	if id != name {
		t.Errorf("container id = %q, want %q", id, name)
	}
	if !running {
		t.Error("running = false for a container the runtime reports as running")
	}
}

// A stopped container is present-but-not-running. Rehydration must be able to
// tell the difference: it registers stats and seeds the reaper only for the
// running ones, and auto-starting a stopped crew at boot would be a surprise.
func TestFindCrewContainer_StoppedContainerIsFoundButNotRunning(t *testing.T) {
	p := newTestProvider(Config{})
	name := p.CrewContainerName("crew1", "eng")
	installFakeContainer(t, listBody(`[{"status":{"state":"stopped"},"configuration":{"id":"`+name+`"}}]`))

	id, running, err := p.FindCrewContainer(context.Background(), "crew1", "eng")
	if err != nil {
		t.Fatalf("FindCrewContainer: %v", err)
	}
	if id != name {
		t.Errorf("container id = %q, want %q — a stopped container still exists", id, name)
	}
	if running {
		t.Error("running = true for a stopped container")
	}
}

// The interface's contract is explicit that "no container" is ("", false, nil)
// and that the error path is reserved for transport failures. Returning an
// error for absence would make rehydration log a warning per crew that has
// never started, and admin_reap_orphan_containers.go `continue`s on error —
// so an absent container reported as an error is indistinguishable there from
// a runtime it could not reach.
func TestFindCrewContainer_MissingIsNotAnError(t *testing.T) {
	installFakeContainer(t, listBody(`[{"status":{"state":"running"},"configuration":{"id":"someone-elses-container"}}]`))
	p := newTestProvider(Config{})

	id, running, err := p.FindCrewContainer(context.Background(), "crew1", "eng")
	if err != nil {
		t.Fatalf("a crew with no container must not be an error: %v", err)
	}
	if id != "" || running {
		t.Errorf("FindCrewContainer = (%q, %v), want (\"\", false)", id, running)
	}
}

// A runtime we could not talk to is NOT "no container". Swallowing it would
// tell the orphan reaper that a crew has no container, and the caller's
// fail-safe (skip this crew) depends on hearing the failure.
func TestFindCrewContainer_RuntimeFailureIsAnError(t *testing.T) {
	installFakeContainer(t, `echo 'cannot connect to container service' >&2; exit 1`)
	p := newTestProvider(Config{})

	id, running, err := p.FindCrewContainer(context.Background(), "crew1", "eng")
	if err == nil {
		t.Fatalf("a runtime failure returned (%q, %v) with no error", id, running)
	}
	if id != "" || running {
		t.Errorf("FindCrewContainer = (%q, %v) alongside an error, want (\"\", false)", id, running)
	}
}

// The idle clock's floor. seedCrewReaperClock reads ContainerStatus.Uptime and
// parses it as RFC3339Nano; an empty or unparseable value falls back to
// time.Now(), which is precisely the "every restart grants a fresh TTL window"
// behaviour #1662 removed. The value asserted here comes from testdata/, a
// verbatim `container inspect` capture, so the field name (`status.startedDate`)
// is observed rather than imagined — this package has already shipped two
// structs written from memory that failed as a silent absence.
func TestContainerStatus_UptimeCarriesTheContainerStartTime(t *testing.T) {
	installFakeContainerServing(t, readFixture(t, fixtureCrewContainer))
	p := newTestProvider(Config{})

	st, err := p.ContainerStatus(context.Background(), "crewship-1-team-quality-cmsg6n9zj000767b5558f")
	if err != nil {
		t.Fatalf("ContainerStatus: %v", err)
	}
	if st.State != "running" {
		t.Errorf("State = %q, want running", st.State)
	}
	if st.Uptime == "" {
		t.Fatal("Uptime is empty, so seedCrewReaperClock dates the idle clock from now — every crewshipd restart then hands this container a fresh full TTL window and it is never reaped")
	}

	// Exactly the parse the server performs, against exactly the value the
	// fixture recorded.
	got, perr := time.Parse(time.RFC3339Nano, st.Uptime) // tsformat:allow: read-only parse of a provider timestamp; never compared or ordered in SQL
	if perr != nil {
		t.Fatalf("Uptime %q is not RFC3339Nano, so the server's parse falls back to now: %v", st.Uptime, perr)
	}
	want := startedDateFromFixture(t, fixtureCrewContainer)
	if !got.Equal(want) {
		t.Errorf("Uptime = %s, want the container's own start %s", got, want)
	}
}

// A payload with no start time must leave Uptime empty rather than invent one.
// The server's fallback (now) is a bounded, documented degradation; a fabricated
// timestamp is not.
func TestContainerStatus_NoStartDateLeavesUptimeEmpty(t *testing.T) {
	installFakeContainerServing(t, `[{"status":{"state":"running"},"configuration":{"id":"x"}}]`)
	p := newTestProvider(Config{})

	st, err := p.ContainerStatus(context.Background(), "x")
	if err != nil {
		t.Fatalf("ContainerStatus: %v", err)
	}
	if st.Uptime != "" {
		t.Errorf("Uptime = %q for a payload that recorded no start time, want \"\"", st.Uptime)
	}
}

// The report has to follow the behaviour. While the two facts above were
// missing, "no idle auto-stop is scheduled; the container runs until it is
// stopped explicitly" was a true statement about a container that survived a
// restart. It is not one now, and a stale entry is not a harmless
// over-report: the capability report feeds the crew read paths and the agent's
// own system prompt, so an entry that outlives its gap instructs every agent
// on this provider to plan around a limitation that is gone.
func TestUnsupportedCrewConfig_TTLHoursIsNoLongerReportedAsDropped(t *testing.T) {
	p := newTestProvider(Config{})
	s := p.UnsupportedCrewConfig(provider.CrewConfig{ID: "crew-1", Slug: "ops", TTLHours: 4})

	if drop, ok := s.Drop("TTLHours"); ok {
		t.Errorf("TTLHours still reported as dropped (%q) — the orchestrator's reaper stops idle containers on this provider, and it now reaches them at boot too (FindCrewContainer) with the container's own start time (ContainerStatus.Uptime)", drop.Detail)
	}
	if !s.Empty() {
		t.Errorf("a crew whose only non-default field is TTLHours must now report nothing, got %+v", s)
	}
}

// startedDateFromFixture reads status.startedDate straight out of the captured
// payload, so the expectation above is the file's own value rather than a
// constant this test could drift away from.
func startedDateFromFixture(t *testing.T, name string) time.Time {
	t.Helper()
	var raw []struct {
		Status struct {
			StartedDate string `json:"startedDate"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(readFixture(t, name)), &raw); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	if len(raw) == 0 || raw[0].Status.StartedDate == "" {
		t.Fatalf("fixture %s records no status.startedDate", name)
	}
	ts, err := time.Parse(time.RFC3339Nano, raw[0].Status.StartedDate) // tsformat:allow: test fixture parse, never reaches SQL
	if err != nil {
		t.Fatalf("fixture start date %q: %v", raw[0].Status.StartedDate, err)
	}
	return ts
}
