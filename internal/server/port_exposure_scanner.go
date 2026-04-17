package server

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// portExposureScanInterval is how often the scanner diffs the port_exposures
// table. 10s is a pragmatic trade-off: Crow's Nest users expect port events
// to appear "soon" (not instantly), and a 10s cycle on even a busy workspace
// (dozens of active exposures) is cheap — one indexed scan per cycle.
const portExposureScanInterval = 10 * time.Second

// exposureSnapshot is the subset of the port_exposures row we need to diff
// between polls: which exposure was ACTIVE (so the prior cycle emitted a
// network.port_opened) and which fields describe it for the matching close
// event. Keyed by the exposure id (PK) so revokes and re-exposures on the
// same (container, port) don't collapse into one slot.
type exposureSnapshot struct {
	WorkspaceID   string
	CrewID        string
	AgentID       string
	ContainerID   string
	ContainerPort int
}

// runPortExposureScanner watches the port_exposures table and emits
// network.port_opened / network.port_closed journal entries as rows flip
// status. Implemented as a poller (not a trigger or Docker-events
// subscriber) because:
//
//  1. The port_exposures table is the already-normalized record of which
//     container:port tuples are user-reachable. Docker events would surface
//     every TCP bind inside the container (including noisy ephemera like
//     package managers or agent-internal IPC) — far more signal than the
//     Crow's Nest UI wants.
//  2. Polling stays within the existing SQLite transaction model. Triggers
//     would require either a SQLite UDF (fragile across driver versions) or
//     hand-rolled trigger tables that duplicate state.
//  3. 10s latency is acceptable for the UI and hides race conditions
//     (ACTIVE→REVOKED→ACTIVE bouncing under TTL pressure) behind a single
//     steady-state snapshot.
//
// Parameters:
//   - ctx: cancelled on server shutdown; scanner returns cleanly.
//   - db: may be nil (tests) in which case the function returns immediately.
//   - j: may be nil in which case we still run the loop (cheap) but skip
//     emits; this keeps the scanner trivially testable without needing a
//     live journal writer.
//   - logger: debug/warn output for transient DB errors.
func runPortExposureScanner(ctx context.Context, db *sql.DB, j journal.Emitter, logger *slog.Logger) {
	if db == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}

	// Seed the prior snapshot on startup so we don't emit a flood of
	// network.port_opened for every still-ACTIVE exposure the moment the
	// server comes up. This is the correct behavior: the journal already
	// has the originating port_opened event from when the exposure was
	// first created (by the port-expose handler), so re-emitting on
	// restart would be a lie about "new" activity.
	prev, err := scanExposures(ctx, db)
	if err != nil {
		logger.Warn("port-exposure scanner: initial scan failed, starting from empty snapshot", "err", err)
		prev = make(map[string]exposureSnapshot)
	}

	t := time.NewTicker(portExposureScanInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		current, err := scanExposures(ctx, db)
		if err != nil {
			// Transient DB failures (locked, interrupted) are expected on
			// busy dev boxes; log at debug so we don't spam warn-level
			// logs. A truly broken DB will surface elsewhere.
			logger.Debug("port-exposure scanner: scan failed", "err", err)
			continue
		}

		diffAndEmit(ctx, j, prev, current)
		prev = current
	}
}

// scanExposures pulls the current ACTIVE set from the port_exposures table.
// Returns a map keyed by row id. Revokes / expires drop out of the result
// naturally because the WHERE clause filters to ACTIVE.
func scanExposures(ctx context.Context, db *sql.DB) (map[string]exposureSnapshot, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, workspace_id, crew_id, agent_id, container_id, container_port
		FROM port_exposures
		WHERE status = 'ACTIVE'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]exposureSnapshot)
	for rows.Next() {
		var (
			id    string
			snap  exposureSnapshot
			agent sql.NullString
		)
		if err := rows.Scan(&id, &snap.WorkspaceID, &snap.CrewID, &agent, &snap.ContainerID, &snap.ContainerPort); err != nil {
			return nil, err
		}
		if agent.Valid {
			snap.AgentID = agent.String
		}
		out[id] = snap
	}
	return out, rows.Err()
}

// diffAndEmit compares prior vs. current ACTIVE exposures and emits one
// network.port_opened for every new row and one network.port_closed for
// every row that disappeared. Pure function so tests can hand in two maps
// and assert the emitted entries without a DB.
func diffAndEmit(ctx context.Context, j journal.Emitter, prev, current map[string]exposureSnapshot) {
	if j == nil {
		return
	}

	for id, snap := range current {
		if _, was := prev[id]; !was {
			emitPortEvent(ctx, j, journal.EntryNetworkPortOpen, id, snap)
		}
	}
	for id, snap := range prev {
		if _, still := current[id]; !still {
			emitPortEvent(ctx, j, journal.EntryNetworkPortClose, id, snap)
		}
	}
}

func emitPortEvent(ctx context.Context, j journal.Emitter, kind journal.EntryType, exposureID string, snap exposureSnapshot) {
	action := "opened"
	if kind == journal.EntryNetworkPortClose {
		action = "closed"
	}
	_, _ = j.Emit(ctx, journal.Entry{
		WorkspaceID: snap.WorkspaceID,
		CrewID:      snap.CrewID,
		AgentID:     snap.AgentID,
		Type:        kind,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorSystem,
		Summary:     "port " + formatPort(snap.ContainerPort) + " " + action + " on container " + shortContainerID(snap.ContainerID),
		Payload: map[string]any{
			"port":         snap.ContainerPort,
			"proto":        "tcp",
			"container_id": snap.ContainerID,
			"exposure_id":  exposureID,
		},
		Refs: map[string]any{"container_id": snap.ContainerID, "exposure_id": exposureID},
	})
}

// formatPort is a cheap decimal formatter that avoids pulling fmt into the
// hot path. Ports are bounded to [1,65535] per the DB CHECK constraint so
// we can safely stack-allocate a small buffer.
func formatPort(p int) string {
	if p <= 0 {
		return "?"
	}
	var buf [6]byte
	i := len(buf)
	for p > 0 {
		i--
		buf[i] = byte('0' + p%10)
		p /= 10
	}
	return string(buf[i:])
}

// shortContainerID returns the first 12 chars of a Docker container id for
// log readability, matching the `docker ps` convention. Empty-in → empty-out.
func shortContainerID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}
