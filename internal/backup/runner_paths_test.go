package backup_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
)

// Drive the CreateOptions.Validate / RBAC error paths so the
// runner's defensive checks stay locked in.

func TestCreateBackup_RejectsUnknownScope(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	_, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:     "bogus",
		Actor:     backup.Actor{UserID: "u_admin", Role: "ADMIN"},
		NoEncrypt: true,
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported scope") {
		t.Errorf("expected unsupported-scope error, got %v", err)
	}
}

func TestCreateBackup_RejectsMissingActor(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	_, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: "ws_x",
		NoEncrypt:   true,
	})
	if err == nil || !strings.Contains(err.Error(), "Actor") {
		t.Errorf("expected Actor required error, got %v", err)
	}
}

func TestCreateBackup_RejectsWorkspaceScopeMissingID(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	_, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:     backup.ScopeWorkspace,
		Actor:     backup.Actor{UserID: "u_admin", Role: "ADMIN"},
		NoEncrypt: true,
	})
	if err == nil || !strings.Contains(err.Error(), "WorkspaceID") {
		t.Errorf("expected WorkspaceID required, got %v", err)
	}
}

func TestCreateBackup_RejectsCrewScopeMissingCrewID(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	_, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:     backup.ScopeCrew,
		Actor:     backup.Actor{UserID: "u_admin", Role: "ADMIN"},
		NoEncrypt: true,
	})
	if err == nil || !strings.Contains(err.Error(), "CrewID") {
		t.Errorf("expected CrewID required, got %v", err)
	}
}

func TestCreateBackup_RejectsConflictingEncryption(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	id, _ := age.GenerateX25519Identity()
	_, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: "ws_x",
		Actor:       backup.Actor{UserID: "u_admin", Role: "ADMIN"},
		Passphrase:  "pwd",
		Recipients:  []age.Recipient{id.Recipient()},
	})
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("expected 'exactly one' encryption-mode error, got %v", err)
	}
}

func TestCreateBackup_RejectsNoEncryptionMode(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	_, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: "ws_x",
		Actor:       backup.Actor{UserID: "u_admin", Role: "ADMIN"},
	})
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("expected encryption-mode required, got %v", err)
	}
}

func TestCreateBackup_RejectsNonAdminRole(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	_, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: "ws_x",
		Actor:       backup.Actor{UserID: "u_member", Role: "MEMBER"},
		NoEncrypt:   true,
	})
	if !errors.Is(err, backup.ErrAdminRequired) {
		t.Errorf("non-admin role should produce ErrAdminRequired, got %v", err)
	}
}

// === RestoreBackup early error paths ===

func TestRestoreBackup_RejectsMissingPath(t *testing.T) {
	ctx := context.Background()
	target := openMigratedDB(t)
	_, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Actor: backup.Actor{UserID: "u_admin", Role: "ADMIN"},
	})
	if err == nil || !strings.Contains(err.Error(), "Path") {
		t.Errorf("missing path should error, got %v", err)
	}
}

func TestRestoreBackup_RejectsMissingActor(t *testing.T) {
	ctx := context.Background()
	target := openMigratedDB(t)
	_, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path: "/tmp/anything.tar.zst",
	})
	if err == nil || !strings.Contains(err.Error(), "Actor") {
		t.Errorf("missing actor should error, got %v", err)
	}
}

func TestRestoreBackup_RejectsNonAdmin(t *testing.T) {
	ctx := context.Background()
	target := openMigratedDB(t)
	_, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:  "/tmp/anything.tar.zst",
		Actor: backup.Actor{UserID: "u_viewer", Role: "VIEWER"},
	})
	if !errors.Is(err, backup.ErrAdminRequired) {
		t.Errorf("non-admin restore should produce ErrAdminRequired, got %v", err)
	}
}

func TestRestoreBackup_RejectsMissingBundleFile(t *testing.T) {
	ctx := context.Background()
	target := openMigratedDB(t)
	_, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:  t.TempDir() + "/nope.tar.zst",
		Actor: backup.Actor{UserID: "u_admin", Role: "ADMIN"},
	})
	if err == nil {
		t.Error("missing bundle file should error")
	}
}

// === Roundtrip with all three scope levels ===

func TestCreateRestore_RoundTripScopeLevels(t *testing.T) {
	for _, level := range []backup.ScopeLevel{
		backup.ScopeLevelQuick,
		backup.ScopeLevelStandard,
		backup.ScopeLevelFull,
	} {
		t.Run(string(level), func(t *testing.T) {
			ctx := context.Background()
			source := openMigratedDB(t)
			workspaceID := seedWorkspace(t, source)

			res, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
				Scope:       backup.ScopeWorkspace,
				WorkspaceID: workspaceID,
				Level:       level,
				OutputDir:   t.TempDir(),
				Actor:       backup.Actor{UserID: "u_admin", Email: "a@b", Role: "ADMIN"},
				NoEncrypt:   true,
			})
			if err != nil {
				t.Fatalf("create level=%s: %v", level, err)
			}
			if res.Manifest.ScopeLevel != level {
				t.Errorf("manifest level drift: want %s got %s", level, res.Manifest.ScopeLevel)
			}

			target := openMigratedDB(t)
			restoreRes, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
				Path:    res.Path,
				Replace: true,
				Actor:   backup.Actor{UserID: "u_admin", Email: "a@b", Role: "ADMIN"},
			})
			if err != nil {
				t.Fatalf("restore level=%s: %v", level, err)
			}
			if restoreRes.RowsInserted <= 0 {
				t.Errorf("level=%s restored 0 rows; expected >0", level)
			}
		})
	}
}

// === Inspect + Verify against real bundle ===

func TestInspectAndVerify_AgainstNoEncryptBundle(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	res, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   t.TempDir(),
		Actor:       backup.Actor{UserID: "u_admin", Email: "a@b", Role: "ADMIN"},
		NoEncrypt:   true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	m, err := backup.Inspect(ctx, res.Path)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if m.Encryption.Enabled {
		t.Error("NoEncrypt bundle should report Encryption.Enabled=false")
	}
	if m.Checksums.PayloadSHA256 != res.SHA256 {
		t.Errorf("inspect checksum drift: %q vs %q", m.Checksums.PayloadSHA256, res.SHA256)
	}

	vr, err := backup.Verify(ctx, res.Path)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !vr.Valid {
		t.Errorf("Verify reported invalid bundle: %v", vr.Err)
	}
}
