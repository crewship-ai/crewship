package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/database"
)

// newAdminResetCmdForTest builds a fresh cobra.Command instance that
// reuses the production RunE so each test exercises the real logic
// without inheriting flag state from a prior test (cobra's globals
// are sticky across reuses of the same *Command pointer).
func newAdminResetCmdForTest() *cobra.Command {
	c := &cobra.Command{Use: "reset-password", RunE: runAdminResetPassword}
	c.Flags().String("email", "", "")
	c.Flags().String("password", "", "")
	c.SetOut(new(bytes.Buffer))
	c.SetErr(new(bytes.Buffer))
	return c
}

func initTestDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dbURL := "file:" + filepath.Join(dir, "test.db")
	db, err := database.Open(dbURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := database.Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Close()
	return dbURL
}

func TestAdminResetPassword_UpdatesHashAndClearsLockout(t *testing.T) {
	dbURL := initTestDB(t)

	db, err := database.Open(dbURL)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	originalHash, err := bcrypt.GenerateFromPassword([]byte("oldpassword"), 4)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name, hashed_password, failed_login_count, locked_until)
		VALUES ('u1', 'admin@example.com', 'Admin', ?, 5, datetime('now', '+1 hour'))`, string(originalHash)); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO user_sessions (id, user_id, expires_at, created_at, last_used_at)
		VALUES ('s1', 'u1', datetime('now', '+1 day'), datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	db.Close()

	t.Setenv("DATABASE_URL", dbURL)

	cmd := newAdminResetCmdForTest()
	cmd.SetArgs([]string{"--email=admin@example.com", "--password=brand-new-pw"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	db2, err := database.Open(dbURL)
	if err != nil {
		t.Fatalf("reopen2: %v", err)
	}
	defer db2.Close()

	var hashed string
	var failedCount int
	var lockedUntil *string
	if err := db2.QueryRow(`SELECT hashed_password, failed_login_count, locked_until FROM users WHERE id='u1'`).
		Scan(&hashed, &failedCount, &lockedUntil); err != nil {
		t.Fatalf("query: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte("brand-new-pw")); err != nil {
		t.Errorf("new password not set: %v", err)
	}
	if failedCount != 0 {
		t.Errorf("failed_login_count = %d, want 0", failedCount)
	}
	if lockedUntil != nil && *lockedUntil != "" {
		t.Errorf("locked_until = %q, want cleared", *lockedUntil)
	}

	var revokedAt *string
	if err := db2.QueryRow(`SELECT revoked_at FROM user_sessions WHERE id='s1'`).Scan(&revokedAt); err != nil {
		t.Fatalf("query session: %v", err)
	}
	if revokedAt == nil || *revokedAt == "" {
		t.Errorf("session not revoked")
	}
}

func TestAdminResetPassword_FailsOnUnknownEmail(t *testing.T) {
	dbURL := initTestDB(t)
	t.Setenv("DATABASE_URL", dbURL)

	cmd := newAdminResetCmdForTest()
	cmd.SetArgs([]string{"--email=nobody@example.com", "--password=whatever12"})
	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected error for unknown email, got nil")
	}
}

func TestAdminResetPassword_RejectsShortPassword(t *testing.T) {
	dbURL := initTestDB(t)
	db, err := database.Open(dbURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	originalHash, _ := bcrypt.GenerateFromPassword([]byte("oldpassword"), 4)
	if _, err := db.Exec(`INSERT INTO users (id, email, hashed_password) VALUES ('u1', 'admin@example.com', ?)`, string(originalHash)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	db.Close()

	t.Setenv("DATABASE_URL", dbURL)

	cmd := newAdminResetCmdForTest()
	cmd.SetArgs([]string{"--email=admin@example.com", "--password=short"})
	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected error for short password, got nil")
	}
}
