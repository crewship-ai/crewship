package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/moby/moby/client"
)

// DetectAll returns every Docker-API runtime reachable on this host, not just
// the first one that answers.
//
// Detect stops at the first match, which is right for choosing what to run on
// and wrong for telling anyone what is available: with it, `/system/runtime`
// can carry at most one Docker-API entry plus Apple, so Colima, OrbStack,
// Podman and Rancher can never appear separately however many of them are
// installed and running — and `crewship system info`'s "Detected:" inventory
// can only ever list Apple.
//
// Three things this owes the caller, all of them learned the hard way:
//
//   - **One daemon, one entry.** A runtime that installed its "set up the
//     Docker socket for you" helper answers on both /var/run/docker.sock and
//     its own path. De-duplication is by what the socket resolves to, not by
//     candidate path, and the surviving entry keeps the specific label —
//     otherwise the operator sees two runtimes where there is one, and one of
//     the two names is wrong.
//   - **It must not be slow.** Nine candidates at socketPingTimeout each is up
//     to ~13s serially, against callers that hold an HTTP request open. The
//     probes run concurrently and the whole thing is bounded by ctx: a partial
//     list returned inside the deadline beats a complete one that misses it.
//   - **It does not say which is in use.** That answer belongs to the running
//     provider — DOCKER_HOST, a pinned provider or --no-docker all make the
//     live choice differ from anything re-derived here. Callers should compare
//     against Provider.Detected() using SameRuntimeEndpoint, which knows that
//     one daemon has several spellings.
//
// Entries come back in candidate order, with the DOCKER_HOST endpoint first
// when one is set, so the natural reading order matches Detect's preference.
func DetectAll(ctx context.Context) []DetectResult {
	return detectAllFrom(ctx, os.Getenv("DOCKER_HOST"), candidateSockets(), pathExists, filepath.EvalSymlinks, pingEndpoint)
}

// runtimeProbe reports the server version at host, whether it is Podman
// underneath, and whether it answered at all.
type runtimeProbe func(ctx context.Context, host string) (version string, podman bool, ok bool)

// pingEndpoint is the real probe: the same ping-then-ServerVersion pair Detect
// performs, with the same per-socket timeout so one unresponsive daemon cannot
// hold the whole enumeration.
func pingEndpoint(ctx context.Context, host string) (string, bool, bool) {
	cli, err := client.New(client.WithHost(host))
	if err != nil {
		return "", false, false
	}
	defer cli.Close()

	pingCtx, cancel := context.WithTimeout(ctx, socketPingTimeout)
	defer cancel()
	info, pingErr := cli.Ping(pingCtx, client.PingOptions{})
	if pingErr != nil {
		return "", false, false
	}
	podman := strings.Contains(info.APIVersion, "libpod")

	sv, _ := cli.ServerVersion(pingCtx, client.ServerVersionOptions{})
	version := sv.Version
	// Podman masquerades as Docker -- check server components.
	for _, comp := range sv.Components {
		if strings.EqualFold(comp.Name, "Podman Engine") {
			podman = true
			version = comp.Version
		}
	}
	return version, podman, true
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// detectAllFrom is the injectable core of DetectAll. The candidate paths are
// hardcoded absolute locations under /var/run and $HOME, so a test that needed
// a daemon listening on each of them could only ever run on one machine.
func detectAllFrom(
	ctx context.Context,
	dockerHost string,
	candidates []socketCandidate,
	exists func(string) bool,
	resolve func(string) (string, error),
	probe runtimeProbe,
) []DetectResult {
	// The endpoints to probe, in the order they should be reported. DOCKER_HOST
	// leads because it is what Detect would pick.
	type endpoint struct {
		socket  string
		host    string
		runtime string
		key     string // resolved identity, for de-duplication
	}
	var endpoints []endpoint
	seen := map[string]bool{}

	add := func(socket, host, runtime string) {
		key := endpointKey(socket, host, resolve)
		if seen[key] {
			return
		}
		seen[key] = true
		endpoints = append(endpoints, endpoint{socket: socket, host: host, runtime: runtime, key: key})
	}

	if dockerHost != "" {
		// Detect labels a DOCKER_HOST endpoint "docker" until the server says
		// otherwise, and so does this — but run it through the same symlink
		// resolution first, so pointing DOCKER_HOST at a socket a specific
		// runtime owns names that runtime rather than the generic one.
		add(dockerHost, dockerHost, labelForHost(dockerHost, candidates, resolve))
	}
	for _, c := range candidates {
		if !exists(c.path) {
			continue
		}
		add(c.path, c.host, resolveRuntimeLabel(c, candidates, resolve))
	}

	results := make([]DetectResult, len(endpoints))
	found := make([]bool, len(endpoints))
	var wg sync.WaitGroup
	for i, e := range endpoints {
		wg.Add(1)
		go func(i int, e endpoint) {
			defer wg.Done()
			version, podman, ok := probe(ctx, e.host)
			if !ok {
				return
			}
			rt := e.runtime
			if podman {
				rt = "podman"
			}
			results[i] = DetectResult{Runtime: rt, Socket: e.socket, Host: e.host, Version: version}
			found[i] = true
		}(i, e)
	}
	wg.Wait()

	out := make([]DetectResult, 0, len(endpoints))
	for i := range results {
		if found[i] {
			out = append(out, results[i])
		}
	}
	return out
}

// labelForHost names the runtime behind an arbitrary host URL by matching it
// against the candidate list, so a DOCKER_HOST aimed at a runtime's own socket
// is named for that runtime rather than reported generically.
func labelForHost(host string, candidates []socketCandidate, resolve func(string) (string, error)) string {
	target := unixSocketPath(host)
	if target == "" {
		return "docker"
	}
	// Resolve the endpoint, then look for the candidate that IS the resolved
	// socket. Matching on the unresolved spelling first would answer "docker"
	// for a DOCKER_HOST aimed squarely at a named runtime's own socket — the
	// generic /var/run/docker.sock is a candidate too, it resolves to the same
	// place, and it sorts first. Found live, with OrbStack owning the generic
	// path: DOCKER_HOST=unix://…/.orbstack/run/docker.sock came back "docker".
	//
	// The unresolved spelling is only the fallback, for a path that cannot be
	// resolved at all.
	resolved := target
	if r, err := resolve(target); err == nil {
		resolved = r
	}
	for _, c := range candidates {
		if c.path == resolved {
			return c.runtime
		}
	}
	for _, c := range candidates {
		if c.path == target {
			return c.runtime
		}
	}
	return "docker"
}

// endpointKey is the identity two endpoints are compared on: the resolved
// filesystem path for a unix socket, and the host string verbatim for anything
// else (tcp, npipe), where there is no path to resolve.
func endpointKey(socket, host string, resolve func(string) (string, error)) string {
	p := unixSocketPath(socket)
	if p == "" {
		p = unixSocketPath(host)
	}
	if p == "" {
		return host
	}
	if resolved, err := resolve(p); err == nil {
		return resolved
	}
	return p
}

// unixSocketPath extracts the filesystem path from a unix endpoint, in either
// of the two spellings that reach here: Detect stores DOCKER_HOST verbatim
// ("unix:///var/run/docker.sock") while candidates carry a plain path.
// Returns "" for anything that is not a unix socket.
func unixSocketPath(s string) string {
	if after, ok := strings.CutPrefix(s, "unix://"); ok {
		return after
	}
	if strings.HasPrefix(s, "/") {
		return s
	}
	return ""
}

// SameRuntimeEndpoint reports whether two results name the same daemon.
//
// Comparing Socket strings will not do it. Detect stores DOCKER_HOST verbatim
// where DetectAll stores a plain path, and a runtime that owns
// /var/run/docker.sock is reachable under two paths that resolve to one socket.
// This is what a caller needs to mark the in-use entry from the running
// provider's own Detected() rather than re-deriving the choice — the two can
// legitimately differ.
func SameRuntimeEndpoint(a, b DetectResult) bool {
	return sameEndpointWith(a, b, filepath.EvalSymlinks)
}

func sameEndpointWith(a, b DetectResult, resolve func(string) (string, error)) bool {
	return endpointKey(a.Socket, a.Host, resolve) == endpointKey(b.Socket, b.Host, resolve)
}
