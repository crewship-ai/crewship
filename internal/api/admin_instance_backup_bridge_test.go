package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/backupplan"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

// waitRun polls GET …/backups/run/{id} until the run leaves "running".
func (f *instanceFixture) waitRun(id string) map[string]any {
	f.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		rr := f.do(f.boss, "GET", "/api/v1/admin/instance/backups/run/"+id, "")
		wantCode(f.t, rr, http.StatusOK, "run status")
		var v map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
			f.t.Fatal(err)
		}
		if v["status"] != "running" {
			return v
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("run %s never finished", id)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func overviewAttention(t *testing.T, f *instanceFixture) map[string]string {
	t.Helper()
	rr := f.do(f.boss, "GET", "/api/v1/admin/instance/backups/overview?scope=instance", "")
	wantCode(t, rr, http.StatusOK, "overview")
	ov := decodeAs[struct {
		NeedsAttention []struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Detail string `json:"detail"`
		} `json:"needs_attention"`
	}](t, rr.Body.Bytes())
	out := map[string]string{}
	for _, a := range ov.NeedsAttention {
		out[a.ID] = a.Title + " — " + a.Detail
	}
	return out
}

// An instance run started with a passphrase goes through the backup service;
// the overview then says, from the bundle's own manifest, that a new server
// cannot unlock its credentials while the recovery kit is off — and stops
// saying it once a run with the kit on lands.
func TestInstanceRunWithPassphraseAndTheVaultKeyAttention(t *testing.T) {
	t.Setenv(encryption.KeyEnvVar("v1"), strings.Repeat("c3", 32))
	t.Setenv(encryption.KeyVersionEnvVar, "")
	f := newInstanceFixture(t)
	sealed, err := encryption.Encrypt("deploy-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO credentials(id,workspace_id,name,type,status,encrypted_value,created_by) VALUES('br-c1','ws-old','deploy','SECRET','ACTIVE',?,'boss')`, sealed); err != nil {
		t.Fatal(err)
	}
	storage := t.TempDir()
	f.r.instanceBackups.SetRecovery(InstanceRecoveryConfig{
		OutputDir: t.TempDir(), DataDir: storage, Quiesce: quiesce.New(), Paths: backup.InstancePaths{Output: storage},
	})

	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/run", `{"scope":"instance","passphrase":"pw","recipients":["age1x"]}`), http.StatusBadRequest, "both keys")
	rr := f.do(f.boss, "POST", "/api/v1/admin/instance/backups/run", `{"scope":"instance","passphrase":"correct horse battery"}`)
	wantCode(t, rr, http.StatusAccepted, "start")
	started := decodeAs[struct {
		ID     string   `json:"id"`
		RunIDs []string `json:"run_ids"`
		Status string   `json:"status"`
	}](t, rr.Body.Bytes())
	if len(started.RunIDs) != 1 || started.Status != "running" {
		t.Fatalf("started = %s", rr.Body.String())
	}
	if strings.Contains(auditMetaFor(t, f, "instance.backup_run_started"), "correct horse") {
		t.Fatal("the passphrase reached the audit log")
	}
	v := f.waitRun(started.ID)
	if v["status"] != "done" {
		t.Fatalf("run = %v", v)
	}
	m, err := backup.Inspect(context.Background(), v["bundle_path"].(string))
	if err != nil || m.Encryption.KeyDerivation != "scrypt" || m.Scope != backup.ScopeInstance {
		t.Fatalf("manifest = %+v, %v", m, err)
	}
	att := overviewAttention(t, f)
	if got := att["vault"]; !strings.Contains(got, "cannot unlock credentials") || !strings.Contains(got, "v1") {
		t.Fatalf("attention with the kit off = %v", att)
	}

	wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/backups/settings/recovery-kit", `{"enabled":true}`), http.StatusOK, "kit on")
	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/backups/run", `{"scope":"instance","passphrase":"correct horse battery"}`)
	wantCode(t, rr, http.StatusAccepted, "start with kit")
	second := decodeAs[struct {
		ID string `json:"id"`
	}](t, rr.Body.Bytes())
	if v := f.waitRun(second.ID); v["status"] != "done" {
		t.Fatalf("second run = %v", v)
	}
	if att := overviewAttention(t, f); att["vault"] != "" {
		t.Fatalf("attention with the kit on = %v", att)
	}
}

func auditMetaFor(t *testing.T, f *instanceFixture, action string) string {
	t.Helper()
	rows, err := f.db.Query(`SELECT * FROM audit_logs WHERE action = ?`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var b strings.Builder
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for _, v := range vals {
			switch x := v.(type) {
			case string:
				b.WriteString(x)
			case []byte:
				b.Write(x)
			}
		}
	}
	return b.String()
}

func TestHoldsPauserHoldsScheduledBackupsUntilResumed(t *testing.T) {
	f := newInstanceFixture(t)
	ctx := context.Background()
	p := holdsPauser{db: f.db}
	if paused, _ := p.Paused(ctx); paused {
		t.Fatal("paused with nothing held")
	}
	for _, key := range []string{quiesce.HoldWebhooks, quiesce.HoldQueue} {
		if err := quiesce.SetHolds(ctx, f.db, []quiesce.Hold{{Key: key}}); err != nil {
			t.Fatal(err)
		}
		if paused, _ := p.Paused(ctx); paused {
			t.Fatalf("hold %s paused scheduled backups", key)
		}
		if _, err := f.db.Exec(`DELETE FROM instance_holds`); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{quiesce.HoldRoutines, quiesce.HoldAll} {
		if err := quiesce.SetHolds(ctx, f.db, []quiesce.Hold{{Key: key}}); err != nil {
			t.Fatal(err)
		}
		paused, reason := p.Paused(ctx)
		if !paused || !strings.Contains(reason, "holds resume "+key) {
			t.Fatalf("hold %s: paused %v reason %q", key, paused, reason)
		}
		if _, err := f.db.Exec(`DELETE FROM instance_holds`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBackupQuiescerHoldsWritesOnlyForInstanceRuns(t *testing.T) {
	f := newInstanceFixture(t)
	ctrl := quiesce.New()
	f.r.instanceBackups.SetRecovery(InstanceRecoveryConfig{Quiesce: ctrl})
	q := backupQuiescer{h: f.r.instanceBackups, db: f.db}
	ctx := context.Background()

	rel, err := q.Quiesce(ctx, backupplan.RunSpec{RunID: "r-ws", Scope: backupplan.ScopeWorkspaces, WorkspaceID: "ws-old"}, time.Minute)
	if err != nil || ctrl.Holding() {
		t.Fatalf("workspace run: err %v holding %v", err, ctrl.Holding())
	}
	rel()

	rel, err = q.Quiesce(ctx, backupplan.RunSpec{RunID: "r-inst", Scope: backupplan.ScopeInstance}, time.Minute)
	if err != nil || !ctrl.Holding() {
		t.Fatalf("instance run: err %v holding %v", err, ctrl.Holding())
	}
	// A second instance run while the window is open goes back to its busy
	// window rather than failing.
	if _, err := q.Quiesce(ctx, backupplan.RunSpec{RunID: "r-2", Scope: backupplan.ScopeInstance}, time.Minute); !errors.Is(err, backup.ErrInstanceBusy) {
		t.Fatalf("second window: %v", err)
	}
	rel()
	if ctrl.Holding() || f.r.instanceBackups.takeWindow("r-inst") != nil {
		t.Fatal("release left the window open or registered")
	}
}
