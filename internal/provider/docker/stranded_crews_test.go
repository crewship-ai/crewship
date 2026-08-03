package docker

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func crewSummary(name, crewID string, state container.ContainerState) container.Summary {
	return container.Summary{
		ID:     "id-" + name,
		Names:  []string{"/" + name},
		State:  state,
		Labels: map[string]string{"managed-by": "crewship", "crewship.kind": "crew", "crewship.crew-id": crewID},
	}
}

// strandedFixture is the exact shape of #1704: the provider is on Colima, and
// the crew container it started earlier is still up on OrbStack under the same
// name.
func strandedFixture(t *testing.T) (*Provider, []DetectResult, func(context.Context, string) ([]container.Summary, error)) {
	t.Helper()
	p := &Provider{
		logger:   quietLogger(),
		cfg:      Config{ContainerPrefix: "crewship-1"},
		detected: DetectResult{Runtime: "colima", Socket: "/Users/x/.colima/default/docker.sock", Host: "unix:///Users/x/.colima/default/docker.sock"},
	}
	all := []DetectResult{
		{Runtime: "colima", Socket: "/Users/x/.colima/default/docker.sock", Host: "unix:///Users/x/.colima/default/docker.sock"},
		{Runtime: "orbstack", Socket: "/Users/x/.orbstack/run/docker.sock", Host: "unix:///Users/x/.orbstack/run/docker.sock"},
	}
	list := func(_ context.Context, host string) ([]container.Summary, error) {
		switch {
		case strings.Contains(host, "orbstack"):
			return []container.Summary{
				crewSummary("crewship-1-team-engineering-cmnp5dbii0003", "cmnp5dbii0003", container.StateRunning),
				crewSummary("crewship-2-team-quality-otherinstance", "otherinstance", container.StateRunning),
				{ID: "unrelated", Names: []string{"/redis"}, State: container.StateRunning},
			}, nil
		case strings.Contains(host, "colima"):
			t.Errorf("the sweep listed containers on the daemon it is already using (%s) — every crew there is live, not stranded", host)
			return nil, nil
		}
		return nil, nil
	}
	return p, all, list
}

// TestFindStrandedCrewsSeesTheOrphanOnTheOtherDaemon is the finding #1704 says
// the product cannot make: a crew container of ours, running, on a daemon we no
// longer talk to.
func TestFindStrandedCrewsSeesTheOrphanOnTheOtherDaemon(t *testing.T) {
	p, all, list := strandedFixture(t)

	got := p.findStrandedCrewsIn(context.Background(), all, list)

	if len(got) != 1 {
		t.Fatalf("found %d stranded crews %+v, want exactly the one on orbstack (crewship-2's container and a plain redis are not ours)", len(got), got)
	}
	s := got[0]
	if s.Name != "crewship-1-team-engineering-cmnp5dbii0003" {
		t.Errorf("stranded container name = %q", s.Name)
	}
	if s.Runtime != "orbstack" {
		t.Errorf("stranded runtime = %q, want orbstack — the operator has to be told WHICH daemon still holds it", s.Runtime)
	}
	if s.CrewID != "cmnp5dbii0003" {
		t.Errorf("stranded crew id = %q, want cmnp5dbii0003", s.CrewID)
	}
	if !s.Running {
		t.Error("stranded container reported as not running; a running one is the whole hazard")
	}
}

// TestFindStrandedCrewsIgnoresAnotherInstancesContainers: ContainerPrefix is
// the multi-instance isolation key, so crewship-2's crews are not crewship-1's
// to stop — dev slot 2 losing its containers to dev slot 1's boot would be a
// worse bug than the one being fixed.
func TestFindStrandedCrewsIgnoresAnotherInstancesContainers(t *testing.T) {
	p, all, list := strandedFixture(t)
	for _, s := range p.findStrandedCrewsIn(context.Background(), all, list) {
		if strings.HasPrefix(s.Name, "crewship-2-") {
			t.Errorf("swept up another instance's container %q", s.Name)
		}
	}
}

// TestFindStrandedCrewsSurvivesAnUnlistableDaemon: a daemon that pings but
// will not list must not abort the sweep across the others.
func TestFindStrandedCrewsSurvivesAnUnlistableDaemon(t *testing.T) {
	p, all, good := strandedFixture(t)
	all = append([]DetectResult{{Runtime: "rancher", Socket: "/Users/x/.rd/docker.sock", Host: "unix:///Users/x/.rd/docker.sock"}}, all...)
	list := func(ctx context.Context, host string) ([]container.Summary, error) {
		if strings.Contains(host, ".rd/") {
			return nil, errors.New("permission denied")
		}
		return good(ctx, host)
	}
	if got := p.findStrandedCrewsIn(context.Background(), all, list); len(got) != 1 {
		t.Errorf("found %d stranded crews, want 1 — one unlistable daemon must not blind the sweep to the others", len(got))
	}
}

// TestActOnStrandedCrewsStopsTheRunningOrphan is the half that matters: the
// hazard is the live write access to /crew, and only stopping the container
// takes it away.
func TestActOnStrandedCrewsStopsTheRunningOrphan(t *testing.T) {
	p := &Provider{logger: quietLogger(), detected: DetectResult{Runtime: "colima"}}
	running := StrandedCrew{Name: "crewship-1-team-engineering-x", ID: "id-1", Runtime: "orbstack", Running: true}
	alreadyStopped := StrandedCrew{Name: "crewship-1-team-quality-y", ID: "id-2", Runtime: "orbstack", Running: false}

	var stopped []string
	p.actOnStrandedCrews(context.Background(), []StrandedCrew{running, alreadyStopped}, "stop",
		func(_ context.Context, s StrandedCrew) error {
			stopped = append(stopped, s.ID)
			return nil
		})

	if len(stopped) != 1 || stopped[0] != "id-1" {
		t.Errorf("stopped %v, want exactly the running orphan id-1 — a stopped container has no write access to take away", stopped)
	}
}

// TestActOnStrandedCrewsHonoursReportPolicy: an operator mid-investigation can
// ask for the orphan to be left alone, and must be told it still has write
// access.
func TestActOnStrandedCrewsHonoursReportPolicy(t *testing.T) {
	var logged strings.Builder
	p := &Provider{
		logger:   slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})),
		detected: DetectResult{Runtime: "colima"},
	}
	var stopped int
	p.actOnStrandedCrews(context.Background(),
		[]StrandedCrew{{Name: "crewship-1-team-engineering-x", ID: "id-1", Runtime: "orbstack", Host: "unix:///o.sock", Running: true}},
		"report",
		func(context.Context, StrandedCrew) error { stopped++; return nil })

	if stopped != 0 {
		t.Errorf("stopped %d containers under the report policy, want 0", stopped)
	}
	out := logged.String()
	if !strings.Contains(out, "docker stop crewship-1-team-engineering-x") {
		t.Errorf("report-only log does not tell the operator how to stop it:\n%s", out)
	}
}

// TestStrandedLogNamesBothRuntimes: the report is useless without saying which
// daemon still has it and which one Crewship moved to.
func TestStrandedLogNamesBothRuntimes(t *testing.T) {
	var logged strings.Builder
	p := &Provider{
		logger:   slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})),
		detected: DetectResult{Runtime: "colima", Socket: "/Users/x/.colima/default/docker.sock"},
	}
	p.actOnStrandedCrews(context.Background(),
		[]StrandedCrew{{Name: "crewship-1-team-engineering-x", ID: "id-1", Runtime: "orbstack", Socket: "/Users/x/.orbstack/run/docker.sock", Host: "unix:///o.sock", CrewID: "cmnp", Running: true}},
		"stop",
		func(context.Context, StrandedCrew) error { return nil })

	out := logged.String()
	for _, want := range []string{"orbstack", "colima", "crewship-1-team-engineering-x", "cmnp"} {
		if !strings.Contains(out, want) {
			t.Errorf("stranded-crew log is missing %q:\n%s", want, out)
		}
	}
}

func TestStrandedCrewPolicyDefaultsToStopping(t *testing.T) {
	t.Setenv("CREWSHIP_STRANDED_CREWS", "")
	if got := strandedCrewPolicy(); got != "stop" {
		t.Errorf("policy = %q with the env unset, want stop — the default has to remove the write access", got)
	}
	t.Setenv("CREWSHIP_STRANDED_CREWS", "REPORT")
	if got := strandedCrewPolicy(); got != "report" {
		t.Errorf("policy = %q for CREWSHIP_STRANDED_CREWS=REPORT, want report", got)
	}
}

func TestPrimaryContainerName(t *testing.T) {
	if got := primaryContainerName([]string{"/crewship-1-team-a-b", "/net-alias"}); got != "crewship-1-team-a-b" {
		t.Errorf("primaryContainerName = %q", got)
	}
	if got := primaryContainerName(nil); got != "" {
		t.Errorf("primaryContainerName(nil) = %q, want empty", got)
	}
}
