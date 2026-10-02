package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/quota"
	"github.com/crewship-ai/crewship/internal/serviceconfig"
	"github.com/crewship-ai/crewship/internal/servicelifecycle"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type serviceImport struct {
	source   serviceSnapshot
	crewRow  map[string]any
	target   quota.Key
	imported bool
}
type serviceRestorePlan struct {
	payload   *ExtractedPayload
	items     []serviceImport
	fences    []serviceBackupFence
	oldFences []serviceBackupFence
}

func (p *ExtractedPayload) prepareServiceRestorePlan(ctx context.Context, count int) (*serviceRestorePlan, error) {
	if err := p.validateServiceSnapshotArchive(ctx, count); err != nil {
		return nil, err
	}
	plan := &serviceRestorePlan{payload: p}
	if p.DBDump == nil {
		if count != 0 {
			return nil, fmt.Errorf("backup: service images require database ownership metadata")
		}
		return plan, nil
	}
	declarations := map[string]serviceSnapshot{}
	crewRows := map[string]map[string]any{}
	for _, row := range p.DBDump.Tables["crews"] {
		if rowIsDeleted(row) {
			continue
		}
		id, _ := row["id"].(string)
		slug, _ := row["slug"].(string)
		body, _ := row["services_json"].(string)
		if _, exists := crewRows[id]; exists {
			return nil, fmt.Errorf("backup: duplicate service owner")
		}
		crewRows[id] = row
		specs, err := declaredServiceSnapshotsWithOpener(body, id, slug, p.openServiceConfig)
		if err != nil {
			return nil, err
		}
		for _, spec := range specs {
			declarations[spec.name()] = spec
		}
	}
	if len(declarations) != count {
		return nil, fmt.Errorf("backup: quota service data is missing; database-only legacy bundles cannot restore these services")
	}
	intents := map[string]map[string]any{}
	for _, row := range p.DBDump.Tables["service_runtime_intents"] {
		crew, _ := row["crew_id"].(string)
		svc, _ := row["service_name"].(string)
		key := crew + "\x00" + svc
		if _, exists := intents[key]; exists {
			return nil, fmt.Errorf("backup: duplicate service intent")
		}
		intents[key] = row
	}
	for id, source := range p.serviceMetadata {
		declared, ok := declarations[id]
		if !ok || declared.key() != source.key() || declared.Bytes != source.Bytes || declared.CrewSlug != source.CrewSlug {
			return nil, fmt.Errorf("backup: service image ownership mismatch")
		}
		row, ok := intents[source.CrewID+"\x00"+source.Service]
		if !ok {
			return nil, fmt.Errorf("backup: missing durable service intent")
		}
		desired, _ := row["desired_state"].(string)
		version, err := rowInt64(row, "version")
		if n, ok := row["version"].(float64); ok && (math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n >= 1<<53) {
			return nil, fmt.Errorf("backup: invalid service intent version")
		}
		if err != nil || desired != source.DesiredState || version != source.IntentVersion {
			return nil, fmt.Errorf("backup: service intent revision mismatch")
		}
		plan.items = append(plan.items, serviceImport{source: source, crewRow: crewRows[source.CrewID]})
	}
	return plan, nil
}

// selectGenerations runs only after the standard restore ID rewrites. Archive
// identity is an origin proof, never an instruction to overwrite a host image.
func (p *serviceRestorePlan) selectGenerations() error {
	for i := range p.items {
		item := &p.items[i]
		crew, _ := item.crewRow["id"].(string)
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			return err
		}
		// Keep integer JSON roundtrips exact, including generic DB dump readers.
		generation := int64(binary.BigEndian.Uint64(random[:]) & ((1 << 52) - 1))
		if generation == 0 {
			generation = 1
		}
		if generation == item.source.Generation {
			generation = generation%((1<<52)-1) + 1
		}
		item.target = quota.Key{Crew: crew, Service: item.source.Service, Volume: item.source.Volume, Generation: generation}
		body, _ := item.crewRow["services_json"].(string)
		plain, err := serviceconfig.Open(body)
		if err != nil {
			return err
		}
		decoder := json.NewDecoder(strings.NewReader(plain))
		decoder.UseNumber()
		var services []map[string]any
		if err = decoder.Decode(&services); err != nil {
			return err
		}
		found := false
		for _, svc := range services {
			if svc["name"] != item.source.Service {
				continue
			}
			volumes, ok := svc["volumes"].([]any)
			if !ok {
				return fmt.Errorf("backup: invalid restored service volumes")
			}
			for _, raw := range volumes {
				volume, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("backup: invalid restored service volume")
				}
				if volume["name"] == item.source.Volume {
					volume["generation"] = generation
					found = true
				}
			}
		}
		if !found {
			return fmt.Errorf("backup: restored service binding disappeared")
		}
		raw, err := json.Marshal(services)
		if err != nil {
			return err
		}
		sealed, err := serviceconfig.Seal(string(raw))
		if err != nil {
			return err
		}
		item.crewRow["services_json"] = sealed
	}
	return nil
}

func (p *serviceRestorePlan) insertFences(ctx context.Context, tx *sql.Tx) error {
	seen := map[string]bool{}
	for _, item := range p.items {
		if seen[item.target.Crew] {
			continue
		}
		seen[item.target.Crew] = true
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return err
		}
		token := fmt.Sprintf("%x", random[:])
		expectedBody, _ := item.crewRow["services_json"].(string)
		var actualBody, actualWorkspace string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(services_json,''),workspace_id FROM crews WHERE id=?`, item.target.Crew).Scan(&actualBody, &actualWorkspace); err != nil {
			return err
		}
		expectedWorkspace, _ := item.crewRow["workspace_id"].(string)
		if actualBody != expectedBody || actualWorkspace != expectedWorkspace {
			return fmt.Errorf("backup: restored quota owner/generation did not land exactly")
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO service_backup_fences(crew_id,workspace_id,token,created_at,operation,producer_until)
   SELECT id,workspace_id,?,?,'restore',? FROM crews WHERE id=?`, token, tsformat.Format(time.Now()), tsformat.Format(time.Now().Add(2*time.Minute)), item.target.Crew)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("backup: restored quota owner fence did not land")
		}
		p.fences = append(p.fences, serviceBackupFence{item.target.Crew, token})
		for _, bound := range p.items {
			if bound.target.Crew != item.target.Crew {
				continue
			}
			var state string
			var version int64
			if err := tx.QueryRowContext(ctx, `SELECT desired_state,version FROM service_runtime_intents WHERE crew_id=? AND service_name=?`, bound.target.Crew, bound.target.Service).Scan(&state, &version); err != nil {
				return err
			}
			if state != bound.source.DesiredState || version != bound.source.IntentVersion {
				return fmt.Errorf("backup: restored service intent did not land exactly")
			}
		}

		if _, err = tx.ExecContext(ctx, `UPDATE service_runtime_intents SET lease_owner='',lease_until='',next_attempt_at='',observed_state='pending',last_error='' WHERE crew_id=?`, item.target.Crew); err != nil {
			return err
		}
	}
	return nil
}

func (p *serviceRestorePlan) importImages(ctx context.Context, runtime ServiceSnapshotRuntime) (retErr error) {
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, p.removeUncommittedImports(ctx, runtime))
		}
	}()
	if len(p.items) == 0 {
		return nil
	}
	if runtime == nil || runtime.QuotaSnapshotNamespace() == "" {
		return fmt.Errorf("backup: quota service restore transport unavailable")
	}
	for i := range p.items {
		item := &p.items[i]
		file, err := p.payload.storageOrDefault().Open(ctx, p.payload.serviceImages[item.source.name()])
		if err != nil {
			return err
		}
		err = runtime.ImportQuotaVolume(ctx, item.target, item.source.Bytes, io.LimitReader(file, item.source.Bytes))
		closeErr := file.Close()
		if err != nil {
			return err
		}
		item.imported = true
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

// Prepare replacement targets before the SQL restore writer transaction. New
// identities need no old-writer drain; existing identities require --replace.
func (p *serviceRestorePlan) fenceExistingTargets(ctx context.Context, db *sql.DB, runtime ServiceSnapshotRuntime, replace, recover bool, scope Scope, register func(serviceBackupFence)) error {
	if p.payload.DBDump == nil {
		return nil
	}
	type currentCrew struct{ id, slug, body string }
	var existing []currentCrew
	if replace {
		rows, err := db.QueryContext(ctx, `SELECT c.id,c.slug,COALESCE(c.services_json,'') FROM crews c JOIN workspaces w ON w.id=c.workspace_id WHERE w.id=? OR w.slug=?`, firstWorkspaceID(p.payload.DBDump), firstWorkspaceSlug(p.payload.DBDump))
		if err != nil {
			return err
		}
		for rows.Next() {
			var crew currentCrew
			if err = rows.Scan(&crew.id, &crew.slug, &crew.body); err != nil {
				rows.Close()
				return err
			}
			existing = append(existing, crew)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	} else {
		seen := map[string]bool{}
		for _, item := range p.items {
			if seen[item.target.Crew] {
				continue
			}
			seen[item.target.Crew] = true
			var crew currentCrew
			err := db.QueryRowContext(ctx, `SELECT id,slug,COALESCE(services_json,'') FROM crews WHERE id=?`, item.target.Crew).Scan(&crew.id, &crew.slug, &crew.body)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("backup: quota service identity already exists; use an explicit replacement or fork")
		}
	}
	targets := map[string]bool{}
	for _, item := range p.items {
		targets[item.target.Crew] = true
	}
	for _, crew := range existing {
		current, err := declaredServiceSnapshots(crew.body, crew.id, crew.slug)
		if err != nil {
			return err
		}
		if len(current) == 0 {
			continue
		}
		if scope == ScopeCrew && !targets[crew.id] {
			return fmt.Errorf("backup: crew replacement would affect another quota service owner; use a fork")
		}
		if runtime == nil {
			return fmt.Errorf("backup: prior quota services require restore transport")
		}
		detach, ok := runtime.(interface {
			DetachQuotaService(context.Context, string, string) error
		})
		if !ok {
			return fmt.Errorf("backup: quota restore cannot detach prior service generations")
		}
		token, err := servicelifecycle.BeginBackupFence(ctx, db, crew.id, "restore")
		if err != nil && recover {
			token, err = servicelifecycle.AdoptBackupFence(ctx, db, crew.id, "restore")
		}
		if err != nil {
			return err
		}
		ownedFence := serviceBackupFence{crew.id, token}
		p.oldFences = append(p.oldFences, ownedFence)
		if register != nil {
			register(ownedFence)
		}
		services := map[string]bool{}
		for _, spec := range current {
			if services[spec.Service] {
				continue
			}
			services[spec.Service] = true
			if err = runtime.StopCrewService(ctx, crew.id, crew.slug, spec.Service); err != nil {
				return err
			}
			if err = detach.DetachQuotaService(ctx, crew.id, spec.Service); err != nil {
				return err
			}
		}
	}
	return nil
}

// dropOldFences is inside the same SQLite writer transaction as replacement.
// Other processes see the old fence until the transaction atomically installs
// the new crew/intent/data-generation binding and its fresh restore fence.
func (p *serviceRestorePlan) dropOldFences(ctx context.Context, tx *sql.Tx) error {
	for _, fence := range p.oldFences {
		result, err := tx.ExecContext(ctx, `DELETE FROM service_backup_fences WHERE crew_id=? AND token=?`, fence.crew, fence.token)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return servicelifecycle.ErrBackupMaintenance
		}
	}
	return nil
}

func (p *serviceRestorePlan) renewCommitFences(ctx context.Context, tx *sql.Tx) error {
	for _, fence := range p.fences {
		result, err := tx.ExecContext(ctx, `UPDATE service_backup_fences SET producer_until=? WHERE crew_id=? AND token=?`, tsformat.Format(time.Now().Add(2*time.Minute)), fence.crew, fence.token)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return servicelifecycle.ErrBackupMaintenance
		}
	}
	return nil
}

// Only fresh target generations successfully imported by this attempt are
// eligible for rollback. Never remove source images or committed targets.
func (p *serviceRestorePlan) removeUncommittedImports(ctx context.Context, runtime ServiceSnapshotRuntime) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	var result error
	for i := range p.items {
		item := &p.items[i]
		if !item.imported {
			continue
		}
		remover, ok := runtime.(interface {
			RemoveQuotaVolume(context.Context, quota.Key) error
		})
		if !ok {
			result = errors.Join(result, fmt.Errorf("backup: import transport cannot remove uncommitted quota generation"))
			continue
		}
		if err := remover.RemoveQuotaVolume(cleanupCtx, item.target); err != nil {
			result = errors.Join(result, err)
			continue
		}
		item.imported = false
	}
	return result
}

func (p *ExtractedPayload) openServiceConfig(raw string) (string, error) {
	if p.serviceConfigKeys != nil {
		return serviceconfig.OpenWithKeys(raw, p.serviceConfigKeys)
	}
	return serviceconfig.Open(raw)
}
