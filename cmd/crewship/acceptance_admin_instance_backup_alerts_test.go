//go:build !clionly

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/notify"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// A real CLI process against a real router: a notification channel (a local
// webhook receiver) is offered for backup alerts, a test alert is refused by
// the SSRF guard until loopback is allowed, the channel joins the alert
// route, and a real run whose off-site copy fails puts "Backup needs
// attention" on it.
func TestAcceptance_AdminInstanceBackupAlertChannels(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	t.Setenv("CREWSHIP_BACKUP_MIN_FREE_PERCENT", "0")
	t.Setenv("CREWSHIP_PUBLIC_URL", "https://crewship.example.com")
	t.Setenv(encryption.KeyEnvVar("v1"), strings.Repeat("d4", 32))
	t.Setenv(encryption.KeyVersionEnvVar, "")
	db := testutil.MigratedDB(t).DB
	token := "crewship_cli_instbackalerts00000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('ba-people','People','people','2026-01-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name) VALUES('ba-boss','boss@people.invalid','Boss')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('ba-m1','ba-people','ba-boss','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('ba-token','ba-boss','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()

	var mu sync.Mutex
	var posts []map[string]any
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		posts = append(posts, m)
		mu.Unlock()
	}))
	defer hook.Close()
	received := func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), posts...)
	}
	// An off-site store that refuses everything, so the run's copy fails.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()

	ch, err := notify.NewChannelStore(db).Create(context.Background(), notify.ChannelInput{WorkspaceID: "ba-people", Type: notify.ChannelWebhook, URL: hook.URL})
	if err != nil {
		t.Fatal(err)
	}

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

	// The channel is offered.
	if out := must(cli("settings", "get")...); !strings.Contains(out, "Could tell:   Webhook 127.0.0.1:") || !strings.Contains(out, " · People ("+ch.ID+")") {
		t.Fatalf("settings get:\n%s", out)
	}

	// A test alert goes through the notification dispatcher, whose SSRF guard
	// refuses this loopback receiver.
	if out, err := run(cli("test-alert", "--channel", ch.ID)...); err == nil || !strings.Contains(out, "was not delivered") {
		t.Fatalf("a loopback receiver was reached:\n%s", out)
	}
	if n := len(received()); n != 0 {
		t.Fatalf("receiver got %d posts through the SSRF guard", n)
	}
	defer notify.SetWebhookTransportForTesting(http.DefaultTransport)()
	if out, err := run(cli("test-alert")...); err == nil || !strings.Contains(out, "--channel is required") {
		t.Fatalf("test-alert without --channel:\n%s", out)
	}
	raw := must(cli("test-alert", "--channel", ch.ID, "--format", "json")...)
	var res struct {
		OK        bool   `json:"ok"`
		ChannelID string `json:"channel_id"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil || !res.OK || res.ChannelID != ch.ID {
		t.Fatalf("test-alert json: %v\n%s", err, raw)
	}
	if got := received(); len(got) != 1 || got[0]["title"] != "Backup alert test" {
		t.Fatalf("receiver got %v", got)
	}

	// Only a real, usable channel joins the route.
	if out, err := run(cli("settings", "set", "--channel", "nch_nope")...); err == nil || !strings.Contains(out, "not a notification channel") {
		t.Fatalf("an unknown channel was accepted:\n%s", out)
	}
	if out := must(cli("settings", "set", "--channel", ch.ID)...); !strings.Contains(out, "Also tell:    Webhook 127.0.0.1:") || !strings.Contains(out, "no alert sent yet") {
		t.Fatalf("settings set --channel:\n%s", out)
	}

	// A run whose off-site copy fails raises an incident; the route hears it.
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	must(cli("recipients", "add", "--name", "ops", "--public-key", id.Recipient().String())...)
	secret := filepath.Join(tmp, "s3.secret")
	if err := os.WriteFile(secret, []byte("s3-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw = must(cli("destinations", "add", "--name", "broken", "--endpoint", broken.URL, "--bucket", "backups", "--access-key-id", "AKID",
		"--secret-file", secret, "--path-style", "--allow-private-network", "--skip-test", "--format", "json")...)
	var dest struct {
		Destination struct {
			ID string `json:"id"`
		} `json:"destination"`
	}
	if err := json.Unmarshal([]byte(raw), &dest); err != nil || dest.Destination.ID == "" {
		t.Fatalf("destinations add: %v\n%s", err, raw)
	}
	raw = must(cli("plans", "create", "--preset", "workspace", "--name", "Nightly", "--workspace", "people", "--recipient", id.Recipient().String(),
		"--destination", "local", "--destination", dest.Destination.ID, "--format", "json")...)
	var plan struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &plan); err != nil || plan.ID == "" {
		t.Fatalf("plan: %v\n%s", err, raw)
	}
	must(cli("run", "--plan", plan.ID, "--wait")...)
	deadline := time.Now().Add(20 * time.Second)
	var alert map[string]any
	for alert == nil && time.Now().Before(deadline) {
		for _, p := range received() {
			if p["title"] == "Backup needs attention" {
				alert = p
			}
		}
		if alert == nil {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if alert == nil {
		t.Fatalf("no backup alert reached the channel; got %v", received())
	}
	body, _ := alert["body"].(string)
	for _, want := range []string{"Plan: Nightly", "no off-site copy", "Last good backup: "} {
		if !strings.Contains(body, want) {
			t.Fatalf("alert body lacks %q:\n%s", want, body)
		}
	}
	if alert["url"] != "https://crewship.example.com/admin?tab=backups&section=overview" {
		t.Fatalf("alert url = %v", alert["url"])
	}
	if out := must(cli("settings", "get")...); !strings.Contains(out, "last alert sent") {
		t.Fatalf("settings get after the alert:\n%s", out)
	}
}
