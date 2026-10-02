package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/crewship-ai/crewship/internal/quota"
)

type instanceServiceLanding struct {
	Items  []instanceServiceLandingItem  `json:"items"`
	Fences []instanceServiceLandingFence `json:"fences"`
}
type instanceServiceLandingItem struct {
	Source serviceSnapshot `json:"source"`
	Target quota.Key       `json:"target"`
	Image  string          `json:"image"`
}
type instanceServiceLandingFence struct {
	Crew  string `json:"crew"`
	Token string `json:"token"`
}

const instanceServicePlanFile = "landing.json"

func writeInstanceServicePlan(payload *ExtractedPayload, plan *serviceRestorePlan, dataDir string) (string, error) {
	record := instanceServiceLanding{}
	for _, item := range plan.items {
		record.Items = append(record.Items, instanceServiceLandingItem{Source: item.source, Target: item.target, Image: filepath.Base(payload.serviceImages[item.source.name()])})
	}
	for _, fence := range plan.fences {
		record.Fences = append(record.Fences, instanceServiceLandingFence{fence.crew, fence.token})
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp(filepath.Join(dataDir, RecoveredServicesDir), ".landing-*.json")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err = errors.Join(err, closeErr); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err = syncServiceLandingDirectory(filepath.Dir(path)); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

type ServiceLandingOptions struct {
	DryRun   bool
	Finalize func(context.Context, *sql.Tx, int) error
}

// LandRecoveredServices is called against the recovered instance and its new
// host helper. The archive's namespace is provenance, never a destination.
func LandRecoveredServices(ctx context.Context, db *sql.DB, dataDir string, runtime ServiceSnapshotRuntime, options ...ServiceLandingOptions) (int, error) {
	var opts ServiceLandingOptions
	if len(options) > 0 {
		opts = options[0]
	}
	dir := filepath.Join(dataDir, RecoveredServicesDir)
	plan, record, err := loadServiceLandingPlan(ctx, db, dir)
	if err != nil {
		return 0, err
	}
	payload := plan.payload
	if opts.DryRun {
		return len(plan.items), nil
	}
	if runtime == nil || runtime.QuotaSnapshotNamespace() == "" {
		return 0, fmt.Errorf("backup: quota service restore transport unavailable")
	}
	captureCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = captureCtx
	keeper := &serviceFenceKeeper{db: db}
	// Claim all existing epochs atomically, without replacing their tokens. A
	// second landing or explicit maintenance recovery cannot race this producer.
	claim, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	for _, fence := range record.Fences {
		result, e := claim.ExecContext(ctx, `UPDATE service_backup_fences SET producer_until=? WHERE crew_id=? AND token=? AND operation='restore' AND producer_until<=?`, tsformat.Format(time.Now().Add(2*time.Minute)), fence.Crew, fence.Token, tsformat.Format(time.Now()))
		if e != nil {
			claim.Rollback()
			return 0, e
		}
		n, e := result.RowsAffected()
		if e != nil || n != 1 {
			claim.Rollback()
			return 0, fmt.Errorf("backup: another service landing owns maintenance")
		}
		keeper.add(serviceBackupFence{fence.Crew, fence.Token})
	}
	if err = claim.Commit(); err != nil {
		return 0, err
	}
	go keeper.run(ctx, cancel)
	for _, item := range plan.items {
		image, err := payload.storageOrDefault().Open(ctx, payload.serviceImages[item.source.name()])
		if err != nil {
			return 0, err
		}
		importErr := runtime.ImportQuotaVolume(ctx, item.target, item.source.Bytes, io.LimitReader(image, item.source.Bytes))
		closeErr := image.Close()
		if closeErr != nil {
			return 0, errors.Join(importErr, closeErr)
		}
		if importErr != nil {
			if err := ctx.Err(); err != nil {
				return 0, errors.Join(importErr, err)
			}
			// A retry may find an already imported prefix. Accept it only after a
			// fresh offline export proves its exact bytes, never merely its identity.
			hash := sha256.New()
			exportErr := runtime.ExportQuotaVolume(ctx, item.target, item.source.Bytes, hash)
			if exportErr != nil || hex.EncodeToString(hash.Sum(nil)) != item.source.SHA256 {
				return 0, errors.Join(importErr, exportErr)
			}
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, fence := range record.Fences {
		result, err := tx.ExecContext(ctx, `DELETE FROM service_backup_fences WHERE crew_id=? AND token=? AND operation='restore' AND producer_until>?`, fence.Crew, fence.Token, tsformat.Format(time.Now()))
		if err != nil {
			return 0, err
		}
		n, err := result.RowsAffected()
		if err != nil || n != 1 {
			return 0, fmt.Errorf("backup: service landing lost its fenced epoch")
		}
	}
	if opts.Finalize != nil {
		if err = opts.Finalize(ctx, tx, len(plan.items)); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(plan.items), nil
}

// Pending plans may survive a committed database transaction whose final
// rename failed. Never promote one until all its images, declarations and
// restore epochs have been verified against the recovered database.
func loadServiceLandingPlan(ctx context.Context, db *sql.DB, dir string) (*serviceRestorePlan, *instanceServiceLanding, error) {
	path := filepath.Join(dir, instanceServicePlanFile)
	plan, record, err := readServiceLandingPlan(ctx, db, dir, path)
	if err == nil {
		if err = syncServiceLandingDirectory(dir); err != nil {
			return nil, nil, err
		}
		return plan, record, nil
	}
	if !os.IsNotExist(err) {
		return nil, nil, err
	}
	missing := err
	candidates, err := filepath.Glob(filepath.Join(dir, ".landing-*.json"))
	if err != nil {
		return nil, nil, err
	}
	if len(candidates) > 16 {
		return nil, nil, fmt.Errorf("backup: too many pending service plans")
	}
	selected := ""
	for _, candidate := range candidates {
		candidatePlan, candidateRecord, validateErr := readServiceLandingPlan(ctx, db, dir, candidate)
		if validateErr != nil || len(candidatePlan.items) == 0 {
			continue
		}
		if selected != "" {
			return nil, nil, fmt.Errorf("backup: ambiguous committed service plans")
		}
		selected, plan, record = candidate, candidatePlan, candidateRecord
	}
	if selected == "" {
		return nil, nil, missing
	}
	if err = os.Rename(selected, path); err != nil {
		// A concurrent retry may have published the same committed intent already.
		concurrentPlan, concurrentRecord, readErr := readServiceLandingPlan(ctx, db, dir, path)
		if readErr != nil {
			return nil, nil, err
		}
		plan, record = concurrentPlan, concurrentRecord
	}
	if err = syncServiceLandingDirectory(dir); err != nil {
		return nil, nil, err
	}
	return plan, record, nil
}

func syncServiceLandingDirectory(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func readServiceLandingPlan(ctx context.Context, db *sql.DB, dir, path string) (*serviceRestorePlan, *instanceServiceLanding, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	var record instanceServiceLanding
	decoder := json.NewDecoder(io.LimitReader(file, 8<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&record); err != nil {
		return nil, nil, err
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, nil, fmt.Errorf("backup: trailing service landing data")
	}
	if len(record.Items) > 4096 {
		return nil, nil, fmt.Errorf("backup: excessive service landing records")
	}
	payload := &ExtractedPayload{storage: LocalStorageOps{}, tempDir: dir, serviceImages: map[string]string{}, serviceMetadata: map[string]serviceSnapshot{}}
	for _, item := range record.Items {
		if item.Target.Crew != item.Source.CrewID || item.Target.Service != item.Source.Service || item.Target.Volume != item.Source.Volume || item.Target.Generation == item.Source.Generation || quota.Validate(item.Target, item.Source.Bytes) != nil {
			return nil, nil, fmt.Errorf("backup: invalid service landing target")
		}
		if filepath.Base(item.Image) != item.Image {
			return nil, nil, fmt.Errorf("backup: unsafe service image path")
		}
		meta := item.Source
		meta.Generation = item.Target.Generation
		if _, exists := payload.serviceMetadata[meta.name()]; exists {
			return nil, nil, fmt.Errorf("backup: duplicate service landing target")
		}
		payload.serviceMetadata[meta.name()] = meta
		payload.serviceImages[meta.name()] = filepath.Join(dir, item.Image)
	}
	dump, err := instanceServiceDump(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	payload.DBDump = dump
	plan, err := payload.prepareServiceRestorePlan(ctx, len(record.Items))
	if err != nil {
		return nil, nil, err
	}
	for i := range plan.items {
		plan.items[i].target = plan.items[i].source.key()
	}
	crews := map[string]bool{}
	for _, item := range plan.items {
		crews[item.target.Crew] = true
	}
	tokens := map[string]string{}
	for _, fence := range record.Fences {
		if !crews[fence.Crew] || tokens[fence.Crew] != "" || len(fence.Token) != 32 {
			return nil, nil, fmt.Errorf("backup: invalid service landing maintenance")
		}
		tokens[fence.Crew] = fence.Token
		var matches int
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM service_backup_fences WHERE crew_id=? AND token=? AND operation='restore'`, fence.Crew, fence.Token).Scan(&matches); err != nil || matches != 1 {
			return nil, nil, fmt.Errorf("backup: recovered service maintenance changed")
		}
	}
	if len(tokens) != len(crews) {
		return nil, nil, fmt.Errorf("backup: missing service landing maintenance")
	}
	return plan, &record, nil
}
