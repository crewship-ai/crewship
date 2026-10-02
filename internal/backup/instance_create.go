package backup

// Instance-scope bundles: the whole Crewship in one file.
//
// A workspace bundle is a logical dump — rows filtered to one workspace and
// re-inserted on restore. An instance bundle is physical: a page-level
// snapshot of the WHOLE database (SQLite's online backup API, the mechanism
// the pre-migration snapshot uses), every file store the server keeps next to
// it, and every crew container's files, taken in one quiet window so they
// agree with each other. Restore (`crewship recover`) writes them into an
// empty data directory offline and resets what must not survive a move:
// sessions, locks, leases, auth keys — and holds every automation until an
// admin resumes it.
//
// Payload layout (inside the age seal):
//
//	instance/db.sqlite                 the database snapshot
//	instance/index.json                every copied file with its size and sha256
//	instance/files/<store>/<rel path>  one tree per file store (see InstanceStores)
//	instance/crews/<workspace id>.tar.zst  that workspace's crew containers, in
//	                                   the workspace bundle's crew layout
//	recovery-kit/keys.json             vault keys, only when the kit is on
//
// Staging never holds plaintext: the database snapshot is taken into memory
// (database.SnapshotToMemory) and serialized straight into the sealed
// payload, and every staged file (store copies, crew and environment
// archives) is encrypted with a per-run key held only in memory
// (staging_cipher.go). The staging directory is removed on success and on
// failure, and swept at the next start after a crash.
//
// Tables the workspace bundles exclude (sessions, locks, leases, runtime
// state, instance bookkeeping) are in the snapshot because the snapshot is
// the database file; RecoverInstance clears the runtime ones afterwards.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/safepath"
	"github.com/crewship-ai/crewship/internal/servicelifecycle"
)

// File stores an instance bundle carries, by the name they have in the
// payload and in a recovered data directory.
const (
	// StoreOutput is the storage root: attachments/<ws>/<sha[:2]>/<sha>,
	// crew output binds and workspace files (DataDir.OutputDir).
	StoreOutput = "output"
	// StoreMemory is the memory root: workspace memory trees and the
	// content-addressed memory version blobs under versions/.
	StoreMemory = "memory"
	// StorePageProjects is the protected page project store
	// (CREWSHIP_PAGE_PROJECTS_PATH).
	StorePageProjects = "page_projects"
	// StoreChats and StoreSkills are the data directory's chats/ and
	// skills/ trees.
	StoreChats  = "chats"
	StoreSkills = "skills"
)

// InstanceStores is every store in payload order.
var InstanceStores = []string{StoreOutput, StoreMemory, StorePageProjects, StoreChats, StoreSkills}

// RecoveredStoreDir is where a store lands under a recovered data directory.
func RecoveredStoreDir(dataDir, store string) string {
	switch store {
	case StorePageProjects:
		return filepath.Join(dataDir, "page-projects")
	default:
		return filepath.Join(dataDir, store)
	}
}

const (
	instanceDBEntry     = "instance/db.sqlite"
	instanceIndexEntry  = "instance/index.json"
	instanceFilesPrefix = "instance/files/"
	instanceCrewsPrefix = "instance/crews/"
	instanceCrewsSuffix = ".tar.zst"
	// instanceEnvPrefix holds one archive per workspace with its crews'
	// environment records (and inline blobs): instance/environments/<ws>.tar.zst.
	instanceEnvPrefix     = "instance/environments/"
	instanceStagingGlob   = ".staging-instance-"
	instanceStagingMaxAge = 6 * time.Hour
)

// InstancePaths are the file stores on THIS server. An empty path is a store
// the server does not keep (page projects unconfigured); it is recorded as
// such, never as missing.
type InstancePaths struct {
	Output       string
	Memory       string
	PageProjects string
	Chats        string
	Skills       string
}

func (p InstancePaths) byStore() map[string]string {
	return map[string]string{
		StoreOutput: p.Output, StoreMemory: p.Memory, StorePageProjects: p.PageProjects,
		StoreChats: p.Chats, StoreSkills: p.Skills,
	}
}

// InstanceContents is the manifest's contents.instance.
type InstanceContents struct {
	Workspaces []InstanceWorkspace     `json:"workspaces" yaml:"workspaces"`
	Stores     map[string]StoreSummary `json:"stores" yaml:"stores"`
	// DatabaseBytes / DatabaseSHA256 describe instance/db.sqlite.
	DatabaseBytes  int64  `json:"database_bytes" yaml:"database_bytes"`
	DatabaseSHA256 string `json:"database_sha256" yaml:"database_sha256"`
	// RecoveryKit: the payload carries recovery-kit/keys.json.
	RecoveryKit bool `json:"recovery_kit" yaml:"recovery_kit"`
	// VaultEnvelopes counts the database's envelopes per key version —
	// what a restore needs keys for, kit or not.
	VaultEnvelopes map[string]int `json:"vault_envelopes,omitempty" yaml:"vault_envelopes,omitempty"`
	// KitVersions lists the key versions the kit carries.
	KitVersions []string `json:"kit_versions,omitempty" yaml:"kit_versions,omitempty"`
	// CrewArchives lists the workspace ids with an instance/crews/ archive.
	CrewArchives []string `json:"crew_archives,omitempty" yaml:"crew_archives,omitempty"`
	// HoldMS is how long writes were held for the consistent copy.
	HoldMS int64 `json:"hold_ms" yaml:"hold_ms"`
}

// InstanceWorkspace summarises one workspace in an instance bundle.
type InstanceWorkspace struct {
	ID                 string `json:"id" yaml:"id"`
	Slug               string `json:"slug" yaml:"slug"`
	Name               string `json:"name" yaml:"name"`
	Crews              int    `json:"crews" yaml:"crews"`
	Agents             int    `json:"agents" yaml:"agents"`
	ContainersCaptured int    `json:"containers_captured" yaml:"containers_captured"`
}

// StoreSummary is one file store in an instance bundle. IndexSHA256 is the
// digest of the store's entries in instance/index.json, so a contents check
// can tie the per-file hashes back to the manifest.
type StoreSummary struct {
	Configured  bool   `json:"configured" yaml:"configured"`
	Files       int    `json:"files" yaml:"files"`
	Bytes       int64  `json:"bytes" yaml:"bytes"`
	IndexSHA256 string `json:"index_sha256,omitempty" yaml:"index_sha256,omitempty"`
	// Skipped counts entries that are not regular files (symlinks, devices);
	// they are not copied.
	Skipped int `json:"skipped,omitempty" yaml:"skipped,omitempty"`
	// Unreadable counts files the server could not read (permission
	// denied); UnreadableSample names up to five of them. Every other file
	// of the store is copied.
	Unreadable       int      `json:"unreadable,omitempty" yaml:"unreadable,omitempty"`
	UnreadableSample []string `json:"unreadable_sample,omitempty" yaml:"unreadable_sample,omitempty"`
}

// InstanceIndex is instance/index.json.
type InstanceIndex struct {
	Stores map[string][]IndexEntry `json:"stores"`
}

// IndexEntry is one copied file.
type IndexEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// StoreIndexDigest is the digest recorded as StoreSummary.IndexSHA256.
func StoreIndexDigest(entries []IndexEntry) string {
	sorted := append([]IndexEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, e := range sorted {
		fmt.Fprintf(h, "%s\x00%d\x00%s\n", e.Path, e.Size, e.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ErrInstanceBusy: running work did not finish in the busy wait; the run is
// skipped, not forced.
var ErrInstanceBusy = errors.New("backup: instance backup skipped: work was still running")

// InstanceOptions configure CreateInstanceBackup.
type InstanceOptions struct {
	ServiceSnapshots          ServiceSnapshotRuntime
	RecoverServiceMaintenance bool
	serviceCapture            *instanceServiceCapture

	OutputDir       string
	CrewshipVersion string
	Actor           Actor
	// Exactly one of Passphrase or Recipients. Instance bundles are never
	// written unencrypted.
	Passphrase string
	Recipients []age.Recipient
	Paths      InstancePaths
	// RecoveryKit puts the vault keys in the payload. The caller reads it
	// from backup_settings (RecoveryKitEnabled).
	RecoveryKit bool
	Level       ScopeLevel
	// DockerOps and CrewContainerName capture crew containers; nil ops
	// captures none (and a provisioned crew is then recorded as missing).
	DockerOps         DockerOps
	CrewContainerName func(id, slug string) string
	// Quiesce is the controller the window opens on (default:
	// quiesce.Default()). BusyWait / HoldCap default to the plan's 120 / 20
	// minutes.
	Quiesce  *quiesce.Controller
	BusyWait time.Duration
	HoldCap  time.Duration
	Poll     time.Duration
	// Busy reports running work; nil uses InstanceBusy(db).
	Busy quiesce.BusyFunc
	// Window is a quiet window the caller already opened (the backup
	// service's quiet window, after its own drain). The copy then runs
	// inside it instead of opening one, and it is released as soon as the
	// copy ends. Nil: CreateInstanceBackup drains and opens the window.
	Window *quiesce.Window
	// Progress, when set, is told "copy" as the copy starts, "pack" once
	// the window is released and packing begins, and "encrypt" before the
	// payload is sealed.
	Progress func(phase string)
	// EnvMode "complete" also captures each crew container's complete
	// environment: committed under the crew's pause inside the window,
	// saved into the environment store after the window is released.
	EnvMode string
	// EnvInline puts the environment blobs in the payload too.
	EnvInline bool
	// EncoderConcurrency caps the zstd encoder's goroutines (cpu_cores);
	// DiskThrottle caps bytes written to the sealed staging file and the
	// bundle (disk_mbps). Zero / nil: library default, no cap.
	EncoderConcurrency int
	DiskThrottle       Throttle
}

// InstanceResult is a finished instance bundle.
type InstanceResult struct {
	CreateResult
	HoldMS int64
}

func (o *InstanceOptions) validate() error {
	if o.Actor.UserID == "" {
		return fmt.Errorf("backup: InstanceOptions.Actor.UserID required")
	}
	modes := 0
	if o.Passphrase != "" {
		modes++
	}
	if len(o.Recipients) > 0 {
		modes++
	}
	if modes != 1 {
		return fmt.Errorf("%w: an instance backup needs exactly one of a passphrase or recipients", ErrEncryptionRequired)
	}
	return nil
}

// ErrEncryptionRequired: a new bundle must be encrypted.
var ErrEncryptionRequired = errors.New("backup: encryption required")

// InstanceBusy is the drain probe: in-process agent runs holding a workspace
// guard, agents the database says are running, durable work that has
// started (claimed work items are 'starting' from the claim), and routine
// runs whose row says running. Routine runs and mission dispatches started
// in this process are also counted from their admission — before their row
// exists — by the quiet window itself (quiesce.StartRun, added to every
// busy check Begin makes).
func InstanceBusy(db *sql.DB) quiesce.BusyFunc {
	return func(ctx context.Context) (int, string, error) {
		var parts []string
		total := 0
		if n := DefaultGuard().ActiveMissions(); n > 0 {
			total += n
			parts = append(parts, fmt.Sprintf("%d agent run(s) in this process", n))
		}
		for _, q := range []struct{ what, sql string }{
			{"agent(s) running", `SELECT COUNT(*) FROM agents WHERE status IN ('running','busy') AND deleted_at IS NULL`},
			{"work item(s) started", `SELECT COUNT(*) FROM work_items WHERE state IN ('starting','running')`},
			{"routine run(s) running", `SELECT COUNT(*) FROM pipeline_runs WHERE status = 'running'`},
		} {
			var n int
			if err := db.QueryRowContext(ctx, q.sql).Scan(&n); err != nil {
				msg := strings.ToLower(err.Error())
				if strings.Contains(msg, "no such table") || strings.Contains(msg, "no such column") {
					continue
				}
				return 0, "", fmt.Errorf("backup: busy probe: %w", err)
			}
			if n > 0 {
				total += n
				parts = append(parts, fmt.Sprintf("%d %s", n, q.what))
			}
		}
		return total, strings.Join(parts, ", "), nil
	}
}

type instanceWorkspaceTarget struct {
	target  *WorkspaceTarget
	agents  int
	archive string // staged crews archive, "" when no container was captured
	capture map[string]CrewCapture
	// envArchive is the staged environments archive, "" without any.
	envArchive string
}

// CreateInstanceBackup writes an instance bundle. The consistent copy — the
// database snapshot, every file store and every crew container — is taken
// inside one quiet window; packing, sealing and writing happen after it is
// released. Returns ErrInstanceBusy (wrapped) when running work did not
// drain in time.
func CreateInstanceBackup(ctx context.Context, db *sql.DB, opts InstanceOptions) (res *InstanceResult, retErr error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if err := requireSupportedInstanceServiceBackups(ctx, db, opts.ServiceSnapshots); err != nil {
		return nil, err
	}
	captureCtx, cancelCapture := context.WithCancel(ctx)
	defer cancelCapture()
	ctx = captureCtx
	keeper := &serviceFenceKeeper{db: db}
	opts.serviceCapture = &instanceServiceCapture{keeper: keeper}
	go keeper.run(ctx, cancelCapture)
	defer func() {
		if !opts.serviceCapture.completed {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		for _, fence := range opts.serviceCapture.fences {
			if err := servicelifecycle.EndBackupFence(cleanupCtx, db, fence.crew, fence.token); err != nil {
				if retErr == nil {
					res = nil
					retErr = err
				}
				slog.Error("backup: release instance service maintenance", "error", err)

			}
		}
	}()
	level := opts.Level
	if !level.Valid() {
		level = DefaultScopeLevel
	}
	outDir := opts.OutputDir
	if outDir == "" {
		d, err := defaultBackupsDirFor(getDefaultStorage())
		if err != nil {
			return nil, err
		}
		outDir = d
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, fmt.Errorf("backup: ensure output dir: %w", err)
	}
	outAbs, err := filepath.Abs(outDir)
	if err != nil {
		return nil, err
	}
	cleanupStaleStaging(outAbs)
	now := time.Now().UTC()
	stage, err := os.MkdirTemp(outAbs, instanceStagingGlob+now.Format("20060102T150405Z")+"-")
	if err != nil {
		return nil, fmt.Errorf("backup: create staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	// Staging is encrypted before it touches disk: every staged file goes
	// through a key that exists only in this process's memory for this run
	// (staging_cipher.go), and the database snapshot is never staged at all
	// — it is held in memory and serialized straight into the sealed payload.
	sc, err := newStagingCipher()
	if err != nil {
		return nil, err
	}
	envRun := newEnvironmentRun(db, outAbs, opts.EnvMode, opts.EnvInline, now, opts.Recipients, opts.Passphrase)
	level = envRun.level(level)
	defer func() {
		envRun.abortAll()
		if retErr != nil {
			envRun.release()
		}
	}()

	// Targets are resolved before the window: they only read, and a probe
	// against the daemon should not spend hold time.
	workspaces, err := loadInstanceWorkspaces(ctx, db, opts.CrewContainerName)
	if err != nil {
		return nil, err
	}
	for _, w := range workspaces {
		if err := reconcileCrewContainers(ctx, opts.DockerOps, w.target); err != nil {
			return nil, err
		}
	}

	progress := func(phase string) {
		if opts.Progress != nil {
			opts.Progress(phase)
		}
	}
	// Quota images may be large. Freeze only their owners while exporting,
	// and open the instance-wide writer barrier for the consistent copy below.
	// Older callers can pass an already-open window; release and reopen it
	// through the configured controller rather than spending its cap on export.
	if opts.Window != nil && opts.ServiceSnapshots != nil {
		opts.Window.Release()
		opts.Window = nil
	}
	if opts.serviceCapture != nil {
		if err := opts.serviceCapture.capture(ctx, db, stage, sc, opts, workspaces); err != nil {
			return nil, err
		}
	}
	window := opts.Window
	releaseGuards := func() {}
	if window != nil {
		// The caller drained and opened the window; the workspace guards
		// are taken inside it, as Begin's Acquire would.
		release, gerr := acquireAllGuards(workspaces)
		if gerr != nil {
			window.Release()
			return nil, fmt.Errorf("%w: %v", ErrInstanceBusy, gerr)
		}
		var once sync.Once
		releaseGuards = func() { once.Do(release) }
		defer releaseGuards()
	} else {
		ctrl := opts.Quiesce
		if ctrl == nil {
			ctrl = quiesce.Default()
		}
		busy := opts.Busy
		if busy == nil {
			busy = InstanceBusy(db)
		}
		window, err = ctrl.Begin(ctx, quiesce.Options{
			BusyWait: opts.BusyWait, HoldCap: opts.HoldCap, Poll: opts.Poll, Busy: busy,
			Reason:  "instance backup",
			Acquire: func() (func(), error) { return acquireAllGuards(workspaces) },
		})
		if err != nil {
			if errors.Is(err, quiesce.ErrBusy) || errors.Is(err, quiesce.ErrAlreadyHeld) {
				return nil, fmt.Errorf("%w: %v", ErrInstanceBusy, err)
			}
			return nil, err
		}
	}
	progress("copy")
	stores, memSnap, copyErr := stageInstanceCopy(window.Context(), db, stage, sc, opts, level, workspaces, envRun)
	expired := window.Expired()
	holdMS := window.Release().Milliseconds()
	releaseGuards()
	var closeSnapOnce sync.Once
	closeSnap := func() {
		if memSnap != nil {
			closeSnapOnce.Do(func() { _ = memSnap.Close() })
		}
	}
	defer closeSnap()
	if expired {
		return nil, fmt.Errorf("%w (%s)", quiesce.ErrHoldCapExceeded, holdCapLabel(opts.HoldCap))
	}
	if copyErr != nil {
		return nil, copyErr
	}
	slog.Info("instance backup: consistent copy taken; writes released", "hold_ms", holdMS)
	progress("pack")
	// Environments committed inside the window are saved now, outside it.
	envArchives, err := envRun.finishDeferred(ctx, filepath.Join(stage, "environments"), sc.Create)
	if err != nil {
		return nil, err
	}
	for _, w := range workspaces {
		w.envArchive = envArchives[w.target.ID]
	}

	// Everything below reads the staged copy, never the live server.
	snap := memSnap.DB

	contents := Contents{ServiceSnapshots: len(opts.serviceCapture.snapshots)}
	inst := &InstanceContents{Stores: map[string]StoreSummary{}, HoldMS: holdMS}
	for name, s := range stores {
		inst.Stores[name] = s.summary
	}
	for _, w := range workspaces {
		iw := InstanceWorkspace{ID: w.target.ID, Slug: w.target.Slug, Name: w.target.Name, Crews: len(w.target.CrewTargets), Agents: w.agents}
		wc := buildContents(w.target, level, w.capture)
		for _, c := range wc.Crews {
			contents.Crews = append(contents.Crews, c)
		}
		for _, c := range wc.Crews {
			if len(c.FailedSections) > 0 {
				contents.Incomplete = append(contents.Incomplete, IncompleteItem{
					Kind: IncompleteCrewSectionFailed, Count: len(c.FailedSections), Workspace: w.target.ID,
					Detail: fmt.Sprintf("crew %s in %s: Docker could not copy %s; the crew's other files are in the bundle", c.Slug, w.target.Slug, strings.Join(c.FailedSections, "; ")),
				})
			}
		}
		for _, slug := range wc.MissingContainerCrews {
			contents.MissingContainerCrews = append(contents.MissingContainerCrews, w.target.Slug+"/"+slug)
			contents.Incomplete = append(contents.Incomplete, IncompleteItem{
				Kind: IncompleteContainerMissing, Count: 1, Workspace: w.target.ID,
				Detail: fmt.Sprintf("crew %s in %s had a provisioned container that was gone when the backup ran, so none of its files are in the bundle", slug, w.target.Slug),
			})
		}
		iw.ContainersCaptured = len(w.capture)
		if w.archive != "" {
			inst.CrewArchives = append(inst.CrewArchives, w.target.ID)
		}
		inst.Workspaces = append(inst.Workspaces, iw)
	}

	counts, err := snapshotRowCounts(ctx, snap)
	if err != nil {
		return nil, err
	}
	contents.TableRowCounts = counts

	for _, name := range InstanceStores {
		st := stores[name]
		if st == nil || st.summary.Unreadable == 0 {
			continue
		}
		contents.Incomplete = append(contents.Incomplete, IncompleteItem{
			Kind: IncompleteFileUnreadable, Count: st.summary.Unreadable,
			Detail: fmt.Sprintf("%d file(s) in the %s store could not be read by the server (permission denied), e.g. %s; every other file of the store is in the bundle",
				st.summary.Unreadable, name, strings.Join(st.summary.UnreadableSample, ", ")),
		})
	}

	attIncluded, attMissing, err := checkStagedAttachments(ctx, snap, stores[StoreOutput])
	if err != nil {
		return nil, err
	}
	contents.AttachmentsIncluded, contents.AttachmentsMissing = attIncluded, attMissing.total()
	contents.Incomplete = append(contents.Incomplete, attMissing.items(IncompleteAttachmentMissing,
		"attachment file(s) referenced by attachment rows were not in the storage root; the rows are in the bundle without their files")...)
	memIncluded, memMissing, err := checkStagedMemoryBlobs(ctx, snap, stores[StoreMemory])
	if err != nil {
		return nil, err
	}
	contents.MemoryBlobsIncluded, contents.MemoryBlobsMissing = memIncluded, memMissing.total()
	contents.Incomplete = append(contents.Incomplete, memMissing.items(IncompleteMemoryBlobMissing,
		"memory version(s) had no content file in the version store; their history rows are in the bundle without content")...)

	scan, err := ScanVaultEnvelopes(ctx, snap)
	if err != nil {
		return nil, err
	}
	inst.VaultEnvelopes = scan.Versions
	var kit *RecoveryKit
	if opts.RecoveryKit {
		k, missing, err := BuildRecoveryKit(scan, now)
		if err != nil {
			return nil, err
		}
		kit = k
		inst.RecoveryKit = true
		for _, v := range k.Versions {
			inst.KitVersions = append(inst.KitVersions, v.Version)
		}
		for v, n := range missing {
			contents.Incomplete = append(contents.Incomplete, IncompleteItem{
				Kind: IncompleteVaultKeyMissing, Count: n,
				Detail: fmt.Sprintf("%d sealed value(s) use key %s, which this server could not resolve (%s unset), so the recovery kit does not carry it", n, v, keyEnvName(v)),
			})
		}
	}
	contents.Environments = envRun.summaries()
	contents.Incomplete = append(contents.Incomplete, envRun.incompleteItems()...)
	contents.Instance = inst
	contents.InstanceConfigIncluded = true
	contents.CredstoreIncluded = scan.Total() > 0
	schemaVersions := AppliedMigrationVersions(ctx, snap)
	hostname := CurrentInstanceHostname(ctx, snap)

	// The database image goes into the payload from memory; the in-memory
	// database is freed as soon as it is serialized.
	dbImage, err := memSnap.Serialize(ctx)
	if err != nil {
		return nil, fmt.Errorf("backup: serialize database snapshot: %w", err)
	}
	closeSnap()
	dbSum := sha256.Sum256(dbImage)
	inst.DatabaseBytes, inst.DatabaseSHA256 = int64(len(dbImage)), hex.EncodeToString(dbSum[:])

	// Pack the payload from the staged copy straight through the sealer:
	// tar → zstd → age into one file, so the packed payload is never on
	// disk unencrypted (seal_stream.go).
	sealedPath := filepath.Join(stage, "sealed")
	sha, sealedSize, err := sealInstancePayload(ctx, sealedPath, opts, func(w io.Writer) error {
		return writeInstancePayload(w, opts.EncoderConcurrency, dbImage, sc, stores, workspaces, kit, now, opts.serviceCapture)
	}, progress)
	dbImage = nil
	if err != nil {
		return nil, err
	}

	manifest := &Manifest{
		FormatVersion:           FormatVersion,
		CrewshipVersionAtBackup: DetectCrewshipVersion(opts.CrewshipVersion),
		SchemaMigrationVersions: schemaVersions,
		Scope:                   ScopeInstance,
		ScopeLevel:              level,
		CompatibleTargets:       compatibleTargetsFor(ScopeInstance),
		CreatedAt:               now,
		CreatedBy:               opts.Actor,
		SourceInstance:          currentInstance(),
		Contents:                contents,
		Checksums:               Checksums{PayloadSHA256: sha},
	}
	if len(opts.Recipients) > 0 {
		manifest.Encryption = Encryption{Enabled: true, Algorithm: EncryptionAlgorithm}
		for _, r := range opts.Recipients {
			manifest.Encryption.Recipients = append(manifest.Encryption.Recipients, recipientString(r))
		}
	} else {
		manifest.Encryption = Encryption{Enabled: true, Algorithm: EncryptionAlgorithm, KeyDerivation: "scrypt"}
	}
	if hostname != "" {
		manifest.SourceInstance.Hostname = hostname
	}

	finalPath := filepath.Join(outAbs, BundleFileName(ScopeInstance, "all", now))
	partial := finalPath + ".partial"
	if err := writeInstanceBundleFile(ctx, opts.DiskThrottle, partial, manifest, sealedPath, sealedSize); err != nil {
		_ = os.Remove(partial)
		return nil, err
	}
	info, err := os.Stat(partial)
	if err != nil {
		_ = os.Remove(partial)
		return nil, err
	}
	if err := publishServiceSnapshotBundle(ctx, db, opts.serviceCapture.fences, func() error { return os.Rename(partial, finalPath) }); err != nil {
		_ = os.Remove(partial)
		return nil, fmt.Errorf("backup: rename final bundle: %w", err)
	}
	if err := envRun.commit(ctx, finalPath); err != nil {
		slog.Warn("backup: could not record which environment layers the bundle needs", "path", finalPath, "error", err)
	}
	return &InstanceResult{
		CreateResult: CreateResult{Path: finalPath, Size: info.Size(), SHA256: sha, Manifest: manifest, MissingContainerCrews: contents.MissingContainerCrews},
		HoldMS:       holdMS,
	}, nil
}

func holdCapLabel(d time.Duration) string {
	if d <= 0 {
		d = quiesce.DefaultHoldCap
	}
	return "cap " + d.String()
}

func keyEnvName(version string) string {
	if version == "v1" {
		return "ENCRYPTION_KEY"
	}
	return "ENCRYPTION_KEY_" + strings.ToUpper(version)
}

// cleanupStaleStaging removes staging directories a crashed instance backup
// left in the output directory.
func cleanupStaleStaging(outDir string) {
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-instanceStagingMaxAge)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), instanceStagingGlob) {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(outDir, e.Name()))
		}
	}
}

// SweepInstanceStaging removes what an interrupted instance backup left in
// outDir: its staging directories (the consistent copy) and a half-written
// crewship-instance-all-….partial bundle, when older than maxAge. The backup
// service calls it at boot with maxAge 0 — no run is active then, so
// anything left is an orphan. Returns how many entries it removed.
func SweepInstanceStaging(outDir string, maxAge time.Duration) int {
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-maxAge)
	partialPrefix := "crewship-" + string(ScopeInstance) + "-"
	removed := 0
	for _, e := range entries {
		name := e.Name()
		staging := e.IsDir() && strings.HasPrefix(name, instanceStagingGlob)
		partial := !e.IsDir() && strings.HasPrefix(name, partialPrefix) && strings.HasSuffix(name, ".partial")
		if !staging && !partial {
			continue
		}
		info, err := e.Info()
		if err != nil || (maxAge > 0 && !info.ModTime().Before(cutoff)) {
			continue
		}
		if os.RemoveAll(filepath.Join(outDir, name)) == nil {
			removed++
		}
	}
	return removed
}

func loadInstanceWorkspaces(ctx context.Context, db *sql.DB, nameFn func(id, slug string) string) ([]*instanceWorkspaceTarget, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM workspaces WHERE deleted_at IS NULL ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("backup: list workspaces: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*instanceWorkspaceTarget, 0, len(ids))
	for _, id := range ids {
		t, err := LoadWorkspaceTarget(ctx, db, id, nameFn)
		if err != nil {
			return nil, err
		}
		var agents int
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND deleted_at IS NULL`, id).Scan(&agents)
		out = append(out, &instanceWorkspaceTarget{target: t, agents: agents, capture: map[string]CrewCapture{}})
	}
	return out, nil
}

func acquireAllGuards(ws []*instanceWorkspaceTarget) (func(), error) {
	var releases []func()
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, w := range ws {
		rel, err := DefaultGuard().BeginBackup(w.target.ID)
		if err != nil {
			releaseAll()
			return nil, fmt.Errorf("%s: %w", w.target.Slug, err)
		}
		releases = append(releases, rel)
	}
	return releaseAll, nil
}

type stagedStore struct {
	root    string // staged copy
	entries []IndexEntry
	byPath  map[string]IndexEntry
	summary StoreSummary
}

// stageInstanceCopy is the part that runs inside the quiet window: snapshot
// the database, copy every file store, capture every crew container. ctx
// ends when the hold cap fires.
//
// The database snapshot is taken into memory (database.SnapshotToMemory) and
// every staged file is written through sc, so nothing it stages is readable
// on disk. On error the snapshot is freed and nil returned.
func stageInstanceCopy(ctx context.Context, db *sql.DB, stage string, sc *stagingCipher, opts InstanceOptions, level ScopeLevel, workspaces []*instanceWorkspaceTarget, envRun *environmentRun) (_ map[string]*stagedStore, _ *database.MemorySnapshot, retErr error) {
	ms, err := database.SnapshotToMemory(ctx, db)
	if err != nil {
		return nil, nil, fmt.Errorf("backup: snapshot database: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = ms.Close()
		}
	}()
	if err := requireSupportedInstanceServiceBackups(ctx, ms.DB, opts.ServiceSnapshots); err != nil {
		return nil, nil, err
	}
	if opts.serviceCapture != nil {
		dump, err := instanceServiceDump(ctx, ms.DB)
		if err != nil {
			return nil, nil, err
		}
		if err := requireSupportedDumpServiceBackups(dump, opts.serviceCapture.snapshots); err != nil {
			return nil, nil, err
		}
	}
	outAbs, _ := filepath.Abs(opts.OutputDir)
	skip := []string{stage, outAbs}
	stores := map[string]*stagedStore{}
	for _, name := range InstanceStores {
		src := opts.Paths.byStore()[name]
		st := &stagedStore{root: filepath.Join(stage, "files", name), byPath: map[string]IndexEntry{}}
		stores[name] = st
		if src == "" {
			continue
		}
		st.summary.Configured = true
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		entries, skipped, unreadable, err := copyTree(ctx, src, st.root, skip, sc)
		if err != nil {
			return nil, nil, fmt.Errorf("backup: copy %s store: %w", name, err)
		}
		st.summary.Unreadable = len(unreadable)
		if len(unreadable) > 5 {
			unreadable = unreadable[:5]
		}
		st.summary.UnreadableSample = unreadable
		st.entries = entries
		for _, e := range entries {
			st.byPath[e.Path] = e
			st.summary.Bytes += e.Size
		}
		st.summary.Files = len(entries)
		st.summary.Skipped = skipped
		st.summary.IndexSHA256 = StoreIndexDigest(entries)
	}
	if err := os.MkdirAll(filepath.Join(stage, "crews"), 0o700); err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	for _, w := range workspaces {
		captured := false
		for _, c := range w.target.CrewTargets {
			if c.ContainerID != "" && opts.DockerOps != nil {
				captured = true
			}
		}
		hasDevcontainer := false
		for _, c := range w.target.CrewTargets {
			if c.DevcontainerConfig != "" || c.MiseConfig != "" {
				hasDevcontainer = true
			}
		}
		if !captured && !hasDevcontainer {
			continue
		}
		path := filepath.Join(stage, "crews", w.target.ID+instanceCrewsSuffix)
		f, err := sc.Create(path)
		if err != nil {
			return nil, nil, err
		}
		tw, err := NewTarZstWriter(f)
		if err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		for _, c := range w.target.CrewTargets {
			if c.ContainerID == "" || opts.DockerOps == nil {
				continue
			}
			if err := ctx.Err(); err != nil {
				_ = tw.Close()
				_ = f.Close()
				return nil, nil, err
			}
			capture, err := envRun.collectDeferred(ctx, opts.DockerOps, tw, c, level, w.target.ID)
			if err != nil {
				_ = tw.Close()
				_ = f.Close()
				return nil, nil, err
			}
			w.capture[c.Slug] = capture
		}
		if err := WriteDevcontainerSection(tw, w.target.CrewTargets, now); err != nil {
			_ = tw.Close()
			_ = f.Close()
			return nil, nil, err
		}
		if err := tw.Close(); err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		if err := f.Close(); err != nil {
			return nil, nil, err
		}
		w.archive = path
	}
	return stores, ms, nil
}

// copyTree copies every regular file under src into dst, returning an index
// entry per file (paths slash-separated, relative to src). Directories in skip
// (the staging and output directories) are not descended into. Non-regular
// entries are counted, not copied.
//
// A file or directory the server may not read (permission denied — agent
// output owned by a container user with mode 0600, typically) is skipped and
// returned in unreadable instead of failing the whole instance backup; the
// caller records it as an incomplete item.
func copyTree(ctx context.Context, src, dst string, skip []string, sc *stagingCipher) (_ []IndexEntry, _ int, unreadable []string, _ error) {
	info, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil, nil
		}
		return nil, 0, nil, err
	}
	if !info.IsDir() {
		return nil, 0, nil, fmt.Errorf("%s is not a directory", src)
	}
	srcAbs, err := filepath.Abs(src)
	if err != nil {
		return nil, 0, nil, err
	}
	var entries []IndexEntry
	skipped := 0
	relOf := func(p string) string {
		if r, err := filepath.Rel(srcAbs, p); err == nil {
			return filepath.ToSlash(r)
		}
		return p
	}
	err = filepath.WalkDir(srcAbs, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil // deleted between readdir and stat
			}
			if os.IsPermission(walkErr) && p != srcAbs {
				unreadable = append(unreadable, relOf(p))
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			for _, s := range skip {
				if s != "" && p == s {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !d.Type().IsRegular() {
			skipped++
			return nil
		}
		rel, err := filepath.Rel(srcAbs, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			return err
		}
		sha, n, err := copyFileHashed(p, out, sc)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			if os.IsPermission(err) {
				_ = os.Remove(out)
				unreadable = append(unreadable, filepath.ToSlash(rel))
				return nil
			}
			return err
		}
		entries = append(entries, IndexEntry{Path: filepath.ToSlash(rel), Size: n, SHA256: sha})
		return nil
	})
	return entries, skipped, unreadable, err
}

// copyFileHashed copies src into the staged file dst, encrypted through sc,
// and returns the plaintext's sha256 and size.
func copyFileHashed(src, dst string, sc *stagingCipher) (string, int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = in.Close() }()
	out, err := sc.Create(dst)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// openSnapshot opens a staged or extracted database file read-only.
func openSnapshot(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("backup: open snapshot: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("backup: open snapshot: %w", err)
	}
	return db, nil
}

// snapshotRowCounts counts every table's rows (sqlite_* and FTS shadow
// tables excluded).
func snapshotRowCounts(ctx context.Context, db *sql.DB) (map[string]int, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("backup: list snapshot tables: %w", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	_ = rows.Close()
	out := map[string]int{}
	for _, n := range names {
		if !validSQLIdent.MatchString(n) {
			continue
		}
		var c int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+n+`"`).Scan(&c); err != nil { // nosemgrep: gosql-sqli — identifier validated above
			// Virtual tables whose module is not loaded cannot be counted;
			// they are not user data (FTS indexes rebuild).
			continue
		}
		out[n] = c
	}
	return out, nil
}

// missingByWorkspace counts gaps per workspace.
type missingByWorkspace map[string]int

func (m missingByWorkspace) total() int {
	n := 0
	for _, c := range m {
		n += c
	}
	return n
}

func (m missingByWorkspace) items(kind, detail string) []IncompleteItem {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []IncompleteItem
	for _, ws := range keys {
		out = append(out, IncompleteItem{Kind: kind, Count: m[ws], Workspace: ws, Detail: fmt.Sprintf("%d %s", m[ws], detail)})
	}
	return out
}

// checkStagedAttachments counts distinct attachment blobs present in the
// staged storage root and those referenced but absent, per workspace.
func checkStagedAttachments(ctx context.Context, db *sql.DB, out *stagedStore) (int, missingByWorkspace, error) {
	missing := missingByWorkspace{}
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT workspace_id, sha256 FROM attachments`)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return 0, missing, nil
		}
		return 0, nil, fmt.Errorf("backup: list attachments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	included := 0
	for rows.Next() {
		var ws, sha sql.NullString
		if err := rows.Scan(&ws, &sha); err != nil {
			return 0, nil, err
		}
		if !validSha256Hex(sha.String) {
			missing[ws.String]++
			continue
		}
		if _, err := safepath.ValidateComponent(ws.String); err != nil || ws.String == "" {
			missing[ws.String]++
			continue
		}
		rel := "attachments/" + ws.String + "/" + sha.String[:2] + "/" + sha.String
		if out == nil {
			missing[ws.String]++
			continue
		}
		if e, ok := out.byPath[rel]; ok && e.SHA256 == sha.String {
			included++
			continue
		}
		missing[ws.String]++
	}
	return included, missing, rows.Err()
}

// checkStagedMemoryBlobs does the same for memory_versions rows against the
// staged memory root's versions/ tree.
func checkStagedMemoryBlobs(ctx context.Context, db *sql.DB, mem *stagedStore) (int, missingByWorkspace, error) {
	missing := missingByWorkspace{}
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT COALESCE(workspace_id,''), sha256 FROM memory_versions WHERE sha256 IS NOT NULL AND sha256 != ''`)
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "no such table") || strings.Contains(msg, "no such column") {
			return 0, missing, nil
		}
		return 0, nil, fmt.Errorf("backup: list memory versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	included := 0
	seen := map[string]bool{}
	for rows.Next() {
		var ws, sha string
		if err := rows.Scan(&ws, &sha); err != nil {
			return 0, nil, err
		}
		if seen[sha] {
			continue
		}
		seen[sha] = true
		if !validSha256Hex(sha) || mem == nil {
			missing[ws]++
			continue
		}
		if e, ok := mem.byPath["versions/"+sha[:2]+"/"+sha]; ok && e.Size >= 0 {
			included++
			continue
		}
		missing[ws]++
	}
	return included, missing, rows.Err()
}

// sealInstancePayload creates sealedPath and streams the payload that write
// produces through the sealer (and the disk throttle) into it, returning the
// sealed bytes' digest and size.
func sealInstancePayload(ctx context.Context, sealedPath string, opts InstanceOptions, write func(io.Writer) error, progress func(string)) (string, int64, error) {
	out, err := os.OpenFile(sealedPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	sealer, err := NewSealWriter(NewThrottledWriter(ctx, out, opts.DiskThrottle), WriteBundleOptions{Recipients: opts.Recipients, Passphrase: opts.Passphrase})
	if err != nil {
		_ = out.Close()
		return "", 0, err
	}
	if err := write(sealer); err != nil {
		_ = out.Close()
		return "", 0, err
	}
	progress("encrypt")
	if err := sealer.Close(); err != nil {
		_ = out.Close()
		return "", 0, err
	}
	if err := out.Close(); err != nil {
		return "", 0, err
	}
	return sealer.Sum(), sealer.Size(), nil
}

func writeInstancePayload(sink io.Writer, concurrency int, dbImage []byte, sc *stagingCipher, stores map[string]*stagedStore, workspaces []*instanceWorkspaceTarget, kit *RecoveryKit, now time.Time, services ...*instanceServiceCapture) error {
	tw, err := NewTarZstWriterConcurrency(sink, concurrency)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = tw.Close()
		return err
	}
	if err := tw.WriteStream(instanceDBEntry, 0o600, now, int64(len(dbImage)), bytes.NewReader(dbImage)); err != nil {
		return fail(err)
	}
	for _, service := range services {
		if err := service.write(tw, sc, now); err != nil {
			return fail(err)
		}
	}
	index := InstanceIndex{Stores: map[string][]IndexEntry{}}
	for _, name := range InstanceStores {
		st := stores[name]
		index.Stores[name] = st.entries
		if index.Stores[name] == nil {
			index.Stores[name] = []IndexEntry{}
		}
		for _, e := range st.entries {
			src := filepath.Join(st.root, filepath.FromSlash(e.Path))
			if err := writeStagedEntry(tw, sc, instanceFilesPrefix+name+"/"+e.Path, src, now); err != nil {
				return fail(err)
			}
		}
	}
	for _, w := range workspaces {
		if w.archive == "" {
			continue
		}
		if err := writeStagedEntry(tw, sc, instanceCrewsPrefix+w.target.ID+instanceCrewsSuffix, w.archive, now); err != nil {
			return fail(err)
		}
	}
	for _, w := range workspaces {
		if w.envArchive == "" {
			continue
		}
		if err := writeStagedEntry(tw, sc, instanceEnvPrefix+w.target.ID+instanceCrewsSuffix, w.envArchive, now); err != nil {
			return fail(err)
		}
	}
	idx, err := json.Marshal(index)
	if err != nil {
		return fail(err)
	}
	if err := tw.WriteFile(instanceIndexEntry, 0o600, now, idx); err != nil {
		return fail(err)
	}
	if kit != nil {
		b, err := json.Marshal(kit)
		if err != nil {
			return fail(err)
		}
		if err := tw.WriteFile(RecoveryKitPath, 0o600, now, b); err != nil {
			return fail(err)
		}
	}
	return tw.Close()
}

// writeStagedEntry decrypts a staged file into the payload.
func writeStagedEntry(tw *TarZstWriter, sc *stagingCipher, name, src string, now time.Time) error {
	r, size, err := sc.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	return tw.WriteStream(name, 0o600, now, size, r)
}

func writeInstanceBundleFile(ctx context.Context, throttle Throttle, path string, m *Manifest, sealedPath string, sealedSize int64) error {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("backup: open partial: %w", err)
	}
	in, err := os.Open(sealedPath)
	if err != nil {
		_ = out.Close()
		return err
	}
	err = WriteBundleStream(NewThrottledWriter(ctx, out, throttle), m, in, sealedSize)
	_ = in.Close()
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
