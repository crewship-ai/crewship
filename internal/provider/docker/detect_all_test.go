package docker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// Detect returns on the first socket that answers, so /system/runtime could
// only ever carry one Docker-API runtime plus Apple — Colima, OrbStack, Podman
// and Rancher could never appear as separate entries however many of them were
// installed and running. DetectAll enumerates all of them.
//
// The tests below are built on the injectable core rather than on real sockets,
// for the same reason candidateSocketsFor has one: the candidate paths are
// hardcoded absolute paths under /var/run and $HOME, and a test that needed a
// daemon listening on each of them could only ever run on one machine.

// probeStub records what it was asked and answers from a table.
type probeStub struct {
	answers map[string]probeAnswer
	delay   time.Duration
	calls   atomic.Int32
	peak    atomic.Int32
	inFlt   atomic.Int32
}

type probeAnswer struct {
	version string
	podman  bool
	ok      bool
}

func (s *probeStub) probe(ctx context.Context, host string) (string, bool, bool) {
	s.calls.Add(1)
	n := s.inFlt.Add(1)
	for {
		peak := s.peak.Load()
		if n <= peak || s.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	defer s.inFlt.Add(-1)
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return "", false, false
		}
	}
	a := s.answers[host]
	return a.version, a.podman, a.ok
}

func allExist(string) bool { return true }

func noSymlinks(p string) (string, error) { return p, nil }

func candidatesFrom(specs ...[2]string) []socketCandidate {
	var out []socketCandidate
	for _, s := range specs {
		out = append(out, socketCandidate{path: s[0], host: "unix://" + s[0], runtime: s[1]})
	}
	return out
}

func TestDetectAll(t *testing.T) {
	t.Parallel()

	t.Run("every runtime that answers is listed, in candidate order", func(t *testing.T) {
		t.Parallel()
		cands := candidatesFrom(
			[2]string{"/var/run/docker.sock", "docker"},
			[2]string{"/home/u/.colima/default/docker.sock", "colima"},
			[2]string{"/run/user/1000/podman/podman.sock", "podman"},
		)
		stub := &probeStub{answers: map[string]probeAnswer{
			"unix:///var/run/docker.sock":                {version: "28.0.4", ok: true},
			"unix:///home/u/.colima/default/docker.sock": {version: "29.5.2", ok: true},
			"unix:///run/user/1000/podman/podman.sock":   {version: "6.0.2", podman: true, ok: true},
		}}
		got := detectAllFrom(context.Background(), "", cands, allExist, noSymlinks, stub.probe)

		if len(got) != 3 {
			t.Fatalf("got %d runtimes, want 3: %+v", len(got), got)
		}
		want := []DetectResult{
			{Runtime: "docker", Socket: "/var/run/docker.sock", Host: "unix:///var/run/docker.sock", Version: "28.0.4"},
			{Runtime: "colima", Socket: "/home/u/.colima/default/docker.sock", Host: "unix:///home/u/.colima/default/docker.sock", Version: "29.5.2"},
			{Runtime: "podman", Socket: "/run/user/1000/podman/podman.sock", Host: "unix:///run/user/1000/podman/podman.sock", Version: "6.0.2"},
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
			}
		}
	})

	t.Run("a socket that does not answer is not listed", func(t *testing.T) {
		t.Parallel()
		cands := candidatesFrom(
			[2]string{"/var/run/docker.sock", "docker"},
			[2]string{"/home/u/.rd/docker.sock", "rancher"},
		)
		// Exactly the Rancher-in-containerd-mode case measured on this
		// machine: the socket file is there, so it is stat-able, and it
		// accepts a connection and then closes it without answering.
		stub := &probeStub{answers: map[string]probeAnswer{
			"unix:///var/run/docker.sock": {version: "29.4.0", ok: true},
		}}
		got := detectAllFrom(context.Background(), "", cands, allExist, noSymlinks, stub.probe)

		if len(got) != 1 {
			t.Fatalf("got %d runtimes, want 1: %+v", len(got), got)
		}
		if got[0].Runtime != "docker" {
			t.Errorf("listed %q, want the one that answered", got[0].Runtime)
		}
	})

	t.Run("one daemon reachable by two paths is one runtime, named specifically", func(t *testing.T) {
		t.Parallel()
		// OrbStack's privileged helper points /var/run/docker.sock at its own.
		// Both candidates stat, both answer, and both are the same daemon —
		// listing it twice would show the operator two runtimes where there is
		// one, and one of the two names would be wrong.
		const orb = "/home/u/.orbstack/run/docker.sock"
		cands := candidatesFrom(
			[2]string{"/var/run/docker.sock", "docker"},
			[2]string{orb, "orbstack"},
		)
		resolve := func(p string) (string, error) {
			if p == "/var/run/docker.sock" {
				return orb, nil
			}
			return p, nil
		}
		stub := &probeStub{answers: map[string]probeAnswer{
			"unix:///var/run/docker.sock":              {version: "29.4.0", ok: true},
			"unix:///home/u/.orbstack/run/docker.sock": {version: "29.4.0", ok: true},
		}}
		got := detectAllFrom(context.Background(), "", cands, allExist, resolve, stub.probe)

		if len(got) != 1 {
			t.Fatalf("got %d runtimes, want 1 (same daemon twice): %+v", len(got), got)
		}
		if got[0].Runtime != "orbstack" {
			t.Errorf("runtime = %q, want orbstack — the generic path must not win the name", got[0].Runtime)
		}
		// The endpoint reported is the one Detect would have picked, so the two
		// surfaces cannot disagree about which socket is in use.
		if got[0].Socket != "/var/run/docker.sock" {
			t.Errorf("socket = %q, want /var/run/docker.sock (what Detect picks)", got[0].Socket)
		}
	})

	t.Run("podman is identified through a Docker-compatible socket", func(t *testing.T) {
		t.Parallel()
		cands := candidatesFrom([2]string{"/var/run/docker.sock", "docker"})
		stub := &probeStub{answers: map[string]probeAnswer{
			"unix:///var/run/docker.sock": {version: "4.9.3", podman: true, ok: true},
		}}
		got := detectAllFrom(context.Background(), "", cands, allExist, noSymlinks, stub.probe)
		if len(got) != 1 || got[0].Runtime != "podman" {
			t.Fatalf("got %+v, want a single podman entry", got)
		}
	})

	t.Run("DOCKER_HOST is listed first and is not duplicated", func(t *testing.T) {
		t.Parallel()
		const host = "unix:///var/run/docker.sock"
		cands := candidatesFrom(
			[2]string{"/var/run/docker.sock", "docker"},
			[2]string{"/home/u/.colima/default/docker.sock", "colima"},
		)
		stub := &probeStub{answers: map[string]probeAnswer{
			host: {version: "28.0.4", ok: true},
			"unix:///home/u/.colima/default/docker.sock": {version: "29.5.2", ok: true},
		}}
		got := detectAllFrom(context.Background(), host, cands, allExist, noSymlinks, stub.probe)

		if len(got) != 2 {
			t.Fatalf("got %d runtimes, want 2 (DOCKER_HOST + colima): %+v", len(got), got)
		}
		if got[0].Host != host {
			t.Errorf("first entry = %+v, want the DOCKER_HOST endpoint — it is the one in use", got[0])
		}
		if got[1].Runtime != "colima" {
			t.Errorf("second entry = %q, want colima", got[1].Runtime)
		}
	})

	// Found live, not by reading the code. With OrbStack owning
	// /var/run/docker.sock and DOCKER_HOST pointed at OrbStack's OWN socket,
	// the entry came back labelled "docker": the generic candidate resolves to
	// the same target, sits first in the list, and matched before the specific
	// one did. A DOCKER_HOST aimed squarely at a named runtime is the least
	// ambiguous input there is and it produced the vaguest possible answer.
	t.Run("DOCKER_HOST on a runtime's own socket is named for that runtime", func(t *testing.T) {
		t.Parallel()
		const orb = "/home/u/.orbstack/run/docker.sock"
		cands := candidatesFrom(
			[2]string{"/var/run/docker.sock", "docker"},
			[2]string{orb, "orbstack"},
		)
		resolve := func(p string) (string, error) {
			if p == "/var/run/docker.sock" {
				return orb, nil
			}
			return p, nil
		}
		host := "unix://" + orb
		stub := &probeStub{answers: map[string]probeAnswer{
			host:                          {version: "29.4.0", ok: true},
			"unix:///var/run/docker.sock": {version: "29.4.0", ok: true},
		}}
		got := detectAllFrom(context.Background(), host, cands, allExist, resolve, stub.probe)

		if len(got) != 1 {
			t.Fatalf("got %d runtimes, want 1: %+v", len(got), got)
		}
		if got[0].Runtime != "orbstack" {
			t.Errorf("runtime = %q, want orbstack — DOCKER_HOST names the runtime's own socket", got[0].Runtime)
		}
	})

	t.Run("a stat miss is never dialled", func(t *testing.T) {
		t.Parallel()
		cands := candidatesFrom(
			[2]string{"/var/run/docker.sock", "docker"},
			[2]string{"/home/u/.rd/docker.sock", "rancher"},
		)
		exists := func(p string) bool { return p == "/var/run/docker.sock" }
		stub := &probeStub{answers: map[string]probeAnswer{
			"unix:///var/run/docker.sock": {version: "28.0.4", ok: true},
		}}
		got := detectAllFrom(context.Background(), "", cands, exists, noSymlinks, stub.probe)
		if len(got) != 1 {
			t.Fatalf("got %d runtimes, want 1: %+v", len(got), got)
		}
		if n := stub.calls.Load(); n != 1 {
			t.Errorf("probed %d endpoints, want 1 — a path that does not exist must not cost a dial", n)
		}
	})
}

// The endpoint this feeds has a 5s context and there are nine candidates. At
// the 1.5s per-socket ping timeout, probing them one after another is up to
// ~13s — the handler would time out before the list was built, which is a
// worse answer than the one-runtime list it replaces.
//
// Asserted as elapsed wall time against a known per-probe cost rather than by
// counting goroutines: what has to be true is that the whole enumeration costs
// about one probe, and a structural assertion would keep passing if the
// concurrency were real but the results were gathered serially anyway.
func TestDetectAllProbesConcurrently(t *testing.T) {
	t.Parallel()

	const (
		perProbe = 250 * time.Millisecond
		n        = 8
	)
	var specs [][2]string
	answers := map[string]probeAnswer{}
	for i := 0; i < n; i++ {
		p := "/sock/" + string(rune('a'+i))
		specs = append(specs, [2]string{p, "docker"})
		answers["unix://"+p] = probeAnswer{version: "1.0", ok: true}
	}
	stub := &probeStub{answers: answers, delay: perProbe}

	start := time.Now()
	got := detectAllFrom(context.Background(), "", candidatesFrom(specs...), allExist, noSymlinks, stub.probe)
	elapsed := time.Since(start)

	if len(got) != n {
		t.Fatalf("got %d runtimes, want %d", len(got), n)
	}
	// Serial would be n*perProbe = 2s. Concurrent is one probe plus overhead.
	if budget := 3 * perProbe; elapsed > budget {
		t.Errorf("enumeration took %s, want under %s — %d probes at %s each were run serially",
			elapsed, budget, n, perProbe)
	}
	if peak := stub.peak.Load(); peak < 2 {
		t.Errorf("peak concurrent probes = %d, want at least 2", peak)
	}
}

// Detect and DetectAll must name the same endpoint the same way, or the API's
// top-level `runtime` field and its `runtimes[].in_use` entry contradict each
// other on screen — the first coming from the running provider, the second from
// the enumeration. Both go through labelForHost / resolveRuntimeLabel, so this
// pins the shared helper rather than either caller.
func TestLabelForHost(t *testing.T) {
	t.Parallel()

	const orb = "/home/u/.orbstack/run/docker.sock"
	cands := candidatesFrom(
		[2]string{"/var/run/docker.sock", "docker"},
		[2]string{orb, "orbstack"},
		[2]string{"/home/u/.rd/docker.sock", "rancher"},
	)
	resolve := func(p string) (string, error) {
		if p == "/var/run/docker.sock" {
			return orb, nil
		}
		return p, nil
	}

	for _, tc := range []struct {
		name, host, want string
	}{
		{"a runtime's own socket wins over the generic one that links to it", "unix://" + orb, "orbstack"},
		{"the generic socket takes the name of what it links to", "unix:///var/run/docker.sock", "orbstack"},
		{"an unrelated named socket keeps its own name", "unix:///home/u/.rd/docker.sock", "rancher"},
		{"an endpoint we know nothing about is plain docker", "unix:///opt/weird.sock", "docker"},
		{"a tcp endpoint is plain docker", "tcp://10.0.0.5:2375", "docker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := labelForHost(tc.host, cands, resolve); got != tc.want {
				t.Errorf("labelForHost(%q) = %q, want %q", tc.host, got, tc.want)
			}
		})
	}
}

// A cancelled context has to end the enumeration rather than run it to
// completion and discard the answer: the handler that cancels is the one whose
// deadline just expired, and it is still holding the connection.
func TestDetectAllHonoursContextCancellation(t *testing.T) {
	t.Parallel()

	specs := [][2]string{{"/sock/a", "docker"}, {"/sock/b", "docker"}}
	stub := &probeStub{
		answers: map[string]probeAnswer{
			"unix:///sock/a": {version: "1.0", ok: true},
			"unix:///sock/b": {version: "1.0", ok: true},
		},
		delay: 5 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	got := detectAllFrom(ctx, "", candidatesFrom(specs...), allExist, noSymlinks, stub.probe)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("enumeration ran for %s after the context expired at 100ms", elapsed)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want nothing — no probe completed before the deadline", got)
	}
}

// The API layer has to say which entry is in use, and it must take that from
// the running provider rather than re-deriving it. Comparing Socket strings
// will not do it: Detect stores DOCKER_HOST verbatim where DetectAll stores a
// plain path, and one daemon is reachable under several spellings.
func TestSameRuntimeEndpoint(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		a, b DetectResult
		want bool
	}{
		{
			"DOCKER_HOST spelling vs plain path",
			DetectResult{Socket: "unix:///var/run/docker.sock", Host: "unix:///var/run/docker.sock"},
			DetectResult{Socket: "/var/run/docker.sock", Host: "unix:///var/run/docker.sock"},
			true,
		},
		{
			"identical plain paths",
			DetectResult{Socket: "/var/run/docker.sock", Host: "unix:///var/run/docker.sock"},
			DetectResult{Socket: "/var/run/docker.sock", Host: "unix:///var/run/docker.sock"},
			true,
		},
		{
			"different sockets",
			DetectResult{Socket: "/var/run/docker.sock", Host: "unix:///var/run/docker.sock"},
			DetectResult{Socket: "/home/u/.rd/docker.sock", Host: "unix:///home/u/.rd/docker.sock"},
			false,
		},
		{
			"tcp endpoints compare on the host string",
			DetectResult{Socket: "tcp://10.0.0.5:2375", Host: "tcp://10.0.0.5:2375"},
			DetectResult{Socket: "tcp://10.0.0.5:2375", Host: "tcp://10.0.0.5:2375"},
			true,
		},
		{
			"different tcp endpoints",
			DetectResult{Socket: "tcp://10.0.0.5:2375", Host: "tcp://10.0.0.5:2375"},
			DetectResult{Socket: "tcp://10.0.0.6:2375", Host: "tcp://10.0.0.6:2375"},
			false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameRuntimeEndpoint(tc.a, tc.b); got != tc.want {
				t.Errorf("SameRuntimeEndpoint(%+v, %+v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}

	t.Run("a symlinked path matches its target", func(t *testing.T) {
		// The real reason this helper exists: OrbStack answering on
		// /var/run/docker.sock is the same daemon as ~/.orbstack/run/docker.sock,
		// and the running provider may hold either spelling.
		if !sameEndpointWith(
			DetectResult{Socket: "/var/run/docker.sock", Host: "unix:///var/run/docker.sock"},
			DetectResult{Socket: "/home/u/.orbstack/run/docker.sock", Host: "unix:///home/u/.orbstack/run/docker.sock"},
			func(p string) (string, error) {
				if p == "/var/run/docker.sock" {
					return "/home/u/.orbstack/run/docker.sock", nil
				}
				return p, nil
			},
		) {
			t.Error("a symlinked generic socket did not match its target")
		}
	})

	t.Run("an unresolvable path still matches itself", func(t *testing.T) {
		fail := func(string) (string, error) { return "", errors.New("nope") }
		a := DetectResult{Socket: "/var/run/docker.sock", Host: "unix:///var/run/docker.sock"}
		if !sameEndpointWith(a, a, fail) {
			t.Error("identical endpoints did not match when resolution failed")
		}
	})
}
