//go:build !clionly

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// A real CLI process uploads an encrypted archive larger than the API's
// 16 MiB body cap to a real router served with the production ReadTimeout.
// A fixture of a few hundred bytes passed both limits unnoticed.
func TestAcceptance_BackupUploadStreamsArchiveAboveTheAPIBodyCap(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	t.Setenv("CREWSHIP_BACKUP_MIN_FREE_PERCENT", "0")
	db := testutil.MigratedDB(t).DB
	boss := "crewship_cli_uploadboss00000000000000"
	for _, q := range []string{
		`INSERT INTO users(id,email,full_name,instance_role) VALUES('up-boss','boss@upload.invalid','Boss','ADMIN')`,
		`INSERT INTO app_settings(key,value,updated_at) VALUES('instance.admin_bootstrapped','already named',datetime('now'))`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('tok-up','up-boss','test',?,datetime('now'))`, sha256HexToken(boss)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	router.SetInstanceRecovery(api.InstanceRecoveryConfig{OutputDir: outDir})
	srv := httptest.NewUnstartedServer(router)
	srv.Config.ReadTimeout = 15 * time.Second
	srv.Start()
	defer srv.Close()

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	manifest := &backup.Manifest{FormatVersion: backup.FormatVersion, Scope: backup.ScopeWorkspace, CompatibleTargets: []backup.Target{backup.TargetAnyInstance},
		CreatedAt: time.Now(), CreatedBy: backup.Actor{UserID: "up-boss", Email: "boss@upload.invalid", Role: "OWNER"}, CrewshipVersionAtBackup: "1.0.0",
		SourceInstance: backup.Instance{Hostname: "test", Platform: "linux/amd64"},
		Contents:       backup.Contents{Workspace: &backup.WorkspaceSummary{ID: "ws-up", Slug: "up", Name: "Up"}}}
	archive := filepath.Join(t.TempDir(), "encrypted.tar.zst")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err = backup.WriteBundle(file, manifest, io.LimitReader(rand.Reader, 20<<20), backup.WriteBundleOptions{Recipients: []age.Recipient{identity.Recipient()}}); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(archive)
	if err != nil || info.Size() <= 16<<20 {
		t.Fatalf("fixture must exceed the 16 MiB API body cap: %v %v", info, err)
	}

	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err = os.WriteFile(cfg, []byte("server: "+srv.URL+"\ntoken: "+boss+"\nformat: table\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(buildCrewshipBinary(t), "admin", "instance", "backups", "upload", archive, "--format", "json")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
	cmd.Stdin = strings.NewReader("")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err = cmd.Run(); err != nil {
		t.Fatalf("upload: %v\n%s%s", err, stdout.String(), stderr.String())
	}
	var receipt struct {
		Path       string `json:"path"`
		SizeBytes  int64  `json:"size_bytes"`
		ProofLevel int    `json:"proof_level"`
	}
	if err = json.Unmarshal(stdout.Bytes(), &receipt); err != nil || receipt.SizeBytes != info.Size() || receipt.ProofLevel != backup.ProofChecksum {
		t.Fatalf("receipt: %v\n%s", err, stdout.String())
	}
	if filepath.Dir(receipt.Path) != outDir {
		t.Fatalf("archive published outside the backups directory: %s", receipt.Path)
	}
	stored, err := os.Stat(receipt.Path)
	if err != nil || stored.Size() != info.Size() {
		t.Fatalf("stored archive: %v %v", stored, err)
	}
}
