package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// runner.go — DefaultBackupsDir + defaultBackupsDirFor.
//
// These resolve "where do bundles live by default?" via a StorageOps
// abstraction so a cloud-backend variant can swap the home-resolution
// rule without touching call sites. Tests exercise both the success
// path (joined "<home>/.crewship/backups") and the error wrapping.
// ---------------------------------------------------------------------------

// stubStorageOpsForDefaultDir overrides only Home; the other methods
// return errors because they should never be called by the resolver.
type stubStorageOpsForDefaultDir struct {
	homePath string
	homeErr  error
}

func (s stubStorageOpsForDefaultDir) Home() (string, error) {
	return s.homePath, s.homeErr
}

func (stubStorageOpsForDefaultDir) MkdirAll(context.Context, string, os.FileMode) error {
	panic("MkdirAll should not be called from defaultBackupsDirFor")
}
func (stubStorageOpsForDefaultDir) ReadDir(context.Context, string) ([]os.DirEntry, error) {
	panic("ReadDir should not be called from defaultBackupsDirFor")
}
func (stubStorageOpsForDefaultDir) Open(context.Context, string) (io.ReadCloser, error) {
	panic("Open should not be called from defaultBackupsDirFor")
}
func (stubStorageOpsForDefaultDir) Create(context.Context, string, os.FileMode) (io.WriteCloser, error) {
	panic("Create should not be called from defaultBackupsDirFor")
}
func (stubStorageOpsForDefaultDir) CreateTemp(context.Context, string, string) (TempFile, error) {
	panic("CreateTemp should not be called from defaultBackupsDirFor")
}
func (stubStorageOpsForDefaultDir) MkdirTemp(context.Context, string, string) (string, error) {
	panic("MkdirTemp should not be called from defaultBackupsDirFor")
}
func (stubStorageOpsForDefaultDir) Remove(context.Context, string) error {
	panic("Remove should not be called from defaultBackupsDirFor")
}
func (stubStorageOpsForDefaultDir) RemoveAll(context.Context, string) error {
	panic("RemoveAll should not be called from defaultBackupsDirFor")
}
func (stubStorageOpsForDefaultDir) Rename(context.Context, string, string) error {
	panic("Rename should not be called from defaultBackupsDirFor")
}
func (stubStorageOpsForDefaultDir) Stat(context.Context, string) (os.FileInfo, error) {
	panic("Stat should not be called from defaultBackupsDirFor")
}

var _ StorageOps = stubStorageOpsForDefaultDir{}

// ---- defaultBackupsDirFor ----
//
// #2262: an instance started with its own CREWSHIP_DATA_DIR must default
// its bundle directory under that root, not under the operating user's
// shared ~/.crewship — otherwise every isolated instance on one host
// (three dev clones plus any number of ephemeral test instances, all
// running as the same unix user) shares one bundle directory, and a
// restore that picks a bundle by name can land foreign data into a live
// database. CREWSHIP_DATA_DIR wins when set (mirroring
// database.defaultDataDirRoot's own env resolution and its
// filepath.Abs + fmt.Errorf("resolve CREWSHIP_DATA_DIR: %w") wrapping);
// the Home()-based ~/.crewship/backups default is the fallback for when
// neither is set, kept for the un-isolated single-instance case.

func TestDefaultBackupsDirFor_JoinsHomeWithDotCrewshipBackups(t *testing.T) {
	t.Setenv("CREWSHIP_DATA_DIR", "") // legacy path only applies when unset
	st := stubStorageOpsForDefaultDir{homePath: "/users/alice"}
	got, err := defaultBackupsDirFor(st)
	if err != nil {
		t.Fatalf("defaultBackupsDirFor: %v", err)
	}
	want := filepath.Join("/users/alice", ".crewship", "backups")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDefaultBackupsDirFor_PropagatesHomeError_Wrapped(t *testing.T) {
	// Home error must surface as "backup: resolve home dir: %w" so an
	// operator can find both the layer (backup) AND the underlying
	// failure (e.g. "permission denied", "no such user").
	t.Setenv("CREWSHIP_DATA_DIR", "")
	homeErr := errors.New("getpwuid_r failed: no entry")
	st := stubStorageOpsForDefaultDir{homeErr: homeErr}
	_, err := defaultBackupsDirFor(st)
	if err == nil {
		t.Fatal("expected error when Home fails")
	}
	if !errors.Is(err, homeErr) {
		t.Errorf("err = %v, want errors.Is(err, %v) for unwrap chain", err, homeErr)
	}
	if !strings.Contains(err.Error(), "backup: resolve home dir") {
		t.Errorf("err = %v, want \"backup: resolve home dir\" prefix", err)
	}
}

func TestDefaultBackupsDirFor_EmptyHomeStillJoins(t *testing.T) {
	// Source doesn't validate the home string — if Home returns ("",
	// nil) the join still produces ".crewship/backups" relative to
	// cwd. Pin the current behavior so a future "reject empty home"
	// refactor surfaces explicitly.
	t.Setenv("CREWSHIP_DATA_DIR", "")
	st := stubStorageOpsForDefaultDir{homePath: ""}
	got, err := defaultBackupsDirFor(st)
	if err != nil {
		t.Fatalf("defaultBackupsDirFor: %v", err)
	}
	want := filepath.Join("", ".crewship", "backups")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDefaultBackupsDirFor_DataDirEnvSet_WinsOverHome(t *testing.T) {
	// The bug: CREWSHIP_DATA_DIR was ignored entirely, so this used to
	// resolve to Home()+".crewship/backups" no matter what. It must
	// resolve under the data dir instead, and never call Home() (Home
	// on the stub panics — if this test doesn't panic, Home was never
	// reached, proving the env short-circuits it).
	dataDir := "/srv/crewship/instance-a/data"
	t.Setenv("CREWSHIP_DATA_DIR", dataDir)
	st := stubStorageOpsForDefaultDir{homeErr: errors.New("Home must not be called when CREWSHIP_DATA_DIR is set")}

	got, err := defaultBackupsDirFor(st)
	if err != nil {
		t.Fatalf("defaultBackupsDirFor: %v", err)
	}
	want := filepath.Join(dataDir, "backups")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDefaultBackupsDirFor_DataDirEnvRelative_MadeAbsolute(t *testing.T) {
	// database.defaultDataDirRoot makes a relative CREWSHIP_DATA_DIR
	// absolute (relative to cwd) before using it; mirror that so the
	// two readers of the env var can never disagree about where an
	// instance's root is.
	tmp := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	t.Setenv("CREWSHIP_DATA_DIR", "relative-data-dir")
	st := stubStorageOpsForDefaultDir{homeErr: errors.New("Home must not be called when CREWSHIP_DATA_DIR is set")}

	got, err := defaultBackupsDirFor(st)
	if err != nil {
		t.Fatalf("defaultBackupsDirFor: %v", err)
	}
	want := filepath.Join(tmp, "relative-data-dir", "backups")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("got %q, want an absolute path", got)
	}
}

// ---- DefaultBackupsDir ----

func TestDefaultBackupsDir_UsesPackageDefaultStorage(t *testing.T) {
	// DefaultBackupsDir wraps defaultBackupsDirFor(getDefaultStorage()).
	// In the default install the storage is LocalStorageOps, whose Home
	// reads $HOME. We use t.Setenv to point it at a temp dir so the
	// test doesn't leak the developer's real path AND isolates the
	// assertion to a known prefix.
	t.Setenv("CREWSHIP_DATA_DIR", "") // exercise the legacy Home() fallback
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	got, err := DefaultBackupsDir()
	if err != nil {
		t.Fatalf("DefaultBackupsDir: %v", err)
	}
	want := filepath.Join(tmp, ".crewship", "backups")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDefaultBackupsDir_DataDirEnvSet_WinsOverHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp) // present but must lose to CREWSHIP_DATA_DIR
	dataDir := filepath.Join(tmp, "isolated-instance")
	t.Setenv("CREWSHIP_DATA_DIR", dataDir)

	got, err := DefaultBackupsDir()
	if err != nil {
		t.Fatalf("DefaultBackupsDir: %v", err)
	}
	want := filepath.Join(dataDir, "backups")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDefaultBackupsDir_TwoDataDirs_NeitherSeesTheOthersBundleByName(t *testing.T) {
	// The scenario from the issue: two instances on one host, each with
	// its own CREWSHIP_DATA_DIR. A bundle created under instance A's
	// resolved dir must not be visible when B resolves its own default
	// and looks for a file of the same name.
	root := t.TempDir()
	dataDirA := filepath.Join(root, "instance-a")
	dataDirB := filepath.Join(root, "instance-b")

	t.Setenv("CREWSHIP_DATA_DIR", dataDirA)
	dirA, err := DefaultBackupsDir()
	if err != nil {
		t.Fatalf("DefaultBackupsDir (A): %v", err)
	}

	t.Setenv("CREWSHIP_DATA_DIR", dataDirB)
	dirB, err := DefaultBackupsDir()
	if err != nil {
		t.Fatalf("DefaultBackupsDir (B): %v", err)
	}

	if dirA == dirB {
		t.Fatalf("instance A and B resolved to the same backups dir %q; env-based isolation did not work", dirA)
	}

	const bundleName = "crewship-workspace-acme-20240101T000000Z.tar.zst"
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatalf("MkdirAll dirA: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirA, bundleName), []byte("bundle contents"), 0o644); err != nil {
		t.Fatalf("write bundle in dirA: %v", err)
	}

	// B's resolved default dir must not contain A's bundle by name.
	if _, err := os.Stat(filepath.Join(dirB, bundleName)); err == nil {
		t.Fatalf("instance B can see instance A's bundle %q at %q", bundleName, dirB)
	} else if !os.IsNotExist(err) {
		t.Fatalf("Stat dirB/%s: %v", bundleName, err)
	}

	ctx := context.Background()
	if _, err := Exists(ctx, filepath.Join(dirB, bundleName)); !errors.Is(err, ErrBundleNotFound) {
		t.Errorf("Exists(dirB/%s) = %v, want %v", bundleName, err, ErrBundleNotFound)
	}
	if ok, err := Exists(ctx, filepath.Join(dirA, bundleName)); err != nil || !ok {
		t.Errorf("Exists(dirA/%s) = (%v, %v), want (true, nil)", bundleName, ok, err)
	}
}

// ---- BundleFileName ----

func TestBundleFileName_IncludesScopeSlugAndUTCTimestamp(t *testing.T) {
	// Format pin: crewship-<scope>-<slug>-<ISO-timestamp>.tar.zst
	// UTC formatting (the "20060102T150405Z" layout) is the contract —
	// a regression to local time would make bundle filenames non-
	// monotonic across timezones and break the chronological sort
	// ListBackups relies on as a fallback when manifests are missing.
	ts := mustParseTime(t, "2024-03-15T10:30:45Z")
	got := BundleFileName(Scope("workspace"), "ws-prod", ts)
	want := "crewship-workspace-ws-prod-20240315T103045Z.tar.zst"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBundleFileName_NormalisesNonUTCInputToUTC(t *testing.T) {
	// Caller may pass a time.Time in any timezone. The format must
	// normalise to UTC so the filename is deterministic.
	loc, err := loadLocationOrSkip(t)
	if err != nil {
		t.Skip(err.Error())
	}
	// 11:30 in a +01:00 zone = 10:30 UTC → expected filename should
	// carry the UTC stamp regardless of input timezone.
	ts := mustParseTimeInLoc(t, "2024-03-15T11:30:45", loc)
	got := BundleFileName(Scope("crew"), "alpha", ts)
	want := "crewship-crew-alpha-20240315T103045Z.tar.zst"
	if got != want {
		t.Errorf("got %q, want %q (must normalise to UTC)", got, want)
	}
}

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return parsed
}

func mustParseTimeInLoc(t *testing.T, s string, loc *time.Location) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02T15:04:05", s, loc)
	if err != nil {
		t.Fatalf("parse time %q in loc: %v", s, err)
	}
	return parsed
}

func loadLocationOrSkip(t *testing.T) (*time.Location, error) {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		return nil, err
	}
	return loc, nil
}
