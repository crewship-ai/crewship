package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Exercise the actual CLI, registered auth/router, channel store and encrypted
// persistence. A mocked successful POST would miss the second URL validator
// that rejected every query-host Teams URL after field composition (#5434).
func TestAcceptance_NotifyChannelTeams_CreateThroughCLI(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "")
	db := testutil.MigratedSQLDB(t)
	const workspace = "cteamschannelworkspace001"
	const token = "crewship_cli_teams_synthetic_owner_token"
	for _, query := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cteamschannelworkspace001','Teams test','teams-test')`,
		`INSERT INTO users(id,email) VALUES('teams-owner','teams-owner@example.test')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('teams-member','cteamschannelworkspace001','teams-owner','OWNER')`,
	} {
		if _, err := db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO cli_tokens(id,user_id,name,token_hash) VALUES('teams-token','teams-owner','test',?)`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithStoragePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+workspace+"\ntoken: "+token+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	const signature = "EXAMPLE-not-a-real-signature"
	workflows := []string{
		"https://example.environment.api.powerplatform.com/powerautomate/automations/direct/workflows/example/triggers/manual/paths/invoke?sig=" + signature,
		"https://example.logic.azure.com/workflows/example/triggers/manual/paths/invoke?api-version=1&sig=" + signature,
	}
	created := 0
	for _, tc := range []struct {
		name, webhook string
		raw           bool
	}{
		{"power-platform-fields", workflows[0], false},
		{"logic-apps-fields", workflows[1], false},
		{"raw-service-url", workflows[0], true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"notifychannel", "add", "--type", "chat", "--provider", "teams"}
			if tc.raw {
				args = append(args, "--url", "teams://?host="+url.QueryEscape(tc.webhook))
			} else {
				args = append(args, "--field", "webhook_url="+tc.webhook)
			}
			out, err := runFeedbackCLI(t, cfg, args...)
			if err != nil {
				t.Fatalf("create signed Teams channel through CLI: %v\n%s", err, out)
			}
			var channel notifyChannelCreateResult
			if err := json.Unmarshal([]byte(out), &channel); err != nil {
				t.Fatalf("create JSON: %v\n%s", err, out)
			}
			if channel.ID == "" || channel.Provider != "teams" || channel.Secret == "" {
				t.Fatalf("missing channel/one-time secret: %+v", channel)
			}
			parsed, err := url.Parse(channel.Secret)
			if err != nil || parsed.Query().Get("host") != tc.webhook {
				t.Fatalf("signed workflow URL changed: %v", err)
			}
			var encrypted string
			if err := db.QueryRowContext(t.Context(), `SELECT secret_enc FROM notification_channels WHERE id=?`, channel.ID).Scan(&encrypted); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(encrypted, signature) || encrypted == channel.Secret {
				t.Fatal("workflow URL was persisted in plaintext")
			}
			decrypted, err := encryption.Decrypt(encrypted)
			if err != nil || decrypted != channel.Secret {
				t.Fatalf("encrypted URL did not round trip: %v", err)
			}
			created++
		})
	}
	// Both field composition and the scripting/raw URL path must fail closed.
	for _, tc := range []struct{ name, value, flag string }{
		{"http-fields", "webhook_url=http://example.logic.azure.com/workflows/example?sig=" + signature, "--field"},
		{"unsigned-fields", "webhook_url=https://example.logic.azure.com/workflows/example", "--field"},
		{"untrusted-fields", "webhook_url=https://example.test/workflows/example?sig=" + signature, "--field"},
		{"http-raw", "teams://?host=" + url.QueryEscape("http://example.logic.azure.com/workflows/example?sig="+signature), "--url"},
		{"unsigned-raw", "teams://?host=" + url.QueryEscape("https://example.logic.azure.com/workflows/example"), "--url"},
		{"untrusted-raw", "teams://?host=" + url.QueryEscape("https://example.test/workflows/example?sig="+signature), "--url"},
		{"provider-mismatch", "ntfy://ntfy.sh/example", "--url"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runFeedbackCLI(t, cfg, "notifychannel", "add", "--type", "chat", "--provider", "teams", tc.flag, tc.value)
			if err == nil {
				t.Fatalf("invalid Teams channel accepted: %s", out)
			}
			if strings.Contains(out, signature) {
				t.Fatal("validation error leaked the workflow signature")
			}
		})
	}
	out, err := runFeedbackCLI(t, cfg, "notifychannel", "list")
	if err != nil {
		t.Fatalf("list via CLI: %v\n%s", err, out)
	}
	if strings.Contains(out, signature) || strings.Contains(out, "teams://") {
		t.Fatal("CLI list leaked the workflow URL")
	}
	var channels []NotifyChannelRow
	if err := json.Unmarshal([]byte(out), &channels); err != nil || len(channels) != created {
		t.Fatalf("list omitted created channels: count=%d want=%d err=%v", len(channels), created, err)
	}
	req, err := http.NewRequestWithContext(t.Context(), "GET", srv.URL+"/api/v1/notification-channels", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Workspace-ID", workspace)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("API list: status=%d err=%v body=%s", res.StatusCode, err, body)
	}
	if strings.Contains(string(body), signature) || strings.Contains(string(body), "teams://") || strings.Contains(string(body), "\"secret\"") {
		t.Fatal("API list leaked the workflow URL")
	}
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM notification_channels WHERE workspace_id=?`, workspace).Scan(&count); err != nil || count != created {
		t.Fatalf("rejected requests changed persistence: count=%d want=%d err=%v", count, created, err)
	}
}
