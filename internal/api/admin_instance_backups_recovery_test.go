package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

func TestInstanceRecoveryRoutesAreForInstanceAdminsOnly(t *testing.T) {
	f := newInstanceFixture(t)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v1/admin/instance/backups/run", `{"scope":"instance"}`},
		{"GET", "/api/v1/admin/instance/backups/run/run_x", ""},
		{"GET", "/api/v1/admin/instance/backups/vault-keys", ""},
		{"PUT", "/api/v1/admin/instance/backups/settings/recovery-kit", `{"enabled":true}`},
		{"POST", "/api/v1/admin/instance/backups/bundles/check", `{"path":"/x"}`},
		{"POST", "/api/v1/admin/instance/backups/restore/checks", `{"path":"/x","target":"empty_server"}`},
		{"POST", "/api/v1/admin/instance/backups/drills", `{"path":"/x"}`},
		{"GET", "/api/v1/admin/instance/backups/drills", ""},
		{"GET", "/api/v1/admin/instance/holds", ""},
		{"POST", "/api/v1/admin/instance/holds/resume", `{"key":"routines"}`},
	} {
		wantCode(t, f.do(f.wsAdmin, c.method, c.path, c.body), http.StatusForbidden, "workspace ADMIN "+c.method+" "+c.path)
	}
}

func TestInstanceVaultKeysAndRecoveryKit(t *testing.T) {
	t.Setenv(encryption.KeyEnvVar("v1"), strings.Repeat("d4", 32))
	t.Setenv(encryption.KeyEnvVar("v2"), "")
	t.Setenv(encryption.KeyVersionEnvVar, "")
	f := newInstanceFixture(t)
	env, err := encryption.Encrypt("a secret")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.db, `INSERT INTO credentials (id, workspace_id, name, type, status, encrypted_value, created_by) VALUES ('c1','ws-old','k','SECRET','ACTIVE',?,'boss')`, env)
	// An envelope under a version this server has no key of its own for:
	// Decrypt would fall back to ENCRYPTION_KEY, so it reads as present.
	mustExec(t, f.db, `INSERT INTO credentials (id, workspace_id, name, type, status, encrypted_value, created_by) VALUES ('c2','ws-old','k2','SECRET','ACTIVE','v3:AAAA','boss')`)

	type vk struct {
		Versions []struct {
			Version   string `json:"version"`
			Env       string `json:"env"`
			Active    bool   `json:"active"`
			Envelopes int    `json:"envelopes"`
			Present   bool   `json:"present"`
		} `json:"versions"`
		RecoveryKit struct {
			Available bool `json:"available"`
			Enabled   bool `json:"enabled"`
		} `json:"recovery_kit"`
	}
	rr := f.do(f.boss, "GET", "/api/v1/admin/instance/backups/vault-keys", "")
	wantCode(t, rr, http.StatusOK, "vault keys")
	got := decodeAs[vk](t, rr.Body.Bytes())
	if len(got.Versions) != 2 || got.Versions[0].Version != "v1" || !got.Versions[0].Active || got.Versions[0].Envelopes != 1 ||
		got.Versions[1].Version != "v3" || got.Versions[1].Envelopes != 1 || got.RecoveryKit.Enabled || !got.RecoveryKit.Available {
		t.Fatalf("vault keys = %s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), strings.Repeat("d4", 8)) {
		t.Fatal("key material in the vault-keys answer")
	}

	wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/backups/settings/recovery-kit", `{}`), http.StatusBadRequest, "no enabled")
	wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/backups/settings/recovery-kit", `{"enabled":true}`), http.StatusOK, "kit on")
	if !f.audited("instance.backup_recovery_kit_enabled") {
		t.Fatal("no audit entry for turning the kit on")
	}
	got = decodeAs[vk](t, f.do(f.boss, "GET", "/api/v1/admin/instance/backups/vault-keys", "").Body.Bytes())
	if !got.RecoveryKit.Enabled {
		t.Fatal("kit did not stick")
	}
	on, _ := backup.RecoveryKitEnabled(context.Background(), f.db)
	if !on {
		t.Fatal("backup_settings not updated")
	}
}

func TestInstanceHoldsListAndResume(t *testing.T) {
	f := newInstanceFixture(t)
	ctx := context.Background()
	if err := quiesce.SetHolds(ctx, f.db, []quiesce.Hold{
		{Key: quiesce.HoldRoutines, Reason: "instance restore", Count: 4, Detail: "4 schedule(s) will not fire"},
		{Key: quiesce.HoldWebhooks, Reason: "instance restore", Count: 1, Detail: "1 inbound webhook(s) answer 503"},
		{Key: quiesce.HoldQueue, Reason: "instance restore", Detail: "nothing queued"},
	}); err != nil {
		t.Fatal(err)
	}
	quiesce.DefaultHolds().Replace([]quiesce.Hold{{Key: quiesce.HoldRoutines}, {Key: quiesce.HoldWebhooks}, {Key: quiesce.HoldQueue}})
	t.Cleanup(func() { quiesce.DefaultHolds().Replace(nil) })

	type hold struct {
		Key       string `json:"key"`
		Count     int    `json:"count"`
		Detail    string `json:"detail"`
		CreatedAt string `json:"created_at"`
	}
	rr := f.do(f.boss, "GET", "/api/v1/admin/instance/holds", "")
	wantCode(t, rr, http.StatusOK, "list holds")
	list := decodeAs[[]hold](t, rr.Body.Bytes())
	if len(list) != 3 || list[1].Key != "routines" || list[1].Count != 4 || list[1].CreatedAt == "" {
		t.Fatalf("holds = %s", rr.Body.String())
	}
	cases := []struct {
		body string
		want int
	}{
		{`{"key":"galaxy"}`, http.StatusBadRequest},
		{`{"key":"routines"}`, http.StatusOK},
		{`{"key":"routines"}`, http.StatusNotFound},
	}
	for _, c := range cases {
		wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/holds/resume", c.body), c.want, "resume "+c.body)
	}
	if !f.audited("instance.hold_resumed") {
		t.Fatal("resume left no instance audit entry")
	}
	if quiesce.RoutinesPaused() {
		t.Fatal("routines still paused in memory after resume")
	}
	if !quiesce.WebhooksPaused() {
		t.Fatal("resuming routines released webhooks too")
	}
	list = decodeAs[[]hold](t, f.do(f.boss, "GET", "/api/v1/admin/instance/holds", "").Body.Bytes())
	if len(list) != 2 {
		t.Fatalf("after resume: %+v", list)
	}
}

// The whole API loop on one server: start an instance run, poll it, check
// the bundle's contents, run the restore checks, and record a drill.
func TestInstanceRunCheckAndDrill(t *testing.T) {
	t.Setenv(encryption.KeyEnvVar("v1"), strings.Repeat("e5", 32))
	f := newInstanceFixture(t)
	outDir := t.TempDir()
	storage := t.TempDir()
	f.r.instanceBackups.SetRecovery(InstanceRecoveryConfig{
		OutputDir: outDir, DataDir: storage, Quiesce: quiesce.New(),
		Paths: backup.InstancePaths{Output: storage},
	})
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}

	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/run", `{"scope":"galaxy"}`), http.StatusBadRequest, "bad scope")
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/run", `{"scope":"instance"}`), http.StatusBadRequest, "no keys")
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/run", `{"scope":"instance","recipients":["nope"]}`), http.StatusBadRequest, "bad recipient")
	rr := f.do(f.boss, "POST", "/api/v1/admin/instance/backups/run", `{"scope":"instance","preset":"complete","recipients":["`+id.Recipient().String()+`"]}`)
	wantCode(t, rr, http.StatusAccepted, "start run")
	started := decodeAs[struct {
		ID     string `json:"id"`
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}](t, rr.Body.Bytes())
	if started.ID == "" || started.ID != started.RunID || started.Status != "running" {
		t.Fatalf("started = %s", rr.Body.String())
	}
	if !f.audited("instance.backup_run_started") {
		t.Fatal("no audit entry for the run")
	}
	// The run is a backup_runs row: GET …/run/{id} answers the same shape
	// as an item of GET …/runs.
	type runView struct {
		ID         string  `json:"id"`
		Scope      string  `json:"scope"`
		Trigger    string  `json:"trigger"`
		Status     string  `json:"status"`
		BundlePath *string `json:"bundle_path"`
		HoldMs     *int64  `json:"hold_ms"`
		Phases     []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"phases"`
	}
	var st runView
	deadline := time.Now().Add(60 * time.Second)
	for {
		rr = f.do(f.boss, "GET", "/api/v1/admin/instance/backups/run/"+started.ID, "")
		wantCode(t, rr, http.StatusOK, "run status")
		st = decodeAs[runView](t, rr.Body.Bytes())
		if st.Status != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run never finished")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st.Status != "done" || st.BundlePath == nil || st.Scope != "instance" || st.Trigger != "manual" || st.HoldMs == nil {
		t.Fatalf("run = %s", rr.Body.String())
	}
	phases := map[string]string{}
	for _, p := range st.Phases {
		phases[p.Name] = p.Status
	}
	if phases["copy"] != "done" || phases["pack"] != "done" || phases["encrypt"] != "done" || phases["check"] != "done" || phases["off-site"] != "skipped" {
		t.Fatalf("phases = %+v", st.Phases)
	}
	listed := decodeAs[struct {
		Data []runView `json:"data"`
	}](t, f.do(f.boss, "GET", "/api/v1/admin/instance/backups/runs?scope=instance", "").Body.Bytes())
	if len(listed.Data) != 1 || listed.Data[0].ID != st.ID || *listed.Data[0].BundlePath != *st.BundlePath {
		t.Fatalf("runs list = %+v", listed.Data)
	}
	bundle := *st.BundlePath
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/instance/backups/run/nope", ""), http.StatusNotFound, "unknown run")
	entry, err := backup.GetCatalogEntry(context.Background(), f.db, bundle)
	if err != nil || entry.Kind != backup.KindInstance || entry.Scope != "instance" {
		t.Fatalf("catalog = %+v, %v", entry, err)
	}

	// Contents check, with the identity in the body.
	key := id.String()
	body, _ := json.Marshal(map[string]string{"path": bundle, "identity": key})
	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/backups/bundles/check", string(body))
	wantCode(t, rr, http.StatusOK, "check")
	chk := decodeAs[struct {
		OK         bool `json:"ok"`
		ProofLevel int  `json:"proof_level"`
	}](t, rr.Body.Bytes())
	if !chk.OK || chk.ProofLevel != 2 {
		t.Fatalf("check = %s", rr.Body.String())
	}
	if e, _ := backup.GetCatalogEntry(context.Background(), f.db, bundle); e.ProofLevel != 2 {
		t.Fatalf("proof level = %d", e.ProofLevel)
	}
	other, _ := age.GenerateX25519Identity()
	body, _ = json.Marshal(map[string]string{"path": bundle, "identity": other.String()})
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/bundles/check", string(body)), http.StatusUnprocessableEntity, "wrong key")
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/bundles/check", `{"path":"/not/catalogued"}`), http.StatusNotFound, "uncatalogued")

	// Restore checks.
	body, _ = json.Marshal(map[string]string{"path": bundle, "target": "empty_server", "identity": key})
	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/backups/restore/checks", string(body))
	wantCode(t, rr, http.StatusOK, "restore checks")
	rc := decodeAs[backup.RestoreChecks](t, rr.Body.Bytes())
	if !rc.Format.OK || !rc.Conflicts.OK || rc.Space.NeedBytes == 0 {
		t.Fatalf("restore checks = %s", rr.Body.String())
	}
	body, _ = json.Marshal(map[string]string{"path": bundle, "target": "replace"})
	rc = decodeAs[backup.RestoreChecks](t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/restore/checks", string(body)).Body.Bytes())
	if rc.Conflicts.OK {
		t.Fatal("an instance bundle passed the conflicts check for a workspace target")
	}
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/restore/checks", `{"path":"x","target":"moon"}`), http.StatusBadRequest, "bad target")

	// A drill recorded from the CLI.
	report := `{"result":"partial","checks":[{"name":"credentials_unlock","status":"skipped"},{"name":"attachments_open","status":"ok"}]}`
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/drills",
		`{"path":"`+bundle+`","sha256":"wrong","result":"partial","report":`+report+`}`), http.StatusConflict, "sha mismatch")
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/drills",
		`{"path":"`+bundle+`","sha256":"`+entry.SHA256+`","result":"maybe"}`), http.StatusBadRequest, "bad result")
	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/backups/drills",
		`{"path":"`+bundle+`","sha256":"`+entry.SHA256+`","result":"partial","report":`+report+`}`)
	wantCode(t, rr, http.StatusCreated, "record drill")
	if e, _ := backup.GetCatalogEntry(context.Background(), f.db, bundle); e.ProofLevel != 3 || e.DrillResult != "partial" {
		t.Fatalf("after drill: proof %d result %q", e.ProofLevel, e.DrillResult)
	}
	if !f.audited("instance.backup_drill_recorded") {
		t.Fatal("drill left no audit entry")
	}
	rr = f.do(f.boss, "GET", "/api/v1/admin/instance/backups/drills", "")
	wantCode(t, rr, http.StatusOK, "list drills")
	drills := decodeAs[[]struct {
		Kind        string `json:"kind"`
		SourceScope string `json:"source_scope"`
		Result      string `json:"result"`
		Warnings    int    `json:"warnings"`
		Actor       string `json:"actor"`
	}](t, rr.Body.Bytes())
	if len(drills) != 1 || drills[0].SourceScope != "instance" || drills[0].Result != "partial" || drills[0].Warnings != 1 || drills[0].Actor != "boss@ex.com" {
		t.Fatalf("drills = %s", rr.Body.String())
	}
	if entries, _ := os.ReadDir(outDir); len(entries) != 1 || filepath.Ext(entries[0].Name()) != ".zst" {
		t.Fatalf("output dir = %v", entries)
	}
}
