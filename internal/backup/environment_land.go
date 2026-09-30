package backup

// Environments after an offline instance recover. `crewship recover` runs
// without a server and usually without Docker, so it only STAGES the crews'
// complete environments: the per-workspace records under
// <data dir>/restore-staging/environments/<ws>.tar.zst and the image layers
// they need under .../environments/blobs/ (copied from the environment
// store beside the bundle when the bundle does not carry them inline). Once
// the recovered server runs with Docker, LandStagedEnvironments loads them
// (POST /api/v1/admin/instance/backups/environments/land).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// stagedEnvironmentStore is the store recover stages layers into.
func stagedEnvironmentStore(dataDir string) *EnvironmentStore {
	return &EnvironmentStore{Dir: filepath.Join(dataDir, RecoveredEnvironmentsDir)}
}

// stageRecoveredEnvironmentLayers copies the layers the bundle's
// environments need (and does not carry inline) from the store beside the
// bundle into the staging store, and says what is missing. A drill only
// checks: its directory is thrown away.
func stageRecoveredEnvironmentLayers(rep *RecoverReport, m *Manifest, opts RecoverOptions, dataDir string) {
	staged := stagedEnvironmentStore(dataDir)
	src := EnvironmentStoreFor(filepath.Dir(opts.BundlePath))
	var missing, copied int
	var bytes int64
	for _, e := range m.Contents.Environments {
		if e.Inline {
			continue // inside the workspace's environments archive
		}
		for _, d := range e.Blobs {
			if staged.Has(d) {
				continue
			}
			if !src.Has(d) {
				missing++
				continue
			}
			if opts.Drill {
				continue
			}
			rc, _, err := src.Open(context.Background(), d)
			if err != nil {
				missing++
				continue
			}
			_, n, err := staged.Put(rc, d)
			_ = rc.Close()
			if err != nil {
				missing++
				continue
			}
			copied++
			bytes += n
		}
	}
	dir := filepath.Join(dataDir, RecoveredEnvironmentsDir)
	if missing > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d environment layer file(s) are neither in the bundle nor in %s: the crews they belong to get their data back and their environment rebuilt from the crew image", missing, src.Dir))
	}
	if !opts.Drill {
		rep.Notes = append(rep.Notes, fmt.Sprintf("%d crew environment(s) are staged under %s (%d layer file(s), %s copied); once the server runs with Docker, land them with `crewship admin instance backups environments land` — processes start fresh, nothing that was only in memory comes back", len(m.Contents.Environments), dir, copied, humanBytes(bytes)))
	}
}

// LandOptions configure LandStagedEnvironments.
type LandOptions struct {
	// DataDir is the recovered server's data directory.
	DataDir string
	// Store is the server's own environment store (layers already there
	// are not needed from staging). Optional.
	Store  *EnvironmentStore
	DryRun bool
}

// LandedEnvironment is one environment's outcome, with its workspace.
type LandedEnvironment struct {
	Workspace string `json:"workspace"`
	EnvironmentOutcome
}

// LandReport is the answer to POST …/environments/land.
type LandReport struct {
	Dir          string              `json:"dir"`
	Staged       bool                `json:"staged"`
	DryRun       bool                `json:"dry_run"`
	Environments []LandedEnvironment `json:"environments"`
}

// LandStagedEnvironments loads every staged environment: each workspace's
// records and inline layers from restore-staging/environments/<ws>.tar.zst,
// other layers from the staging store and the server's own store. A managed
// crew's image is loaded and tagged; an unmanaged container is recreated
// with its sanitized configuration and mount content (from the workspace's
// crew archive in restore-staging/crews). Each environment reports restored,
// rebuilt or skipped; nothing here fails another crew's landing.
func LandStagedEnvironments(ctx context.Context, ops DockerOps, opts LandOptions) (*LandReport, error) {
	dir := filepath.Join(opts.DataDir, RecoveredEnvironmentsDir)
	rep := &LandReport{Dir: dir, DryRun: opts.DryRun, Environments: []LandedEnvironment{}}
	archives, err := filepath.Glob(filepath.Join(dir, "*"+instanceCrewsSuffix))
	if err != nil {
		return nil, err
	}
	sort.Strings(archives)
	if len(archives) == 0 {
		return rep, nil
	}
	rep.Staged = true
	staged := stagedEnvironmentStore(opts.DataDir)
	for _, a := range archives {
		ws := strings.TrimSuffix(filepath.Base(a), instanceCrewsSuffix)
		outs, err := landWorkspace(ctx, ops, opts, staged, a, ws)
		if err != nil {
			return rep, fmt.Errorf("backup: land environments of %s: %w", ws, err)
		}
		rep.Environments = append(rep.Environments, outs...)
	}
	if !opts.DryRun {
		b, _ := json.MarshalIndent(rep, "", "  ")
		_ = os.WriteFile(filepath.Join(dir, "landed.json"), b, 0o600)
	}
	return rep, nil
}

func landWorkspace(ctx context.Context, ops DockerOps, opts LandOptions, staged *EnvironmentStore, archive, ws string) ([]LandedEnvironment, error) {
	envEx, err := extractArchive(ctx, archive)
	if err != nil {
		return nil, err
	}
	defer func() { _ = envEx.Close() }()
	// Mount content rides the workspace's crew archive.
	var crewEx *ExtractedPayload
	crews := filepath.Join(opts.DataDir, RecoveredCrewsDir, ws+instanceCrewsSuffix)
	if _, err := os.Stat(crews); err == nil {
		crewEx, err = extractArchive(ctx, crews)
		if err != nil {
			return nil, err
		}
		defer func() { _ = crewEx.Close() }()
	}
	open, has := BlobSources(envEx.InlineEnvironmentBlobs(), staged, opts.Store)
	var out []LandedEnvironment
	for _, slug := range envEx.Environments() {
		env := envEx.EnvironmentBySlug[slug]
		ro := EnvironmentRestoreOptions{Open: open, Has: has, DryRun: opts.DryRun, Recreate: true, Name: env.Container}
		if crewEx != nil {
			ro.MountData = func(ctx context.Context, m EnvironmentMount) (io.ReadCloser, bool, error) {
				return crewEx.OpenEnvironmentMount(ctx, slug, m)
			}
		}
		var o EnvironmentOutcome
		if ops == nil {
			o = EnvironmentOutcome{Crew: env.Crew, Result: EnvSkipped, Reason: "no Docker on this server", Unsafe: env.UnsafeSentences()}
		} else {
			o = RestoreEnvironment(ctx, ops, env, ro)
		}
		out = append(out, LandedEnvironment{Workspace: ws, EnvironmentOutcome: o})
	}
	return out, nil
}

func extractArchive(ctx context.Context, path string) (*ExtractedPayload, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ExtractPayload(ctx, f)
}

// landTimeout bounds one land request's Docker work per environment set.
const landTimeout = 30 * time.Minute

// LandTimeout is how long a land request may run.
func LandTimeout() time.Duration { return landTimeout }
