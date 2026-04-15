package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/webhook"
)

// TestSendEvent runs the webhook client through every shape of
// outcome we care about: URL absent / present without secret / valid
// end-to-end (including HMAC-signed body) / 4xx surfaced / deadline
// exceeded. Table-driven with per-case httptest servers so each case
// owns a clean lifecycle and new cases drop in by appending to the
// table.
func TestSendEvent(t *testing.T) {
	type caseCfg struct {
		name            string
		cfg             WebhookConfig
		event           WebhookEvent
		handler         http.HandlerFunc // nil → no server started
		wantErr         bool
		wantErrContains string
		// extra runs after SendEvent returns, for per-case server-side
		// or delivery assertions.
		extra func(t *testing.T, err error, delivered *atomic.Bool)
	}

	const secret = "shhh"
	var delivered atomic.Bool

	// The handler that does real HMAC + header + body assertions for
	// the happy-path case. Lives outside the table so we can reset
	// `delivered` before the case runs.
	signedOK := func(t *testing.T) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read body: %v", err)
				http.Error(w, "read", http.StatusInternalServerError)
				return
			}
			got := r.Header.Get("X-Crewship-Signature")
			if !strings.HasPrefix(got, "sha256=") {
				t.Errorf("signature prefix missing: %q", got)
			}
			if !webhook.ValidateHMAC(body, strings.TrimPrefix(got, "sha256="), secret) {
				t.Errorf("signature does not validate")
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type: got %q want application/json", ct)
			}
			if ev := r.Header.Get("X-Crewship-Event"); ev != "backup.created" {
				t.Errorf("X-Crewship-Event: got %q", ev)
			}
			var parsed WebhookEvent
			if err := json.Unmarshal(body, &parsed); err != nil {
				t.Errorf("unmarshal: %v", err)
			}
			if parsed.Path != "/tmp/foo.tar.zst" {
				t.Errorf("Path: got %q", parsed.Path)
			}
			delivered.Store(true)
			w.WriteHeader(http.StatusOK)
		}
	}

	cases := []caseCfg{
		{
			name:    "no URL is a no-op",
			cfg:     WebhookConfig{},
			event:   WebhookEvent{Event: "backup.created"},
			wantErr: false,
		},
		{
			name:            "URL without secret is refused",
			cfg:             WebhookConfig{URL: "http://example.test"},
			event:           WebhookEvent{},
			wantErr:         true,
			wantErrContains: "secret",
		},
		{
			name: "signed body delivers and verifies",
			cfg: WebhookConfig{
				// URL filled in at runtime from the httptest server.
				Secret: secret,
			},
			event: WebhookEvent{
				Event:     "backup.created",
				Timestamp: time.Now().UTC(),
				Scope:     "workspace",
				Path:      "/tmp/foo.tar.zst",
				Bytes:     1234,
			},
			handler: signedOK(t),
			wantErr: false,
			extra: func(t *testing.T, _ error, delivered *atomic.Bool) {
				if !delivered.Load() {
					t.Fatal("server never received the request")
				}
			},
		},
		{
			name: "4xx response surfaces as error",
			cfg: WebhookConfig{
				Secret: "s",
			},
			event: WebhookEvent{},
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "bad", http.StatusBadRequest)
			},
			wantErr:         true,
			wantErrContains: "400",
		},
		{
			name: "timeout surfaces as deadline-class error",
			cfg: WebhookConfig{
				Secret:  "s",
				Timeout: 100 * time.Millisecond,
			},
			event: WebhookEvent{},
			handler: func(w http.ResponseWriter, _ *http.Request) {
				// 500ms vs a 100ms cfg timeout — generous gap so the
				// case stays stable under CI cgroup throttling.
				time.Sleep(500 * time.Millisecond)
			},
			wantErr: true,
			extra: func(t *testing.T, err error, _ *atomic.Bool) {
				if !errors.Is(err, context.DeadlineExceeded) &&
					!strings.Contains(err.Error(), "context deadline exceeded") &&
					!strings.Contains(err.Error(), "Client.Timeout") {
					t.Errorf("expected deadline-class error, got %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			delivered.Store(false)
			cfg := tc.cfg
			if tc.handler != nil {
				srv := httptest.NewServer(tc.handler)
				t.Cleanup(srv.Close)
				cfg.URL = srv.URL
			}
			err := SendEvent(context.Background(), cfg, tc.event)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if tc.wantErrContains != "" && err != nil && !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Errorf("err should contain %q, got %v", tc.wantErrContains, err)
			}
			if tc.extra != nil {
				tc.extra(t, err, &delivered)
			}
		})
	}
}
