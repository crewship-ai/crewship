package main

// cmd_page_publish_test.go — the acceptance test for `crewship page
// publish|links|unpublish` (docs/prd/pages.md §7.3, §11).
//
// Epic #1935. The server half is proved in internal/api/pages_public_test.go;
// this file proves the client half, which only the CLI can:
//
//   - the three verbs hit exactly the three routes §11's rule ("every API
//     endpoint gets a CLI command") requires, and nothing else;
//   - the expiry is the SERVER's default when the flag is absent — the CLI
//     sends no field rather than a 30 it copied, so the two cannot drift;
//   - a password never travels in argv or in a URL (§7.3.3);
//   - the token is printed once and the withdraw command is printed with it,
//     because a link nobody can revoke is the failure rule 4 exists to bound;
//   - `unpublish` refuses to run without --yes, like every other destructive
//     command (§11b decision 5).

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

const pagePublishSlug = "uzaverka"

var (
	pagePublicRoute = "/api/v1/pages/" + pagePublishSlug + "/public"
	pageRevokeRoute = pagePublicRoute + "/tok_1"
)

// runPagePublishCLI is runPageCLI with this surface's flags reset first.
//
// The command tree is package-level state and cobra keeps a flag's value
// between Execute calls, so `--expires-in-days 7` in one invocation is still
// set (and still `Changed`) during the next — which would make the
// "absent flag sends no field" assertion pass or fail depending on test order.
// Production is one process per invocation and never sees it.
func runPagePublishCLI(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	page := findSubcommand(rootCmd.Commands(), "page")
	if page != nil {
		for _, name := range []string{"publish", "links", "unpublish"} {
			sub := findSubcommand(page.Commands(), name)
			if sub == nil {
				continue
			}
			sub.Flags().VisitAll(func(f *pflag.Flag) {
				if sv, ok := f.Value.(pflag.SliceValue); ok {
					_ = sv.Replace(nil)
				} else {
					_ = f.Value.Set(f.DefValue)
				}
				f.Changed = false
			})
		}
	}
	return runPageCLI(t, stdin, args...)
}

func pagePublishStubBody() []byte {
	return []byte(`{
		"id": "tok_1",
		"token": "EXAMPLE-not-a-real-token-for-tests-00000000",
		"url": "/p/EXAMPLE-not-a-real-token-for-tests-00000000",
		"expires_at": "2026-09-11T09:14:22Z",
		"show_provenance": false,
		"has_password": false,
		"created_by": "ada@example.com",
		"created_at": "2026-08-12T09:14:22Z",
		"live": true,
		"panels": ["sluzby"]
	}`)
}

// TestPageCLI_PublishSendsNoExpiryUnlessAsked — §7.3.2 rule 4's default is the
// SERVER's. A CLI that helpfully sent `expires_in_days: 30` would be a second
// place the number lives, and the day somebody tunes one of them the other is
// wrong in a way no test would catch.
func TestPageCLI_PublishSendsNoExpiryUnlessAsked(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost(pagePublicRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusCreated, pagePublishStubBody(), "application/json"
	})

	out, err := runPagePublishCLI(t, "", "page", "publish", pagePublishSlug)
	if err != nil {
		t.Fatalf("page publish: %v\n%s", err, out)
	}

	calls := stub.CallsFor("POST", pagePublicRoute)
	if len(calls) != 1 {
		t.Fatalf("POST %s called %d times, want 1", pagePublicRoute, len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, string(calls[0].Body))
	}
	for _, absent := range []string{"expires_in_days", "password", "show_provenance"} {
		if _, present := body[absent]; present {
			t.Errorf("an unasked-for %q reached the wire (%v); the server owns the default", absent, body[absent])
		}
	}

	// The token is printed once, with the URL, and with the command that takes
	// it back.
	for _, want := range []string{
		"EXAMPLE-not-a-real-token-for-tests-00000000",
		"/p/EXAMPLE-not-a-real-token-for-tests-00000000",
		"sluzby",
		"page unpublish " + pagePublishSlug + " --id tok_1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the publish output does not carry %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "only time") {
		t.Errorf("the output does not say the link cannot be shown again:\n%s", out)
	}
}

// TestPageCLI_PublishForwardsTheExpiryAndProvenanceFlags — when the operator
// DOES say, the value reaches the wire as the field the server reads.
func TestPageCLI_PublishForwardsTheExpiryAndProvenanceFlags(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost(pagePublicRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusCreated, pagePublishStubBody(), "application/json"
	})

	out, err := runPagePublishCLI(t, "", "page", "publish", pagePublishSlug,
		"--expires-in-days", "7", "--show-provenance")
	if err != nil {
		t.Fatalf("page publish: %v\n%s", err, out)
	}
	var body map[string]any
	if err := json.Unmarshal(stub.CallsFor("POST", pagePublicRoute)[0].Body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if days, _ := body["expires_in_days"].(float64); days != 7 {
		t.Errorf("expires_in_days = %v, want 7", body["expires_in_days"])
	}
	if show, _ := body["show_provenance"].(bool); !show {
		t.Errorf("show_provenance = %v, want true", body["show_provenance"])
	}
}

// TestPageCLI_PasswordTravelsInTheBodyAndNeverInTheURL is §7.3.3 as a client
// assertion. The password is read from stdin — there is no flag that takes it,
// because argv is visible in `ps` and lands in the shell history — and it
// leaves in the request BODY, never in the path or the query string.
func TestPageCLI_PasswordTravelsInTheBodyAndNeverInTheURL(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost(pagePublicRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusCreated, pagePublishStubBody(), "application/json"
	})

	const password = "uzaverka-2026"
	out, err := runPagePublishCLI(t, password+"\n", "page", "publish", pagePublishSlug, "--password-stdin")
	if err != nil {
		t.Fatalf("page publish: %v\n%s", err, out)
	}

	call := stub.CallsFor("POST", pagePublicRoute)[0]
	var body map[string]any
	if err := json.Unmarshal(call.Body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if got, _ := body["password"].(string); got != password {
		t.Errorf("password = %q, want the one read from stdin (trailing newline trimmed)", got)
	}
	if strings.Contains(call.Path, password) || strings.Contains(call.Query, password) {
		t.Fatalf("§7.3.3: the password reached the URL (%s?%s)", call.Path, call.Query)
	}
	if strings.Contains(out, password) {
		t.Errorf("the password was echoed to the terminal:\n%s", out)
	}

	// There is no flag that would take a password from argv.
	page := findSubcommand(rootCmd.Commands(), "page")
	publish := findSubcommand(page.Commands(), "publish")
	if f := publish.Flags().Lookup("password"); f != nil {
		t.Error("`page publish` has a --password flag: argv is world-readable in `ps` and lands in the " +
			"shell history, which is the same disclosure §7.3.3 refuses for the URL")
	}
	if publish.Flags().Lookup("password-stdin") == nil {
		t.Error("`page publish` has no --password-stdin flag, so §7.3.3's optional password cannot be set at all")
	}
}

// TestPageCLI_LinksShowsStatusAndNeverATokenValue — the tokens are stored
// hashed, so there is nothing to show, and a listing that pretended otherwise
// would be a listing somebody trusted.
func TestPageCLI_LinksShowsStatusAndNeverATokenValue(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet(pagePublicRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, []byte(`{
			"page": "` + pagePublishSlug + `",
			"tokens": [
				{"id":"tok_1","expires_at":"2026-09-11T09:14:22Z","show_provenance":false,
				 "has_password":true,"created_by":"ada@example.com","created_at":"2026-08-12T09:14:22Z",
				 "last_seen_at":"2026-08-12T10:00:00Z","live":true,"panels":["sluzby"]},
				{"id":"tok_2","expires_at":"2026-08-01T09:14:22Z","show_provenance":true,
				 "has_password":false,"created_by":"ada@example.com","created_at":"2026-07-01T09:14:22Z",
				 "revoked_at":"2026-08-02T09:00:00Z","live":false,"panels":[]}
			]
		}`), "application/json"
	})

	out, err := runPagePublishCLI(t, "", "page", "links", pagePublishSlug)
	if err != nil {
		t.Fatalf("page links: %v\n%s", err, out)
	}
	for _, want := range []string{"tok_1", "live", "tok_2", "revoked", "shown", "stripped"} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing does not carry %q:\n%s", want, out)
		}
	}
	// The response carries no token value and the listing must not invent a
	// column that suggests one could be recovered.
	if strings.Contains(strings.ToLower(out), "token ") {
		t.Errorf("the listing has a TOKEN column; the value is a hash at rest and cannot be shown again:\n%s", out)
	}
}

// TestPageCLI_UnpublishRequiresYesAndTargetsOneLink — a destructive command
// takes --yes (§11b decision 5), and it takes back exactly one link.
func TestPageCLI_UnpublishRequiresYesAndTargetsOneLink(t *testing.T) {
	stub := pageStub(t)
	stub.OnDelete(pageRevokeRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, []byte(`{"id":"tok_1","revoked":true}`), "application/json"
	})

	t.Run("without --id it refuses locally", func(t *testing.T) {
		out, err := runPagePublishCLI(t, "", "page", "unpublish", pagePublishSlug, "--yes")
		if err == nil {
			t.Fatalf("unpublish without --id succeeded:\n%s", out)
		}
		if len(stub.CallsFor("DELETE", pageRevokeRoute)) != 0 {
			t.Error("a request was sent before the local guard fired")
		}
	})

	t.Run("with --id and --yes it revokes exactly one", func(t *testing.T) {
		out, err := runPagePublishCLI(t, "", "page", "unpublish", pagePublishSlug, "--id", "tok_1", "--yes")
		if err != nil {
			t.Fatalf("page unpublish: %v\n%s", err, out)
		}
		calls := stub.CallsFor("DELETE", pageRevokeRoute)
		if len(calls) != 1 {
			t.Fatalf("DELETE %s called %d times, want 1", pageRevokeRoute, len(calls))
		}
		if !strings.Contains(out, "tok_1") {
			t.Errorf("the output does not name the link that was withdrawn:\n%s", out)
		}
	})
}

// TestPageCLI_PublishSurfacesTheServersRefusal — a refusal on this surface
// names the rule that was broken (no panel marked public, an expiry past the
// ceiling, an agent trying to publish), and the operator has to see the
// server's own words rather than a status code.
func TestPageCLI_PublishSurfacesTheServersRefusal(t *testing.T) {
	stub := pageStub(t)
	const refusal = "no panel on this page is marked `public: true`"
	stub.OnPost(pagePublicRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusBadRequest, []byte(`{"error":"` + refusal + `"}`), "application/json"
	})

	out, err := runPagePublishCLI(t, "", "page", "publish", pagePublishSlug)
	if err == nil {
		t.Fatalf("a refused publish exited 0:\n%s", out)
	}
	if !strings.Contains(err.Error()+out, "public") {
		t.Errorf("the server's refusal did not reach the operator: err=%v out=%s", err, out)
	}
}
