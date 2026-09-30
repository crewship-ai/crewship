package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/crewship-ai/crewship/internal/quota"
	"github.com/crewship-ai/crewship/internal/serviceconfig"
	"github.com/crewship-ai/crewship/internal/servicelifecycle"
)

const serviceSnapshotsPrefix = "service-snapshots/"

// ServiceSnapshotRuntime is implemented by the host Docker provider, rather
// than a tenant-selectable path or container supplied in a bundle.
type ServiceSnapshotRuntime interface {
	QuotaSnapshotNamespace() string
	StopCrewService(context.Context, string, string, string) error
	ExportQuotaVolume(context.Context, quota.Key, int64, io.Writer) error
	ImportQuotaVolume(context.Context, quota.Key, int64, io.Reader) error
}

type serviceSnapshot struct {
	Namespace     string `json:"namespace" yaml:"namespace"`
	CrewID        string `json:"crew_id" yaml:"crew_id"`
	CrewSlug      string `json:"crew_slug" yaml:"crew_slug"`
	Service       string `json:"service" yaml:"service"`
	Volume        string `json:"volume" yaml:"volume"`
	Generation    int64  `json:"generation" yaml:"generation"`
	Bytes         int64  `json:"bytes" yaml:"bytes"`
	SHA256        string `json:"sha256" yaml:"sha256"`
	DesiredState  string `json:"desired_state" yaml:"desired_state"`
	IntentVersion int64  `json:"intent_version" yaml:"intent_version"`
}

func (s serviceSnapshot) key() quota.Key {
	return quota.Key{Crew: s.CrewID, Service: s.Service, Volume: s.Volume, Generation: s.Generation}
}
func (s serviceSnapshot) name() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", s.CrewID, s.Service, s.Volume, s.Generation)))
	return hex.EncodeToString(sum[:])
}

type snapshotDeclaration struct {
	Name    string `json:"name" yaml:"name"`
	Quota   bool   `json:"quota_enforced" yaml:"quota_enforced"`
	Volumes []struct {
		Name       string `json:"name" yaml:"name"`
		Mount      string `json:"mount" yaml:"mount"`
		Bytes      int64  `json:"quota_bytes" yaml:"quota_bytes"`
		Generation int64  `json:"generation" yaml:"generation"`
	} `json:"volumes" yaml:"volumes"`
}

func declaredServiceSnapshots(body, crew, slug string) ([]serviceSnapshot, error) {
	plain, err := serviceconfig.Open(body)
	if err != nil {
		return nil, err
	}
	if plain == "" {
		return nil, nil
	}
	var specs []snapshotDeclaration
	if err = json.Unmarshal([]byte(plain), &specs); err != nil {
		return nil, err
	}
	var snapshots []serviceSnapshot
	seen := map[string]bool{}
	for _, svc := range specs {
		if !svc.Quota {
			continue
		}
		for _, v := range svc.Volumes {
			gen := v.Generation
			if gen == 0 {
				gen = 1
			}
			if quota.ValidateVolume(true, svc.Name, v.Name, v.Mount, gen, v.Bytes) != nil {
				return nil, quota.ErrDenied
			}
			item := serviceSnapshot{CrewID: crew, CrewSlug: slug, Service: svc.Name, Volume: v.Name, Generation: gen, Bytes: v.Bytes}
			if seen[item.name()] {
				return nil, quota.ErrDenied
			}
			seen[item.name()] = true
			snapshots = append(snapshots, item)
		}
	}
	return snapshots, nil
}

type serviceBackupFence struct{ crew, token string }

// captureServiceSnapshots leaves every acquired fence intact on failure. This
// intentionally requires explicit operator recovery instead of restarting a
// database whose capture may have failed midway through a consistent snapshot.
func captureServiceSnapshots(ctx context.Context, db *sql.DB, runtime ServiceSnapshotRuntime, w *TarZstWriter, crews []CrewTarget, now time.Time, recoverMaintenance bool, register func(serviceBackupFence)) ([]serviceBackupFence, int, error) {
	var fences []serviceBackupFence
	count := 0
	for _, crew := range crews {
		var body string
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(services_json,'') FROM crews WHERE id=?`, crew.ID).Scan(&body); err != nil {
			return fences, count, err
		}
		snapshots, err := declaredServiceSnapshots(body, crew.ID, crew.Slug)
		if err != nil {
			return fences, count, err
		}
		if len(snapshots) == 0 {
			continue
		}
		if runtime == nil || runtime.QuotaSnapshotNamespace() == "" {
			return fences, count, fmt.Errorf("backup: quota service snapshot transport unavailable")
		}
		token, err := servicelifecycle.BeginBackupFence(ctx, db, crew.ID, "backup")
		if err != nil && recoverMaintenance {
			token, err = servicelifecycle.AdoptBackupFence(ctx, db, crew.ID, "backup")
		}
		if err != nil {
			return fences, count, fmt.Errorf("%w: inspect backup status; only after the producer stops retry with --recover-services", err)
		}
		fence := serviceBackupFence{crew.ID, token}
		fences = append(fences, fence)
		if register != nil {
			register(fence)
		}
		// Re-read after the atomic mutation fence closes the declaration race.
		if err = db.QueryRowContext(ctx, `SELECT COALESCE(services_json,'') FROM crews WHERE id=?`, crew.ID).Scan(&body); err != nil {
			return fences, count, err
		}
		snapshots, err = declaredServiceSnapshots(body, crew.ID, crew.Slug)
		if err != nil {
			return fences, count, err
		}
		stopped := map[string]bool{}
		for _, snapshot := range snapshots {
			snapshot.Namespace = runtime.QuotaSnapshotNamespace()
			if err = db.QueryRowContext(ctx, `SELECT desired_state,version FROM service_runtime_intents WHERE crew_id=? AND service_name=?`, crew.ID, snapshot.Service).Scan(&snapshot.DesiredState, &snapshot.IntentVersion); err != nil {
				return fences, count, fmt.Errorf("backup: quota service requires durable intent: %w", err)
			}
			if !stopped[snapshot.Service] {
				if err = runtime.StopCrewService(ctx, crew.ID, crew.Slug, snapshot.Service); err != nil {
					return fences, count, err
				}
				stopped[snapshot.Service] = true
			}
			reader, writer := io.Pipe()
			hasher := sha256.New()
			done := make(chan error, 1)
			go func() {
				exportErr := runtime.ExportQuotaVolume(ctx, snapshot.key(), snapshot.Bytes, io.MultiWriter(writer, hasher))
				_ = writer.CloseWithError(exportErr)
				done <- exportErr
			}()
			writeErr := w.WriteStream(serviceSnapshotsPrefix+snapshot.name()+".ext4", 0600, now, snapshot.Bytes, reader)
			_ = reader.CloseWithError(writeErr)
			exportErr := <-done
			if writeErr != nil {
				return fences, count, writeErr
			}
			if exportErr != nil {
				return fences, count, exportErr
			}
			snapshot.SHA256 = hex.EncodeToString(hasher.Sum(nil))
			raw, _ := json.Marshal(snapshot)
			if err = w.WriteFile(serviceSnapshotsPrefix+snapshot.name()+".json", 0600, now, raw); err != nil {
				return fences, count, err
			}
			count++
		}
	}
	return fences, count, nil
}
