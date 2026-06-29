package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestValidateBackupPath_AllowsDefaultDir(t *testing.T) {
	// Sandbox HOME so DefaultBackupsDir resolves inside t.TempDir()
	// and MkdirAll never touches the real developer's machine.
	t.Setenv("HOME", t.TempDir())
	defaultDir, err := backup.DefaultBackupsDir()
	if err != nil {
		t.Fatalf("default dir: %v", err)
	}
	if err := os.MkdirAll(defaultDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ok := filepath.Join(defaultDir, "crewship-workspace-example-20260101T000000Z.tar.zst")
	if err := validateBackupPath(ok); err != nil {
		t.Errorf("valid path should pass, got %v", err)
	}
}

func TestValidateBackupPath_RejectsParentEscape(t *testing.T) {
	cases := []string{
		"../../etc/passwd",
		"/tmp/something",
		"",
	}
	for _, p := range cases {
		if err := validateBackupPath(p); err == nil {
			t.Errorf("path %q should be rejected", p)
		}
	}
}

func TestStatusForBackupError(t *testing.T) {
	// Cases drive off the backup package sentinel errors rather than
	// substring-matching fmt.Errorf strings — if the backup package
	// reworks its wording, status mapping stays stable.
	cases := map[error]int{
		nil: http.StatusOK,
		fmt.Errorf("wrapped: %w", backup.ErrAdminRequired):   http.StatusForbidden,
		fmt.Errorf("wrapped: %w", backup.ErrAgentRunning):    http.StatusConflict,
		fmt.Errorf("wrapped: %w", backup.ErrLockHeld):        http.StatusConflict,
		fmt.Errorf("wrapped: %w", backup.ErrFormatTooNew):    http.StatusBadRequest,
		fmt.Errorf("wrapped: %w", backup.ErrFormatTooOld):    http.StatusBadRequest,
		fmt.Errorf("wrapped: %w", backup.ErrSchemaTooOld):    http.StatusBadRequest,
		fmt.Errorf("wrapped: %w", backup.ErrInvalidManifest): http.StatusBadRequest,
		fmt.Errorf("wrapped: %w", backup.ErrInvalidScope):    http.StatusBadRequest,
		fmt.Errorf("wrapped: %w", backup.ErrDecryption):      http.StatusBadRequest,
		fmt.Errorf("wrapped: %w", backup.ErrInvalidChecksum): http.StatusBadRequest,
		fmt.Errorf("wrapped: %w", backup.ErrNoOpRestore):     http.StatusConflict,
		errors.New("database unavailable"):                   http.StatusInternalServerError,
	}
	for err, wantStatus := range cases {
		if got := statusForBackupError(err); got != wantStatus {
			t.Errorf("statusForBackupError(%v) = %d, want %d", err, got, wantStatus)
		}
	}
}

func TestResolveCrewContainerName_Default(t *testing.T) {
	// No SetCrewContainerName called → falls back to the legacy default
	// prefix so old setups + tests without a provider still work.
	h := &BackupHandler{}
	fn := h.resolveCrewContainerName()
	if got := fn("ckcrew1", "my-crew"); got != "crewship-team-my-crew-ckcrew1" {
		t.Errorf("default prefix: got %q, want crewship-team-my-crew-ckcrew1", got)
	}
}

func TestResolveCrewContainerName_Injected(t *testing.T) {
	// SetCrewContainerName called → uses the injected mapping (this is
	// what fixes multi-instance setups; the router wires the active
	// ContainerProvider's CrewContainerName method).
	h := &BackupHandler{}
	h.SetCrewContainerName(func(_, slug string) string { return "crewship-3-team-" + slug })
	fn := h.resolveCrewContainerName()
	if got := fn("ckcrew1", "my-crew"); got != "crewship-3-team-my-crew" {
		t.Errorf("injected prefix: got %q, want crewship-3-team-my-crew", got)
	}
}
