package backup_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
)

// `recover --force` onto a data dir whose server already booted (and so
// minted its own keyring) replaces that keyring with the kit's, keeps the old
// one as a backup beside it, and leaves the directory untouched when it
// refuses up front.
func TestRecoverForceReplacesBootedAuditKeyring(t *testing.T) {
	for _, target := range []string{"booted", "same_keyring", "unsafe_keyring"} {
		t.Run(target, func(t *testing.T) {
			f := newInstanceFixture(t)
			keys, path, err := journal.EnsureSigningKeys(t.TempDir(), "")
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(journal.SigningKeysPathEnv, path)
			writer := journal.NewWriter(f.db, slog.New(slog.NewTextHandler(io.Discard, nil)), journal.WriterOptions{SigningKeys: keys})
			if _, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: "ws_e2e_1", Type: journal.EntryInvitationCreated, ActorType: journal.ActorSystem, Summary: "Signed by the source keyring"}); err != nil {
				t.Fatal(err)
			}
			writer.Close()
			created := f.create(t, true)
			t.Setenv(journal.SigningKeysPathEnv, "")

			root := t.TempDir()
			keyring := filepath.Join(root, journal.SigningKeysFile)
			var previous []byte
			switch target {
			case "booted":
				if _, _, err := journal.EnsureSigningKeys(root, ""); err != nil {
					t.Fatal(err)
				}
			case "same_keyring":
				if _, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: created.Path, Identities: []age.Identity{f.identity}, DataDir: root}); err != nil {
					t.Fatal(err)
				}
			case "unsafe_keyring":
				elsewhere := filepath.Join(t.TempDir(), "keyring.json")
				if err := os.WriteFile(elsewhere, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(elsewhere, keyring); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("operator data"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if target != "unsafe_keyring" {
				if previous, err = os.ReadFile(keyring); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotDir(t, root)

			restored, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: created.Path, Identities: []age.Identity{f.identity}, DataDir: root, Force: true})
			if target == "unsafe_keyring" {
				if err == nil {
					t.Fatal("forced recovery accepted a keyring that is not a regular file")
				}
				if after := snapshotDir(t, root); after != before {
					t.Errorf("refused recovery changed the data dir:\nbefore %s\nafter  %s", before, after)
				}
				return
			}
			if err != nil {
				t.Fatalf("forced recovery onto a %s data dir: %v", target, err)
			}
			if _, err := os.Stat(filepath.Join(root, "secrets.env")); err != nil {
				t.Errorf("recovery stopped before writing secrets.env: %v", err)
			}
			recoveredKeys, err := journal.LoadSigningKeys(keyring)
			if err != nil || recoveredKeys.ActiveID() != keys.ActiveID() {
				t.Fatalf("recovery did not install the kit's audit keyring: %v", err)
			}
			backups, _ := filepath.Glob(keyring + ".replaced-*")
			if target == "same_keyring" {
				if len(backups) != 0 {
					t.Errorf("identical keyring was backed up: %v", backups)
				}
			} else {
				if len(backups) != 1 {
					t.Fatalf("replaced keyring backups=%v want one", backups)
				}
				kept, err := os.ReadFile(backups[0])
				if err != nil || !bytes.Equal(kept, previous) {
					t.Error("replaced keyring backup does not hold the booted keyring")
				}
				if info, err := os.Stat(backups[0]); err != nil || info.Mode().Perm() != 0o600 {
					t.Errorf("keyring backup mode: %v %v", info, err)
				}
			}
			db, err := database.Open("file:" + restored.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			chain, err := journal.VerifyChainWithKeys(t.Context(), db.DB, "ws_e2e_1", recoveredKeys)
			if err != nil || !chain.OK {
				t.Fatalf("restored chain does not verify with the installed keyring: %+v %v", chain, err)
			}
		})
	}
}

func snapshotDir(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		line := rel + " " + info.Mode().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			line += fmt.Sprintf(" %x", sha256.Sum256(data))
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
