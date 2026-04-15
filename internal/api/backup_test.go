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
	cases := map[error]int{
		nil: http.StatusOK,
		fmt.Errorf("admin role required (have MEMBER)"):                                http.StatusForbidden,
		fmt.Errorf("backup refused: agent \"x\" is running"):                           http.StatusConflict,
		fmt.Errorf("backup: another backup is already in progress for this workspace"): http.StatusConflict,
		errors.New("backup: bundle format version too old"):                            http.StatusBadRequest,
		errors.New("database unavailable"):                                             http.StatusInternalServerError,
	}
	for err, wantStatus := range cases {
		if got := statusForBackupError(err); got != wantStatus {
			t.Errorf("statusForBackupError(%v) = %d, want %d", err, got, wantStatus)
		}
	}
}

func TestCrewContainerNameFunc(t *testing.T) {
	fn := crewContainerNameFunc()
	if got := fn("my-crew"); got != "crewship-team-my-crew" {
		t.Errorf("unexpected container name: %q", got)
	}
}
