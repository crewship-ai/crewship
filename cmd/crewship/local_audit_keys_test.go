//go:build !clionly

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestLocalRecoveryUsesPersistentAppAuditSnapshot(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("9", 64))
	t.Setenv(journal.SigningKeysPathEnv, "")
	dir := t.TempDir()
	t.Setenv("CREWSHIP_DATA_DIR", dir)
	keys, _, err := journal.EnsureSigningKeys(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "")
	target, err := resolveLocalDBTarget()
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := localAuditSigningContext(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := journal.SigningKeysForContext(ctx)
	if err != nil || actual.ActiveID() != keys.ActiveID() {
		t.Fatalf("local recovery selected a different app keyring: %v", err)
	}
}

// Local recovery signs with the keyring of the instance whose database it
// writes, and never mints a keyring of its own: a stray key would sign an
// entry the server cannot verify, or be adopted by the next boot.
func TestAdminResetPasswordUsesTargetInstanceKeyring(t *testing.T) {
	for _, tc := range []struct {
		name         string
		beside, root bool // keyring next to the DB / in the configured data dir
		explicitRoot bool // CREWSHIP_DATA_DIR names the data dir
		want         string
	}{
		{name: "beside_database", beside: true, root: true, want: "beside"},
		{name: "explicit_data_dir", root: true, explicitRoot: true, want: "root"},
		{name: "unrelated_default_root", root: true, want: ""},
		{name: "none", explicitRoot: true, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", strings.Repeat("9", 64))
			t.Setenv(journal.SigningKeysPathEnv, "")
			t.Setenv("CREWSHIP_HOME", "")
			instance, other := t.TempDir(), t.TempDir()
			home := t.TempDir()
			t.Setenv("HOME", home)
			root := filepath.Join(home, ".crewship")
			t.Setenv("CREWSHIP_DATA_DIR", "")
			if tc.explicitRoot {
				root = other
				t.Setenv("CREWSHIP_DATA_DIR", other)
			}
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			keyrings := map[string]*journal.SigningKeys{}
			if tc.beside {
				keys, _, err := journal.EnsureSigningKeys(instance, "")
				if err != nil {
					t.Fatal(err)
				}
				keyrings["beside"] = keys
			}
			if tc.root {
				keys, _, err := journal.EnsureSigningKeys(root, "")
				if err != nil {
					t.Fatal(err)
				}
				keyrings["root"] = keys
			}
			db := testutil.MigratedDBAt(t, filepath.Join(instance, "crewship.db"))
			hash, err := bcrypt.GenerateFromPassword([]byte("old-local-password"), bcrypt.MinCost)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO users(id,email,hashed_password) VALUES('kr-user','kr@example.invalid',?)`, string(hash)); err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
			t.Setenv("DATABASE_URL", "file:"+filepath.Join(instance, "crewship.db"))

			cmd, _ := newAdminCovCmd(runAdminResetPassword)
			cmd.SetIn(strings.NewReader("new-local-password\n"))
			cmd.SetArgs([]string{"--local", "--email=kr@example.invalid", "--password-stdin"})
			err = cmd.Execute()

			for dir, had := range map[string]bool{instance: tc.beside, root: tc.root} {
				if _, statErr := os.Lstat(filepath.Join(dir, journal.SigningKeysFile)); (statErr == nil) != had {
					t.Errorf("keyring presence in %s changed (had=%v)", dir, had)
				}
			}
			if tc.want == "" {
				if err == nil || !strings.Contains(err.Error(), "audit keyring") {
					t.Fatalf("reset without the instance keyring: err=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := database.Open("file:" + filepath.Join(instance, "crewship.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			result, err := journal.VerifyInstanceAuditWithKeys(t.Context(), reopened.DB, keyrings[tc.want])
			if err != nil || !result.OK || result.Count == 0 {
				t.Fatalf("reset was not signed by the %s keyring: %+v %v", tc.want, result, err)
			}
		})
	}
}

func TestAuditExportKeyringPlaintextForDowngrade(t *testing.T) {
	for _, tc := range []string{"exported", "no_plaintext_flag", "existing_output"} {
		t.Run(tc, func(t *testing.T) {
			master := strings.Repeat("7a", 32)
			root := t.TempDir()
			t.Setenv(journal.SigningKeysPathEnv, "")
			t.Setenv("CREWSHIP_HOME", "")
			t.Setenv("CREWSHIP_DATA_DIR", root)
			t.Setenv("DATABASE_URL", "")
			t.Setenv("ENCRYPTION_KEY", master)
			keys, _, err := journal.EnsureSigningKeys(root, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "secrets.env"), []byte("ENCRYPTION_KEY="+master+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ENCRYPTION_KEY", "")
			output := filepath.Join(t.TempDir(), "plain.json")
			args := []string{"--plaintext", "--output", output}
			switch tc {
			case "no_plaintext_flag":
				args = []string{"--output", output}
			case "existing_output":
				if err := os.WriteFile(output, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := newAuditExportKeyringCommand()
			cmd.SetOut(new(bytes.Buffer))
			cmd.SetArgs(args)
			err = cmd.Execute()
			if tc != "exported" {
				raw, _ := os.ReadFile(output)
				if err == nil || (tc == "existing_output" && string(raw) != "keep") || (tc == "no_plaintext_flag" && raw != nil) {
					t.Fatalf("export was not refused: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			exported, err := journal.ParseSigningKeys(raw)
			want, _ := journal.ExportSigningKeys(keys)
			if err != nil || exported.ActiveID() != keys.ActiveID() || !bytes.Equal(raw, want) {
				t.Fatalf("export is not the plaintext keyring: %v", err)
			}
			if info, _ := os.Stat(output); info.Mode().Perm() != 0o600 {
				t.Errorf("export mode %v", info.Mode().Perm())
			}
		})
	}
}
