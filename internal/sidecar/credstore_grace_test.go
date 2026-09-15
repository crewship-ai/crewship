package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Rotation grace inside the sidecar (#1882).
//
// A rotation keeps the credential's previous value for a grace window so a run
// that started on it can drain. The value reaches the sidecar the same way the
// current one does — the boot payload — and the store enforces the window
// locally, like a lease: fail-closed, no round-trip. Cancellation is learned
// from the reaper's metadata listing, so it propagates with the same latency a
// revocation does.

func TestCredStore_GraceFor(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		creds   []Credential
		credID  string
		wantTok string
		wantRot string
		wantOK  bool
	}{
		{
			name: "active unexpired grace is served",
			creds: []Credential{{
				ID: "c1", Provider: ProviderAnthropic, Token: "new",
				GraceToken: "old", GraceExpiresAt: rfc3339(now.Add(time.Hour)), GraceRotationID: "rot-1",
			}},
			credID: "c1", wantTok: "old", wantRot: "rot-1", wantOK: true,
		},
		{
			name: "expired grace is refused",
			creds: []Credential{{
				ID: "c1", Provider: ProviderAnthropic, Token: "new",
				GraceToken: "old", GraceExpiresAt: rfc3339(now.Add(-time.Minute)), GraceRotationID: "rot-1",
			}},
			credID: "c1",
		},
		{
			name: "grace with no deadline is refused (fail closed)",
			creds: []Credential{{
				ID: "c1", Provider: ProviderAnthropic, Token: "new",
				GraceToken: "old", GraceRotationID: "rot-1",
			}},
			credID: "c1",
		},
		{
			name: "grace with an unparseable deadline is refused (fail closed)",
			creds: []Credential{{
				ID: "c1", Provider: ProviderAnthropic, Token: "new",
				GraceToken: "old", GraceExpiresAt: "yesterday", GraceRotationID: "rot-1",
			}},
			credID: "c1",
		},
		{
			name: "another credential's grace is never served",
			creds: []Credential{
				{ID: "c1", Provider: ProviderAnthropic, Token: "new"},
				{ID: "c2", Provider: ProviderAnthropic, Token: "other",
					GraceToken: "old-other", GraceExpiresAt: rfc3339(now.Add(time.Hour)), GraceRotationID: "rot-2"},
			},
			credID: "c1",
		},
		{
			name: "a lapsed lease refuses the grace too",
			creds: []Credential{{
				ID: "c1", Provider: ProviderAnthropic, Token: "new",
				LeaseExpiresAt: rfc3339(now.Add(-time.Minute)),
				GraceToken:     "old", GraceExpiresAt: rfc3339(now.Add(time.Hour)), GraceRotationID: "rot-1",
			}},
			credID: "c1",
		},
		{
			name:   "unknown credential",
			creds:  nil,
			credID: "nope",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cs := NewCredStore()
			cs.Load(tc.creds)
			tok, rot, ok := cs.GraceFor(tc.credID, now)
			if ok != tc.wantOK || tok != tc.wantTok || rot != tc.wantRot {
				t.Fatalf("GraceFor(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.credID, tok, rot, ok, tc.wantTok, tc.wantRot, tc.wantOK)
			}
		})
	}
}

// Load drops a grace value it can never serve, so the plaintext is not
// resident for the container's life waiting for a reaper tick.
func TestCredStore_LoadDropsUnservableGrace(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "no-deadline", Provider: ProviderAnthropic, Token: "t", GraceToken: "g"},
		{ID: "bad-deadline", Provider: ProviderAnthropic, Token: "t", GraceToken: "g", GraceExpiresAt: "nope"},
		{ID: "ok", Provider: ProviderAnthropic, Token: "t", GraceToken: "g", GraceExpiresAt: rfc3339(time.Now().Add(time.Hour))},
	})
	for _, id := range []string{"no-deadline", "bad-deadline"} {
		if _, _, ok := cs.GraceFor(id, time.Now()); ok {
			t.Errorf("%s: grace served without a usable deadline", id)
		}
		if got := cs.graceTokenForTest(id); got != "" {
			t.Errorf("%s: grace plaintext still resident after Load", id)
		}
	}
	if _, _, ok := cs.GraceFor("ok", time.Now()); !ok {
		t.Error("ok: usable grace was dropped")
	}
}

func TestCredStore_ExpireGrace(t *testing.T) {
	now := time.Now()
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "live", Provider: ProviderAnthropic, Token: "t", GraceToken: "g", GraceExpiresAt: rfc3339(now.Add(time.Hour))},
		{ID: "gone", Provider: ProviderAnthropic, Token: "t", GraceToken: "g", GraceExpiresAt: rfc3339(now.Add(-time.Second))},
	})
	if n := cs.ExpireGrace(now); n != 1 {
		t.Fatalf("ExpireGrace dropped %d, want 1", n)
	}
	if cs.graceTokenForTest("gone") != "" {
		t.Error("expired grace plaintext still resident")
	}
	if _, _, ok := cs.GraceFor("live", now); !ok {
		t.Error("live grace was dropped by ExpireGrace")
	}
	// The credential itself stays: only the grace value expired.
	if cs.Select(ProviderAnthropic, "") == nil {
		t.Error("ExpireGrace removed the credential, not just its grace value")
	}
}

// ScrubGrace is the reaper's cancellation primitive: after a successful
// metadata fetch, every grace value whose credential no longer lists an ACTIVE
// rotation is dropped. The credentials themselves are untouched.
func TestCredStore_ScrubGrace(t *testing.T) {
	now := time.Now()
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "kept", Provider: ProviderAnthropic, Token: "t", GraceToken: "g", GraceExpiresAt: rfc3339(now.Add(time.Hour))},
		{ID: "cancelled", Provider: ProviderAnthropic, Token: "t", GraceToken: "g", GraceExpiresAt: rfc3339(now.Add(time.Hour))},
		{ID: "plain", Provider: ProviderAnthropic, Token: "t"},
	})
	if n := cs.ScrubGrace(map[string]struct{}{"kept": {}}); n != 1 {
		t.Fatalf("ScrubGrace dropped %d, want 1", n)
	}
	if _, _, ok := cs.GraceFor("cancelled", now); ok {
		t.Error("cancelled rotation's grace still served")
	}
	if _, _, ok := cs.GraceFor("kept", now); !ok {
		t.Error("still-active rotation's grace was scrubbed")
	}
	if cs.Count(ProviderAnthropic) != 3 {
		t.Errorf("ScrubGrace changed the credential set: %d, want 3", cs.Count(ProviderAnthropic))
	}
}

// graceTokenForTest reads the resident grace plaintext for one credential —
// test-only, so an assertion that a value was DROPPED can look past GraceFor's
// eligibility gate.
func (cs *CredStore) graceTokenForTest(credID string) string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	for _, c := range cs.creds {
		if c.ID == credID {
			return c.GraceToken
		}
	}
	return ""
}

// The reaper is how an operator ending a grace window early reaches the
// sidecar (#1882): the metadata listing carries rotation_grace_until for a
// credential whose rotation is still ACTIVE and omits it once cancelled or
// expired server-side, and the sidecar's copy of the value follows. Neither the
// credential itself nor its current value is touched.
func TestSidecar_ReapRevokedCredentials_ScrubsCancelledGrace(t *testing.T) {
	var mu sync.Mutex
	cancelled := false
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if cancelled {
			_, _ = w.Write([]byte(`[{"id":"a","status":"ACTIVE"},{"id":"b","status":"ACTIVE"}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":"a","status":"ACTIVE","rotation_grace_until":"` +
			rfc3339(time.Now().Add(time.Hour)) + `"},{"id":"b","status":"ACTIVE"}]`))
	}))
	defer backend.Close()

	s := newJournalTestServer(backend.URL)
	s.credStore = NewCredStore()
	s.credStore.Load([]Credential{
		{ID: "a", Provider: ProviderAnthropic, Token: "sk-new", GraceToken: "sk-old",
			GraceExpiresAt: rfc3339(time.Now().Add(time.Hour)), GraceRotationID: "rot-a"},
		{ID: "b", Provider: ProviderOpenAI, Token: "t2"},
	})

	s.reapRevokedCredentials(context.Background())
	if _, _, ok := s.credStore.GraceFor("a", time.Now()); !ok {
		t.Fatal("grace value dropped while the server still lists the rotation as ACTIVE")
	}

	mu.Lock()
	cancelled = true
	mu.Unlock()
	s.reapRevokedCredentials(context.Background())
	if _, _, ok := s.credStore.GraceFor("a", time.Now()); ok {
		t.Error("grace value still served after the server stopped listing the rotation")
	}
	if got := s.credStore.Select(ProviderAnthropic, ""); got == nil || got.Token != "sk-new" {
		t.Errorf("credential a itself was disturbed by the grace scrub: %+v", got)
	}
}

// A fetch failure keeps the grace value, exactly as it keeps the credentials:
// the deadline delivered at boot still bounds it, and a crewshipd blip must not
// strip a draining run of the fallback it is about to need.
func TestSidecar_ReapRevokedCredentials_FetchFailureKeepsGrace(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer backend.Close()

	s := newJournalTestServer(backend.URL)
	s.credStore = NewCredStore()
	s.credStore.Load([]Credential{{
		ID: "a", Provider: ProviderAnthropic, Token: "sk-new", GraceToken: "sk-old",
		GraceExpiresAt: rfc3339(time.Now().Add(time.Hour)), GraceRotationID: "rot-a",
	}})
	s.reapRevokedCredentials(context.Background())
	if _, _, ok := s.credStore.GraceFor("a", time.Now()); !ok {
		t.Error("a failed fetch scrubbed the grace value")
	}
}

// The grace fallback observer makes the replay visible as a network.egress
// journal entry (#1882) — the type the sidecar may emit — naming the
// credential and the rotation and carrying no value.
func TestSidecar_GraceFallbackObserver_EmitsAnnotatedEgress(t *testing.T) {
	got := make(chan journalEmitRequest, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req journalEmitRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"jrn_1"}`))
		got <- req
	}))
	defer backend.Close()

	s := newJournalTestServer(backend.URL)
	s.buildGraceFallbackObserver()(GraceFallback{
		CredentialID: "cred_1", RotationID: "rot_1", Provider: "ANTHROPIC",
		AgentID: "agt_1", Host: "api.anthropic.com", Method: "POST", Status: 200,
	})

	var req journalEmitRequest
	select {
	case req = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("backend never received the grace fallback journal entry")
	}
	if req.Type != "network.egress" {
		t.Errorf("type = %q, want network.egress (the only egress type the sidecar may emit)", req.Type)
	}
	for _, want := range []string{"api.anthropic.com", "401", "cred_1", "rot_1", "200"} {
		if !strings.Contains(req.Summary, want) {
			t.Errorf("summary %q does not name %q", req.Summary, want)
		}
	}
	if req.Payload["grace_fallback"] != true || req.Payload["credential_id"] != "cred_1" || req.Payload["rotation_id"] != "rot_1" {
		t.Errorf("payload = %+v, want grace_fallback/credential_id/rotation_id", req.Payload)
	}
	if strings.Contains(req.Summary, "sk-") {
		t.Errorf("summary carries what looks like a key: %q", req.Summary)
	}
}
