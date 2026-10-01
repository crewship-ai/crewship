// Package resourcelifecycle removes containers of positively deleted owners.
// Persistent volumes and host data are outside this controller's scope.
package resourcelifecycle

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/quiesce"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

var ErrNotFound = errors.New("container not found")

type Mount struct {
	Type        string `json:"type" yaml:"type"`
	Name        string `json:"name,omitempty" yaml:"name,omitempty"`
	Source      string `json:"source,omitempty" yaml:"source,omitempty"`
	Destination string `json:"destination" yaml:"destination"`
}
type Container struct {
	ID         string
	InstanceID string
	CrewID     string
	Kind       string
	Mounts     []Mount
}
type Runtime interface {
	List(context.Context) ([]Container, error)
	Inspect(context.Context, string) (Container, error)
	Stop(context.Context, string) error
	Remove(context.Context, string) error // MUST preserve all volumes.
	Close() error
}
type Status struct {
	CrewID       string `json:"crew_id" yaml:"crew_id"`
	Scope        string `json:"scope" yaml:"scope"`
	State        string `json:"state" yaml:"state"`
	ObservedAt   string `json:"observed_at,omitempty" yaml:"observed_at,omitempty"`
	Complete     bool   `json:"complete" yaml:"complete"`
	Remaining    int    `json:"remaining" yaml:"remaining"`
	Unattributed int    `json:"unattributed" yaml:"unattributed"`
	Error        string `json:"error,omitempty" yaml:"error,omitempty"`
}
type Controller struct {
	DB             *sql.DB
	InstanceID     string
	Connect        func(context.Context) (Runtime, error)
	Batch          int
	nextOffset     int
	lastScanFailed atomic.Bool
	// BootAt marks persisted observations stale until this process scans.
	BootAt time.Time
}

func (c *Controller) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		c.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Pending records that the crew was just deleted. workspace scopes who may
// read the observation back through the admin API.
func (c *Controller) Pending(ctx context.Context, workspace, crew string) Status {
	s := Status{CrewID: crew, Scope: "containers", State: "pending"}
	if c == nil || c.InstanceID == "" || c.Connect == nil || c.DB == nil {
		s.State = "disabled"
		return s
	}
	if err := c.store(ctx, workspace, s); err != nil {
		s.State = "error"
		s.Error = "diagnostic_write_failed"
	}
	return s
}
func (c *Controller) store(ctx context.Context, workspace string, s Status) error {
	_, err := c.DB.ExecContext(ctx, `INSERT INTO resource_cleanup_status(instance_id,crew_id,workspace_id,state,observed_at,complete,remaining,unattributed,error_code)
 VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(instance_id,crew_id) DO UPDATE SET workspace_id=excluded.workspace_id,state=excluded.state,observed_at=excluded.observed_at,complete=excluded.complete,remaining=excluded.remaining,unattributed=excluded.unattributed,error_code=excluded.error_code`, c.InstanceID, s.CrewID, workspace, s.State, s.ObservedAt, s.Complete, s.Remaining, s.Unattributed, s.Error)
	return err
}

// Statuses returns this installation's observations for one workspace. Rows
// are instance-wide, but workspace admins must not see other tenants' crews.
func (c *Controller) Statuses(ctx context.Context, workspace string) ([]Status, error) {
	return c.statuses(ctx, &workspace)
}

// InstanceStatuses returns all observations owned by this installation. Its
// caller must enforce instance-admin authorization; workspace reads use Statuses.
func (c *Controller) InstanceStatuses(ctx context.Context) ([]Status, error) {
	return c.statuses(ctx, nil)
}

func (c *Controller) statuses(ctx context.Context, workspace *string) ([]Status, error) {
	out := []Status{}
	if c == nil || c.DB == nil || c.InstanceID == "" {
		return out, nil
	}
	var scan scanRow
	err := c.DB.QueryRowContext(ctx, `SELECT observed_at,complete,error_code FROM resource_cleanup_scans WHERE instance_id=?`, c.InstanceID).Scan(&scan.observedAt, &scan.complete, &scan.errorCode)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	fresh := false
	if t, parseErr := time.Parse(time.RFC3339, scan.observedAt); err == nil && parseErr == nil {
		fresh = scan.complete && !t.Before(c.BootAt) && time.Since(t) <= StaleAfter && !c.lastScanFailed.Load()
	}
	query := `SELECT crew_id,state,complete,remaining,unattributed,error_code FROM resource_cleanup_status WHERE instance_id=?`
	args := []any{c.InstanceID}
	if workspace != nil {
		query += ` AND workspace_id=?`
		args = append(args, *workspace)
	}
	rows, err := c.DB.QueryContext(ctx, query+` ORDER BY crew_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		s := Status{Scope: "containers"}
		if err := rows.Scan(&s.CrewID, &s.State, &s.Complete, &s.Remaining, &s.Unattributed, &s.Error); err != nil {
			return nil, err
		}
		// A per-owner row is written only when it changes; the installation
		// scan row says when it was last confirmed.
		s.ObservedAt = scan.observedAt
		if !fresh {
			s.State = "unknown"
			s.Complete = false
			if s.Error == "" {
				s.Error = scan.errorCode
			}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// StaleAfter is how old the last complete scan may be before observations read
// as unknown. Three missed 30-second ticks.
const StaleAfter = 90 * time.Second

type scanRow struct {
	observedAt string
	complete   bool
	errorCode  string
}

// storedStatus is the comparable part of a persisted per-owner row.
type storedStatus struct {
	state        string
	complete     bool
	remaining    int
	unattributed int
	errorCode    string
}

func (c *Controller) storedStatuses(ctx context.Context) (map[string]storedStatus, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT crew_id,state,complete,remaining,unattributed,error_code FROM resource_cleanup_status WHERE instance_id=?`, c.InstanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]storedStatus{}
	for rows.Next() {
		var id string
		var s storedStatus
		if err := rows.Scan(&id, &s.state, &s.complete, &s.remaining, &s.unattributed, &s.errorCode); err != nil {
			return nil, err
		}
		out[id] = s
	}
	return out, rows.Err()
}

func (c *Controller) storeScan(ctx context.Context, complete bool, code string) error {
	_, err := c.DB.ExecContext(ctx, `INSERT INTO resource_cleanup_scans(instance_id,observed_at,complete,error_code) VALUES(?,?,?,?)
 ON CONFLICT(instance_id) DO UPDATE SET observed_at=excluded.observed_at,complete=excluded.complete,error_code=excluded.error_code`, c.InstanceID, tsformat.Format(time.Now()), complete, code)
	return err
}
func (c *Controller) deleted(ctx context.Context, crew string) (bool, error) {
	var deleted sql.NullString
	err := c.DB.QueryRowContext(ctx, `SELECT deleted_at FROM crews WHERE id=?`, crew).Scan(&deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return deleted.Valid, err
}
func eligible(x Container, instance, crew string) bool {
	return x.ID != "" && x.InstanceID == instance && x.CrewID == crew && (x.Kind == "crew" || x.Kind == "sidecar")
}

// Tick always scans the whole daemon; Batch limits removals, never inventory.
// Errors are fixed codes: provider responses may contain credentials or URLs.
func (c *Controller) Tick(ctx context.Context) {
	if c == nil || c.DB == nil || c.InstanceID == "" || c.Connect == nil {
		return
	}
	// Docker removal and its mount evidence are one background writer. A
	// backup closes admission and drains any stop/remove already in flight
	// before copying the database and container files. A new tick stays due
	// while admission is closed; it resumes on the next scheduled scan.
	writer, ok := quiesce.Enter(ctx)
	if !ok {
		return
	}
	defer writer.Leave()
	ctx = writer.Context()
	rows, err := c.DB.QueryContext(ctx, `SELECT id,workspace_id FROM crews WHERE deleted_at IS NOT NULL`)
	if err != nil {
		c.invalidate(ctx, "owner_inventory_failed")
		return
	}
	states := map[string]Status{}
	workspaces := map[string]string{}
	for rows.Next() {
		var id, workspace string
		if rows.Scan(&id, &workspace) != nil {
			rows.Close()
			c.invalidate(ctx, "owner_inventory_failed")
			return
		}
		states[id] = Status{CrewID: id, Scope: "containers", State: "pending"}
		workspaces[id] = workspace
	}
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		c.invalidate(ctx, "owner_inventory_failed")
		return
	}
	failAll := func(code string) { c.invalidate(ctx, code) }
	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	rt, err := c.Connect(connectCtx)
	cancel()
	if err != nil {
		failAll("provider_unavailable")
		return
	}
	defer rt.Close()
	scanCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	list, err := rt.List(scanCtx)
	cancel()
	if err != nil {
		failAll("inventory_failed")
		return
	}
	cap := c.Batch
	if cap <= 0 {
		cap = 100
	}
	attempts := 0
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	start := 0
	if len(list) > 0 {
		start = c.nextOffset % len(list)
	}
	for pos := 0; pos < len(list); pos++ {
		// Let a backup copy between candidates, rather than holding its drain
		// up for the entire removal backlog. Never yield halfway through a
		// candidate's mount evidence, owner checks and stop/remove.
		if err := quiesce.Yield(ctx); err != nil {
			return
		}
		x := list[(start+pos)%len(list)]
		s, ok := states[x.CrewID]
		if !ok {
			continue
		}
		if !eligible(x, c.InstanceID, x.CrewID) {
			if x.InstanceID == "" {
				s.Unattributed++
				states[x.CrewID] = s
			}
			continue
		}
		if attempts >= cap {
			continue
		}
		attempts++
		c.nextOffset = (start + pos + 1) % len(list)
		stepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		fresh, inspectErr := rt.Inspect(stepCtx, x.ID)
		if errors.Is(inspectErr, ErrNotFound) {
			cancel()
			continue
		}
		code := ""
		if inspectErr != nil {
			code = "inspect_failed"
		} else if fresh.ID != x.ID || !eligible(fresh, c.InstanceID, x.CrewID) {
			code = "ownership_changed"
		} else {
			deleted, ownerErr := c.deleted(stepCtx, x.CrewID)
			if ownerErr != nil {
				code = "owner_check_failed"
			} else if !deleted {
				cancel()
				continue
			} else {
				// Write the limited mount evidence before any stop/remove. No env,
				// commands, logs or credentials are inspected or stored.
				b, jsonErr := mountSnapshot(fresh.Mounts)
				if jsonErr != nil {
					code = "mount_snapshot_failed"
				} else {
					_, saveErr := c.DB.ExecContext(stepCtx, `INSERT INTO resource_cleanup_mounts(instance_id,crew_id,container_id,mounts_json,observed_at) VALUES(?,?,?,?,?) ON CONFLICT(instance_id,container_id) DO NOTHING`, c.InstanceID, x.CrewID, x.ID, string(b), tsformat.Format(time.Now()))
					if saveErr != nil {
						code = "mount_snapshot_failed"
					} else if saveErr = c.store(stepCtx, workspaces[x.CrewID], s); saveErr != nil {
						code = "diagnostic_write_failed"
					} else {
						stopErr := rt.Stop(stepCtx, x.ID)
						if stopErr != nil && !errors.Is(stopErr, ErrNotFound) {
							code = "stop_failed"
						} else {
							// Recheck owner after stop too. This is deliberately not fencing:
							// a revive can still race the last check and lose this writable layer.
							deleted, ownerErr = c.deleted(stepCtx, x.CrewID)
							if ownerErr != nil {
								code = "owner_check_failed"
							} else if deleted {
								removeErr := rt.Remove(stepCtx, x.ID)
								if removeErr != nil && !errors.Is(removeErr, ErrNotFound) {
									code = "remove_failed"
								}
							}
						}
					}
				}
			}
		}
		cancel()
		if code != "" {
			s.State = "error"
			s.Error = code
		}
		states[x.CrewID] = s
	}
	// Fresh complete inventory, after mutations: only this may establish clear.
	scanCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
	remaining, err := rt.List(scanCtx)
	cancel()
	if err != nil {
		failAll("inventory_failed")
		return
	}
	for _, x := range remaining {
		if s, ok := states[x.CrewID]; ok && eligible(x, c.InstanceID, x.CrewID) {
			s.Remaining++
			states[x.CrewID] = s
		}
	}
	// Write only what changed. Every soft-deleted crew ever is an owner here,
	// so rewriting all of them each tick would be a steady write load that
	// grows with every reseed. A clean tombstone with no row needs none.
	stored, err := c.storedStatuses(ctx)
	if err != nil {
		c.invalidate(ctx, "diagnostic_write_failed")
		c.logWriteFailure(ctx)
		return
	}
	writeFailed := false
	// A row whose owner is no longer a tombstone describes the past: the crew
	// was revived, so its old error or pending state must not be reported as
	// confirmed by this scan. Drop it; mount evidence stays in its own table.
	for id := range stored {
		if _, ok := states[id]; ok {
			continue
		}
		var live int
		err := c.DB.QueryRowContext(ctx, `SELECT 1 FROM crews WHERE id=? AND deleted_at IS NULL`, id).Scan(&live)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err == nil {
			_, err = c.DB.ExecContext(ctx, `DELETE FROM resource_cleanup_status WHERE instance_id=? AND crew_id=?`, c.InstanceID, id)
		}
		if err != nil {
			writeFailed = true
			c.logWriteFailure(ctx)
		}
	}
	for id, s := range states {
		s.ObservedAt = tsformat.Format(time.Now())
		s.Complete = true
		if s.Error != "" {
			s.State = "error"
		} else if s.Remaining == 0 {
			s.State = "observed_clear"
		} else {
			s.State = "pending"
		}
		next := storedStatus{state: s.State, complete: true, remaining: s.Remaining, unattributed: s.Unattributed, errorCode: s.Error}
		prev, ok := stored[id]
		if ok && prev == next {
			continue
		}
		if !ok && s.State == "observed_clear" && s.Unattributed == 0 {
			continue
		}
		if err := c.store(ctx, workspaces[id], s); err != nil {
			writeFailed = true
			c.logWriteFailure(ctx)
		}
	}
	if writeFailed {
		c.invalidate(ctx, "diagnostic_write_failed")
		return
	}
	if err := c.storeScan(ctx, true, ""); err != nil {
		c.invalidate(ctx, "diagnostic_write_failed")
		c.logWriteFailure(ctx)
		return
	}
	c.lastScanFailed.Store(false)
}

// Bound individual snapshots without truncating ownership evidence. Control
// characters and oversized values are rejected before destruction.
func mountSnapshot(mounts []Mount) ([]byte, error) {
	if len(mounts) > 256 {
		return nil, errors.New("too many mounts")
	}
	for _, m := range mounts {
		for _, s := range []string{m.Type, m.Name, m.Source, m.Destination} {
			if len(s) > 4096 || strings.ContainsAny(s, "\x00\r\n") {
				return nil, errors.New("invalid mount reference")
			}
		}
	}
	return json.Marshal(mounts)
}

func (c *Controller) invalidate(ctx context.Context, code string) {
	c.lastScanFailed.Store(true)
	if err := c.storeScan(ctx, false, code); err != nil {
		c.logWriteFailure(ctx)
	}
}
func (c *Controller) logWriteFailure(ctx context.Context) {
	slog.WarnContext(ctx, "container cleanup diagnostic write failed", "code", "diagnostic_write_failed")
}
