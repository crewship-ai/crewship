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
	"errors"
	"fmt"
	"github.com/crewship-ai/crewship/internal/memory"
	"io"
	"os"
	"path/filepath"
	"slices"
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
// bundle into the staging store, and says what is missing. The layers are
// copied encrypted, as the store holds them; the keys that open them ride
// the workspaces' environments archives (envArchives). A drill only checks
// — that every layer is there and opens with the key the bundle carries —
// and its directory is thrown away.
func stageRecoveredEnvironmentLayers(ctx context.Context, rep *RecoverReport, m *Manifest, opts RecoverOptions, dataDir string, envArchives []string) error {
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
				if opts.Force {
					return fmt.Errorf("backup: required environment layer %s is missing", d)
				}
				missing++
				continue
			}
			if opts.Drill {
				continue
			}
			rc, _, err := src.Open(ctx, d)
			if err != nil {
				if opts.Force || resourceFailure(err) {
					return err
				}
				missing++
				continue
			}
			_, n, err := staged.Put(&restoreReadGuard{ctx: ctx, r: rc, b: readBudget(ctx), decoded: true, generated: true}, d)
			_ = rc.Close()
			if err != nil {
				if opts.Force || resourceFailure(err) {
					return err
				}
				missing++
				continue
			}
			copied++
			bytes += n
		}
	}
	if opts.Force {
		layers := staged
		if opts.Drill {
			layers = src
		}
		if err := validateForceEnvironments(ctx, m.Contents.Environments, layers, envArchives); err != nil {
			return err
		}
	} else if opts.Drill {
		if err := checkRecoveredEnvironmentKeys(ctx, rep, src, envArchives); err != nil {
			return err
		}
	}
	dir := filepath.Join(rep.DataDir, RecoveredEnvironmentsDir)
	if missing > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d environment layer file(s) are neither in the bundle nor in %s: the crews they belong to get their data back and their environment rebuilt from the crew image", missing, src.Dir))
	}
	if !opts.Drill {
		rep.Notes = append(rep.Notes, fmt.Sprintf("%d crew environment(s) are staged under %s (%d layer file(s), %s copied); once the server runs with Docker, land them with `crewship admin instance backups environments land` — processes start fresh, nothing that was only in memory comes back", len(m.Contents.Environments), dir, copied, humanBytes(bytes)))
	}
	return ctx.Err()
}

// Verify each environment record against the manifest and read its layers
// through the same keys and store paths that the landing operation will use.
func validateForceEnvironments(ctx context.Context, summaries []EnvironmentSummary, store *EnvironmentStore, archives []string) error {
	remaining := make(map[string]EnvironmentSummary, len(summaries))
	for _, summary := range summaries {
		if _, ok := remaining[summary.ID]; ok {
			return errors.New("backup: duplicate environment in the manifest")
		}
		remaining[summary.ID] = summary
	}
	for _, archive := range archives {
		records, err := extractArchive(ctx, archive)
		if err != nil {
			return err
		}
		err = validateForceEnvironmentArchive(ctx, records, store, remaining)
		if err = errors.Join(err, records.Close()); err != nil {
			return err
		}
	}
	if len(remaining) > 0 {
		return errors.New("backup: declared environment missing from staged archives")
	}
	return nil
}

func validateForceEnvironmentArchive(ctx context.Context, records *ExtractedPayload, store *EnvironmentStore, remaining map[string]EnvironmentSummary) error {
	for _, slug := range records.Environments() {
		env := records.EnvironmentBySlug[slug]
		want, ok := remaining[env.ID]
		if !ok || env.Crew != want.Crew || env.Platform.String() != want.Platform || env.BlobBytes() != want.Bytes || !slices.Equal(env.Blobs(), want.Blobs) {
			return fmt.Errorf("backup: environment %s differs from its manifest record", slug)
		}
		delete(remaining, env.ID)
		layers := store
		if want.Inline {
			layers = records.InlineEnvironmentBlobs()
		}
		raw, _ := BlobSources(layers)
		open := EnvironmentBlobOpener(env, records.EnvironmentKeys(), raw)
		checked := map[string]bool{}
		for _, entry := range env.Image.Entries {
			if entry.Type != "file" || checked[entry.Digest] {
				continue
			}
			rc, _, err := open(ctx, entry.Digest)
			if err != nil {
				return err
			}
			digest, n, err := hashAll(&restoreReadGuard{ctx: ctx, r: rc, b: readBudget(ctx), decoded: true})
			if err = errors.Join(err, rc.Close()); err != nil {
				return err
			}
			if "sha256:"+digest != entry.Digest || n != entry.Size {
				return fmt.Errorf("backup: invalid environment layer for %s", slug)
			}
			checked[entry.Digest] = true
		}
	}
	return nil
}

// checkRecoveredEnvironmentKeys proves, for a drill, that the keys the
// bundle carries open the encrypted layers in the store beside it: every
// object's first chunk is decrypted (the whole object when it is one chunk).
func checkRecoveredEnvironmentKeys(ctx context.Context, rep *RecoverReport, src *EnvironmentStore, envArchives []string) error {
	var checked, locked int
	for _, a := range envArchives {
		envEx, err := extractArchive(ctx, a)
		if err != nil {
			if resourceFailure(err) {
				return err
			}
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("the environments archive %s does not open: %v", filepath.Base(a), err))
			continue
		}
		keys := envEx.EnvironmentKeys()
		for _, slug := range envEx.Environments() {
			env := envEx.EnvironmentBySlug[slug]
			seen := map[string]bool{}
			for _, e := range env.Image.Entries {
				if e.Object == "" || seen[e.Object] || !src.Has(e.Object) {
					continue
				}
				seen[e.Object] = true
				if err := src.CheckSealed(ctx, e.Object, e.Digest, keys); err != nil {
					if resourceFailure(err) {
						_ = envEx.Close()
						return err
					}
					locked++
					continue
				}
				checked++
			}
		}
		_ = envEx.Close()
	}
	if locked > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d encrypted environment layer file(s) do not open with the key the bundle carries: those crews get their data back and their environment rebuilt from the crew image", locked))
	}
	if checked > 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("%d encrypted environment layer file(s) open with the key the bundle carries", checked))
	}
	return ctx.Err()
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
	ctx, release, limitErr := BeginRestoreRead(ctx)
	if limitErr != nil {
		return nil, limitErr
	}
	defer release()

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
		if err := reserveRestoreGenerated(ctx, int64(len(b))); err != nil {
			return rep, err
		}
		if err := memory.WriteFileDurable(filepath.Join(dir, "landed.json"), b, 0o600); err != nil {
			return rep, err
		}
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
		ro := EnvironmentRestoreOptions{Open: open, Has: has, Keys: envEx.EnvironmentKeys(), DryRun: opts.DryRun, Recreate: true, Name: env.Container}
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
		if err := restoreOperationError(ctx); err != nil {
			return out, err
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
	return ExtractPayload(ctx, f, filepath.Dir(path))
}

// landTimeout bounds one land request's Docker work per environment set.
const landTimeout = 30 * time.Minute

// LandTimeout is how long a land request may run.
func LandTimeout() time.Duration { return landTimeout }
