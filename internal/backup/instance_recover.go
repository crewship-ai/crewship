package backup

// Offline instance restore: `crewship recover` and the isolated restore a
// drill runs. No server is running and none is started — this writes a data
// directory a server can then boot from.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/safepath"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Files recover writes into the data directory.
const (
	// RecoveredKeysFile holds the recovery kit's vault keys as environment
	// assignments, plus the rotated NEXTAUTH_SECRET. Mode 0600.
	RecoveredKeysFile = "recovered-keys.env"
	// recoveredSecretsFile is the file `crewship start` reads its managed
	// secrets from (internal/secrets); recover seeds it so the first boot
	// neither mints a new ENCRYPTION_KEY over restored envelopes nor keeps
	// the source's session key.
	recoveredSecretsFile = "secrets.env"
	// RecoveredCrewsDir holds each workspace's crew container archive. The
	// files cannot be landed offline: there are no containers yet.
	RecoveredCrewsDir = "restore-staging/crews"
	// RecoveredEnvironmentsDir holds each workspace's environment records
	// (<ws>.tar.zst) and, under blobs/, the image layers they need — an
	// environment store the server lands from once it runs with Docker
	// (POST /api/v1/admin/instance/backups/environments/land).
	RecoveredEnvironmentsDir = "restore-staging/environments"
	RecoveredServicesDir     = "restore-staging/services"
)

// Incomplete kinds recover adds on top of what the bundle recorded.
const (
	// IncompleteFileMismatch: a file's content did not match the bundle's
	// own index.
	IncompleteFileMismatch = "file_mismatch"
	// IncompleteFileMissing: the index names a file the payload did not carry.
	IncompleteFileMissing = "file_missing"
	// IncompleteCredentialLocked: the database holds sealed values and no
	// key to open them came with the bundle.
	IncompleteCredentialLocked = "credential"
)

var (
	// ErrDataDirNotEmpty: recover refuses a non-empty data directory
	// without Force.
	ErrDataDirNotEmpty = errors.New("backup: the data directory is not empty")
	// ErrNotInstanceBundle: recover only reads instance-scope bundles.
	ErrNotInstanceBundle = errors.New("backup: not an instance bundle")
)

// RecoverOptions configure RecoverInstance.
type RecoverOptions struct {
	BundlePath string
	Identities []age.Identity
	Passphrase string
	DataDir    string
	// Force prepares and validates a private copy, then publishes it atomically.
	Force bool
	// DiscardWorkbooks acknowledges that a forced recovery destroys every
	// workbook in the target database; no bundle contains one. Without it a
	// forced recovery over workbooks refuses (ErrWorkbookTargetReplacement).
	DiscardWorkbooks bool
	// Drill restores for a test only: no key files are written and auth keys
	// are not rotated (the directory is thrown away); the kit stays in memory
	// for the drill's checks. The report is recorded as kind drill.
	Drill bool
	// Actor is recorded on the restore report.
	Actor  string
	Logger *slog.Logger
}

// StoreRestore is what landed for one file store.
type StoreRestore struct {
	Path  string `json:"path"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// RecoverReport is what recover did — printed by the CLI and persisted in the
// restored database's restore_reports.
type RecoverReport struct {
	Result            string                  `json:"result"`
	Summary           string                  `json:"summary"`
	BundlePath        string                  `json:"bundle_path"`
	DataDir           string                  `json:"data_dir"`
	PreviousDataDir   string                  `json:"previous_data_dir,omitempty"`
	DatabasePath      string                  `json:"database_path"`
	SourceHost        string                  `json:"source_host"`
	BundleCreatedAt   time.Time               `json:"bundle_created_at"`
	FormatVersion     int                     `json:"format_version"`
	Workspaces        []InstanceWorkspace     `json:"workspaces"`
	Stores            map[string]StoreRestore `json:"stores"`
	MigrationsApplied int                     `json:"migrations_applied"`
	RecoveryKit       bool                    `json:"recovery_kit"`
	KitVersions       []string                `json:"kit_versions,omitempty"`
	KeysFile          string                  `json:"keys_file,omitempty"`
	SecretsFile       string                  `json:"secrets_file,omitempty"`
	VaultEnvelopes    map[string]int          `json:"vault_envelopes,omitempty"`
	SessionsDropped   int                     `json:"sessions_dropped"`
	AuthKeysRotated   bool                    `json:"auth_keys_rotated"`
	RuntimeReset      []string                `json:"runtime_reset"`
	Holds             []quiesce.Hold          `json:"holds"`
	ServiceSnapshots  int                     `json:"service_snapshots,omitempty"`
	CrewArchives      []string                `json:"crew_archives,omitempty"`
	PageProjectsPath  string                  `json:"page_projects_path,omitempty"`
	Incomplete        []IncompleteItem        `json:"incomplete"`
	Warnings          []string                `json:"warnings"`
	Notes             []string                `json:"notes"`
	ReportID          string                  `json:"report_id,omitempty"`

	kit *RecoveryKit
}

// Kit returns the recovery kit read from the bundle (nil when absent). Only
// the drill uses it; it is never serialised.
func (r *RecoverReport) Kit() *RecoveryKit { return r.kit }

func (o *RecoverOptions) validate() error {
	if o.BundlePath == "" {
		return errors.New("backup: recover needs a bundle")
	}
	if o.DataDir == "" {
		return errors.New("backup: recover needs a data directory")
	}
	if o.Force && o.Drill {
		return errors.New("backup: a recovery drill cannot force replacement of the data directory")
	}
	if (len(o.Identities) > 0) == (o.Passphrase != "") {
		return errors.New("backup: recover needs exactly one of an identity or a passphrase")
	}
	return nil
}

// RecoverInstance restores an instance bundle into an empty data directory:
// checksum, decrypt, database and every file store, migrations forward,
// runtime state reset, sessions dropped, auth keys rotated, the recovery kit
// written out, and every automation held. The report is returned (and stored
// in the restored database) even when the result is partial; an error means
// the result is failed.
func RecoverInstance(ctx context.Context, opts RecoverOptions) (_ *RecoverReport, retErr error) {
	ctx, release, limitErr := BeginRestoreRead(ctx)
	if limitErr != nil {
		return nil, limitErr
	}
	defer release()
	committed := false
	defer func() {
		if retErr == nil && !committed {
			retErr = restoreOperationError(ctx)
		}
	}()

	if err := opts.validate(); err != nil {
		return nil, err
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	dataDir, err := filepath.Abs(opts.DataDir)
	if err != nil {
		return nil, err
	}
	rep := &RecoverReport{
		Result: RestoreResultFailed, BundlePath: opts.BundlePath, DataDir: dataDir,
		DatabasePath: filepath.Join(dataDir, "crewship.db"),
		Stores:       map[string]StoreRestore{}, Incomplete: []IncompleteItem{},
		Warnings: []string{}, Notes: []string{}, RuntimeReset: []string{}, Holds: []quiesce.Hold{},
	}
	// SQLite interprets URI delimiters when opening the published database.
	// The staging name shares its parent but omits the target's basename.
	if opts.Force && strings.ContainsAny(rep.DatabasePath, "?#%") {
		return rep, errors.New("backup: recovery database path contains SQLite URI delimiters")
	}

	// 1. Integrity before anything is written.
	vr, err := Verify(ctx, opts.BundlePath)
	if err != nil {
		return rep, err
	}
	m := vr.Manifest
	if m != nil {
		rep.FormatVersion = m.FormatVersion
	}
	if !vr.Valid {
		return rep, fmt.Errorf("backup: bundle failed its checksum: %w", vr.Err)
	}
	if m.Scope != ScopeInstance || m.Contents.Instance == nil {
		return rep, fmt.Errorf("%w (scope %s): restore a workspace or crew bundle with `crewship backup restore`", ErrNotInstanceBundle, m.Scope)
	}
	rep.SourceHost = m.SourceInstance.Hostname
	rep.BundleCreatedAt = m.CreatedAt
	rep.Workspaces = m.Contents.Instance.Workspaces
	rep.VaultEnvelopes = m.Contents.Instance.VaultEnvelopes
	rep.Incomplete = append(rep.Incomplete, m.Contents.Incomplete...)

	// 2. The target.
	var stage *forceStage
	if opts.Force {
		stage, err = beginForceStage(ctx, dataDir)
		if err != nil {
			return rep, err
		}
		defer stage.close()
		dataDir = stage.dir
		logger.DebugContext(ctx, "recovery target cloned")
		// Until publication these counts and paths describe the scratch
		// generation. A failed report must describe the active target.
		before := *rep
		rep.Stores = map[string]StoreRestore{}
		defer func() {
			if !committed {
				*rep = before
			}
		}()
		if err := rejectPrivateArtifactRecoveryTarget(ctx, dataDir, opts.DiscardWorkbooks); err != nil {
			return rep, err
		}
		// A booted target holds its own audit keyring. Refuse one recovery
		// could not back up and replace now, before the database is removed.
		if err := journal.CheckSigningKeysReplaceable(dataDir); err != nil {
			return rep, err
		}
	}
	if err := prepareDataDir(dataDir, opts.Force); err != nil {
		return rep, err
	}

	// This directory is exclusively recovery staging. A forced retry replaces
	// its previous images and plan, rather than mixing recovery generations.
	recoveryRoot, err := os.OpenRoot(dataDir)
	if err != nil {
		return rep, err
	}
	defer recoveryRoot.Close()
	if opts.Force {
		for _, name := range []string{RecoveredServicesDir, RecoveredCrewsDir, RecoveredEnvironmentsDir} {
			if err := recoveryRoot.RemoveAll(name); err != nil {
				return rep, err
			}
		}
	}
	var ex *extractedInstance
	defer func() {
		if stage == nil && retErr != nil && (ex == nil || ex.services == nil || !ex.services.serviceRecoveryCommitted) {
			if err := recoveryRoot.RemoveAll(RecoveredServicesDir); err != nil {
				logger.Error("backup: remove failed recovery service staging", "error", err)
			}
		}
	}()
	// 3. Decrypt and extract.
	ex, err = extractInstancePayload(ctx, opts, m, dataDir)
	if err != nil {
		return rep, err
	}
	logger.DebugContext(ctx, "recovery payload extracted")
	rep.kit = ex.kit
	for store, sr := range ex.stores {
		sr.Path = RecoveredStoreDir(rep.DataDir, store)
		rep.Stores[store] = sr
	}
	for _, archive := range ex.crewArchives {
		rep.CrewArchives = append(rep.CrewArchives, filepath.Join(rep.DataDir, RecoveredCrewsDir, filepath.Base(archive)))
	}
	rep.ServiceSnapshots = m.Contents.ServiceSnapshots
	gaps := ex.compareIndex(m)
	if stage != nil {
		if len(gaps) > 0 {
			return rep, fmt.Errorf("backup: invalid file index: %s", gaps[0].Detail)
		}
		if err := ex.validateForceContents(ctx, m, dataDir); err != nil {
			return rep, err
		}
		logger.DebugContext(ctx, "recovery snapshot validated")
	}
	rep.Incomplete = append(rep.Incomplete, gaps...)
	if m.Contents.Instance.DatabaseSHA256 != "" && ex.dbSHA != m.Contents.Instance.DatabaseSHA256 {
		return rep, fmt.Errorf("backup: the database in the bundle does not match its manifest digest")
	}
	if st, ok := m.Contents.Instance.Stores[StorePageProjects]; ok && st.Configured {
		rep.PageProjectsPath = RecoveredStoreDir(rep.DataDir, StorePageProjects)
	}
	if ex.kit != nil {
		rep.RecoveryKit = true
		for _, v := range ex.kit.Versions {
			rep.KitVersions = append(rep.KitVersions, v.Version)
		}
	}

	// 4. The database: migrations forward, runtime reset, holds.
	var final *forceRecoveryValidation
	if stage != nil {
		final = &forceRecoveryValidation{memoryObjects: make(map[string]int64)}
	}
	if err := finishRecoveredDatabase(ctx, rep, ex.kit, dataDir, logger, instanceServiceRecovery{payload: ex.services, count: m.Contents.ServiceSnapshots, validation: final, restoredStores: ex.hashes}); err != nil {
		return rep, err
	}

	// 5. Keys and secrets.
	logger.DebugContext(ctx, "recovery database prepared")
	if !opts.Drill {
		if stage != nil && (ex.kit == nil || len(ex.kit.AuditKeyring) == 0) {
			if err := recoveryRoot.Remove(journal.SigningKeysFile); err != nil && !errors.Is(err, os.ErrNotExist) {
				return rep, err
			}
		}
		if stage != nil && ex.kit == nil {
			if err := recoveryRoot.Remove(RecoveredKeysFile); err != nil && !errors.Is(err, os.ErrNotExist) {
				return rep, err
			}
		}
		if err := writeRecoveredSecrets(rep, ex.kit, dataDir); err != nil {
			return rep, err
		}
		logger.DebugContext(ctx, "recovery secrets written")
	}
	envelopes := 0
	for _, n := range rep.VaultEnvelopes {
		envelopes += n
	}
	if ex.kit == nil && envelopes > 0 {
		rep.Incomplete = append(rep.Incomplete, IncompleteItem{
			Kind: IncompleteCredentialLocked, Count: envelopes,
			Detail: fmt.Sprintf("%d sealed value(s) (credentials, webhook secrets, integration keys) need the source's vault keys and the bundle carries no recovery kit: they stay unreadable until you set %s to the original values", envelopes, strings.Join(envNamesFor(rep.VaultEnvelopes), ", ")),
		})
	}
	if len(ex.crewArchives) > 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("%d workspace(s) carry crew container files; they are kept under %s and are not in any container yet — start the crews, then land them from a workspace bundle or copy them in", len(ex.crewArchives), filepath.Join(rep.DataDir, RecoveredCrewsDir)))
	}
	if len(m.Contents.Environments) > 0 || (opts.Force && len(ex.envArchives) > 0) {
		if err := stageRecoveredEnvironmentLayers(ctx, rep, m, opts, dataDir, ex.envArchives); err != nil {
			return rep, err
		}
	}
	if rep.PageProjectsPath != "" {
		rep.Notes = append(rep.Notes, "set CREWSHIP_PAGE_PROJECTS_PATH="+rep.PageProjectsPath+" before starting the server")
	}
	rep.Notes = append(rep.Notes, "every automation is held: resume each with `crewship admin instance holds resume <key>` once you have checked the server")

	rep.Result = RestoreResultOK
	if len(rep.Incomplete) > 0 {
		rep.Result = RestoreResultPartial
	}
	rep.Summary = recoverSummary(rep)

	// 6. Write the report into the restored database.
	if stage != nil {
		rep.PreviousDataDir = stage.previous
	}
	logger.DebugContext(ctx, "writing recovery report")
	if err := recordRecoverReport(ctx, rep, opts, dataDir); err != nil {
		if stage != nil {
			return rep, fmt.Errorf("backup: store staged recovery report: %w", err)
		}
		rep.Warnings = append(rep.Warnings, "the restore report could not be stored: "+err.Error())
	}
	if stage != nil {
		if err := final.verify(ctx, rep, ex.kit, dataDir); err != nil {
			return rep, err
		}
		logger.DebugContext(ctx, "publishing recovered directory")
		if err := restoreOperationError(ctx); err != nil {
			return rep, err
		}
		committed, err = stage.commit(ctx)
		if !committed {
			return rep, fmt.Errorf("backup: recovered generation was not published: %w", err)
		}
		logger.DebugContext(ctx, "recovered directory published")
		if err != nil {
			rep.Warnings = append(rep.Warnings, "the recovered directory is active, but its publication could not be confirmed durable: "+err.Error())
		}
	}
	return rep, nil
}

func recoverSummary(r *RecoverReport) string {
	files := 0
	for _, s := range r.Stores {
		files += s.Files
	}
	head := "Instance restored"
	if r.Result == RestoreResultPartial {
		head = "Instance restored with gaps"
	}
	return fmt.Sprintf("%s: %d workspace(s), %d file(s), %d migration(s) applied, automations held", head, len(r.Workspaces), files, r.MigrationsApplied)
}

func envNamesFor(versions map[string]int) []string {
	vs := make([]string, 0, len(versions))
	for v, n := range versions {
		if n > 0 {
			vs = append(vs, v)
		}
	}
	sort.Strings(vs)
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, keyEnvName(v))
	}
	return out
}

// prepareDataDir creates the directory, refusing a non-empty one without
// force. With force the old database (and its WAL/SHM) is removed so the
// snapshot is not mixed with a stale log.
func prepareDataDir(dir string, force bool) error {
	entries, err := os.ReadDir(dir)
	switch {
	case os.IsNotExist(err):
		return os.MkdirAll(dir, 0o700)
	case err != nil:
		return err
	}
	if len(entries) > 0 && !force {
		return fmt.Errorf("%w: %s (pass --force to replace its database and overwrite the files the bundle carries)", ErrDataDirNotEmpty, dir)
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(filepath.Join(dir, "crewship.db"+suffix)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

type extractedInstance struct {
	dbBytes      int64
	dbSHA        string
	stores       map[string]StoreRestore
	hashes       map[string]map[string]IndexEntry // store → path → what landed
	index        *InstanceIndex
	kit          *RecoveryKit
	crewArchives []string
	services     *ExtractedPayload
	envArchives  []string
}

// openInstancePayload opens the bundle and returns a plaintext payload
// reader over the decrypted tar.zst, plus a closer.
func openInstancePayload(path string, identities []age.Identity, passphrase string, contexts ...context.Context) (*Manifest, *TarZstReader, func(), error) {
	ctx := context.Background()
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	m, tr, _, closeAll, err := readInstancePayload(ctx, path, identities, passphrase, nil)
	return m, tr, closeAll, err
}

func readInstancePayload(ctx context.Context, path string, identities []age.Identity, passphrase string, expected *Manifest) (*Manifest, *TarZstReader, func() error, func(), error) {
	ctx, release, limitErr := ensureReadBudget(ctx)
	if limitErr != nil {
		return nil, nil, nil, nil, limitErr
	}
	success := false
	defer func() {
		if !success {
			release()
		}
	}()
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	m, sealed, closeBundle, err := ReadBundleStream(f, ctx)
	if err != nil {
		_ = f.Close()
		return m, nil, nil, nil, err
	}
	var hash *HashingReader
	if expected != nil {
		if m.Encryption.Enabled != expected.Encryption.Enabled || m.Checksums.PayloadSHA256 != expected.Checksums.PayloadSHA256 {
			_ = closeBundle()
			_ = f.Close()
			return m, nil, nil, nil, errors.New("backup: bundle changed after checksum verification")
		}
		hash = NewHashingReader(sealed)
		sealed = hash
	}
	var plain io.Reader
	switch {
	case !m.Encryption.Enabled:
		plain = sealed
	case passphrase != "":
		plain, err = DecryptStreamPassphrase(sealed, passphrase)
	default:
		plain, err = DecryptStream(sealed, identities...)
	}
	if err != nil {
		_ = closeBundle()
		_ = f.Close()
		return m, nil, nil, nil, err
	}
	tr, err := NewTarZstReader(plain, ctx)
	if err != nil {
		_ = closeBundle()
		_ = f.Close()
		return m, nil, nil, nil, err
	}
	var finish func() error
	if hash != nil {
		finish = func() error {
			// tar EOF can precede the zstd checksum or age's final authenticated chunk.
			if _, err := io.Copy(io.Discard, &restoreReadGuard{ctx: ctx, r: tr.zr, b: readBudget(ctx), decoded: true}); err != nil {
				return err
			}
			if _, err := io.Copy(io.Discard, plain); err != nil {
				return err
			}
			return VerifyChecksum(expected.Checksums.PayloadSHA256, hash.Sum())
		}
	}
	success = true
	return m, tr, finish, func() { _ = tr.Close(); _ = closeBundle(); _ = f.Close(); release() }, nil
}

func extractInstancePayload(ctx context.Context, opts RecoverOptions, m *Manifest, dataDir string) (*extractedInstance, error) {
	var expected *Manifest
	if opts.Force {
		expected = m
	}
	_, tr, finish, closeAll, err := readInstancePayload(ctx, opts.BundlePath, opts.Identities, opts.Passphrase, expected)
	if err != nil {
		return nil, err
	}
	defer closeAll()
	serviceDir := filepath.Join(dataDir, RecoveredServicesDir)
	if err := os.MkdirAll(serviceDir, 0700); err != nil {
		return nil, err
	}
	services := &ExtractedPayload{storage: LocalStorageOps{}, tempDir: serviceDir, serviceImages: map[string]string{}, serviceMetadata: map[string]serviceSnapshot{}}
	var serviceBytes int64
	ex := &extractedInstance{services: services, stores: map[string]StoreRestore{}, hashes: map[string]map[string]IndexEntry{}}
	for _, s := range InstanceStores {
		if st, ok := m.Contents.Instance.Stores[s]; ok && st.Configured {
			ex.stores[s] = StoreRestore{Path: RecoveredStoreDir(dataDir, s)}
			ex.hashes[s] = map[string]IndexEntry{}
		}
	}
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("backup: read payload: %w", err)
		}
		if opts.Force {
			if hdr.Typeflag != '0' || filepath.ToSlash(filepath.Clean(hdr.Name)) != hdr.Name || seen[hdr.Name] {
				return nil, fmt.Errorf("backup: invalid or duplicate instance payload entry %q", hdr.Name)
			}
			seen[hdr.Name] = true
		}
		if hdr.Typeflag != '0' && hdr.Typeflag != 0 {
			continue // only regular files are ever written
		}
		name := hdr.Name
		switch {
		case strings.HasPrefix(name, serviceSnapshotsPrefix):
			if err := services.extractServiceSnapshot(ctx, tr, hdr, name, &serviceBytes); err != nil {
				return nil, err
			}
		case name == instanceDBEntry:
			dst := filepath.Join(dataDir, "crewship.db")
			sha, size, err := writeExtracted(ctx, dst, tr)
			if err != nil {
				return nil, fmt.Errorf("backup: write database: %w", err)
			}
			ex.dbSHA, ex.dbBytes = sha, size
		case name == instanceIndexEntry:
			var idx InstanceIndex
			if hdr.Size > maxBackupDBDumpBytes {
				return nil, errors.New("backup: file index exceeds size limit")
			}
			dec := json.NewDecoder(RestoreBufferedReader(ctx, tr))
			if err := dec.Decode(&idx); err != nil {
				return nil, fmt.Errorf("backup: read file index: %w", err)
			}
			if opts.Force && dec.Decode(new(any)) != io.EOF {
				return nil, errors.New("backup: trailing data in file index")
			}
			ex.index = &idx
		case name == RecoveryKitPath:
			if hdr.Size > 1<<20 {
				return nil, errors.New("backup: recovery kit exceeds size limit")
			}
			data, err := io.ReadAll(io.LimitReader(RestoreBufferedReader(ctx, tr), 1<<20))
			if err != nil {
				return nil, err
			}
			kit, err := ParseRecoveryKit(data)
			if err != nil {
				return nil, err
			}
			ex.kit = kit
		case strings.HasPrefix(name, instanceFilesPrefix):
			rest := strings.TrimPrefix(name, instanceFilesPrefix)
			store, rel, ok := strings.Cut(rest, "/")
			if !ok || !knownStore(store) {
				if opts.Force {
					return nil, fmt.Errorf("backup: unknown file store in %q", name)
				}
				continue
			}
			dst, err := safepath.JoinRel(RecoveredStoreDir(dataDir, store), filepath.FromSlash(rel))
			if err != nil || dst == RecoveredStoreDir(dataDir, store) {
				return nil, fmt.Errorf("backup: refuse unsafe payload path %q", name)
			}
			sha, n, err := writeExtracted(ctx, dst, tr)
			if err != nil {
				return nil, fmt.Errorf("backup: write %s: %w", name, err)
			}
			sr := ex.stores[store]
			sr.Path = RecoveredStoreDir(dataDir, store)
			sr.Files++
			sr.Bytes += n
			ex.stores[store] = sr
			if ex.hashes[store] == nil {
				ex.hashes[store] = map[string]IndexEntry{}
			}
			ex.hashes[store][rel] = IndexEntry{Path: rel, Size: n, SHA256: sha}
		case strings.HasPrefix(name, instanceCrewsPrefix) && strings.HasSuffix(name, instanceCrewsSuffix):
			id := strings.TrimSuffix(strings.TrimPrefix(name, instanceCrewsPrefix), instanceCrewsSuffix)
			dst, err := safepath.JoinUnder(filepath.Join(dataDir, RecoveredCrewsDir), id+instanceCrewsSuffix)
			if err != nil {
				return nil, fmt.Errorf("backup: refuse unsafe payload path %q", name)
			}
			if _, _, err := writeExtracted(ctx, dst, tr); err != nil {
				return nil, err
			}
			ex.crewArchives = append(ex.crewArchives, dst)
		case strings.HasPrefix(name, instanceEnvPrefix) && strings.HasSuffix(name, instanceCrewsSuffix):
			id := strings.TrimSuffix(strings.TrimPrefix(name, instanceEnvPrefix), instanceCrewsSuffix)
			dst, err := safepath.JoinUnder(filepath.Join(dataDir, RecoveredEnvironmentsDir), id+instanceCrewsSuffix)
			if err != nil {
				return nil, fmt.Errorf("backup: refuse unsafe payload path %q", name)
			}
			if _, _, err := writeExtracted(ctx, dst, tr); err != nil {
				return nil, err
			}
			ex.envArchives = append(ex.envArchives, dst)
		default:
			if opts.Force {
				return nil, fmt.Errorf("backup: unknown instance payload entry %q", name)
			}
		}
	}
	if finish != nil {
		if err := finish(); err != nil {
			return nil, fmt.Errorf("backup: finish verifying payload: %w", err)
		}
	}
	if ex.dbSHA == "" {
		return nil, fmt.Errorf("%w: the payload carries no database", ErrInvalidManifest)
	}
	for _, path := range services.serviceImages {
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return nil, err
		}
		err = file.Sync()
		closeErr := file.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	if err := services.validateServiceSnapshotArchive(ctx, m.Contents.ServiceSnapshots); err != nil {
		return nil, err
	}
	// Make image directory entries durable before the database references them.
	dir, err := os.Open(serviceDir)
	if err != nil {
		return nil, err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return nil, err
	}
	return ex, nil
}

func knownStore(s string) bool {
	for _, x := range InstanceStores {
		if x == s {
			return true
		}
	}
	return false
}

func writeExtracted(ctx context.Context, dst string, r io.Reader) (string, int64, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", 0, err
	}
	if info, err := os.Lstat(dst); err == nil && !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("refuse to overwrite non-regular file %s", dst)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(RestoreWriter(ctx, io.MultiWriter(f, h)), r)
	if err == nil {
		// A recovered data directory is the only copy the operator will
		// boot from: every landed file reaches the disk before recover
		// reports success.
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// compareIndex checks what landed against the payload's index and the
// manifest's per-store digests.
func (ex *extractedInstance) compareIndex(m *Manifest) []IncompleteItem {
	var out []IncompleteItem
	if ex.index == nil {
		return []IncompleteItem{{Kind: IncompleteFileMissing, Count: 1, Detail: "the payload carries no file index, so no file could be checked"}}
	}
	for _, store := range InstanceStores {
		entries := ex.index.Stores[store]
		want := m.Contents.Instance.Stores[store]
		if want.Configured && want.IndexSHA256 != "" && StoreIndexDigest(entries) != want.IndexSHA256 {
			out = append(out, IncompleteItem{Kind: IncompleteFileMismatch, Count: 1, Detail: fmt.Sprintf("the %s file index does not match the manifest", store)})
		}
		missing, mismatch := 0, 0
		for _, e := range entries {
			got, ok := ex.hashes[store][e.Path]
			switch {
			case !ok:
				missing++
			case got.SHA256 != e.SHA256 || got.Size != e.Size:
				mismatch++
			}
		}
		if missing > 0 {
			out = append(out, IncompleteItem{Kind: IncompleteFileMissing, Count: missing, Detail: fmt.Sprintf("%d %s file(s) listed in the bundle's index were not in its payload", missing, store)})
		}
		if mismatch > 0 {
			out = append(out, IncompleteItem{Kind: IncompleteFileMismatch, Count: mismatch, Detail: fmt.Sprintf("%d %s file(s) did not match their recorded sha256", mismatch, store)})
		}
	}
	return out
}

// validateForceContents compares the extracted generation with the source
// manifest before migrations or runtime resets alter the snapshot.
func (ex *extractedInstance) validateForceContents(ctx context.Context, m *Manifest, dataDir string) error {
	inst := m.Contents.Instance
	if ex.dbBytes != inst.DatabaseBytes || ex.dbSHA != inst.DatabaseSHA256 {
		return errors.New("backup: snapshot size or digest differs from the manifest")
	}
	for store, entries := range ex.index.Stores {
		if !knownStore(store) {
			return fmt.Errorf("backup: unknown indexed store %q", store)
		}
		want := inst.Stores[store]
		if len(entries) != want.Files || len(ex.hashes[store]) != want.Files || ex.stores[store].Bytes != want.Bytes {
			return fmt.Errorf("backup: file totals for %s differ from the manifest", store)
		}
		indexed := make(map[string]bool, len(entries))
		for _, entry := range entries {
			if indexed[entry.Path] {
				return fmt.Errorf("backup: duplicate index entry in %s", store)
			}
			indexed[entry.Path] = true
		}
	}
	for store, want := range inst.Stores {
		if !knownStore(store) {
			return fmt.Errorf("backup: unknown manifest store %q", store)
		}
		if got := ex.stores[store]; got.Files != want.Files || got.Bytes != want.Bytes || (!want.Configured && got.Files > 0) || (want.Configured && want.IndexSHA256 != StoreIndexDigest(ex.index.Stores[store])) {
			return fmt.Errorf("backup: store %s differs from the manifest", store)
		}
	}
	for store := range ex.hashes {
		if _, ok := inst.Stores[store]; !ok {
			return fmt.Errorf("backup: undeclared store %s", store)
		}
	}
	var crewIDs []string
	for _, p := range ex.crewArchives {
		extracted, err := extractArchive(ctx, p)
		if err != nil {
			return fmt.Errorf("backup: validate crew archive %s: %w", filepath.Base(p), err)
		}
		if err := extracted.Close(); err != nil {
			return err
		}
		crewIDs = append(crewIDs, strings.TrimSuffix(filepath.Base(p), instanceCrewsSuffix))
	}
	if !slices.Equal(slices.Sorted(slices.Values(inst.CrewArchives)), slices.Sorted(slices.Values(crewIDs))) {
		return errors.New("backup: staged crew archives differ from the manifest")
	}
	if inst.RecoveryKit != (ex.kit != nil) {
		return errors.New("backup: recovery kit presence differs from the manifest")
	}
	keys, err := ex.kit.KeyMap()
	if err != nil {
		return err
	}
	if ex.kit != nil && (len(keys) != len(ex.kit.Versions) || (ex.kit.CurrentVersion != "" && !encryption.ValidKeyVersion(ex.kit.CurrentVersion))) {
		return errors.New("backup: invalid key versions in the recovery kit")
	}
	if !slices.Equal(slices.Sorted(maps.Keys(keys)), slices.Sorted(slices.Values(inst.KitVersions))) {
		return errors.New("backup: recovery kit key versions differ from the manifest")
	}
	db, err := openSnapshot(filepath.Join(dataDir, "crewship.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	if err := checkRecoveryIntegrity(ctx, db); err != nil {
		return err
	}
	versions := AppliedMigrationVersions(ctx, db)
	if len(versions) == 0 || !slices.Equal(versions, m.SchemaMigrationVersions) {
		return errors.New("backup: database migration ledger missing or differs from the manifest")
	}
	if newest := versions[len(versions)-1]; newest > database.MaxKnownMigrationVersion() {
		return fmt.Errorf("backup: database schema v%d requires a newer Crewship binary", newest)
	}
	counts, err := snapshotRowCounts(ctx, db)
	if err != nil {
		return err
	}
	if !maps.Equal(m.Contents.TableRowCounts, counts) {
		return errors.New("backup: database row counts differ from the manifest")
	}
	if ex.kit == nil {
		keys = nil
	}
	envelopes, err := recoveryEnvelopeInventory(ctx, db, keys)
	if err != nil {
		return err
	}
	if !maps.Equal(inst.VaultEnvelopes, envelopes) {
		return errors.New("backup: vault envelope inventory differs from the manifest")
	}
	return nil
}

// A nil key map counts envelopes in a keyless backup. With supplied keys,
// prove every envelope opens, using the runtime's v1 fallback for reading
// historical versions which lack a dedicated key.
func recoveryEnvelopeInventory(ctx context.Context, db *sql.DB, keys map[string][]byte) (map[string]int, error) {
	counts := map[string]int{}
	reads := maps.Clone(keys)
	_, err := EachSealedValue(ctx, db, func(v SealedValue) error {
		version, _ := encryption.ParseEnvelopeVersion(v.Envelope)
		counts[version]++
		if reads != nil {
			if _, ok := reads[version]; !ok {
				reads[version] = reads["v1"]
			}
			if _, err := encryption.DecryptWithKeys(v.Envelope, reads); err != nil {
				return fmt.Errorf("backup: recovered key cannot open %s.%s envelope (%s): %w", v.Table, v.Column, version, err)
			}
		}
		return nil
	})
	return counts, err
}

func checkRecoveryIntegrity(ctx context.Context, db *sql.DB) error {
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("backup: check staged SQLite integrity: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("backup: staged SQLite integrity check failed: %s", result)
	}
	// integrity_check excludes foreign keys. One orphan is enough to refuse
	// publication, including when its child is a WITHOUT ROWID table.
	var child, parent string
	var rowID sql.NullInt64
	var constraint int
	err := db.QueryRowContext(ctx, "PRAGMA main.foreign_key_check").Scan(&child, &rowID, &parent, &constraint)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("backup: staged SQLite foreign_key_check: %w", err)
	}
	return fmt.Errorf("backup: staged SQLite foreign_key_check failed: %s references missing %s (constraint %d)", child, parent, constraint)
}

// Legacy migrations resolve vault keys from the process environment. Keep
// concurrent recoveries out of this scope and shadow host-only key versions.
var recoveryEnvMu sync.Mutex

func withRecoveryEnv(kvs map[string]string, fn func() error) error {
	recoveryEnvMu.Lock()
	defer recoveryEnvMu.Unlock()
	if kvs == nil {
		return fn()
	}
	kvs = maps.Clone(kvs)
	for _, version := range encryption.ConfiguredKeyVersions() {
		name := encryption.KeyEnvVar(version)
		if _, ok := kvs[name]; !ok {
			kvs[name] = ""
		}
	}
	type prev struct {
		v  string
		ok bool
	}
	saved := map[string]prev{}
	defer func() {
		for k, p := range saved {
			if p.ok {
				_ = os.Setenv(k, p.v)
			} else {
				_ = os.Unsetenv(k)
			}
		}
	}()
	for k, v := range kvs {
		old, ok := os.LookupEnv(k)
		saved[k] = prev{old, ok}
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("backup: set recovery variable %s: %w", k, err)
		}
	}
	return fn()
}

func kitEnv(kit *RecoveryKit) (map[string]string, error) {
	if kit == nil {
		return nil, nil
	}
	out := map[string]string{encryption.KeyVersionEnvVar: "v1"}
	lines, err := kit.EnvLines()
	if err != nil {
		return nil, err
	}
	for _, l := range lines {
		k, v, _ := strings.Cut(l, "=")
		out[k] = v
	}
	return out, nil
}

// runtimeResets are the rows that belong to the process that wrote them and
// must not survive a move: sign-in sessions, pairings, locks, the scheduler
// lease, OAuth polling. Tables absent from this schema are skipped.
var runtimeResets = []struct{ table, what string }{
	{"gridcraft_intents", "workbook run intents"},
	{"gridcraft_sessions", "workbook editor sessions"},
	{"mooring_session_owners", "Mooring session owner leases"},
	{"document_creation_consents", "document creation consents"},
	{"document_iteration_intents", "document iteration intents"},
	{"artifact_project_message_intents", "artifact project message permissions"},
	{"artifact_project_run_consents", "artifact project run permissions"},
	{"artifact_run_scope", "artifact run reference scopes"},
	{"artifact_run_access", "artifact run created and read pages"},
	{"artifact_unattended_run_access", "unattended artifact run read pages"},
	{"artifact_unattended_runs", "unattended artifact runs"},
	{"artifact_run_principals", "artifact run principal admissions"},
	{"artifact_message_refs", "artifact chat references"},
	{"sessions", "sign-in sessions"},
	{"user_sessions", "sign-in sessions"},
	{"cli_pairings", "CLI pairings"},
	{"backup_locks", "backup locks"},
	{"scheduler_leader", "scheduler lease"},
	{"provider_login_refresh", "provider login refresh leases"},
	{"provider_device_logins", "provider device logins in progress"},
	{"instance_holds", "old holds"},
	{"service_backup_fences", "source service backup epochs"},
	{"service_operation_leases", "source service operation epochs"},
}

func finishRecoveredDatabase(ctx context.Context, rep *RecoverReport, kit *RecoveryKit, dataDir string, logger *slog.Logger, serviceArgs ...instanceServiceRecovery) error {
	if kit != nil {
		keys, err := kit.AuditSigningKeys()
		if err != nil {
			if !errors.Is(err, journal.ErrSigningKeyUnavailable) {
				return err
			}
			rep.Warnings = append(rep.Warnings, "the older recovery kit has no usable audit keyring: historical MACs remain unverified and affected audit writes require an explicit OWNER checkpoint")
		} else {
			ctx = journal.WithSigningKeys(ctx, keys)
		}
	}
	env, err := kitEnv(kit)
	if err != nil {
		return err
	}
	return withRecoveryEnv(env, func() (retErr error) {
		db, err := database.Open("file:" + filepath.Join(dataDir, "crewship.db"))
		if err != nil {
			return fmt.Errorf("backup: open restored database: %w", err)
		}
		defer func() { retErr = errors.Join(retErr, db.Close()) }()
		before := len(AppliedMigrationVersions(ctx, db.DB))
		pending, err := database.PendingForegroundMigrations(ctx, db.DB)
		if err != nil {
			return err
		}
		force := len(serviceArgs) > 0 && serviceArgs[0].validation != nil
		// Parse even unused configured keys. A database with no sealed rows
		// provides no decryption evidence for a malformed master key.
		var suppliedKeys map[string][]byte
		if force {
			for _, version := range encryption.ConfiguredKeyVersions() {
				key, err := encryption.ResolveKey(version)
				if err != nil {
					return fmt.Errorf("backup: invalid supplied vault key: %w", err)
				}
				if suppliedKeys == nil {
					suppliedKeys = map[string][]byte{}
				}
				suppliedKeys[version] = key.Key
			}
			if pending > 0 && (kit != nil || len(suppliedKeys) > 0) {
				if err := encryption.VerifyCurrentKey(); err != nil {
					return fmt.Errorf("backup: validate migration encryption key: %w", err)
				}
			}
			if pending > 0 && kit == nil && suppliedKeys != nil {
				if _, err := recoveryEnvelopeInventory(ctx, db.DB, suppliedKeys); err != nil {
					return err
				}
			}
		}
		if pending > 0 && kit == nil && os.Getenv("ENCRYPTION_KEY") == "" {
			if force {
				return fmt.Errorf("backup: %d pending migrations require the original vault key before forced recovery", pending)
			}
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d migration(s) are pending and no vault key is available; they run when the server first starts, with the ENCRYPTION_KEY it is given — give it the original one", pending))
		} else if pending > 0 || force {
			if err := database.Migrate(ctx, db.DB, logger); err != nil {
				return fmt.Errorf("backup: migrate restored database: %w", err)
			}
		}
		// An instance snapshot may already carry the current schema ledger.
		// Qualify its rows while the recovered directory is still offline.
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := convertRestoreWaitpoints(ctx, tx); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		rep.MigrationsApplied = len(AppliedMigrationVersions(ctx, db.DB)) - before

		// A physical snapshot carries source-instance execution approvals too.
		// Recovery must require local review even when no migration is pending.
		count, err := resetRecoveredHostHooks(ctx, db.DB)
		if err != nil {
			return err
		}
		if count > 0 {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d restored host shell hook(s) require instance administrator review; configurations are preserved and disabled. Review in Settings > Lifecycle hooks, then use crewship hooks enable <id>.", count))
		}

		if force && serviceArgs[0].count == 0 {
			// A manifest cannot suppress a quota-service image declared by the database.
			dump, err := instanceServiceDump(ctx, db.DB)
			if err != nil {
				return err
			}
			if err := requireSupportedDumpServiceBackups(dump, nil); err != nil {
				return err
			}
		}
		if len(serviceArgs) != 0 && serviceArgs[0].count > 0 {
			services := serviceArgs[0].payload
			count := serviceArgs[0].count
			if err := stageInstanceServiceRecovery(ctx, db.DB, services, count, dataDir); err != nil {
				return err
			}
			if count > 0 {
				rep.Notes = append(rep.Notes, fmt.Sprintf("%d quota-service image(s) verified and staged; services remain in maintenance until their data are landed on the new host", count))
			}
		}
		if err := preservePrivateArtifactContextProof(ctx, db.DB); err != nil {
			return err
		}
		for _, r := range runtimeResets {
			if len(serviceArgs) != 0 && serviceArgs[0].count > 0 && (r.table == "service_backup_fences" || r.table == "service_operation_leases") {
				continue
			}
			res, err := db.ExecContext(ctx, `DELETE FROM `+r.table) // nosemgrep: gosql-sqli — table names are constants
			if err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "no such table") {
					continue
				}
				return fmt.Errorf("backup: reset %s: %w", r.table, err)
			}
			n, _ := res.RowsAffected()
			if r.what == "sign-in sessions" {
				rep.SessionsDropped += int(n)
			}
			if n > 0 {
				rep.RuntimeReset = append(rep.RuntimeReset, fmt.Sprintf("%s (%d)", r.what, n))
			}
		}
		if err := resetRecoveredGridCraft(ctx, db.DB); err != nil {
			return err
		}
		rep.RuntimeReset = append(rep.RuntimeReset, "workbook writer generations and reservations")
		// Close source delivery capabilities, but retain issued expiry bounds for
		// read-revocation barriers if recovery shares the same OS boot. Never
		// interpret discarded runtime metadata as an acknowledgement.
		if _, err := db.ExecContext(ctx, `UPDATE mooring_delivery_gates SET closed=1,blocked=0`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return fmt.Errorf("backup: close source Mooring delivery gates: %w", err)
		}
		if err := resetRecoveredCraftEditLeases(ctx, db.DB); err != nil {
			return err
		}
		// Physical instance bundles include document bytes and their source leases.
		// Preserve content/history, fence every source engine before first boot.
		var documentsExist bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='documents')`).Scan(&documentsExist); err != nil {
			return err
		} else if documentsExist {
			// Older D1a physical bundles predate the editor generation column.
			query := `UPDATE documents SET epoch=epoch+1,writer='',owner='',lease_until=0`
			columns, err := db.QueryContext(ctx, `PRAGMA table_info(documents)`)
			if err != nil {
				return err
			}
			for columns.Next() {
				var cid, notnull, pk int
				var name, kind string
				var def any
				if err := columns.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
					columns.Close()
					return err
				}
				if name == "generation" {
					query = `UPDATE documents SET epoch=epoch+1,generation=generation+1,writer='',owner='',lease_until=0`
				}
			}
			if err := columns.Err(); err != nil {
				columns.Close()
				return err
			}
			columns.Close()
			if _, err := db.ExecContext(ctx, query); err != nil {
				return fmt.Errorf("backup: reset document writer leases: %w", err)
			}
			rep.RuntimeReset = append(rep.RuntimeReset, "document writer leases and epochs")
			if err := rotateDocumentDatabaseIdentity(ctx, db); err != nil {
				return err
			}
		}
		// Backup runs that were in flight (or queued) on the source when the
		// bundle was taken — the run that took it, usually — are not this
		// server's to finish: left "running", the scheduler would treat them
		// as interrupted by a crash and retry them on first boot.
		if res, err := db.ExecContext(ctx, `UPDATE backup_runs SET status = 'interrupted',
			error = 'was running on the source server when this backup was taken; not retried after a restore',
			ended_at = ? WHERE status = 'running'`, tsformat.Format(time.Now())); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "no such table") {
				return fmt.Errorf("backup: close source backup runs: %w", err)
			}
		} else if n, _ := res.RowsAffected(); n > 0 {
			rep.RuntimeReset = append(rep.RuntimeReset, fmt.Sprintf("backup runs in flight on the source (%d)", n))
		}
		// The new host fills in its own identity at first boot.
		if _, err := db.ExecContext(ctx, `UPDATE instance_config SET hostname = '' WHERE id = 1`); err != nil && !strings.Contains(err.Error(), "no such table") {
			return err
		}
		// memory_versions.payload_ref is an absolute path on the source.
		memRoot := RecoveredStoreDir(rep.DataDir, StoreMemory)
		if force {
			if err := rebaseForceMemoryVersions(ctx, db.DB, dataDir, memRoot, serviceArgs[0].restoredStores, serviceArgs[0].validation.memoryObjects); err != nil {
				return fmt.Errorf("backup: verify memory versions: %w", err)
			}
		} else if _, err := db.ExecContext(ctx, `UPDATE memory_versions SET payload_ref = ? || '/versions/' || substr(sha256, 1, 2) || '/' || sha256 WHERE length(sha256) = 64`, memRoot); err != nil && !strings.Contains(err.Error(), "no such table") {
			return fmt.Errorf("backup: rewrite memory blob paths: %w", err)
		}

		holds := recoveredHolds(ctx, db.DB)
		if err := quiesce.SetHolds(ctx, db, holds); err != nil {
			return err
		}
		rep.Holds = holds
		if force && kit == nil && pending > 0 {
			// Preserve exactly the key set checked before migration, even after
			// withRecoveryEnv restores the caller's environment.
			serviceArgs[0].validation.migrationKeys = suppliedKeys
		}
		return nil
	})
}

func countOr0(ctx context.Context, db *sql.DB, q string) int {
	var n int
	if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0
	}
	return n
}

// recoveredHolds are the holds a restored instance boots with.
func recoveredHolds(ctx context.Context, db *sql.DB) []quiesce.Hold {
	now := time.Now().UTC()
	schedules := countOr0(ctx, db, `SELECT COUNT(*) FROM agents WHERE schedule_enabled = 1 AND deleted_at IS NULL`) +
		countOr0(ctx, db, `SELECT COUNT(*) FROM pipeline_schedules WHERE enabled = 1`)
	recurring := countOr0(ctx, db, `SELECT COUNT(*) FROM recurring_issues WHERE enabled = 1`)
	webhooks := countOr0(ctx, db, `SELECT COUNT(*) FROM pipeline_webhooks`) +
		countOr0(ctx, db, `SELECT COUNT(*) FROM agents WHERE webhook_secret IS NOT NULL AND webhook_secret != '' AND deleted_at IS NULL`)
	queued := countOr0(ctx, db, `SELECT COUNT(*) FROM work_items WHERE state IN ('queued','waiting','retry_wait')`)
	pending := countOr0(ctx, db, `SELECT COUNT(*) FROM pending_runs WHERE status = 'pending'`)
	const reason = "instance restore"
	return []quiesce.Hold{
		{Key: quiesce.HoldQueue, Reason: reason, Count: queued + pending, CreatedAt: now,
			Detail: fmt.Sprintf("%d queued work item(s) and %d delayed routine run(s); notification retries paused", queued, pending)},
		{Key: quiesce.HoldRoutines, Reason: reason, Count: schedules + recurring, CreatedAt: now,
			Detail: fmt.Sprintf("%d schedule(s) and %d recurring issue(s) will not fire", schedules, recurring)},
		{Key: quiesce.HoldWebhooks, Reason: reason, Count: webhooks, CreatedAt: now,
			Detail: fmt.Sprintf("%d inbound webhook(s) answer 503", webhooks)},
	}
}

// writeRecoveredSecrets writes secrets.env (the file `crewship start` reads)
// and, with a kit, recovered-keys.env. The session key is rotated either way:
// every session signed on the source stops working here.
func writeRecoveredSecrets(rep *RecoverReport, kit *RecoveryKit, dataDir string) error {
	secretsPath := filepath.Join(dataDir, recoveredSecretsFile)
	values := map[string]string{}
	if existing, err := readEnvFile(secretsPath); err == nil {
		values = existing
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// The master keys a booted target sealed its own keyring with, before
	// the kit's replace them: they let a replaced keyring be re-sealed.
	previousMaster := previousMasterKeys(values)
	newSecret, err := RotateAuthKeys(func(s string) error { values[NextAuthSecretEnv] = s; return nil })
	if err != nil {
		return err
	}
	rep.AuthKeysRotated = true
	hmacKey := make([]byte, 32)
	if _, err := rand.Read(hmacKey); err != nil {
		return err
	}
	values["CREWSHIP_ADMIN_TOKEN_HMAC_KEY"] = hex.EncodeToString(hmacKey)
	for k := range values {
		if k == "ENCRYPTION_KEY" || strings.HasPrefix(k, "ENCRYPTION_KEY_V") || k == encryption.KeyVersionEnvVar || k == journal.SigningKeysPathEnv {
			delete(values, k)
		}
	}
	kitValues, err := kitEnv(kit)
	if err != nil {
		return err
	}
	if v := kitValues["ENCRYPTION_KEY"]; v != "" {
		values["ENCRYPTION_KEY"] = v
	}
	if kit != nil && len(kit.AuditKeyring) > 0 {
		var auditPath, replaced, unreadable string
		if err := withRecoveryEnv(kitValues, func() (err error) {
			auditPath, replaced, unreadable, err = journal.ReplaceSigningKeys(dataDir, kit.AuditKeyring, time.Now(), previousMaster)
			return err
		}); err != nil {
			return err
		}
		auditPath = filepath.Join(rep.DataDir, filepath.Base(auditPath))
		if replaced != "" {
			replaced = filepath.Join(rep.DataDir, filepath.Base(replaced))
			rep.Notes = append(rep.Notes, "the data dir's previous audit keyring was replaced by the bundle's and kept as "+replaced)
		}
		if unreadable != "" {
			rep.Warnings = append(rep.Warnings, "the data dir's previous audit keyring could not be read ("+unreadable+"); it was kept unchanged as "+replaced)
		}
		kitValues[journal.SigningKeysPathEnv] = auditPath
	}
	if err := writeEnvFile(secretsPath, []string{
		"# crewship: secrets for a recovered instance, written by `crewship recover`.",
		"# NEXTAUTH_SECRET and CREWSHIP_ADMIN_TOKEN_HMAC_KEY are new: every session from the source is gone.",
	}, values); err != nil {
		return err
	}
	rep.SecretsFile = filepath.Join(rep.DataDir, recoveredSecretsFile)
	if kit == nil {
		rep.Warnings = append(rep.Warnings, "the bundle carries no recovery kit: set the source's ENCRYPTION_KEY (and ENCRYPTION_KEY_V2… for later key versions) before starting the server, or credentials stay unreadable")
		return nil
	}
	keysPath := filepath.Join(dataDir, RecoveredKeysFile)
	kitValues[NextAuthSecretEnv] = newSecret
	if err := writeEnvFile(keysPath, []string{
		"# crewship: vault keys from the backup's recovery kit, written by `crewship recover`.",
		"# Give these to the server before it starts — e.g. systemd EnvironmentFile=" + filepath.Join(rep.DataDir, RecoveredKeysFile) + ",",
		"# or export them. ENCRYPTION_KEY is also in secrets.env; ENCRYPTION_KEY_V2 and later are",
		"# read from the environment only. Whoever reads this file can read every secret in the",
		"# instance: move the keys into your secret store and delete it.",
	}, kitValues); err != nil {
		return err
	}
	rep.KeysFile = filepath.Join(rep.DataDir, RecoveredKeysFile)
	return nil
}

// Check the files the recovered installation will actually read.
func validateRecoveredKeyFiles(rep *RecoverReport, kit *RecoveryKit, dataDir string) (map[string][]byte, error) {
	values, err := readEnvFile(filepath.Join(dataDir, recoveredSecretsFile))
	if err != nil {
		return nil, err
	}
	for name, size := range map[string]int{NextAuthSecretEnv: 64, "CREWSHIP_ADMIN_TOKEN_HMAC_KEY": 32} {
		key, err := hex.DecodeString(values[name])
		if err != nil || len(key) != size {
			return nil, fmt.Errorf("backup: invalid recovered %s", name)
		}
	}
	if kit == nil {
		return nil, nil
	}
	want, err := kitEnv(kit)
	if err != nil {
		return nil, err
	}
	if values["ENCRYPTION_KEY"] != want["ENCRYPTION_KEY"] {
		return nil, errors.New("backup: staged master key differs from the kit")
	}
	want[NextAuthSecretEnv] = values[NextAuthSecretEnv]
	if len(kit.AuditKeyring) > 0 {
		want[journal.SigningKeysPathEnv] = filepath.Join(rep.DataDir, journal.SigningKeysFile)
	}
	got, err := readEnvFile(filepath.Join(dataDir, RecoveredKeysFile))
	if err != nil {
		return nil, err
	}
	if !maps.Equal(want, got) {
		return nil, errors.New("backup: staged vault key file differs from the recovered keys")
	}
	// Decode the actual published assignments. This check must still fail
	// if a migration accidentally consumed a key from the recovering host.
	keys := make(map[string][]byte, len(kit.Versions))
	for _, v := range kit.Versions {
		key, err := hex.DecodeString(got[v.Env])
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("backup: invalid staged %s", v.Env)
		}
		keys[v.Version] = key
	}
	return keys, nil
}

type forceRecoveryValidation struct {
	memoryObjects map[string]int64
	migrationKeys map[string][]byte
}

// Run after the report writer closes. Its INSERT and any triggers have now
// completed, and nothing else writes the staged database before publication.
func (v *forceRecoveryValidation) verify(ctx context.Context, rep *RecoverReport, kit *RecoveryKit, dataDir string) (retErr error) {
	keys, err := validateRecoveredKeyFiles(rep, kit, dataDir)
	if err != nil {
		return err
	}
	if kit == nil {
		keys = v.migrationKeys
	}
	db, err := openSnapshot(filepath.Join(dataDir, "crewship.db"))
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, db.Close()) }()
	if err := checkRecoveryIntegrity(ctx, db); err != nil {
		return err
	}
	if err := verifyForceMemoryVersions(ctx, db, dataDir, RecoveredStoreDir(rep.DataDir, StoreMemory), v.memoryObjects); err != nil {
		return fmt.Errorf("backup: verify final memory versions: %w", err)
	}
	if keys != nil {
		_, err = recoveryEnvelopeInventory(ctx, db, keys)
	}
	return err
}

// previousMasterKeys resolves master-key versions from the target's previous
// secrets.env, then this process's environment, with Decrypt's fallback from
// a missing ENCRYPTION_KEY_V<N> to ENCRYPTION_KEY.
func previousMasterKeys(persisted map[string]string) journal.MasterKeyFunc {
	recoveryEnvMu.Lock()
	defer recoveryEnvMu.Unlock()
	// Snapshot now: the call runs later, under the kit's environment.
	snapshot := map[string]string{}
	for _, kv := range os.Environ() {
		if name, value, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(name, "ENCRYPTION_KEY") {
			snapshot[name] = value
		}
	}
	for k, v := range persisted {
		if v != "" {
			snapshot[k] = v
		}
	}
	return func(version string) ([]byte, error) {
		for _, name := range []string{encryption.KeyEnvVar(version), "ENCRYPTION_KEY"} {
			value := snapshot[name]
			if value == "" {
				continue
			}
			key, err := hex.DecodeString(value)
			if err != nil || len(key) != 32 {
				return nil, fmt.Errorf("%s is not a 32-byte hex key", name)
			}
			return key, nil
		}
		return nil, fmt.Errorf("%s is not set", encryption.KeyEnvVar(version))
	}
}

func readEnvFile(p string) (map[string]string, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return out, nil
}

// ReadEnvFile parses KEY=value lines (comments and blanks skipped).
func ReadEnvFile(p string) (map[string]string, error) { return readEnvFile(p) }

func writeEnvFile(p string, header []string, values map[string]string) error {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, h := range header {
		b.WriteString(h + "\n")
	}
	for _, k := range keys {
		b.WriteString(k + "=" + values[k] + "\n")
	}
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, err = f.WriteString(b.String())
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func recordRecoverReport(ctx context.Context, rep *RecoverReport, opts RecoverOptions, dataDir string) (retErr error) {
	db, err := database.Open("file:" + filepath.Join(dataDir, "crewship.db"))
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, db.Close()) }()
	body, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	kind, target := RestoreKindRestore, "empty_server"
	if opts.Drill {
		kind, target = RestoreKindDrill, "isolated"
	}
	saved, err := RecordRestoreReport(ctx, db.DB, RestoreReport{
		Kind: kind, ActorUserID: opts.Actor, BundlePath: rep.BundlePath, Target: target,
		Result: rep.Result, Report: body,
	})
	if err != nil {
		return err
	}
	rep.ReportID = saved.ID
	return nil
}
