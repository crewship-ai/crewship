package server

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/provider"
)

// listeningPortScanInterval governs how often each crew container's
// /proc/net/tcp{,6} is read. 15s is a compromise: a fresh `python -m
// http.server` shows up within one cycle (Crow's Nest UX expectation),
// while a busy node_modules install isn't pummeled with extra exec
// invocations on top of the 5s stats poll.
const listeningPortScanInterval = 15 * time.Second

// listeningPortKey identifies a discovered listener uniquely so the
// diff between cycles emits opened/closed exactly once. Container is
// part of the key so two crews that both bind 8000 don't fight.
type listeningPortKey struct {
	ContainerID string
	Port        int
}

// listeningPortValue carries the scope needed to populate a journal
// entry — workspace + crew so Crow's Nest can filter, container for the
// payload's container_id field.
type listeningPortValue struct {
	WorkspaceID string
	CrewID      string
}

// runListeningPortScanner emits network.port_opened /
// network.port_closed journal entries by polling /proc/net/tcp{,6}
// inside each tracked crew container.
//
// Why this exists separately from runPortExposureScanner:
//
//   - port_exposures is an EXPLICIT list — agents call sidecar
//     /expose-port to publish a capability URL. That misses every
//     internal server an agent spins up (python -m http.server, node
//     dev, redis, postgres) which is exactly the noise Crow's Nest is
//     supposed to surface so the operator can SEE what crew runtimes
//     are doing.
//   - We can't read host /proc/net/tcp because the agent containers run
//     in their own netns. Polling /proc inside each container via exec
//     is cheap (single read, no fork tree) and uses the existing exec
//     plumbing.
//
// Trade-off: ports bound to 127.0.0.1 inside the container ARE
// visible (and surfaced) — that includes the sidecar on :9119. The
// alternative (filter to 0.0.0.0 only) hides legit dev-server activity
// like python -m http.server on a non-default bind. We prefer over-
// surfacing on a panel meant for visibility.
func runListeningPortScanner(
	ctx context.Context,
	ctr provider.ContainerProvider,
	stats *StatsCollector,
	j journal.Emitter,
	logger *slog.Logger,
) {
	if ctr == nil || stats == nil || j == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}

	prev := make(map[listeningPortKey]listeningPortValue)
	t := time.NewTicker(listeningPortScanInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		current := make(map[listeningPortKey]listeningPortValue)
		for _, tc := range stats.Tracked() {
			if tc.ContainerID == "" || tc.WorkspaceID == "" {
				continue
			}
			ports, err := scanContainerListeningPorts(ctx, ctr, tc.ContainerID, logger)
			if err != nil {
				// Container may have been removed mid-cycle, paused, or
				// /proc unreadable — debug-level, scanner moves on.
				logger.Debug("listening-port scan failed", "container_id", tc.ContainerID, "err", err)
				continue
			}
			for _, p := range ports {
				current[listeningPortKey{ContainerID: tc.ContainerID, Port: p}] = listeningPortValue{
					WorkspaceID: tc.WorkspaceID,
					CrewID:      tc.CrewID,
				}
			}
		}

		// Emit deltas only — keeps the journal proportional to changes
		// rather than to the cardinality of long-lived listeners.
		for k, v := range current {
			if _, was := prev[k]; !was {
				emitListeningPortEvent(ctx, j, journal.EntryNetworkPortOpen, k, v)
			}
		}
		for k, v := range prev {
			if _, still := current[k]; !still {
				emitListeningPortEvent(ctx, j, journal.EntryNetworkPortClose, k, v)
			}
		}
		prev = current
	}
}

// scanContainerListeningPorts reads /proc/net/tcp and /proc/net/tcp6
// inside the container, returning the deduplicated list of listening
// TCP ports (state == 0x0A). Ordering is undefined; the caller stores
// in a map.
//
// The exec returns the concatenated content of both files; one read,
// one parse — keeps cost ~constant per container regardless of port
// cardinality.
func scanContainerListeningPorts(
	ctx context.Context,
	ctr provider.ContainerProvider,
	containerID string,
	logger *slog.Logger,
) ([]int, error) {
	scanCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// Read both v4 and v6 tables. The shell loop is more resilient than a
	// straight `cat … 2>/dev/null` because it tolerates either file
	// being missing/unreadable (some container runtimes disable IPv6 and
	// /proc/net/tcp6 is absent — `cat` would still exit 0 there with
	// 2>/dev/null, but `[ -r ]` avoids relying on that quirk).
	res, err := ctr.Exec(scanCtx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", `for f in /proc/net/tcp /proc/net/tcp6; do [ -r "$f" ] && cat "$f"; done`},
	})
	if err != nil {
		return nil, err
	}
	defer func() {
		if res != nil && res.Reader != nil {
			_ = res.Reader.Close()
		}
	}()

	seen := make(map[int]struct{})
	scanner := bufio.NewScanner(res.Reader)
	// /proc/net/tcp lines are short; the default 64KB buffer is plenty.
	for scanner.Scan() {
		port, ok := parseProcNetTCPLine(scanner.Text())
		if !ok {
			continue
		}
		seen[port] = struct{}{}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, err
	}

	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	return out, nil
}

// parseProcNetTCPLine extracts the local port from one /proc/net/tcp
// line, returning false unless the line represents a LISTEN socket.
//
// Format (kernel docs, net/ipv4/tcp_ipv4.c):
//
//	sl  local_address rem_address   st  ...
//	0:  0100007F:9119 00000000:0000 0A   ...
//
// `local_address` is hex IP:PORT, `st` is the connection state. 0x0A
// is TCP_LISTEN. Anything else (ESTABLISHED, TIME_WAIT, …) is ignored
// because Crow's Nest cares about server-side bindings, not client
// connections (egress is covered by the sidecar HTTP proxy).
func parseProcNetTCPLine(line string) (int, bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return 0, false
	}
	// Skip the header row ("sl  local_address  rem_address  st  …").
	if fields[0] == "sl" {
		return 0, false
	}
	if fields[3] != "0A" {
		return 0, false
	}
	local := fields[1]
	colon := strings.LastIndex(local, ":")
	if colon == -1 || colon+1 >= len(local) {
		return 0, false
	}
	port, err := strconv.ParseUint(local[colon+1:], 16, 32)
	if err != nil || port == 0 || port > 65535 {
		return 0, false
	}
	return int(port), true
}

// emitListeningPortEvent persists one network.port_opened or
// network.port_closed entry. Keeps the payload key set in lock-step
// with port_exposure_scanner.go so the Crow's Nest Network panel can
// render both feeds with one renderer.
func emitListeningPortEvent(
	ctx context.Context,
	j journal.Emitter,
	kind journal.EntryType,
	key listeningPortKey,
	val listeningPortValue,
) {
	action := "opened"
	if kind == journal.EntryNetworkPortClose {
		action = "closed"
	}
	cidShort := key.ContainerID
	if len(cidShort) > 12 {
		cidShort = cidShort[:12]
	}
	_, _ = j.Emit(ctx, journal.Entry{
		WorkspaceID: val.WorkspaceID,
		CrewID:      val.CrewID,
		Type:        kind,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorSystem,
		Summary:     "port " + strconv.Itoa(key.Port) + " " + action + " in container " + cidShort,
		Payload: map[string]any{
			"port":         key.Port,
			"protocol":     "tcp",
			"container_id": key.ContainerID,
			"source":       "proc_scan",
		},
		Refs: map[string]any{"container_id": key.ContainerID},
	})
}
