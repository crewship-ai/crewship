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

func TestDefaultBackupsDirFor_JoinsHomeWithDotCrewshipBackups(t *testing.T) {
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

// ---- DefaultBackupsDir ----

func TestDefaultBackupsDir_UsesPackageDefaultStorage(t *testing.T) {
	// DefaultBackupsDir wraps defaultBackupsDirFor(getDefaultStorage()).
	// In the default install the storage is LocalStorageOps, whose Home
	// reads $HOME. We use t.Setenv to point it at a temp dir so the
	// test doesn't leak the developer's real path AND isolates the
	// assertion to a known prefix.
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
