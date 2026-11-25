//go:build linux

package backup_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
)

// The child dies synchronously on the recovery goroutine at the named
// checkpoint. Neither deferred cleanup nor a timing race can mask a write.
type recoveryCrashLog struct {
	slog.Handler
	checkpoint string
}

func (h recoveryCrashLog) Handle(_ context.Context, r slog.Record) error {
	if r.Message == h.checkpoint {
		fmt.Fprintln(os.Stdout, "reached:", r.Message)
		return syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}
	return nil
}

func TestForceRecoveryInterrupted(t *testing.T) {
	args := flag.Args()
	if len(args) == 4 && args[0] == "interrupt-recovery" {
		ids, err := age.ParseIdentities(os.Stdin)
		if err != nil {
			t.Fatal(err)
		}
		handler := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
		_, err = backup.RecoverInstance(t.Context(), backup.RecoverOptions{
			BundlePath: args[1], DataDir: args[2], Force: true, Identities: ids,
			Logger: slog.New(recoveryCrashLog{handler, args[3]}),
		})
		t.Fatalf("recovery finished before interruption at %q: %v", args[3], err)
	}

	// Use a real pending migration so "applied migration" marks a committed
	// schema change in the scratch DB. The kit exercises both key-file writes.
	t.Setenv("ENCRYPTION_KEY", instKeyV1)
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.MigrateBefore(t.Context(), db, slog.New(slog.NewTextHandler(io.Discard, nil)), "convert_waitpoint_identities"); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := backup.CreateInstanceBackup(t.Context(), db, backup.InstanceOptions{
		OutputDir: t.TempDir(), Recipients: []age.Recipient{identity.Recipient()}, RecoveryKit: true,
		Actor: backup.Actor{UserID: "u_admin", Role: "instance_admin"},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, checkpoint := range []string{
		"recovery target cloned", "recovery payload extracted", "recovery snapshot validated",
		"applied migration", "recovery database prepared", "recovery secrets written",
		"writing recovery report", "publishing recovered directory", "recovered directory published",
	} {
		t.Run(checkpoint, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "data")
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(target, "crewship.db")
			originalDB, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = originalDB.Exec("CREATE TABLE local_data(value TEXT); INSERT INTO local_data VALUES ('original')")
			if err := errors.Join(err, originalDB.Close()); err != nil {
				t.Fatal(err)
			}
			for _, file := range []string{"secrets.env", backup.RecoveredKeysFile} {
				if err := os.WriteFile(filepath.Join(target, file), []byte("LOCAL=before\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := forceGenerationSnapshot(t, target)
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestForceRecoveryInterrupted$", "--",
				"interrupt-recovery", bundle.Path, target, checkpoint)
			cmd.Stdin = strings.NewReader(identity.String() + "\n")
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL ||
				!bytes.Contains(out, []byte("reached: "+checkpoint+"\n")) {
				t.Fatalf("child failed to reach %q: %v\n%s", checkpoint, err, out)
			}
			if checkpoint != "recovered directory published" {
				assertOriginalGeneration(t, target, before)
				return
			}

			// The committed report must identify the actual original inodes
			// even when the CLI never got to print the result.
			active := forceGenerationSnapshot(t, target)
			for _, name := range []string{".", "crewship.db", "secrets.env", backup.RecoveredKeysFile} {
				item, ok := active[name]
				if !ok || os.SameFile(before[name].info, item.info) || (name != "." && item.hash == before[name].hash) {
					t.Fatalf("incomplete switch at %s", name)
				}
			}
			recovered, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			var body []byte
			if err := recovered.QueryRow("SELECT report FROM restore_reports WHERE bundle_path=?", bundle.Path).Scan(&body); err != nil {
				t.Fatal(err)
			}
			var report backup.RecoverReport
			if err := json.Unmarshal(body, &report); err != nil {
				t.Fatal(err)
			}
			if report.DataDir != target || report.PreviousDataDir == "" || report.MigrationsApplied < 1 || !report.AuthKeysRotated {
				t.Fatal("committed recovery report lacks the active path, original path, migration or key rotation")
			}
			assertOriginalGeneration(t, report.PreviousDataDir, before)
		})
	}
}
