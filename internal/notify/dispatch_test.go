package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/mailer"
	"github.com/crewship-ai/crewship/internal/webhook"
)

// staticLister returns a fixed channel set (dispatch tests need no DB).
type staticLister struct{ channels []Channel }

func (s staticLister) ListEnabled(context.Context, string) ([]Channel, error) {
	return s.channels, nil
}

// fastDispatcher builds a dispatcher with a near-zero backoff so retry
// tests don't sleep for real.
func fastDispatcher(t *testing.T, lister ChannelLister, m mailer.Mailer) *Dispatcher {
	t.Helper()
	d := NewDispatcher(lister, m, nil)
	d.baseBackoff = time.Millisecond
	return d
}

func TestDispatch_Webhook_SignedPayloadReaches(t *testing.T) {
	var (
		mu      sync.Mutex
		gotBody []byte
		gotSig  string
		gotEvt  string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = b
		gotSig = r.Header.Get("X-Crewship-Signature")
		gotEvt = r.Header.Get("X-Crewship-Event")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch := Channel{ID: "c1", Type: ChannelWebhook, URL: srv.URL, Secret: "topsecret", Enabled: true}
	d := fastDispatcher(t, staticLister{[]Channel{ch}}, nil)

	d.Dispatch(context.Background(), NotificationEvent{
		Type:        EventRunFailed,
		WorkspaceID: "w",
		RunID:       "run_9",
		RoutineSlug: "nightly-report",
		Status:      "failed",
	})

	mu.Lock()
	defer mu.Unlock()
	if len(gotBody) == 0 {
		t.Fatal("webhook server received no body")
	}
	// Signature must verify against the raw body with the channel secret.
	sig := strings.TrimPrefix(gotSig, "sha256=")
	if !webhook.ValidateHMAC(gotBody, sig, "topsecret") {
		t.Fatalf("HMAC signature %q does not verify over body", gotSig)
	}
	if gotEvt != EventRunFailed {
		t.Errorf("X-Crewship-Event = %q, want %q", gotEvt, EventRunFailed)
	}
	var p webhookPayload
	if err := json.Unmarshal(gotBody, &p); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if p.Event != EventRunFailed || p.RunID != "run_9" || p.Routine != "nightly-report" || p.Status != "failed" {
		t.Errorf("payload mismatch: %+v", p)
	}
}

func TestDispatch_Webhook_ScrubsOutputPreview(t *testing.T) {
	var (
		mu      sync.Mutex
		gotBody []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = b
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	secret := "sk-ant-api03-" + strings.Repeat("A", 90)
	ch := Channel{ID: "c1", Type: ChannelWebhook, URL: srv.URL, Secret: "s", Enabled: true, Events: []string{EventRunCompleted}}
	d := fastDispatcher(t, staticLister{[]Channel{ch}}, nil)

	d.Dispatch(context.Background(), NotificationEvent{
		Type: EventRunCompleted, WorkspaceID: "w", RunID: "r", RoutineSlug: "x", Status: "completed",
		OutputPreview: "here is a leaked key " + secret + " end",
	})

	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(string(gotBody), secret) {
		t.Fatalf("output preview leaked a secret into the webhook payload: %s", gotBody)
	}
}

func TestDispatch_Webhook_CapsPreview(t *testing.T) {
	var (
		mu sync.Mutex
		p  webhookPayload
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(b, &p)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch := Channel{ID: "c1", Type: ChannelWebhook, URL: srv.URL, Secret: "s", Enabled: true, Events: []string{EventRunCompleted}}
	d := fastDispatcher(t, staticLister{[]Channel{ch}}, nil)
	d.Dispatch(context.Background(), NotificationEvent{
		Type: EventRunCompleted, WorkspaceID: "w", RunID: "r", RoutineSlug: "x", Status: "completed",
		OutputPreview: strings.Repeat("z", 5000),
	})

	mu.Lock()
	defer mu.Unlock()
	if len([]rune(p.OutputPreview)) > outputPreviewCap+1 { // +1 for the ellipsis
		t.Fatalf("preview not capped: len=%d", len([]rune(p.OutputPreview)))
	}
}

func TestDispatch_Webhook_RetriesOn500(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch := Channel{ID: "c1", Type: ChannelWebhook, URL: srv.URL, Secret: "s", Enabled: true}
	d := fastDispatcher(t, staticLister{nil}, nil)
	if err := d.DispatchOne(context.Background(), ch, NotificationEvent{
		Type: EventRunFailed, WorkspaceID: "w", RunID: "r", RoutineSlug: "x", Status: "failed",
	}); err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("expected 3 attempts (2×500 then 200), got %d", got)
	}
}

func TestDispatch_Webhook_NoRetryOn4xx(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	ch := Channel{ID: "c1", Type: ChannelWebhook, URL: srv.URL, Secret: "s", Enabled: true}
	d := fastDispatcher(t, staticLister{nil}, nil)
	if err := d.DispatchOne(context.Background(), ch, NotificationEvent{
		Type: EventRunFailed, WorkspaceID: "w", RunID: "r", RoutineSlug: "x", Status: "failed",
	}); err == nil {
		t.Fatal("expected error on 400")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("4xx must not retry: expected 1 attempt, got %d", got)
	}
}

// fakeMailer records the last message and reports itself configured.
type fakeMailer struct {
	mu   sync.Mutex
	sent []mailer.Message
}

func (f *fakeMailer) Send(_ context.Context, m mailer.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}
func (f *fakeMailer) Configured() bool { return true }

func TestDispatch_Email_Sends(t *testing.T) {
	fm := &fakeMailer{}
	ch := Channel{ID: "c1", Type: ChannelEmail, To: "admin@example.com", Enabled: true}
	d := fastDispatcher(t, staticLister{[]Channel{ch}}, fm)
	d.Dispatch(context.Background(), NotificationEvent{
		Type: EventRunFailed, WorkspaceID: "w", RunID: "r1", RoutineSlug: "nightly", Status: "failed",
	})
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if len(fm.sent) != 1 {
		t.Fatalf("expected 1 email, got %d", len(fm.sent))
	}
	if fm.sent[0].To != "admin@example.com" || !strings.Contains(fm.sent[0].Subject, "nightly") {
		t.Errorf("unexpected email: %+v", fm.sent[0])
	}
}

func TestDispatch_EventFilter_SkipsUnsubscribed(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// A failed-only channel must ignore a run.completed event...
	failedOnly := Channel{ID: "c1", Type: ChannelWebhook, URL: srv.URL, Secret: "s", Enabled: true, Events: []string{EventRunFailed}}
	d := fastDispatcher(t, staticLister{[]Channel{failedOnly}}, nil)
	d.Dispatch(context.Background(), NotificationEvent{Type: EventRunCompleted, WorkspaceID: "w", RunID: "r", RoutineSlug: "x", Status: "completed"})
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("failed-only channel should skip run.completed, got %d hits", got)
	}

	// ...but receive a run.failed event.
	d.Dispatch(context.Background(), NotificationEvent{Type: EventRunFailed, WorkspaceID: "w", RunID: "r", RoutineSlug: "x", Status: "failed"})
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("failed-only channel should receive run.failed, got %d hits", got)
	}
}

func TestScrubPreview_CapIsRuneSafe(t *testing.T) {
	d := NewDispatcher(staticLister{nil}, nil, nil)
	// 4-byte runes so a byte-offset cut would land mid-rune.
	preview := strings.Repeat("😀", 1000) // 4000 bytes, well over the 1KB cap
	got := d.scrubPreview(preview)
	if !utf8.ValidString(got) {
		t.Fatalf("capped preview is not valid UTF-8: %q", got)
	}
	if len([]byte(got)) > outputPreviewCap+len("…") {
		t.Fatalf("preview exceeds cap: %d bytes", len([]byte(got)))
	}
}

func TestChannel_Wants_EmptyDefaultsToFailed(t *testing.T) {
	c := Channel{}
	if c.Wants(EventRunCompleted) {
		t.Error("empty events must not want completed")
	}
	if !c.Wants(EventRunFailed) {
		t.Error("empty events must default to failed")
	}
}

func TestDispatch_Email_DisabledMailerIsNoOp(t *testing.T) {
	ch := Channel{ID: "c1", Type: ChannelEmail, To: "a@b.com", Enabled: true}
	d := fastDispatcher(t, staticLister{nil}, mailer.Disabled{})
	if err := d.DispatchOne(context.Background(), ch, NotificationEvent{
		Type: EventRunCompleted, WorkspaceID: "w", RunID: "r", RoutineSlug: "x", Status: "completed",
	}); err != nil {
		t.Fatalf("disabled mailer should be a no-op, got %v", err)
	}
}
