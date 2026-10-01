//go:build !clionly

package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// miniS3 is the smallest S3 the off-site layer can use, path-style: PUT
// (keeping the x-amz-meta-sha256 it is sent), HEAD, GET and DELETE. It does
// not check signatures; internal/backup/offsite's own fake does that.
type miniS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	sums    map[string]string
}

func (m *miniS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := r.URL.Path
	switch r.Method {
	case http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		m.objects[key], m.sums[key] = b, r.Header.Get("X-Amz-Meta-Sha256")
		w.Header().Set("ETag", `"etag"`)
	case http.MethodHead, http.MethodGet:
		b, ok := m.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.Header().Set("X-Amz-Meta-Sha256", m.sums[key])
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		if r.Method == http.MethodGet {
			_, _ = w.Write(b)
		}
	case http.MethodDelete:
		delete(m.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (m *miniS3) keys(prefix string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

// A real CLI process against a real router: the backup settings, backup
// keys, an off-site destination a real run copies to (a local S3 stand-in,
// allowed only through allow_private_network), incidents and the recovery
// sheet. Nothing in the server is stubbed.
func TestAcceptance_AdminInstanceBackupSettings(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	t.Setenv("CREWSHIP_BACKUP_MIN_FREE_PERCENT", "0") // the space floor has its own tests
	t.Setenv(encryption.KeyEnvVar("v1"), strings.Repeat("c3", 32))
	t.Setenv(encryption.KeyVersionEnvVar, "")
	db := testutil.MigratedDB(t).DB
	token := "crewship_cli_instbacksettings000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('bs-people','People','people','2026-01-01 00:00:00'),('bs-lab','Lab','lab','2026-06-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name) VALUES('bs-boss','boss@people.invalid','Boss'),('bs-carol','carol@lab.invalid','Carol')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('bs-m1','bs-people','bs-boss','OWNER'),('bs-m2','bs-lab','bs-carol','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('bs-token','bs-boss','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
	s3 := &miniS3{objects: map[string][]byte{}, sums: map[string]string{}}
	s3srv := httptest.NewServer(s3)
	defer s3srv.Close()

	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: people\ntoken: "+token+"\nformat: table\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}
	cli := func(args ...string) []string { return append([]string{"admin", "instance", "backups"}, args...) }

	// ── Settings.
	if out := must(cli("settings", "get")...); !strings.Contains(out, "1 run(s) at once") || !strings.Contains(out, "1 instance admin(s)") {
		t.Fatalf("settings get:\n%s", out)
	}
	if out, err := run(cli("settings", "set", "--heartbeat-url", "http://hc.example.com/ping/x")...); err == nil || !strings.Contains(out, "https") {
		t.Fatalf("an http heartbeat was accepted:\n%s", out)
	}
	raw := must(cli("settings", "set", "--concurrency", "2", "--disk-mbps", "50", "--alerts", "failed,stale,offsite",
		"--heartbeat-url", "https://hc.example.com/ping/abc", "--drill-reminder", "weekly", "--format", "json")...)
	var set struct {
		Limits struct {
			Concurrency int `json:"concurrency"`
			CPUCores    int `json:"cpu_cores"`
			DiskMBps    int `json:"disk_mbps"`
		} `json:"limits"`
		HeartbeatURL  *string         `json:"heartbeat_url"`
		Events        map[string]bool `json:"events"`
		DrillReminder string          `json:"drill_reminder"`
	}
	if err := json.Unmarshal([]byte(raw), &set); err != nil {
		t.Fatalf("settings json: %v\n%s", err, raw)
	}
	if set.Limits.Concurrency != 2 || set.Limits.CPUCores != 2 || set.Limits.DiskMBps != 50 || set.HeartbeatURL == nil ||
		set.Events["incomplete"] || !set.Events["offsite"] || set.DrillReminder != "weekly" {
		t.Fatalf("after settings set: %s", raw)
	}
	// Leave the heartbeat off for the run below: a real ping would leave the box.
	must(cli("settings", "set", "--heartbeat-url", "")...)

	// ── Backup keys.
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := run(cli("recipients", "add", "--name", "oops", "--public-key", id.String())...); err == nil || !strings.Contains(out, "PRIVATE") {
		t.Fatalf("a private key was accepted:\n%s", out)
	}
	raw = must(cli("recipients", "add", "--name", "ops-2026", "--public-key", id.Recipient().String(), "--holder", "platform lead", "--format", "json")...)
	var rec struct {
		ID          string `json:"id"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal([]byte(raw), &rec); err != nil || rec.ID == "" {
		t.Fatalf("recipients add: %v\n%s", err, raw)
	}
	if out := must(cli("recipients", "list")...); !strings.Contains(out, "ops-2026") || !strings.Contains(out, rec.Fingerprint) {
		t.Fatalf("recipients list:\n%s", out)
	}

	// ── An off-site destination: private addresses only with the switch.
	secret := filepath.Join(tmp, "s3.secret")
	if err := os.WriteFile(secret, []byte("s3-secret-access-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := []string{"destinations", "add", "--name", "lab-minio", "--endpoint", s3srv.URL, "--bucket", "backups", "--prefix", "crewship",
		"--access-key-id", "AKIDTEST", "--secret-file", secret, "--path-style"}
	if out, err := run(cli(dest...)...); err == nil {
		t.Fatalf("a loopback endpoint was accepted without --allow-private-network:\n%s", out)
	}
	raw = must(cli(append(dest, "--allow-private-network", "--format", "json")...)...)
	var created struct {
		Destination struct {
			ID string `json:"id"`
		} `json:"destination"`
		Test *struct {
			OK bool `json:"ok"`
		} `json:"test"`
		Warning *string `json:"warning"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil || created.Destination.ID == "" || created.Test == nil || !created.Test.OK || created.Warning == nil {
		t.Fatalf("destinations add: %v\n%s", err, raw)
	}
	if strings.Contains(raw, "s3-secret-access-key") {
		t.Fatal("the secret came back")
	}
	if out := must(cli("destinations", "test", created.Destination.ID)...); !strings.Contains(out, "connection ok") {
		t.Fatalf("destinations test:\n%s", out)
	}

	// ── A plan that copies off-site, run now and waited for.
	raw = must(cli("plans", "create", "--preset", "workspace", "--name", "Nightly", "--workspace", "lab", "--recipient", rec.ID,
		"--destination", "local", "--destination", created.Destination.ID, "--format", "json")...)
	var plan struct {
		ID           string   `json:"id"`
		Destinations []string `json:"destinations"`
	}
	if err := json.Unmarshal([]byte(raw), &plan); err != nil || strings.Join(plan.Destinations, ",") != "local,"+created.Destination.ID {
		t.Fatalf("plan: %v\n%s", err, raw)
	}
	raw = must(cli("run", "--plan", plan.ID, "--wait", "--format", "json")...)
	var runs []struct {
		Status     string `json:"status"`
		BundlePath string `json:"bundle_path"`
		Phases     []struct {
			Name   string  `json:"name"`
			Status string  `json:"status"`
			Detail *string `json:"detail"`
		} `json:"phases"`
	}
	if err := json.Unmarshal([]byte(raw), &runs); err != nil || len(runs) != 1 || runs[0].Status != "done" {
		t.Fatalf("run: %v\n%s", err, raw)
	}
	var offsitePhase string
	for _, p := range runs[0].Phases {
		if p.Name == "off-site" {
			offsitePhase = p.Status
		}
	}
	if offsitePhase != "done" {
		t.Fatalf("off-site phase = %q\n%s", offsitePhase, raw)
	}
	if keys := s3.keys("/backups/crewship/workspaces/bs-lab/"); len(keys) != 1 || !strings.HasSuffix(keys[0], filepath.Base(runs[0].BundlePath)) {
		t.Fatalf("objects in the bucket = %v", keys)
	}
	if out := must(cli("destinations", "list")...); !strings.Contains(out, "lab-minio") || !strings.Contains(out, "Nightly") {
		t.Fatalf("destinations list:\n%s", out)
	}

	// The key and the destination cannot go while the plan uses them.
	if out, err := run(cli("recipients", "remove", rec.ID)...); err == nil || !strings.Contains(out, "Nightly") {
		t.Fatalf("a key in use was removed:\n%s", out)
	}
	if out, err := run(cli("destinations", "remove", created.Destination.ID)...); err == nil || !strings.Contains(out, "Nightly") {
		t.Fatalf("a destination in use was removed:\n%s", out)
	}

	// ── Incidents: a good run leaves none open.
	if out := must(cli("incidents", "--state", "open")...); !strings.Contains(out, "No backup incidents") {
		t.Fatalf("incidents:\n%s", out)
	}

	// ── The recovery sheet names the key, the destination, the commands.
	sheetFile := filepath.Join(tmp, "sheet.md")
	must(cli("recovery-sheet", "--out", sheetFile)...)
	sheet, err := os.ReadFile(sheetFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ops-2026", rec.Fingerprint, "lab-minio", "crewship recover --bundle", "crewship backup drill", "boss@people.invalid"} {
		if !strings.Contains(string(sheet), want) {
			t.Errorf("recovery sheet lacks %q\n%s", want, sheet)
		}
	}
	if strings.Contains(string(sheet), "s3-secret-access-key") || strings.Contains(string(sheet), "AGE-SECRET-KEY") {
		t.Fatal("the recovery sheet carries a secret")
	}

	// Change the plan, then both can go.
	other, _ := age.GenerateX25519Identity()
	must(cli("plans", "update", plan.ID, "--recipient", other.Recipient().String(), "--destination", "local")...)
	must(cli("recipients", "remove", rec.ID)...)
	must(cli("destinations", "remove", created.Destination.ID)...)
	if out := must(cli("destinations", "list")...); !strings.Contains(out, "No off-site destination") {
		t.Fatalf("destinations after remove:\n%s", out)
	}
}
