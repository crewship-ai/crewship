package backup

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/webhook"
)

func TestSendEvent_NoURLIsNoOp(t *testing.T) {
	if err := SendEvent(context.Background(), WebhookConfig{}, WebhookEvent{Event: "backup.created"}); err != nil {
		t.Errorf("empty URL should be no-op, got %v", err)
	}
}

func TestSendEvent_SignsBodyWithHMAC(t *testing.T) {
	secret := "shhh"
	var delivered atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
	defer srv.Close()

	err := SendEvent(context.Background(), WebhookConfig{URL: srv.URL, Secret: secret}, WebhookEvent{
		Event:     "backup.created",
		Timestamp: time.Now().UTC(),
		Scope:     "workspace",
		Path:      "/tmp/foo.tar.zst",
		Bytes:     1234,
	})
	if err != nil {
		t.Fatalf("SendEvent: %v", err)
	}
	if !delivered.Load() {
		t.Fatal("server never received the request")
	}
}

func TestSendEvent_RejectsURLWithoutSecret(t *testing.T) {
	err := SendEvent(context.Background(), WebhookConfig{URL: "http://example.test"}, WebhookEvent{})
	if err == nil {
		t.Fatal("expected error when URL set without secret")
	}
}

func TestSendEvent_SurfacesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad", http.StatusBadRequest)
	}))
	defer srv.Close()
	err := SendEvent(context.Background(), WebhookConfig{URL: srv.URL, Secret: "s"}, WebhookEvent{})
	if err == nil {
		t.Fatal("expected non-nil err for 400 response")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("err should mention status 400, got %v", err)
	}
}

func TestSendEvent_RespectsContextTimeout(t *testing.T) {
	// Server blocks long enough that our 50ms timeout fires first.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()
	err := SendEvent(context.Background(), WebhookConfig{URL: srv.URL, Secret: "s", Timeout: 50 * time.Millisecond}, WebhookEvent{})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
