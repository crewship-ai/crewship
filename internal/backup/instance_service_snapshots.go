package backup

import (
	"archive/tar"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The consistent copy stays encrypted on disk while the instance payload is
// packed. Fences remain owned until publication, including after the window.
type instanceServiceCapture struct {
	archive   string
	fences    []serviceBackupFence
	snapshots []serviceSnapshot
	keeper    *serviceFenceKeeper
	completed bool
}

func (c *instanceServiceCapture) capture(ctx context.Context, db *sql.DB, stage string, sc *stagingCipher, opts InstanceOptions, workspaces []*instanceWorkspaceTarget) error {
	c.archive = filepath.Join(stage, "service-snapshots.sealed")
	out, err := sc.Create(c.archive)
	if err != nil {
		return err
	}
	tw, err := NewTarZstWriterConcurrency(out, opts.EncoderConcurrency)
	if err != nil {
		_ = out.Close()
		return err
	}
	var crews []CrewTarget
	for _, ws := range workspaces {
		crews = append(crews, ws.target.CrewTargets...)
	}
	c.fences, c.snapshots, err = captureServiceSnapshots(ctx, db, opts.ServiceSnapshots, tw, crews, time.Now().UTC(), opts.RecoverServiceMaintenance, c.keeper.add)
	closeTar := tw.Close()
	closeFile := out.Close()
	if err != nil {
		return err
	}
	if closeTar != nil {
		return closeTar
	}
	if closeFile != nil {
		return closeFile
	}
	c.completed = true
	return nil
}

func (c *instanceServiceCapture) write(tw *TarZstWriter, sc *stagingCipher, now time.Time) error {
	if c == nil || len(c.snapshots) == 0 {
		return nil
	}
	r, _, err := sc.Open(c.archive)
	if err != nil {
		return err
	}
	defer r.Close()
	tr, err := NewTarZstReader(r)
	if err != nil {
		return err
	}
	defer tr.Close()
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || !strings.HasPrefix(h.Name, "service-snapshots/") {
			return fmt.Errorf("backup: invalid staged service snapshot entry")
		}
		if err := tw.WriteStream(h.Name, 0600, now, h.Size, tr); err != nil {
			return err
		}
	}
	// Consume the staging cipher's final frame even if the tar reader stopped at
	// its end markers; this also authenticates the encrypted staging trailer.
	_, err = io.Copy(io.Discard, r)
	return err
}

// Read only declarations and intents from the immutable instance database.
// Loading the whole logical dump would double its already resident footprint.
func instanceServiceDump(ctx context.Context, db *sql.DB) (*DBDump, error) {
	dump := &DBDump{Tables: map[string][]map[string]any{}}
	for _, query := range []struct{ name, sql string }{
		{"crews", `SELECT c.id,c.workspace_id,c.slug,c.services_json FROM crews c JOIN workspaces w ON w.id=c.workspace_id WHERE c.deleted_at IS NULL AND w.deleted_at IS NULL`},
		{"service_runtime_intents", `SELECT i.crew_id,i.service_name,i.desired_state,i.version FROM service_runtime_intents i JOIN crews c ON c.id=i.crew_id JOIN workspaces w ON w.id=c.workspace_id WHERE c.deleted_at IS NULL AND w.deleted_at IS NULL`},
	} {
		rows, err := db.QueryContext(ctx, query.sql)
		if err != nil {
			return nil, err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return nil, err
			}
			row := map[string]any{}
			for i, col := range columns {
				row[col] = normalizeScan(values[i])
			}
			dump.Tables[query.name] = append(dump.Tables[query.name], row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return dump, nil
}

type instanceServiceRecovery struct {
	payload *ExtractedPayload
	count   int
}

// Staging is deliberately separate from starting a service on the new host.
// The database must never point at a source generation or an empty replacement.
func stageInstanceServiceRecovery(ctx context.Context, db *sql.DB, payload *ExtractedPayload, count int, dataDir string) error {
	dump, err := instanceServiceDump(ctx, db)
	if err != nil {
		return err
	}
	payload.DBDump = dump
	plan, err := payload.prepareServiceRestorePlan(ctx, count)
	if err != nil {
		return err
	}
	if err = plan.selectGenerations(); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The source process's producer and operation epochs cannot own this host.
	if _, err = tx.ExecContext(ctx, `DELETE FROM service_backup_fences`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM service_operation_leases`); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, item := range plan.items {
		if seen[item.target.Crew] {
			continue
		}
		seen[item.target.Crew] = true
		body, _ := item.crewRow["services_json"].(string)
		if _, err = tx.ExecContext(ctx, `UPDATE crews SET services_json=? WHERE id=?`, body, item.target.Crew); err != nil {
			return err
		}
	}
	if err = plan.insertFences(ctx, tx); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE service_backup_fences SET producer_until=''`); err != nil {
		return err
	}
	temporaryPlan, err := writeInstanceServicePlan(payload, plan, dataDir)
	if err != nil {
		return err
	}
	defer os.Remove(temporaryPlan)
	if err = tx.Commit(); err != nil {
		return err
	}
	// Once committed, these images are referenced by the recovered database.
	// Retain them on publication failure, with maintenance still in force.
	payload.serviceRecoveryCommitted = true
	path := filepath.Join(dataDir, RecoveredServicesDir, instanceServicePlanFile)
	if err = os.Rename(temporaryPlan, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
