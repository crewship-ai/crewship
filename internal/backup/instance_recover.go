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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/database"
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
	// Force allows a non-empty data directory: its database is replaced and
	// files the bundle carries are overwritten.
	Force bool
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
		if err := recoveryRoot.RemoveAll(RecoveredServicesDir); err != nil {
			return rep, err
		}
	}
	var ex *extractedInstance
	defer func() {
		if retErr != nil && (ex == nil || ex.services == nil || !ex.services.serviceRecoveryCommitted) {
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
	rep.kit = ex.kit
	for store, sr := range ex.stores {
		rep.Stores[store] = sr
	}
	rep.CrewArchives = ex.crewArchives
	rep.ServiceSnapshots = m.Contents.ServiceSnapshots
	rep.Incomplete = append(rep.Incomplete, ex.compareIndex(m)...)
	if m.Contents.Instance.DatabaseSHA256 != "" && ex.dbSHA != m.Contents.Instance.DatabaseSHA256 {
		return rep, fmt.Errorf("backup: the database in the bundle does not match its manifest digest")
	}
	if st, ok := m.Contents.Instance.Stores[StorePageProjects]; ok && st.Configured {
		rep.PageProjectsPath = RecoveredStoreDir(dataDir, StorePageProjects)
	}
	if ex.kit != nil {
		rep.RecoveryKit = true
		for _, v := range ex.kit.Versions {
			rep.KitVersions = append(rep.KitVersions, v.Version)
		}
	}

	// 4. The database: migrations forward, runtime reset, holds.
	if err := finishRecoveredDatabase(ctx, rep, ex.kit, dataDir, logger, instanceServiceRecovery{ex.services, m.Contents.ServiceSnapshots}); err != nil {
		return rep, err
	}

	// 5. Keys and secrets.
	if !opts.Drill {
		if err := writeRecoveredSecrets(rep, ex.kit, dataDir); err != nil {
			return rep, err
		}
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
		rep.Notes = append(rep.Notes, fmt.Sprintf("%d workspace(s) carry crew container files; they are kept under %s and are not in any container yet — start the crews, then land them from a workspace bundle or copy them in", len(ex.crewArchives), filepath.Join(dataDir, RecoveredCrewsDir)))
	}
	if len(m.Contents.Environments) > 0 {
		stageRecoveredEnvironmentLayers(rep, m, opts, dataDir, ex.envArchives)
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
	if err := recordRecoverReport(ctx, rep, opts); err != nil {
		rep.Warnings = append(rep.Warnings, "the restore report could not be stored: "+err.Error())
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
func openInstancePayload(path string, identities []age.Identity, passphrase string) (*Manifest, *TarZstReader, func(), error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, err
	}
	m, sealed, closeBundle, err := ReadBundleStream(f)
	if err != nil {
		_ = f.Close()
		return m, nil, nil, err
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
		return m, nil, nil, err
	}
	tr, err := NewTarZstReader(plain)
	if err != nil {
		_ = closeBundle()
		_ = f.Close()
		return m, nil, nil, err
	}
	return m, tr, func() { _ = tr.Close(); _ = closeBundle(); _ = f.Close() }, nil
}

func extractInstancePayload(ctx context.Context, opts RecoverOptions, m *Manifest, dataDir string) (*extractedInstance, error) {
	_, tr, closeAll, err := openInstancePayload(opts.BundlePath, opts.Identities, opts.Passphrase)
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
			sha, _, err := writeExtracted(dst, tr)
			if err != nil {
				return nil, fmt.Errorf("backup: write database: %w", err)
			}
			ex.dbSHA = sha
		case name == instanceIndexEntry:
			var idx InstanceIndex
			if err := json.NewDecoder(io.LimitReader(tr, maxBackupDBDumpBytes)).Decode(&idx); err != nil {
				return nil, fmt.Errorf("backup: read file index: %w", err)
			}
			ex.index = &idx
		case name == RecoveryKitPath:
			data, err := io.ReadAll(io.LimitReader(tr, 1<<20))
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
				continue
			}
			dst, err := safepath.JoinRel(RecoveredStoreDir(dataDir, store), filepath.FromSlash(rel))
			if err != nil || dst == RecoveredStoreDir(dataDir, store) {
				return nil, fmt.Errorf("backup: refuse unsafe payload path %q", name)
			}
			sha, n, err := writeExtracted(dst, tr)
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
			if _, _, err := writeExtracted(dst, tr); err != nil {
				return nil, err
			}
			ex.crewArchives = append(ex.crewArchives, dst)
		case strings.HasPrefix(name, instanceEnvPrefix) && strings.HasSuffix(name, instanceCrewsSuffix):
			id := strings.TrimSuffix(strings.TrimPrefix(name, instanceEnvPrefix), instanceCrewsSuffix)
			dst, err := safepath.JoinUnder(filepath.Join(dataDir, RecoveredEnvironmentsDir), id+instanceCrewsSuffix)
			if err != nil {
				return nil, fmt.Errorf("backup: refuse unsafe payload path %q", name)
			}
			if _, _, err := writeExtracted(dst, tr); err != nil {
				return nil, err
			}
			ex.envArchives = append(ex.envArchives, dst)
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

func writeExtracted(dst string, r io.Reader) (string, int64, error) {
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
	n, err := io.Copy(io.MultiWriter(f, h), r)
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

// withEnv sets kvs for the duration of fn and restores the previous values.
// Recover applies migrations with the source's keys in place: a migration
// keyed off ENCRYPTION_KEY (the journal chain, webhook secret enveloping)
// run under a different key writes permanently wrong data.
func withEnv(kvs map[string]string, fn func() error) error {
	type prev struct {
		v  string
		ok bool
	}
	saved := map[string]prev{}
	for k, v := range kvs {
		old, ok := os.LookupEnv(k)
		saved[k] = prev{old, ok}
		_ = os.Setenv(k, v)
	}
	defer func() {
		for k, p := range saved {
			if p.ok {
				_ = os.Setenv(k, p.v)
			} else {
				_ = os.Unsetenv(k)
			}
		}
	}()
	return fn()
}

func kitEnv(kit *RecoveryKit) (map[string]string, error) {
	out := map[string]string{}
	if kit == nil {
		return out, nil
	}
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
	{"sessions", "sign-in sessions"},
	{"user_sessions", "sign-in sessions"},
	{"cli_pairings", "CLI pairings"},
	{"backup_locks", "backup locks"},
	{"scheduler_leader", "scheduler lease"},
	{"provider_login_refresh", "provider login refresh leases"},
	{"provider_device_logins", "provider device logins in progress"},
	{"instance_holds", "old holds"},
}

func finishRecoveredDatabase(ctx context.Context, rep *RecoverReport, kit *RecoveryKit, dataDir string, logger *slog.Logger, serviceArgs ...instanceServiceRecovery) error {
	env, err := kitEnv(kit)
	if err != nil {
		return err
	}
	return withEnv(env, func() error {
		db, err := database.Open("file:" + filepath.Join(dataDir, "crewship.db"))
		if err != nil {
			return fmt.Errorf("backup: open restored database: %w", err)
		}
		defer func() { _ = db.Close() }()
		before := len(AppliedMigrationVersions(ctx, db.DB))
		_, _, pending, err := database.PendingMigrations(ctx, db.DB)
		if err != nil {
			return err
		}
		if pending > 0 && kit == nil && os.Getenv("ENCRYPTION_KEY") == "" {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d migration(s) are pending and no vault key is available; they run when the server first starts, with the ENCRYPTION_KEY it is given — give it the original one", pending))
		} else if pending > 0 {
			if err := database.Migrate(ctx, db.DB, logger); err != nil {
				return fmt.Errorf("backup: migrate restored database: %w", err)
			}
		}
		rep.MigrationsApplied = len(AppliedMigrationVersions(ctx, db.DB)) - before

		if len(serviceArgs) != 0 {
			services := serviceArgs[0].payload
			count := serviceArgs[0].count
			if err := stageInstanceServiceRecovery(ctx, db.DB, services, count, dataDir); err != nil {
				return err
			}
			if count > 0 {
				rep.Notes = append(rep.Notes, fmt.Sprintf("%d quota-service image(s) verified and staged; services remain in maintenance until their data are landed on the new host", count))
			}
		}
		for _, r := range runtimeResets {
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
		memRoot := RecoveredStoreDir(dataDir, StoreMemory)
		if _, err := db.ExecContext(ctx, `UPDATE memory_versions SET payload_ref = ? || '/versions/' || substr(sha256, 1, 2) || '/' || sha256 WHERE length(sha256) = 64`, memRoot); err != nil && !strings.Contains(err.Error(), "no such table") {
			return fmt.Errorf("backup: rewrite memory blob paths: %w", err)
		}

		holds := recoveredHolds(ctx, db.DB)
		if err := quiesce.SetHolds(ctx, db, holds); err != nil {
			return err
		}
		rep.Holds = holds
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
	}
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
	delete(values, "ENCRYPTION_KEY")
	var kitLines []string
	if kit != nil {
		kitLines, err = kit.EnvLines()
		if err != nil {
			return err
		}
		for _, l := range kitLines {
			if k, v, _ := strings.Cut(l, "="); k == "ENCRYPTION_KEY" {
				values[k] = v
			}
		}
	}
	if err := writeEnvFile(secretsPath, []string{
		"# crewship: secrets for a recovered instance, written by `crewship recover`.",
		"# NEXTAUTH_SECRET and CREWSHIP_ADMIN_TOKEN_HMAC_KEY are new: every session from the source is gone.",
	}, values); err != nil {
		return err
	}
	rep.SecretsFile = secretsPath
	if kit == nil {
		rep.Warnings = append(rep.Warnings, "the bundle carries no recovery kit: set the source's ENCRYPTION_KEY (and ENCRYPTION_KEY_V2… for later key versions) before starting the server, or credentials stay unreadable")
		return nil
	}
	keysPath := filepath.Join(dataDir, RecoveredKeysFile)
	kv := map[string]string{NextAuthSecretEnv: newSecret}
	for _, l := range kitLines {
		k, v, _ := strings.Cut(l, "=")
		kv[k] = v
	}
	if err := writeEnvFile(keysPath, []string{
		"# crewship: vault keys from the backup's recovery kit, written by `crewship recover`.",
		"# Give these to the server before it starts — e.g. systemd EnvironmentFile=" + keysPath + ",",
		"# or export them. ENCRYPTION_KEY is also in secrets.env; ENCRYPTION_KEY_V2 and later are",
		"# read from the environment only. Whoever reads this file can read every secret in the",
		"# instance: move the keys into your secret store and delete it.",
	}, kv); err != nil {
		return err
	}
	rep.KeysFile = keysPath
	return nil
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
	tmp := p + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(b.String())
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

func recordRecoverReport(ctx context.Context, rep *RecoverReport, opts RecoverOptions) error {
	db, err := database.Open("file:" + rep.DatabasePath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
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
