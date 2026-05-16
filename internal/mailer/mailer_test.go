package mailer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Disabled stub ---

func TestDisabled_SendAlwaysReturnsErrDisabled(t *testing.T) {
	t.Parallel()
	var m Mailer = Disabled{}
	err := m.Send(context.Background(), Message{To: "a@b.c", Subject: "x", HTML: "<p>x</p>"})
	if !errors.Is(err, ErrDisabled) {
		t.Errorf("want ErrDisabled, got %v", err)
	}
}

func TestDisabled_ConfiguredFalse(t *testing.T) {
	t.Parallel()
	var d Disabled
	if d.Configured() {
		t.Error("Disabled.Configured() should always be false")
	}
}

// --- NewFromEnv ---

func TestNewFromEnv_DisabledWhenAnyEnvMissing(t *testing.T) {
	// Subtests cover every missing-piece variant so a regression that
	// only checks one env var names which one in the test report.
	cases := []struct {
		name           string
		apiKey, sender string
	}{
		{"both_empty", "", ""},
		{"only_apikey", "key", ""},
		{"only_from", "", "noreply@x.com"},
		{"whitespace_apikey", "   ", "noreply@x.com"},
		{"whitespace_from", "key", "   "},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RESEND_API_KEY", tc.apiKey)
			t.Setenv("RESEND_FROM", tc.sender)
			m := NewFromEnv()
			if m.Configured() {
				t.Errorf("Configured() should be false when env is %s", tc.name)
			}
			if _, ok := m.(Disabled); !ok {
				t.Errorf("NewFromEnv should return Disabled, got %T", m)
			}
		})
	}
}

func TestNewFromEnv_ResendWhenBothSet(t *testing.T) {
	t.Setenv("RESEND_API_KEY", "re_test_key")
	t.Setenv("RESEND_FROM", "noreply@example.com")
	m := NewFromEnv()
	if !m.Configured() {
		t.Error("Configured() should be true when both env vars are set")
	}
	if _, ok := m.(*Resend); !ok {
		t.Errorf("NewFromEnv should return *Resend, got %T", m)
	}
}

// --- Resend.Configured (independent of env) ---

func TestResend_Configured(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		apiKey, sender string
		want           bool
	}{
		{"both_set", "key", "from", true},
		{"empty_apikey", "", "from", false},
		{"empty_from", "key", "", false},
		{"both_whitespace", "   ", "   ", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := NewResend(tc.apiKey, tc.sender)
			if got := r.Configured(); got != tc.want {
				t.Errorf("Configured() = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- Resend.Send ---

// withResendURL points resendAPIURL at the provided URL for the
// duration of the test, restoring the original on cleanup. The
// package-level URL is declared as var (not const) specifically to
// support this test override.
func withResendURL(t *testing.T, url string) {
	t.Helper()
	original := resendAPIURL
	resendAPIURL = url
	t.Cleanup(func() { resendAPIURL = original })
}

func TestResend_Send_HappyPath(t *testing.T) {
	// Captures the request the transport sent so we can assert the
	// payload + headers Resend actually receives. Not t.Parallel
	// because we mutate the package-level resendAPIURL.
	var got struct {
		method      string
		path        string
		contentType string
		auth        string
		body        resendRequest
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		got.contentType = r.Header.Get("Content-Type")
		got.auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"em_abc"}`))
	}))
	t.Cleanup(srv.Close)
	withResendURL(t, srv.URL)

	r := NewResend("re_test_key", "noreply@example.com")
	err := r.Send(context.Background(), Message{
		To:      "user@example.com",
		Subject: "Reset",
		HTML:    "<p>Click here</p>",
		Text:    "Click here",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.method != http.MethodPost {
		t.Errorf("method: want POST, got %s", got.method)
	}
	if got.contentType != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", got.contentType)
	}
	if got.auth != "Bearer re_test_key" {
		t.Errorf("Authorization: want Bearer re_test_key, got %q", got.auth)
	}
	if got.body.From != "noreply@example.com" ||
		len(got.body.To) != 1 || got.body.To[0] != "user@example.com" ||
		got.body.Subject != "Reset" || got.body.HTML != "<p>Click here</p>" ||
		got.body.Text != "Click here" {
		t.Errorf("body mismatched: %+v", got.body)
	}
}

func TestResend_Send_OmitsEmptyTextField(t *testing.T) {
	// The resendRequest struct tags Text as omitempty so a sender that
	// only supplies HTML doesn't ship an empty `text` key (Resend's
	// content-quality heuristics flag empty text bodies). Verify the
	// wire payload reflects that.
	var bodyBytes []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		bodyBytes = buf
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	withResendURL(t, srv.URL)

	r := NewResend("k", "from@x.com")
	if err := r.Send(context.Background(), Message{
		To: "u@x.com", Subject: "S", HTML: "<p>h</p>",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if strings.Contains(string(bodyBytes), `"text"`) {
		t.Errorf("empty Text should be omitted from JSON; got %s", bodyBytes)
	}
}

func TestResend_Send_StructuredErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"name":"validation_error","message":"To address invalid"}`))
	}))
	t.Cleanup(srv.Close)
	withResendURL(t, srv.URL)

	r := NewResend("k", "from@x.com")
	err := r.Send(context.Background(), Message{To: "bad", Subject: "x", HTML: "x"})
	if err == nil {
		t.Fatal("expected error on non-2xx")
	}
	// Error message should contain both the API name and the message
	// so ops can grep logs by either axis.
	msg := err.Error()
	if !strings.Contains(msg, "422") ||
		!strings.Contains(msg, "validation_error") ||
		!strings.Contains(msg, "To address invalid") {
		t.Errorf("error missing status/name/message: %q", msg)
	}
}

func TestResend_Send_UnparseableErrorBodyFallsBackToRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`<html>upstream down</html>`))
	}))
	t.Cleanup(srv.Close)
	withResendURL(t, srv.URL)

	r := NewResend("k", "from@x.com")
	err := r.Send(context.Background(), Message{To: "u@x.com", Subject: "x", HTML: "x"})
	if err == nil {
		t.Fatal("expected error on non-2xx")
	}
	msg := err.Error()
	if !strings.Contains(msg, "503") {
		t.Errorf("error should carry status code: %q", msg)
	}
	if !strings.Contains(msg, "upstream down") {
		t.Errorf("error should fall back to raw body on JSON parse failure: %q", msg)
	}
}

func TestResend_Send_NetworkErrorBubblesUp(t *testing.T) {
	// httptest.NewServer + immediate Close = closed-connection error.
	// Listener address is reachable but accept loop is gone, so the
	// Dial succeeds and Read fails — exact path used in production
	// when Resend's edge node restarts mid-request.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()
	withResendURL(t, srv.URL)

	r := NewResend("k", "from@x.com")
	err := r.Send(context.Background(), Message{To: "u@x.com", Subject: "x", HTML: "x"})
	if err == nil {
		t.Fatal("expected network error")
	}
	if !strings.Contains(err.Error(), "mailer/resend") {
		t.Errorf("error should be wrapped with mailer/resend prefix: %q", err)
	}
}

func TestResend_Send_ContextCancellationPropagates(t *testing.T) {
	// Long-running server that blocks. We cancel the ctx before it
	// responds and assert Send returns the context error wrapped, so
	// upstream handlers can react to /forgot endpoint cancellations
	// instead of waiting out the 10s client timeout.
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-blocked
	}))
	t.Cleanup(srv.Close)
	withResendURL(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := NewResend("k", "from@x.com")
	err := r.Send(ctx, Message{To: "u@x.com", Subject: "x", HTML: "x"})
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error should wrap context.Canceled; got %v", err)
	}
}
