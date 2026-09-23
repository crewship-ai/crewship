package main

// Acceptance for the GitHub ingress profile and `routine webhooks fire`,
// driven through the BUILT BINARY against the REAL api router (#2580).
//
// The dispatch routes take no CLI token: the only credential is the signing
// secret, so a stub cannot prove a delivery the CLI signs is one the server
// accepts. Everything here goes over the wire — the endpoint is created by
// the CLI, the delivery is signed by the CLI, the receipt is read by the CLI.

import (
	"encoding/json"
	"strings"
	"testing"
)

type webhookFireAcceptanceOut struct {
	HTTPStatus       int    `json:"http_status"`
	Profile          string `json:"profile"`
	SourceDeliveryID string `json:"source_delivery_id"`
	Status           string `json:"status"`
	RunID            string `json:"run_id"`
	DeliveryID       string `json:"delivery_id"`
	Duplicate        bool   `json:"duplicate"`
	Reason           string `json:"reason"`
}

func decodeWebhookFire(t *testing.T, out string) webhookFireAcceptanceOut {
	t.Helper()
	var got webhookFireAcceptanceOut
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode fire output: %v\nraw: %s", err, out)
	}
	return got
}

// TestAcceptance_RoutineWebhooksFire_GitHubProfile creates a GitHub-profile
// endpoint with `create --ingress-profile github`, fires a pull_request
// payload at its /github-pull-request URL with the secret the create
// response revealed, and reads the receipt back by the delivery id the CLI
// sent. A wrong secret is refused, a ping is acknowledged without a run, and
// a redelivery answers the original run.
func TestAcceptance_RoutineWebhooksFire_GitHubProfile(t *testing.T) {
	cfgPath, serverURL := startReliabilityAcceptanceServer(t)

	if out, err := runReliabilityCLI(t, cfgPath, "routine", "webhooks", "create",
		"--slug", "rel-routine", "--ingress-profile", "bitbucket"); err == nil {
		t.Fatalf("an unknown ingress profile must be refused client-side, got:\n%s", out)
	} else if !strings.Contains(out, "--ingress-profile must be crewship, github or unsigned") {
		t.Fatalf("unknown profile refused without naming the enum:\n%s", out)
	}

	createOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "create",
		"--slug", "rel-routine", "--name", "gh-prs", "--ingress-profile", "github", "--base-url", serverURL)
	if err != nil {
		t.Fatalf("routine webhooks create --ingress-profile github failed: %v\n%s", err, createOut)
	}
	var created struct {
		ID             string `json:"id"`
		IngressProfile string `json:"ingress_profile"`
		Token          string `json:"token"`
		PublicURL      string `json:"public_url"`
		SigningSecret  string `json:"signing_secret"`
	}
	if err := json.Unmarshal([]byte(createOut), &created); err != nil {
		t.Fatalf("decode create output: %v\nraw: %s", err, createOut)
	}
	if created.IngressProfile != "github" {
		t.Fatalf("server stored profile %q, want github:\n%s", created.IngressProfile, createOut)
	}
	if !strings.HasSuffix(created.PublicURL, "/api/v1/webhooks/"+created.Token+"/github-pull-request") {
		t.Fatalf("public URL for a github endpoint must carry the /github-pull-request suffix: %s", created.PublicURL)
	}
	if created.SigningSecret == "" {
		t.Fatalf("create did not reveal the signing secret:\n%s", createOut)
	}

	listOut, err := runReliabilityCLI(t, cfgPath, "routine", "webhooks", "list")
	if err != nil {
		t.Fatalf("routine webhooks list failed: %v\n%s", err, listOut)
	}
	if !strings.Contains(listOut, "PROFILE") || !strings.Contains(listOut, "github") {
		t.Fatalf("the list table must show the ingress profile:\n%s", listOut)
	}

	prBody := `{"action":"opened","pull_request":{"number":42},"repository":{"full_name":"example/project"}}`

	if out, err := runReliabilityCLI(t, cfgPath, "routine", "webhooks", "fire", created.PublicURL,
		"--secret", "not-the-secret", "--body", prBody); err == nil {
		t.Fatalf("a delivery signed with the wrong secret must fail, got:\n%s", out)
	} else if !strings.Contains(out, "401") {
		t.Fatalf("wrong secret should surface the server's 401:\n%s", out)
	}

	pingOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "fire", created.PublicURL,
		"--secret", created.SigningSecret, "--body", `{"zen":"keep it logically awesome"}`, "--event", "ping")
	if err != nil {
		t.Fatalf("ping fire failed: %v\n%s", err, pingOut)
	}
	if ping := decodeWebhookFire(t, pingOut); ping.HTTPStatus != 200 || ping.Status != "IGNORED" || ping.Reason != "ping" || ping.RunID != "" {
		t.Fatalf("ping must be acknowledged without a run: %+v", ping)
	}

	// A bare token + --base-url + --profile github: the CLI supplies the
	// suffix, so the delivery lands on the GitHub verifier.
	fireOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "fire", created.Token,
		"--base-url", serverURL, "--profile", "github", "--secret", created.SigningSecret,
		"--body", prBody, "--delivery-id", "gh-acc-0001")
	if err != nil {
		t.Fatalf("pull_request fire failed: %v\n%s", err, fireOut)
	}
	first := decodeWebhookFire(t, fireOut)
	if first.HTTPStatus != 202 || first.Status != "PENDING" || first.Duplicate || first.RunID == "" || first.DeliveryID == "" {
		t.Fatalf("first delivery must be accepted with a run and a receipt: %+v", first)
	}
	if first.Profile != "github" || first.SourceDeliveryID != "github:gh-acc-0001" {
		t.Fatalf("the CLI must report the receipt identity it sent under: %+v", first)
	}
	if strings.Contains(fireOut, created.SigningSecret) || strings.Contains(fireOut, created.Token) {
		t.Fatalf("fire output must not echo the secret or the token:\n%s", fireOut)
	}

	againOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "fire", created.PublicURL,
		"--secret", created.SigningSecret, "--body", prBody, "--delivery-id", "gh-acc-0001")
	if err != nil {
		t.Fatalf("redelivery failed: %v\n%s", err, againOut)
	}
	if again := decodeWebhookFire(t, againOut); !again.Duplicate || again.RunID != first.RunID || again.DeliveryID != first.DeliveryID {
		t.Fatalf("a redelivery must answer the original run and receipt: first=%+v again=%+v", first, again)
	}

	if out, err := runReliabilityCLI(t, cfgPath, "routine", "webhooks", "fire", created.PublicURL,
		"--secret", created.SigningSecret, "--body", `{"action":"opened","pull_request":{"number":99}}`, "--delivery-id", "gh-acc-0001"); err == nil {
		t.Fatalf("the same delivery id with a different body must conflict, got:\n%s", out)
	} else if !strings.Contains(out, "409") {
		t.Fatalf("body conflict should surface the server's 409:\n%s", out)
	}

	receiptsOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "receipts", "list",
		"--webhook", created.ID, "--source-id", first.SourceDeliveryID)
	if err != nil {
		t.Fatalf("receipts list failed: %v\n%s", err, receiptsOut)
	}
	if !strings.Contains(receiptsOut, first.DeliveryID) || !strings.Contains(receiptsOut, first.RunID) {
		t.Fatalf("the receipt the fire reported must be readable by its identity:\n%s", receiptsOut)
	}
	getOut, err := runReliabilityCLI(t, cfgPath, "routine", "webhooks", "receipts", "get", first.DeliveryID)
	if err != nil {
		t.Fatalf("receipts get failed: %v\n%s", err, getOut)
	}
	if !strings.Contains(getOut, "github") {
		t.Fatalf("the receipt must record the github profile:\n%s", getOut)
	}
}

// TestAcceptance_RoutineWebhooksFire_CrewshipProfile is the default profile
// end to end: no --ingress-profile, no suffix, X-Crewship-Signature — both
// the body-only and the timestamped scheme — and the receipt found by the
// X-Crewship-Event-ID the CLI sent.
func TestAcceptance_RoutineWebhooksFire_CrewshipProfile(t *testing.T) {
	cfgPath, serverURL := startReliabilityAcceptanceServer(t)

	createOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "create",
		"--slug", "rel-routine", "--base-url", serverURL)
	if err != nil {
		t.Fatalf("routine webhooks create failed: %v\n%s", err, createOut)
	}
	var created struct {
		ID             string `json:"id"`
		IngressProfile string `json:"ingress_profile"`
		PublicURL      string `json:"public_url"`
		SigningSecret  string `json:"signing_secret"`
	}
	if err := json.Unmarshal([]byte(createOut), &created); err != nil {
		t.Fatalf("decode create output: %v\nraw: %s", err, createOut)
	}
	if created.IngressProfile != "crewship" || strings.HasSuffix(created.PublicURL, "/github-pull-request") {
		t.Fatalf("default profile must be crewship with a suffix-less URL:\n%s", createOut)
	}

	fireOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "fire", created.PublicURL,
		"--secret", created.SigningSecret, "--body", `{"event":"deploy"}`, "--delivery-id", "evt-acc-0001")
	if err != nil {
		t.Fatalf("body-only fire failed: %v\n%s", err, fireOut)
	}
	first := decodeWebhookFire(t, fireOut)
	if first.HTTPStatus != 202 || first.Profile != "crewship" || first.SourceDeliveryID != "evt-acc-0001" || first.RunID == "" {
		t.Fatalf("body-only delivery not accepted as expected: %+v", first)
	}

	tsOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "fire", created.PublicURL,
		"--secret", created.SigningSecret, "--body", `{"event":"deploy-2"}`, "--timestamp")
	if err != nil {
		t.Fatalf("timestamped fire failed: %v\n%s", err, tsOut)
	}
	if ts := decodeWebhookFire(t, tsOut); ts.HTTPStatus != 202 || ts.Duplicate || ts.RunID == first.RunID || !strings.HasPrefix(ts.SourceDeliveryID, "cli-") {
		t.Fatalf("timestamped delivery must be a fresh accepted run under a generated id: %+v", ts)
	}

	// A crewship endpoint fired under the GitHub suffix is a profile
	// mismatch the server answers 404 — the CLI must not paper over it.
	if out, err := runReliabilityCLI(t, cfgPath, "routine", "webhooks", "fire", created.PublicURL,
		"--profile", "github", "--secret", created.SigningSecret, "--body", `{"action":"opened","pull_request":{"number":1}}`); err == nil {
		t.Fatalf("firing a crewship endpoint as github must fail, got:\n%s", out)
	} else if !strings.Contains(out, "404") {
		t.Fatalf("profile mismatch should surface the server's 404:\n%s", out)
	}

	receiptsOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "receipts", "list",
		"--webhook", created.ID, "--source-id", "evt-acc-0001")
	if err != nil {
		t.Fatalf("receipts list failed: %v\n%s", err, receiptsOut)
	}
	if !strings.Contains(receiptsOut, first.DeliveryID) {
		t.Fatalf("the receipt must be found by the event id the CLI sent:\n%s", receiptsOut)
	}
}
