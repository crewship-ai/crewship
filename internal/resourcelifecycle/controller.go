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
func (c *Controller) Pending(ctx context.Context, crew string) Status {
	s := Status{CrewID: crew, Scope: "containers", State: "pending"}
	if c == nil || c.InstanceID == "" || c.Connect == nil || c.DB == nil {
		s.State = "disabled"
		return s
	}
	if err := c.store(ctx, s); err != nil {
		s.State = "error"
		s.Error = "diagnostic_write_failed"
	}
	return s
}
func (c *Controller) store(ctx context.Context, s Status) error {
	_, err := c.DB.ExecContext(ctx, `INSERT INTO resource_cleanup_status(instance_id,crew_id,state,observed_at,complete,remaining,unattributed,error_code)
 VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(instance_id,crew_id) DO UPDATE SET state=excluded.state,observed_at=excluded.observed_at,complete=excluded.complete,remaining=excluded.remaining,unattributed=excluded.unattributed,error_code=excluded.error_code`, c.InstanceID, s.CrewID, s.State, s.ObservedAt, s.Complete, s.Remaining, s.Unattributed, s.Error)
	return err
}
func (c *Controller) Statuses(ctx context.Context) ([]Status, error) {
	out := []Status{}
	if c == nil || c.DB == nil || c.InstanceID == "" {
		return out, nil
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT crew_id,state,observed_at,complete,remaining,unattributed,error_code FROM resource_cleanup_status WHERE instance_id=? ORDER BY crew_id`, c.InstanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		s := Status{Scope: "containers"}
		if err := rows.Scan(&s.CrewID, &s.State, &s.ObservedAt, &s.Complete, &s.Remaining, &s.Unattributed, &s.Error); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, s.ObservedAt)
		if err != nil || t.Before(c.BootAt) || time.Since(t) > 90*time.Second || c.lastScanFailed.Load() {
			s.State = "unknown"
			s.Complete = false
		}
		out = append(out, s)
	}
	return out, rows.Err()
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
	rows, err := c.DB.QueryContext(ctx, `SELECT id FROM crews WHERE deleted_at IS NOT NULL`)
	if err != nil {
		c.invalidate(ctx, "owner_inventory_failed")
		return
	}
	states := map[string]Status{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			c.invalidate(ctx, "owner_inventory_failed")
			return
		}
		states[id] = Status{CrewID: id, Scope: "containers", State: "pending"}
	}
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		c.invalidate(ctx, "owner_inventory_failed")
		return
	}
	failAll := func(code string) {
		c.invalidate(ctx, code)
		for id, s := range states {
			s.State = "unknown"
			s.Error = code
			s.Complete = false
			s.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
			states[id] = s
			if err := c.store(ctx, s); err != nil {
				c.logWriteFailure(ctx)
			}
		}
	}
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
					_, saveErr := c.DB.ExecContext(stepCtx, `INSERT INTO resource_cleanup_mounts(instance_id,crew_id,container_id,mounts_json,observed_at) VALUES(?,?,?,?,?) ON CONFLICT(instance_id,container_id) DO NOTHING`, c.InstanceID, x.CrewID, x.ID, string(b), time.Now().UTC().Format(time.RFC3339Nano))
					if saveErr != nil {
						code = "mount_snapshot_failed"
					} else if saveErr = c.store(stepCtx, s); saveErr != nil {
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
	writeFailed := false
	for _, s := range states {
		s.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
		s.Complete = true
		if s.Error != "" {
			s.State = "error"
		} else if s.Remaining == 0 {
			s.State = "observed_clear"
		} else {
			s.State = "pending"
		}
		if err := c.store(ctx, s); err != nil {
			writeFailed = true
			c.logWriteFailure(ctx)
		}
	}
	if writeFailed {
		c.invalidate(ctx, "diagnostic_write_failed")
	} else {
		c.lastScanFailed.Store(false)
	}
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
	_, err := c.DB.ExecContext(ctx, `UPDATE resource_cleanup_status SET state='unknown',complete=0,error_code=? WHERE instance_id=?`, code, c.InstanceID)
	if err != nil {
		c.logWriteFailure(ctx)
	}
}
func (c *Controller) logWriteFailure(ctx context.Context) {
	slog.WarnContext(ctx, "container cleanup diagnostic write failed", "code", "diagnostic_write_failed")
}
