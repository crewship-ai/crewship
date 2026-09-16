package main

// `routine webhooks fire` — send one signed delivery to a webhook's public
// URL, the way the sender the endpoint was made for would.
//
// The dispatch routes (POST /api/v1/webhooks/{token} and its
// /github-pull-request sibling) take no session or CLI token: the only
// credential is the signing secret, and the only way to know whether an
// endpoint is wired correctly is to sign a body with that secret and read
// the receipt. Until this command the CLI could create the endpoint but not
// exercise it, so "does my GitHub webhook work" meant hand-rolling the HMAC
// with openssl and guessing the header names. The signature schemes below
// mirror what the server verifies — pipeline.Webhook.ValidateSignature /
// ValidateTimestampedSignature for the crewship profile and
// webhook/profiles.verifyGitHub for the github one — and nothing here is
// optional to the server: a delivery this command sends is a delivery the
// endpoint accepts.
//
// The token is hashed at rest, so the argument is the public URL (or the
// bare token) the create response showed, never a webhook id; the secret
// likewise comes from the create / --rotate-secret reveal. Neither is
// printed back.

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// webhookFireResult is `routine webhooks fire`'s machine payload: the HTTP
// status and the dispatch response, plus the receipt identity the delivery
// was sent under so `receipts list --webhook <id> --source-id <it>` can find
// it. The URL and secret are deliberately absent — both are credentials.
type webhookFireResult struct {
	HTTPStatus       int    `json:"http_status" yaml:"http_status"`
	Profile          string `json:"profile" yaml:"profile"`
	SourceDeliveryID string `json:"source_delivery_id,omitempty" yaml:"source_delivery_id,omitempty"`
	Status           string `json:"status" yaml:"status"`
	RunID            string `json:"run_id,omitempty" yaml:"run_id,omitempty"`
	DeliveryID       string `json:"delivery_id,omitempty" yaml:"delivery_id,omitempty"`
	Duplicate        bool   `json:"duplicate" yaml:"duplicate"`
	Reason           string `json:"reason,omitempty" yaml:"reason,omitempty"`
}

var routineWebhooksFireCmd = &cobra.Command{
	Use:   "fire <public_url|token>",
	Short: "Send a signed delivery to a webhook's public URL and print the dispatch receipt",
	Long: `Sign a body with the webhook's secret and POST it to the public dispatch
URL, exactly as the configured sender would. The argument is the public URL
the create response printed (or the bare token; --base-url then supplies the
host). The secret is the one revealed on create or "update --rotate-secret".

The signature follows the endpoint's ingress profile, inferred from the URL:
  crewship  X-Crewship-Signature: sha256=<HMAC-SHA256 of the body>, with
            --timestamp the header X-Crewship-Timestamp is added and the
            signature covers "<timestamp>.<body>" instead. --delivery-id is
            sent as X-Crewship-Event-ID and becomes the receipt's source id.
  github    X-Hub-Signature-256: sha256=<HMAC-SHA256 of the body>, plus
            X-GitHub-Event (--event, default pull_request) and
            X-GitHub-Delivery (--delivery-id). The receipt's source id is
            "github:<delivery id>". Only pull_request opened / synchronize /
            reopened payloads start a run; ping and anything else answer
            200 IGNORED.

The command exits 0 on 200 and 202 and prints the run id, the receipt
(delivery) id and whether the server already held this delivery. Any other
status is the server's error, exit code included: 401 is a wrong secret, 404
an unknown or disabled token (or the wrong profile suffix), 409 the same
delivery id with a different body, 413 an oversized body, 429/503 a retry.

Examples:
  crewship routine webhooks fire https://crewship.example.com/api/v1/webhooks/<token> \
      --secret "$SECRET" --body '{"event":"deploy"}' --delivery-id deploy-2026-09-15
  crewship routine webhooks fire <token>/github-pull-request --base-url https://crewship.example.com \
      --secret "$SECRET" --body @pull_request.json --delivery-id 72d3162e-cc78-11e3-81ab-4c9367dc0958
  crewship routine webhooks receipts list --webhook <webhook_id> --source-id github:72d3162e-...`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		secret, _ := cmd.Flags().GetString("secret")
		if secret == "" {
			return cli.WithExitCode(fmt.Errorf("--secret is required: the signing secret revealed by create or update --rotate-secret"), cli.ExitValidation)
		}
		bodySpec, _ := cmd.Flags().GetString("body")
		if bodySpec == "" {
			return cli.WithExitCode(fmt.Errorf("--body is required (inline text, @file, or - for stdin)"), cli.ExitValidation)
		}
		body, err := pageReadPayload(bodySpec)
		if err != nil {
			return err
		}

		baseURL, _ := cmd.Flags().GetString("base-url")
		if baseURL == "" {
			baseURL = clientBaseURL(newAPIClient())
		}
		target, err := routineWebhookFireURL(args[0], baseURL)
		if err != nil {
			return cli.WithExitCode(err, cli.ExitValidation)
		}
		profile, _ := cmd.Flags().GetString("profile")
		switch profile {
		case "":
			profile = webhookProfileCrewship
			if strings.HasSuffix(target.Path, webhookGitHubURLSuffix) {
				profile = webhookProfileGitHub
			}
		case webhookProfileCrewship, webhookProfileGitHub:
		default:
			return cli.WithExitCode(fmt.Errorf("--profile must be %s or %s", webhookProfileCrewship, webhookProfileGitHub), cli.ExitValidation)
		}
		// The github suffix and the github profile select each other on the
		// server: a bare token asked to fire as github needs the suffix, and
		// a crewship-profile row answers 404 under it. Add the suffix rather
		// than let the user learn that from an "unknown webhook".
		if profile == webhookProfileGitHub && !strings.HasSuffix(target.Path, webhookGitHubURLSuffix) {
			target.Path = strings.TrimRight(target.Path, "/") + webhookGitHubURLSuffix
		}

		deliveryID, _ := cmd.Flags().GetString("delivery-id")
		if deliveryID == "" {
			deliveryID = newWebhookDeliveryID()
		}
		useTimestamp, _ := cmd.Flags().GetBool("timestamp")
		event, _ := cmd.Flags().GetString("event")
		// A flag the other profile cannot honour is a mistake, not a no-op:
		// --timestamp on a github endpoint would silently sign body-only,
		// --event on a crewship endpoint would never be sent.
		if profile == webhookProfileGitHub && useTimestamp {
			return cli.WithExitCode(fmt.Errorf("--timestamp applies to the crewship profile only; the github profile signs the body alone"), cli.ExitValidation)
		}
		if profile == webhookProfileCrewship && cmd.Flags().Changed("event") {
			return cli.WithExitCode(fmt.Errorf("--event applies to the github profile only"), cli.ExitValidation)
		}

		req, err := http.NewRequest(http.MethodPost, target.String(), bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", cli.UserAgent())
		sourceID := deliveryID
		if profile == webhookProfileGitHub {
			// webhook/profiles.verifyGitHub: HMAC over the raw body, the
			// "sha256=" prefix mandatory. Event and delivery id are unsigned
			// headers there too; the server reads the action from the body.
			req.Header.Set("X-Hub-Signature-256", "sha256="+hmacHex(secret, body))
			req.Header.Set("X-GitHub-Event", event)
			req.Header.Set("X-GitHub-Delivery", deliveryID)
			sourceID = "github:" + deliveryID
		} else if useTimestamp {
			// pipeline.Webhook.ValidateTimestampedSignature: "<ts>.<body>",
			// ts in unix seconds, fresh within the server's tolerance.
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			req.Header.Set("X-Crewship-Timestamp", ts)
			req.Header.Set("X-Crewship-Signature", "sha256="+hmacHex(secret, append([]byte(ts+"."), body...)))
			req.Header.Set("X-Crewship-Event-ID", deliveryID)
		} else {
			// pipeline.Webhook.ValidateSignature: HMAC over the body alone.
			req.Header.Set("X-Crewship-Signature", "sha256="+hmacHex(secret, body))
			req.Header.Set("X-Crewship-Event-ID", deliveryID)
		}

		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			return fmt.Errorf("fire webhook: %w", err)
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var answer struct {
			RunID      string `json:"run_id" yaml:"run_id"`
			Status     string `json:"status" yaml:"status"`
			DeliveryID string `json:"delivery_id" yaml:"delivery_id"`
			Duplicate  bool   `json:"duplicate" yaml:"duplicate"`
			Reason     string `json:"reason" yaml:"reason"`
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		if err := json.Unmarshal(raw, &answer); err != nil {
			return fmt.Errorf("webhook answered %d but the body is not the dispatch receipt: %w", resp.StatusCode, err)
		}
		result := webhookFireResult{
			HTTPStatus:       resp.StatusCode,
			Profile:          profile,
			SourceDeliveryID: sourceID,
			Status:           answer.Status,
			RunID:            answer.RunID,
			DeliveryID:       answer.DeliveryID,
			Duplicate:        answer.Duplicate,
			Reason:           answer.Reason,
		}
		return resolvedFormatter(cmd).AutoHuman(result, func() {
			switch {
			case resp.StatusCode == http.StatusOK:
				fmt.Printf("Ignored (%d): %s — no run started.\n", resp.StatusCode, answer.Reason)
			case answer.Duplicate:
				fmt.Printf("Already received (%d): the server holds this delivery as receipt %s.\n", resp.StatusCode, answer.DeliveryID)
			default:
				fmt.Printf("Accepted (%d).\n", resp.StatusCode)
			}
			fmt.Printf("  Profile:     %s\n", profile)
			fmt.Printf("  Source id:   %s\n", sourceID)
			if answer.DeliveryID != "" {
				fmt.Printf("  Receipt:     %s\n", answer.DeliveryID)
			}
			if answer.RunID != "" {
				fmt.Printf("  Run:         %s  (%s)\n", answer.RunID, answer.Status)
				fmt.Printf("  Follow up:   crewship routine logs %s\n", answer.RunID)
			}
		})
	},
}

// routineWebhookFireURL resolves the positional argument: an absolute URL is
// used as given; anything else is a token (optionally already carrying the
// /github-pull-request suffix) under baseURL's /api/v1/webhooks/.
func routineWebhookFireURL(arg, baseURL string) (*url.URL, error) {
	if strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
		u, err := url.Parse(arg)
		if err != nil {
			return nil, fmt.Errorf("public URL is not parseable: %w", err)
		}
		return u, nil
	}
	token, suffix := arg, ""
	if strings.HasSuffix(arg, webhookGitHubURLSuffix) {
		token, suffix = strings.TrimSuffix(arg, webhookGitHubURLSuffix), webhookGitHubURLSuffix
	}
	if token == "" || strings.ContainsAny(token, "/?#") {
		// Never echo the argument: a scheme-less URL carries the token.
		return nil, fmt.Errorf("argument must be the webhook's public URL (with http:// or https://) or its bare token")
	}
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + "/api/v1/webhooks/" + url.PathEscape(token) + suffix)
	if err != nil {
		return nil, fmt.Errorf("base URL %q: %w", baseURL, err)
	}
	return u, nil
}

func hmacHex(secret string, signed []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(signed)
	return hex.EncodeToString(mac.Sum(nil))
}

// newWebhookDeliveryID mints the sender-side delivery id when the caller did
// not pass one. Random rather than time-derived so two fires of the same body
// within a second are two deliveries, which is what a sender would do.
func newWebhookDeliveryID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "cli-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "cli-" + hex.EncodeToString(b[:])
}
