package backup_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
)

// The first recovery prepares a real installation from the earlier encrypted
// snapshot. Only then do we add newer output. The second, forced recovery must
// refuse before replacing that installation's database or retained log files.
func TestPrivateArtifactTargetForceRecover(t *testing.T) {
	for _, kind := range []string{"consent", "created", "ordinary"} {
		t.Run(kind, func(t *testing.T) {
			f := newInstanceFixture(t)
			seedReplacementChat(t, f.db, f.paths.Output)
			bundle := f.create(t, true)
			if !bundle.Manifest.Encryption.Enabled {
				t.Fatal("snapshot must be encrypted")
			}
			dir := filepath.Join(t.TempDir(), "installation")
			opts := backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: dir, Actor: "u_admin"}
			report, err := backup.RecoverInstance(t.Context(), opts)
			if err != nil {
				t.Fatalf("prepare earlier installation: %v", err)
			}
			target, err := database.Open("file:" + report.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			logPath, before := advanceReplacementTarget(t, target.DB, backup.RecoveredStoreDir(dir, backup.StoreOutput), filepath.Join(dir, "logs"), kind)
			// Offline recovery owns its database: close all target connections
			// before invoking the supported force operation.
			if err := target.Close(); err != nil {
				t.Fatal(err)
			}
			opts.Force = true
			_, recoverErr := backup.RecoverInstance(t.Context(), opts)
			target, err = database.Open("file:" + report.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			// Physical recovery replaces SQL with the archived one-message snapshot.
			assertReplacementOutcome(t, target.DB, logPath, before, kind, 1, recoverErr)
		})
	}
}

// Exercise Cobra flag parsing and the actual offline binary, not rootCmd or a
// substitute executable. The package has no reusable binary builder; the
// cmd/crewship builders are private to package main. Build once for both cases.
func TestPrivateArtifactTargetForceRecoverThroughBinary(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "crewship")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/crewship")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual binary: %v %s", err, output)
	}
	for _, kind := range []string{"consent", "ordinary"} {
		t.Run(kind, func(t *testing.T) {
			f := newInstanceFixture(t)
			seedReplacementChat(t, f.db, f.paths.Output)
			bundle := f.create(t, true)
			if !bundle.Manifest.Encryption.Enabled {
				t.Fatal("snapshot must be encrypted")
			}
			dir := filepath.Join(t.TempDir(), "installation")
			report, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{
				BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: dir, Actor: "u_admin",
			})
			if err != nil {
				t.Fatalf("prepare earlier installation: %v", err)
			}
			target, err := database.Open("file:" + report.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			logPath, before := advanceReplacementTarget(t, target.DB, backup.RecoveredStoreDir(dir, backup.StoreOutput), filepath.Join(dir, "logs"), kind)
			if err := target.Close(); err != nil {
				t.Fatal(err)
			}
			cliHome := t.TempDir()
			identityFile := filepath.Join(cliHome, "recovery.agekey")
			if err := os.WriteFile(identityFile, []byte(f.identity.String()+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "recover", "--bundle", bundle.Path, "--identity", identityFile, "--data-dir", dir, "--force", "--format", "json")
			command.Dir = cliHome
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "CREWSHIP_") && !strings.HasPrefix(entry, "HOME=") && !strings.HasPrefix(entry, "XDG_CONFIG_HOME=") {
					command.Env = append(command.Env, entry)
				}
			}
			command.Env = append(command.Env, "HOME="+cliHome, "XDG_CONFIG_HOME="+cliHome, "CREWSHIP_CONFIG="+filepath.Join(cliHome, "cli.yaml"), "NO_COLOR=1")
			output, commandErr := command.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("offline binary exceeded bounded invocation: %v", ctx.Err())
			}
			if kind == "consent" && (commandErr == nil || !bytes.Contains(bytes.ToLower(output), []byte("private artifact"))) {
				t.Errorf("actual CLI must refuse protected target: exit_error=%v privacy_refusal=%v", commandErr != nil, bytes.Contains(bytes.ToLower(output), []byte("private artifact")))
			}
			target, err = database.Open("file:" + report.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			// Full CLI reports and generated identity material are never printed.
			// The preceding binary assertion checks the refusal text separately.
			var outcomeErr error
			if commandErr != nil {
				outcomeErr = errors.New("offline CLI failed")
				if bytes.Contains(bytes.ToLower(output), []byte("private artifact")) {
					outcomeErr = errors.New("private artifact refusal")
				}
			}
			assertReplacementOutcome(t, target.DB, logPath, before, kind, 1, outcomeErr)
		})
	}
}
