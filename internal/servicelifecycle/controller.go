// Package servicelifecycle reconciles opt-in service intent independently of
// agent runs. The database is the desired state; Docker IDs are observations.
package servicelifecycle

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type Runtime interface {
	EnsureCrewServices(context.Context, provider.CrewConfig) (map[string]string, error)
	StopCrewService(context.Context, string, string, string) error
}
type Resolver func(context.Context, string, string, string) (provider.CrewConfig, error)
type Controller struct {
	DB      *sql.DB
	Runtime Runtime
	Resolve Resolver
}
type intent struct {
	id, crew, workspace, slug, name, state string
	version                                int64
}

func (c *Controller) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		c.Reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Reconcile can run in two processes: row leases prevent duplicate work, and
// runtime operations must also be idempotent under a lost response or restart.
func (c *Controller) Reconcile(ctx context.Context) {
	if c.DB == nil || c.Runtime == nil || c.Resolve == nil {
		return
	}
	now := tsformat.Format(time.Now())
	rows, err := c.DB.QueryContext(ctx, `SELECT i.id,i.crew_id,c.workspace_id,c.slug,i.service_name,
 CASE WHEN c.deleted_at IS NOT NULL THEN 'stopped' ELSE i.desired_state END,i.version
 FROM service_runtime_intents i JOIN crews c ON c.id=i.crew_id
 WHERE i.next_attempt_at<=? AND i.lease_until<=? ORDER BY i.next_attempt_at,i.id LIMIT 100`, now, now)
	if err != nil {
		return
	}
	var jobs []intent
	for rows.Next() {
		var j intent
		if rows.Scan(&j.id, &j.crew, &j.workspace, &j.slug, &j.name, &j.state, &j.version) != nil {
			rows.Close()
			return
		}
		jobs = append(jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, job := range jobs {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		go func(j intent) { defer wg.Done(); defer func() { <-sem }(); c.reconcileOne(ctx, j) }(job)
	}
	wg.Wait()
}

func (c *Controller) reconcileOne(ctx context.Context, j intent) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return
	}
	owner := hex.EncodeToString(nonce[:])
	now := time.Now()
	res, err := c.DB.ExecContext(ctx, `UPDATE service_runtime_intents SET lease_owner=?,lease_until=?
 WHERE id=? AND version=? AND lease_until<=? AND next_attempt_at<=?`, owner, tsformat.Format(now.Add(2*time.Minute)), j.id, j.version, tsformat.Format(now), tsformat.Format(now))
	if err != nil {
		return
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	state, errorCode := j.state, ""
	if j.state == "stopped" {
		err = c.Runtime.StopCrewService(opCtx, j.crew, j.slug, j.name)
	} else {
		var cfg provider.CrewConfig
		cfg, err = c.Resolve(opCtx, j.crew, j.workspace, j.name)
		if err != nil {
			errorCode = "configuration_or_credentials_unavailable"
			// Revoke already-running processes too: they may retain old env secrets.
			if stopErr := c.Runtime.StopCrewService(opCtx, j.crew, j.slug, j.name); stopErr != nil {
				errorCode = "runtime_unavailable"
			}
		} else {
			_, err = c.Runtime.EnsureCrewServices(opCtx, cfg)
		}
	}
	delay := 30 * time.Second
	if err != nil {
		state = "error"
		delay = time.Minute
		if errorCode == "" {
			errorCode = "runtime_unavailable"
		}
	}
	// A newer user intent must not acquire an old operation's success status.
	// Release our lease even when its version changed while Docker was working.
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	_, _ = c.DB.ExecContext(finishCtx, `UPDATE service_runtime_intents SET observed_state=?,last_error=?,next_attempt_at=?,updated_at=? WHERE id=? AND version=? AND lease_owner=?`, state, errorCode, tsformat.Format(time.Now().Add(delay)), tsformat.Format(time.Now()), j.id, j.version, owner)
	_, _ = c.DB.ExecContext(finishCtx, `UPDATE service_runtime_intents SET lease_owner='',lease_until='' WHERE id=? AND lease_owner=?`, j.id, owner)
}

// FilterServices leaves managed services exclusively to the controller, even
// when a caller supplies resolved services. Agent startup cannot bypass strict
// credential resolution or resurrect a manually stopped service.
func FilterServices(ctx context.Context, db *sql.DB, cfg provider.CrewConfig) (provider.CrewConfig, error) {
	if db == nil || cfg.ID == "" || len(cfg.Services) == 0 {
		return cfg, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT service_name FROM service_runtime_intents WHERE crew_id=?`, cfg.ID)
	if err != nil {
		return cfg, err
	}
	defer rows.Close()
	managed := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return cfg, err
		}
		managed[name] = true
	}
	if err := rows.Err(); err != nil {
		return cfg, err
	}
	services := make([]provider.CrewService, 0, len(cfg.Services))
	for _, svc := range cfg.Services {
		if !managed[svc.Name] {
			services = append(services, svc)
		}
	}
	cfg.Services = services
	return cfg, nil
}

// StopCrew makes an explicit whole-crew stop survive controller/server restarts.
func StopCrew(ctx context.Context, db *sql.DB, crew string) error {
	if db == nil {
		return nil
	}
	_, err := db.ExecContext(ctx, `UPDATE service_runtime_intents SET desired_state='stopped',version=version+1,observed_state='pending',next_attempt_at='',updated_at=? WHERE crew_id=?`, tsformat.Format(time.Now()), crew)
	return err
}
