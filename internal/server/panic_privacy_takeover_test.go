package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/crashreport"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/getsentry/sentry-go"
)

type panicPrivacyTransport struct{ event *sentry.Event }

func (tr *panicPrivacyTransport) Configure(sentry.ClientOptions)        {}
func (tr *panicPrivacyTransport) SendEvent(event *sentry.Event)         { tr.event = event }
func (tr *panicPrivacyTransport) Flush(time.Duration) bool              { return true }
func (tr *panicPrivacyTransport) FlushWithContext(context.Context) bool { return true }
func (tr *panicPrivacyTransport) Close()                                {}

// Exercise the real middleware -> crashreport -> Sentry backend -> production
// BeforeSend chain, replacing only the final network transport with a recorder.
func TestPanicRecovery_CapabilityPrivacyInCapturedEvent(t *testing.T) {
	// crashreport and the Sentry hub have process-global startup state. Keep
	// the real initialization in a child test process so shuffle/race runs
	// cannot leave telemetry enabled or replace another test's backend.
	const childFlag = "CREWSHIP_TEST_PANIC_PRIVACY_CHILD"
	if os.Getenv(childFlag) != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPanicRecovery_CapabilityPrivacyInCapturedEvent$")
		cmd.Env = append(os.Environ(), childFlag+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated panic privacy test: %v\n%s", err, output)
		}
		return
	}
	originalClient := sentry.CurrentHub().Client()
	defer sentry.CurrentHub().BindClient(originalClient)
	db := testutil.MigratedDB(t)
	t.Setenv("CREWSHIP_SENTRY_DSN", "https://qa@example.test/1")
	if _, _, err := crashreport.SetOptIn(t.Context(), db.DB, true); err != nil {
		t.Fatal(err)
	}
	if err := crashreport.Init(t.Context(), db.DB, "dev", newQuietLogger()); err != nil {
		t.Fatal(err)
	}
	initialized := sentry.CurrentHub().Client()
	if initialized == nil {
		t.Fatal("real Sentry backend was not initialized")
	}
	options := initialized.Options()
	initialized.Close()
	tr := &panicPrivacyTransport{}
	options.Transport = tr
	client, err := sentry.NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	sentry.CurrentHub().BindClient(client)
	defer client.Close()
	synthetic := strings.Repeat("c3", 32)
	for _, tc := range []struct{ name, path, pattern string }{
		{"registered", "/api/v1/public/pages/" + synthetic, "GET /api/v1/public/pages/{token}"},
		{"nested", "/exposed/" + synthetic + "/private-path?token=" + synthetic, "GET /exposed/{token}/{rest...}"},
		{"unmatched", "/unregistered/" + synthetic, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr.event = nil
			logger, buf := jsonLogBuf()
			handler := http.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret panic payload " + synthetic) }))
			if tc.pattern != "" {
				mux := http.NewServeMux()
				mux.Handle(tc.pattern, handler)
				handler = mux
			}
			response := httptest.NewRecorder()
			panicRecoveryMiddleware(logger, handler).ServeHTTP(response, httptest.NewRequest("GET", tc.path, nil))
			if response.Code != 500 || tr.event == nil {
				t.Fatal("panic not captured")
			}
			encoded, err := json.Marshal(tr.event)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), synthetic) || strings.Contains(string(encoded), "secret panic payload") {
				t.Fatal("captured event contains request/panic secrets")
			}
			if strings.Contains(buf.String(), synthetic) || strings.Contains(buf.String(), "secret panic payload") {
				t.Fatal("local panic log contains request/panic secrets")
			}
			expected := tc.pattern
			if expected == "" {
				expected = "unmatched"
			}
			if tr.event.Tags["path"] != expected {
				t.Fatalf("route identity: %q", tr.event.Tags["path"])
			}
		})
	}
}
