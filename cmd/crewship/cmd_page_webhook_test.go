package main

// cmd_page_webhook_test.go — the acceptance test for `crewship page webhook
// create|list|revoke` (docs/prd/pages.md §10b.5c).
//
// Epic #1935. The endpoint half is proved in internal/api/pages_webhooks_test.go;
// this file proves the client half, which only the CLI can:
//
//   - create sends the PANEL — a token is bound to one panel, so a create that
//     forgot it would mint nothing or, worse, something page-wide;
//   - the token is printed once, with the URL the SERVER returned rather than
//     one the CLI composed, and the output says it will not be shown again;
//   - list never prints a token, because there is none to print;
//   - revoke names the token in the path, and refuses locally when it was not
//     given one — a revoke that guesses is the worst possible emergency verb.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const pageWebhookSlug = "uzaverka"

var (
	pageWebhookRoute   = "/api/v1/pages/" + pageWebhookSlug + "/webhooks"
	pageWebhookID      = "pgwh_c0ffee"
	pageWebhookRevoke  = pageWebhookRoute + "/" + pageWebhookID
	pageWebhookExample = "pgw_" + strings.Repeat("ab", 32)
)

// runPageWebhookCLI is runPageCLI with this surface's flags reset first.
//
// The command tree is package-level state and cobra keeps a flag's value
// between Execute calls, so `--panel cron` in one invocation is still set
// during the next — which is exactly what the "--panel is required" test would
// then fail to observe. Production is one process per invocation and never sees
// it; the test has to put the flags back.
func runPageWebhookCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	page := findSubcommand(rootCmd.Commands(), "page")
	if page != nil {
		if webhook := findSubcommand(page.Commands(), "webhook"); webhook != nil {
			for _, sub := range webhook.Commands() {
				sub.Flags().VisitAll(func(f *pflag.Flag) {
					_ = f.Value.Set(f.DefValue)
					f.Changed = false
				})
			}
		}
	}
	return runPageCLI(t, "", args...)
}

func pageWebhookCreatedBody() []byte {
	return []byte(`{
		"id": "` + pageWebhookID + `",
		"panel": "cron",
		"name": "PLC hall 2",
		"token": "` + pageWebhookExample + `",
		"url": "/api/v1/page-webhooks/` + pageWebhookExample + `",
		"created_by": "ada@example.com",
		"created_at": "2026-08-13T09:14:22Z",
		"fire_count": 0,
		"live": true
	}`)
}

// TestPageCLI_WebhookCreateSendsThePanelAndShowsTheTokenOnce — the create body
// carries the panel (§10b.5c: bound to exactly one panel) and nothing the
// server decides for itself, and the output is the one moment the token exists
// outside the sender's secret store.
func TestPageCLI_WebhookCreateSendsThePanelAndShowsTheTokenOnce(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost(pageWebhookRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusCreated, pageWebhookCreatedBody(), "application/json"
	})

	out, err := runPageWebhookCLI(t, "page", "webhook", "create", pageWebhookSlug,
		"--panel", "cron", "--name", "PLC hall 2")
	if err != nil {
		t.Fatalf("page webhook create: %v\n%s", err, out)
	}

	calls := stub.CallsFor("POST", pageWebhookRoute)
	if len(calls) != 1 {
		t.Fatalf("POST %s called %d times, want 1", pageWebhookRoute, len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, string(calls[0].Body))
	}
	if got, _ := body["panel"].(string); got != "cron" {
		t.Errorf("body panel = %v, want cron — a token with no panel is a token bound to nothing", body["panel"])
	}
	if got, _ := body["name"].(string); got != "PLC hall 2" {
		t.Errorf("body name = %v, want the label", body["name"])
	}
	// The secret, the issuer and the id are the server's. A client that sent
	// any of them would be a client that could choose one.
	for _, forbidden := range []string{"token", "id", "created_by", "created_by_user_id", "rate_limit_per_min"} {
		if _, present := body[forbidden]; present {
			t.Errorf("the request body carries %q; that is the server's to decide", forbidden)
		}
	}

	if !strings.Contains(out, pageWebhookExample) {
		t.Errorf("the token was not printed — this is the only time it can be:\n%s", out)
	}
	if !strings.Contains(out, "/api/v1/page-webhooks/"+pageWebhookExample) {
		t.Errorf("the inbound URL is not the one the server returned:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "only time") {
		t.Errorf("the output does not warn that the token will not be shown again:\n%s", out)
	}
	if !strings.Contains(out, pageWebhookID) {
		t.Errorf("the output does not name the webhook id, so it cannot be revoked from it:\n%s", out)
	}
}

// TestPageCLI_WebhookCreateRefusesWithoutAPanel — refused LOCALLY, before a
// request is sent. The panel is the token's whole blast radius, and a create
// that omits it is a mistake worth catching in the terminal.
func TestPageCLI_WebhookCreateRefusesWithoutAPanel(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost(pageWebhookRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusCreated, pageWebhookCreatedBody(), "application/json"
	})

	out, err := runPageWebhookCLI(t, "page", "webhook", "create", pageWebhookSlug)
	if err == nil {
		t.Fatalf("create with no --panel succeeded:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--panel") {
		t.Errorf("the refusal does not name the missing flag: %v", err)
	}
	if n := len(stub.CallsFor("POST", pageWebhookRoute)); n != 0 {
		t.Errorf("%d request(s) were sent for an invocation that could not be valid", n)
	}
}

// TestPageCLI_WebhookListShowsStatusAndNeverAToken — the listing is what an
// operator reads before revoking something, and the one thing it cannot contain
// is the secret: the column holds a digest.
func TestPageCLI_WebhookListShowsStatusAndNeverAToken(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet(pageWebhookRoute, clitest.JSONResponse(200, map[string]any{
		"page": pageWebhookSlug,
		"webhooks": []map[string]any{
			{"id": pageWebhookID, "panel": "cron", "name": "PLC hall 2",
				"created_by": "ada@example.com", "created_at": "2026-08-13T09:14:22Z",
				"last_fired_at": "2026-08-13T10:00:00Z", "fire_count": 41, "live": true},
			{"id": "pgwh_dead", "panel": "cron", "name": "old zapier",
				"created_by": "ada@example.com", "created_at": "2026-08-01T09:14:22Z",
				"revoked_at": "2026-08-12T09:14:22Z", "fire_count": 3, "live": false},
		},
	}))

	out, err := runPageWebhookCLI(t, "page", "webhook", "list", pageWebhookSlug)
	if err != nil {
		t.Fatalf("page webhook list: %v\n%s", err, out)
	}
	for _, want := range []string{pageWebhookID, "cron", "live", "revoked", "PLC hall 2", "41"} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing does not show %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "pgw_") {
		t.Errorf("the listing printed something token-shaped; tokens are stored hashed and cannot be shown:\n%s", out)
	}
	// The revoked row survives: "was it used after we pulled it" is the
	// question an incident asks.
	if !strings.Contains(out, "pgwh_dead") {
		t.Errorf("the revoked token is missing from the listing:\n%s", out)
	}
}

// TestPageCLI_WebhookRevokeNamesTheTokenInThePath — and refuses locally when it
// was not given one.
func TestPageCLI_WebhookRevokeNamesTheTokenInThePath(t *testing.T) {
	stub := pageStub(t)
	stub.OnDelete(pageWebhookRevoke, clitest.JSONResponse(200, map[string]any{
		"id": pageWebhookID, "revoked": true,
	}))

	out, err := runPageWebhookCLI(t, "page", "webhook", "revoke", pageWebhookSlug, "--id", pageWebhookID, "--yes")
	if err != nil {
		t.Fatalf("page webhook revoke: %v\n%s", err, out)
	}
	if n := len(stub.CallsFor("DELETE", pageWebhookRevoke)); n != 1 {
		t.Fatalf("DELETE %s called %d times, want 1", pageWebhookRevoke, n)
	}
	if !strings.Contains(out, "Revoked") {
		t.Errorf("the output does not confirm the revoke:\n%s", out)
	}

	stub.ResetCalls()
	out, err = runPageWebhookCLI(t, "page", "webhook", "revoke", pageWebhookSlug, "--yes")
	if err == nil {
		t.Fatalf("revoke with no --id succeeded:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--id") {
		t.Errorf("the refusal does not name the missing flag: %v", err)
	}
	if n := len(stub.CallsFor("DELETE", pageWebhookRevoke)); n != 0 {
		t.Errorf("%d request(s) were sent for a revoke with no target", n)
	}
}
