package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestLandEnvironmentsIsForInstanceAdminsOnly(t *testing.T) {
	f := newInstanceFixture(t)
	wantCode(t, f.do(f.wsAdmin, "POST", "/api/v1/admin/instance/backups/environments/land", `{}`), http.StatusForbidden, "workspace ADMIN land")
}

func TestLandEnvironments(t *testing.T) {
	f := newInstanceFixture(t)
	dataDir := t.TempDir()
	f.r.instanceBackups.SetRecovery(InstanceRecoveryConfig{DataDir: dataDir, OutputDir: t.TempDir()})

	type landed struct {
		Dir          string `json:"dir"`
		Staged       bool   `json:"staged"`
		DryRun       bool   `json:"dry_run"`
		Environments []struct {
			Workspace string   `json:"workspace"`
			Crew      string   `json:"crew"`
			Result    string   `json:"result"`
			Reason    string   `json:"reason"`
			Unsafe    []string `json:"unsafe"`
		} `json:"environments"`
	}

	// Nothing staged: an honest empty answer, not an error.
	rr := f.do(f.boss, "POST", "/api/v1/admin/instance/backups/environments/land", `{}`)
	wantCode(t, rr, http.StatusOK, "land with nothing staged")
	got := decodeAs[landed](t, rr.Body.Bytes())
	if got.Staged || len(got.Environments) != 0 {
		t.Fatalf("nothing staged = %s", rr.Body.String())
	}

	// One staged workspace with one environment; this server has no Docker.
	dir := filepath.Join(dataDir, backup.RecoveredEnvironmentsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	env := backup.Environment{Format: backup.EnvironmentFormat, ID: "e1", Crew: "ops", Managed: true, ImageRef: "crewship-env/ops:e1",
		Unsafe: []backup.UnsafeSetting{{Kind: backup.UnsafePrivileged}}}
	rec, _ := json.Marshal(env)
	f1, err := os.Create(filepath.Join(dir, "ws-old.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	tw, err := backup.NewTarZstWriter(f1)
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteFile("environments/ops.json", 0o600, time.Now(), rec); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	_ = f1.Close()

	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/backups/environments/land", `{"dry_run":true}`)
	wantCode(t, rr, http.StatusOK, "dry run")
	got = decodeAs[landed](t, rr.Body.Bytes())
	if !got.Staged || !got.DryRun || len(got.Environments) != 1 {
		t.Fatalf("dry run = %s", rr.Body.String())
	}
	e := got.Environments[0]
	if e.Workspace != "ws-old" || e.Crew != "ops" || e.Result != backup.EnvSkipped || !strings.Contains(e.Reason, "Docker") {
		t.Fatalf("environment = %+v", e)
	}
	if len(e.Unsafe) != 1 || e.Unsafe[0] != "privileged mode on ops" {
		t.Fatalf("unsafe = %v", e.Unsafe)
	}
	if f.audited("instance.environments_landed") {
		t.Fatal("a dry run left an audit entry")
	}
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/environments/land", `{}`), http.StatusOK, "land")
	if !f.audited("instance.environments_landed") {
		t.Fatal("landing left no instance audit entry")
	}
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/environments/land", `{"dry_run":`), http.StatusBadRequest, "bad body")
}
