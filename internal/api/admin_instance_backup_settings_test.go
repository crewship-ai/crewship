package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup/offsite"
	"github.com/crewship-ai/crewship/internal/backupplan"
	"github.com/crewship-ai/crewship/internal/encryption"
)

func TestInstanceBackupSettingsRoutesAreForInstanceAdminsOnly(t *testing.T) {
	f := newInstanceFixture(t)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/admin/instance/backups/settings", ""},
		{"PUT", "/api/v1/admin/instance/backups/settings", `{"limits":{"concurrency":2}}`},
		{"GET", "/api/v1/admin/instance/backups/recipients", ""},
		{"POST", "/api/v1/admin/instance/backups/recipients", `{"name":"x","public_key":"age1x"}`},
		{"DELETE", "/api/v1/admin/instance/backups/recipients/brk_x", ""},
		{"GET", "/api/v1/admin/instance/backups/destinations", ""},
		{"POST", "/api/v1/admin/instance/backups/destinations", `{}`},
		{"POST", "/api/v1/admin/instance/backups/destinations/bdst_x/test", `{}`},
		{"DELETE", "/api/v1/admin/instance/backups/destinations/bdst_x", ""},
		{"GET", "/api/v1/admin/instance/backups/incidents", ""},
		{"GET", "/api/v1/admin/instance/backups/recovery-sheet", ""},
	} {
		wantCode(t, f.do(f.wsAdmin, c.method, c.path, c.body), http.StatusForbidden, "workspace ADMIN "+c.method+" "+c.path)
	}
}

type settingsView struct {
	Limits             backupplan.Limits      `json:"limits"`
	HeartbeatURL       *string                `json:"heartbeat_url"`
	RecoveryKitEnabled bool                   `json:"recovery_kit_enabled"`
	Channels           []string               `json:"channels"`
	Events             backupplan.AlertEvents `json:"events"`
	StaleAlertHours    int                    `json:"stale_alert_hours"`
	DrillReminder      string                 `json:"drill_reminder"`
	Destinations       []struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		Label     string `json:"label"`
		Verified  bool   `json:"verified"`
		Available bool   `json:"available"`
	} `json:"destinations"`
	InstanceAdmins int     `json:"instance_admins"`
	LocalPath      *string `json:"local_path"`
}

func TestInstanceBackupSettingsGetAndPartialPut(t *testing.T) {
	f := newInstanceFixture(t)
	rr := f.do(f.boss, "GET", "/api/v1/admin/instance/backups/settings", "")
	wantCode(t, rr, http.StatusOK, "get settings")
	got := decodeAs[settingsView](t, rr.Body.Bytes())
	if got.Limits.Concurrency != 1 || got.StaleAlertHours != 36 || got.DrillReminder != "monthly" || !got.Events.Failed || got.InstanceAdmins != 1 {
		t.Fatalf("defaults = %+v", got)
	}
	if len(got.Destinations) != 2 || got.Destinations[0].Kind != "local" || got.Destinations[1].Kind != "drive" || got.Destinations[1].Available {
		t.Fatalf("destinations = %+v", got.Destinations)
	}

	for _, bad := range []string{
		`{"limits":{"concurrency":0}}`,
		`{"heartbeat_url":"http://hc.example.com/ping"}`,
		`{"drill_reminder":"daily"}`,
		`{"stale_alert_hours":0}`,
	} {
		wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/backups/settings", bad), http.StatusBadRequest, bad)
	}

	rr = f.do(f.boss, "PUT", "/api/v1/admin/instance/backups/settings",
		`{"limits":{"disk_mbps":80},"heartbeat_url":"https://hc.example.com/ping/abc","events":{"failed":true,"incomplete":false,"stale":true,"offsite":true,"drill":true},"drill_reminder":"weekly"}`)
	wantCode(t, rr, http.StatusOK, "put settings")
	got = decodeAs[settingsView](t, rr.Body.Bytes())
	// Only what was sent changed: concurrency and cpu_cores kept their value.
	if got.Limits != (backupplan.Limits{Concurrency: 1, CPUCores: 2, DiskMBps: 80}) || got.HeartbeatURL == nil || got.Events.Incomplete || got.DrillReminder != "weekly" {
		t.Fatalf("after put = %+v", got)
	}
	var audits int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action = 'instance.backup_settings_updated'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit rows = %d %v", audits, err)
	}
	// The recovery-kit route keeps working, and the settings read it back.
	wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/backups/settings/recovery-kit", `{"enabled":true}`), http.StatusOK, "kit on")
	got = decodeAs[settingsView](t, f.do(f.boss, "GET", "/api/v1/admin/instance/backups/settings", "").Body.Bytes())
	if !got.RecoveryKitEnabled || got.Limits.DiskMBps != 80 {
		t.Fatalf("after kit on = %+v", got)
	}
	// Clearing the heartbeat.
	got = decodeAs[settingsView](t, f.do(f.boss, "PUT", "/api/v1/admin/instance/backups/settings", `{"heartbeat_url":null}`).Body.Bytes())
	if got.HeartbeatURL != nil {
		t.Fatalf("heartbeat not cleared: %v", *got.HeartbeatURL)
	}
}

func TestInstanceBackupRecipientsLifecycle(t *testing.T) {
	f := newInstanceFixture(t)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/recipients", `{"name":"ops","public_key":"AGE-SECRET-KEY-1ABC"}`), http.StatusBadRequest, "private key")
	rr := f.do(f.boss, "POST", "/api/v1/admin/instance/backups/recipients", `{"name":"ops-2026","public_key":"`+id.Recipient().String()+`","holder":"platform lead"}`)
	wantCode(t, rr, http.StatusCreated, "add key")
	rec := decodeAs[backupplan.Recipient](t, rr.Body.Bytes())
	if rec.ID == "" || !strings.HasPrefix(rec.Fingerprint, "SHA256:") {
		t.Fatalf("recipient = %+v", rec)
	}
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/recipients", `{"name":"again","public_key":"`+id.Recipient().String()+`"}`), http.StatusConflict, "duplicate key")

	// A plan names the key by id.
	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/backups/plans", `{"preset":"workspace","name":"Nightly","recipient_ids":["`+rec.ID+`"]}`)
	wantCode(t, rr, http.StatusCreated, "plan with the key")
	plan := decodeAs[backupplan.Plan](t, rr.Body.Bytes())
	list := decodeAs[struct {
		Data []backupplan.Recipient `json:"data"`
	}](t, f.do(f.boss, "GET", "/api/v1/admin/instance/backups/recipients", "").Body.Bytes())
	if len(list.Data) != 1 || strings.Join(list.Data[0].UsedBy, ",") != "Nightly" {
		t.Fatalf("list = %+v", list.Data)
	}
	rr = f.do(f.boss, "DELETE", "/api/v1/admin/instance/backups/recipients/"+rec.ID, "")
	wantCode(t, rr, http.StatusConflict, "delete a key in use")
	if !strings.Contains(rr.Body.String(), "Nightly") {
		t.Fatalf("409 does not name the plan: %s", rr.Body.String())
	}
	other, _ := age.GenerateX25519Identity()
	wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/backups/plans/"+plan.ID, `{"recipient_ids":["`+other.Recipient().String()+`"]}`), http.StatusOK, "change the plan")
	wantCode(t, f.do(f.boss, "DELETE", "/api/v1/admin/instance/backups/recipients/"+rec.ID, ""), http.StatusNoContent, "delete")
	wantCode(t, f.do(f.boss, "DELETE", "/api/v1/admin/instance/backups/recipients/"+rec.ID, ""), http.StatusNotFound, "delete again")
	var audits int
	_ = f.db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action IN ('instance.backup_recipient_added','instance.backup_recipient_removed')`).Scan(&audits)
	if audits != 2 {
		t.Fatalf("audit rows = %d", audits)
	}
}

// testDest answers Test with err.
type testDest struct{ err error }

func (d testDest) Kind() string { return "s3" }
func (d testDest) Put(context.Context, string, io.Reader, int64, string) (offsite.Object, error) {
	return offsite.Object{}, nil
}
func (d testDest) Get(context.Context, string) (io.ReadCloser, offsite.Object, error) {
	return nil, offsite.Object{}, offsite.ErrNotFound
}
func (d testDest) List(context.Context, string) ([]offsite.Object, error) { return nil, nil }
func (d testDest) Head(context.Context, string) (offsite.Object, error) {
	return offsite.Object{}, offsite.ErrNotFound
}
func (d testDest) Delete(context.Context, string) error { return nil }
func (d testDest) Test(context.Context) error           { return d.err }

func TestInstanceBackupDestinationsLifecycle(t *testing.T) {
	t.Setenv(encryption.KeyEnvVar("v1"), strings.Repeat("a7", 32))
	t.Setenv(encryption.KeyVersionEnvVar, "")
	f := newInstanceFixture(t)
	testErr := errors.New("403 AccessDenied")
	var tested []offsite.S3Config
	prev := newDestinationClient
	newDestinationClient = func(cfg offsite.S3Config) (offsite.Destination, error) {
		tested = append(tested, cfg)
		return testDest{err: testErr}, nil
	}
	t.Cleanup(func() { newDestinationClient = prev })
	const body = `{"name":"r2","endpoint":"https://acct.r2.cloudflarestorage.com","region":"auto","bucket":"crewship-backups","prefix":"prod","access_key_id":"AKID","secret_access_key":"super-secret-value"}`

	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/destinations", `{"endpoint":"https://169.254.169.254","bucket":"b1b","access_key_id":"a","secret_access_key":"s"}`), http.StatusBadRequest, "metadata endpoint")
	rr := f.do(f.boss, "POST", "/api/v1/admin/instance/backups/destinations", body)
	wantCode(t, rr, http.StatusUnprocessableEntity, "failed connection test")
	if !strings.Contains(rr.Body.String(), "403 AccessDenied") {
		t.Fatalf("422 body = %s", rr.Body.String())
	}
	var n int
	_ = f.db.QueryRow(`SELECT COUNT(*) FROM backup_offsite_destinations`).Scan(&n)
	if n != 0 {
		t.Fatal("a destination whose test failed was stored")
	}

	testErr = nil
	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/backups/destinations", body)
	wantCode(t, rr, http.StatusCreated, "add destination")
	if strings.Contains(rr.Body.String(), "super-secret-value") {
		t.Fatal("the secret came back in the response")
	}
	created := decodeAs[struct {
		Destination backupplan.Destination `json:"destination"`
		Test        *struct {
			OK bool `json:"ok"`
		} `json:"test"`
		Warning *string `json:"warning"`
	}](t, rr.Body.Bytes())
	if created.Destination.ID == "" || created.Test == nil || !created.Test.OK || created.Warning != nil || tested[len(tested)-1].SecretAccessKey != "super-secret-value" {
		t.Fatalf("created = %+v", created)
	}
	var sealed string
	if err := f.db.QueryRow(`SELECT secret_enc FROM backup_offsite_destinations WHERE id = ?`, created.Destination.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if sealed == "super-secret-value" || !encryption.IsEncrypted(sealed) {
		t.Fatalf("secret stored as %q", sealed)
	}
	if plain, err := encryption.Decrypt(sealed); err != nil || plain != "super-secret-value" {
		t.Fatalf("unseal = %q %v", plain, err)
	}
	var auditMeta string
	_ = f.db.QueryRow(`SELECT metadata FROM instance_audit_logs WHERE action = 'instance.backup_destination_added'`).Scan(&auditMeta)
	if auditMeta == "" || strings.Contains(auditMeta, "super-secret") {
		t.Fatalf("audit metadata = %q", auditMeta)
	}

	// A private endpoint only with the explicit switch, and then with a warning.
	priv := `{"endpoint":"http://10.0.0.5:9000","bucket":"lab","access_key_id":"a","secret_access_key":"s","path_style":true`
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/destinations", priv+`}`), http.StatusBadRequest, "private without the switch")
	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/backups/destinations", priv+`,"allow_private_network":true}`)
	wantCode(t, rr, http.StatusCreated, "private with the switch")
	if !strings.Contains(rr.Body.String(), "allow_private_network is on") {
		t.Fatalf("no warning: %s", rr.Body.String())
	}

	list := f.do(f.boss, "GET", "/api/v1/admin/instance/backups/destinations", "")
	wantCode(t, list, http.StatusOK, "list")
	if strings.Contains(list.Body.String(), "super-secret") || strings.Contains(list.Body.String(), "secret_enc") {
		t.Fatalf("list leaks the secret: %s", list.Body.String())
	}
	settings := decodeAs[settingsView](t, f.do(f.boss, "GET", "/api/v1/admin/instance/backups/settings", "").Body.Bytes())
	if len(settings.Destinations) != 4 || settings.Destinations[1].Label != "r2" || settings.Destinations[1].Verified || !settings.Destinations[1].Available {
		t.Fatalf("settings destinations = %+v", settings.Destinations)
	}

	// A plan copies there; the destination cannot go until the plan changes.
	id, _ := age.GenerateX25519Identity()
	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/backups/plans", `{"preset":"workspace","name":"Offsite","recipient_ids":["`+id.Recipient().String()+`"],"destinations":["`+created.Destination.ID+`"]}`)
	wantCode(t, rr, http.StatusCreated, "plan with destination")
	plan := decodeAs[backupplan.Plan](t, rr.Body.Bytes())
	if strings.Join(plan.Destinations, ",") != "local,"+created.Destination.ID {
		t.Fatalf("plan destinations = %v (local always first)", plan.Destinations)
	}
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/plans", `{"preset":"workspace","name":"Bad","recipient_ids":["`+id.Recipient().String()+`"],"destinations":["bdst_nope"]}`), http.StatusBadRequest, "unknown destination")
	wantCode(t, f.do(f.boss, "DELETE", "/api/v1/admin/instance/backups/destinations/"+created.Destination.ID, ""), http.StatusConflict, "delete in use")
	wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/backups/plans/"+plan.ID, `{"destinations":["local"]}`), http.StatusOK, "plan local only")
	wantCode(t, f.do(f.boss, "DELETE", "/api/v1/admin/instance/backups/destinations/"+created.Destination.ID, ""), http.StatusNoContent, "delete")
}

func TestBackupIncidentReachesEveryInstanceAdminsInbox(t *testing.T) {
	f := newInstanceFixture(t)
	ctx := context.Background()
	// Name two instance admins: boss (member of ws-old) and carol (OWNER of
	// ws-new). wsadmin administers a workspace, not the instance.
	mustExec(t, f.db, `UPDATE users SET instance_role = 'ADMIN' WHERE id IN ('boss','carol')`)
	mustExec(t, f.db, `INSERT INTO users (id, email, full_name, instance_role) VALUES ('lonely','lonely@ex.com','Lonely','ADMIN')`)
	now := time.Now().UTC()
	inc, opened, err := backupplan.RaiseIncident(ctx, f.db, "bp_1", backupplan.IncidentFailed,
		"Complete recovery backup failed. Last successful backup: 32 hours ago.", "br_1", now, true)
	if err != nil || !opened {
		t.Fatalf("raise: %v %v", opened, err)
	}
	a := backupIncidentAlerter{db: f.db, logger: newTestLogger()}
	a.Raised(ctx, inc, true, "disk on fire")

	type row struct{ ws, target, kind, title, body, payload, state string }
	read := func() map[string]row {
		rows, err := f.db.Query(`SELECT workspace_id, COALESCE(target_user_id,''), kind, title, COALESCE(body_md,''), payload_json, state FROM inbox_items WHERE source_id LIKE 'backup_incident:%'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]row{}
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.ws, &r.target, &r.kind, &r.title, &r.body, &r.payload, &r.state); err != nil {
				t.Fatal(err)
			}
			out[r.target] = r
		}
		return out
	}
	items := read()
	if len(items) != 2 {
		t.Fatalf("inbox items = %+v, want one each for boss and carol (lonely has no workspace)", items)
	}
	if items["boss"].ws != "ws-old" || items["carol"].ws != "ws-new" || items["wsadmin"].ws != "" {
		t.Fatalf("routing = %+v", items)
	}
	b := items["boss"]
	var payload map[string]any
	_ = json.Unmarshal([]byte(b.payload), &payload)
	if b.kind != "message" || b.title != "Backup needs attention" || !strings.Contains(b.body, "Last successful backup: 32 hours ago") ||
		!strings.Contains(b.body, "disk on fire") || payload["subkind"] != SubkindBackupIncident || payload["plan_id"] != "bp_1" ||
		payload["view_url"] != "/admin?run=br_1&section=history&tab=backups" {
		t.Fatalf("boss item = %+v payload %v", b, payload)
	}
	// A repeat updates the same items: one incident, one card each.
	inc, _, _ = backupplan.RaiseIncident(ctx, f.db, "bp_1", backupplan.IncidentFailed, "Complete recovery backup failed. Last successful backup: 56 hours ago.", "br_2", now.Add(24*time.Hour), true)
	a.Raised(ctx, inc, false, "")
	items = read()
	if len(items) != 2 || !strings.Contains(items["boss"].body, "56 hours ago") || !strings.Contains(items["boss"].body, "2 failures · one incident") {
		t.Fatalf("after repeat = %+v", items)
	}
	// The next good run resolves the incident and every card it delivered.
	closed, err := backupplan.ResolveIncidents(ctx, f.db, "bp_1", []string{backupplan.IncidentFailed}, now.Add(48*time.Hour))
	if err != nil || len(closed) != 1 {
		t.Fatalf("resolve: %v %v", closed, err)
	}
	a.Resolved(ctx, closed[0])
	for who, it := range read() {
		if it.state != "resolved" {
			t.Fatalf("%s's card still %s", who, it.state)
		}
	}
	// And the incidents endpoint lists it.
	rr := f.do(f.boss, "GET", "/api/v1/admin/instance/backups/incidents", "")
	wantCode(t, rr, http.StatusOK, "incidents")
	list := decodeAs[struct {
		Data []backupplan.Incident `json:"data"`
	}](t, rr.Body.Bytes())
	if len(list.Data) != 1 || list.Data[0].State != "resolved" || list.Data[0].Count != 2 {
		t.Fatalf("incidents = %+v", list.Data)
	}
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/instance/backups/incidents?state=bogus", ""), http.StatusBadRequest, "bad state")
}

func TestInstanceBackupRecoverySheet(t *testing.T) {
	t.Setenv(encryption.KeyEnvVar("v1"), strings.Repeat("b8", 32))
	f := newInstanceFixture(t)
	id, _ := age.GenerateX25519Identity()
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/recipients", `{"name":"ops-2026","public_key":"`+id.Recipient().String()+`","holder":"the platform lead"}`), http.StatusCreated, "key")
	rr := f.do(f.boss, "GET", "/api/v1/admin/instance/backups/recovery-sheet", "")
	wantCode(t, rr, http.StatusOK, "sheet")
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("content type %q", ct)
	}
	sheet := rr.Body.String()
	for _, want := range []string{
		"# Crewship recovery sheet", "ops-2026", backupplan.KeyFingerprint(id.Recipient().String()), "the platform lead",
		"crewship recover --bundle", "crewship backup drill --bundle", "boss@ex.com", "recovery kit is off", "Off-site:** none",
	} {
		if !strings.Contains(sheet, want) {
			t.Errorf("sheet lacks %q\n%s", want, sheet)
		}
	}
	if strings.Contains(sheet, "AGE-SECRET-KEY") || strings.Contains(sheet, "carol@ex.com") {
		t.Fatalf("sheet carries a private key or a non-admin:\n%s", sheet)
	}
}
