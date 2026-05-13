package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// resendAPIURL is the only Resend endpoint we hit. Hardcoded rather
// than env-configurable: if Resend ever moves their API base, every
// other library will break too — there's no scenario where pointing
// this elsewhere produces a working setup.
const resendAPIURL = "https://api.resend.com/emails"

// Resend is the production transport for transactional auth emails
// (password reset today, email verification next). Reads its
// configuration from environment variables at construction time so
// the Disabled fallback can be wired uniformly via NewFromEnv.
//
// Auth is via Bearer API key, posted as JSON. The Resend API is
// pleasantly minimal — From/To/Subject/HTML is the entire schema we
// care about. We don't surface their tracking/tagging fields here;
// system auth emails should not be analytics-tracked.
type Resend struct {
	apiKey string
	from   string
	client *http.Client
}

// NewResend constructs a Resend transport. apiKey and from must both
// be non-empty; pass NewFromEnv if you want fallback to Disabled when
// the env vars are missing. Inputs are trimmed here so a caller that
// hand-builds Resend (tests, future call sites) can't sneak through
// with whitespace-only values that Configured() would otherwise read
// as legitimately set.
func NewResend(apiKey, from string) *Resend {
	return &Resend{
		apiKey: strings.TrimSpace(apiKey),
		from:   strings.TrimSpace(from),
		// 10s is generous: Resend's median latency is ~200ms and the
		// /forgot endpoint runs synchronously inside the request
		// goroutine. Anything longer and a misconfigured SMTP/Resend
		// would stall the user's "reset password" click long enough
		// to look broken.
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// NewFromEnv returns a Resend transport when RESEND_API_KEY and
// RESEND_FROM are both set; otherwise returns Disabled. This is the
// constructor server.go startup should call.
//
// The choice of "both required" (vs. "API key required, From
// defaulted") is deliberate: a default From address would silently
// send emails as the wrong sender, which is worse than not sending
// at all. Operators must opt in to a specific sender identity.
func NewFromEnv() Mailer {
	apiKey := strings.TrimSpace(os.Getenv("RESEND_API_KEY"))
	from := strings.TrimSpace(os.Getenv("RESEND_FROM"))
	if apiKey == "" || from == "" {
		return Disabled{}
	}
	return NewResend(apiKey, from)
}

// Configured reports whether the transport actually has credentials
// to make a Send attempt. Defends against direct callers of NewResend
// passing empty values — the auth-recovery handler uses Configured()
// to gate enumeration vs. real send paths, so an honest answer here
// is load-bearing.
func (r *Resend) Configured() bool {
	return r.apiKey != "" && r.from != ""
}

type resendRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
	Text    string   `json:"text,omitempty"`
}

type resendErrorResponse struct {
	Message string `json:"message"`
	Name    string `json:"name"`
}

// Send posts the message to Resend's API. Errors from the network or
// from a non-2xx Resend response come back wrapped with enough
// context for ops to grep server logs.
func (r *Resend) Send(ctx context.Context, msg Message) error {
	body, err := json.Marshal(resendRequest{
		From:    r.from,
		To:      []string{msg.To},
		Subject: msg.Subject,
		HTML:    msg.HTML,
		Text:    msg.Text,
	})
	if err != nil {
		return fmt.Errorf("mailer/resend: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, resendAPIURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("mailer/resend: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("mailer/resend: http error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// We deliberately don't parse the success body — Resend
		// returns an `id` we have no use for (we don't retry, we
		// don't track open rates), and reading it just to discard it
		// is busywork that can also leak goroutines if the body
		// never closes.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	// Resend's error envelope is consistent: { name, message }.
	// Surface both — name is enum-like (validation_error,
	// rate_limit, etc.), message is human prose.
	respBody, _ := io.ReadAll(resp.Body)
	var er resendErrorResponse
	_ = json.Unmarshal(respBody, &er)
	if er.Message != "" {
		return fmt.Errorf("mailer/resend: %d %s: %s", resp.StatusCode, er.Name, er.Message)
	}
	return fmt.Errorf("mailer/resend: %d %s", resp.StatusCode, string(respBody))
}
