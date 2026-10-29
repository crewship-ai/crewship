package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Real page/source/SQL policy through the handler; controlled response deadlines
// and a non-negotiated runtime/binding fixture. This is NOT native qualification.
func TestCollaborationHumanContinuityHTTPBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, body, origin      string
		closed, retired, reader bool
		status                  int
	}{
		{name: "same writer", body: `{"connection_ref":"live-connection"}`, origin: "https://studio.example", status: 204},
		{name: "reader remains reader", body: `{"connection_ref":"live-connection"}`, origin: "https://studio.example", reader: true, status: 204},
		{name: "foreign connection", body: `{"connection_ref":"another-connection"}`, origin: "https://studio.example", status: 403},
		{name: "cross origin", body: `{"connection_ref":"live-connection"}`, origin: "https://foreign.example", status: 403},
		{name: "missing origin", body: `{"connection_ref":"live-connection"}`, status: 403},
		{name: "casefold field", body: `{"CONNECTION_REF":"live-connection"}`, origin: "https://studio.example", status: 400},
		{name: "casefold override", body: `{"connection_ref":"live-connection","CONNECTION_REF":"another"}`, origin: "https://studio.example", status: 400},
		{name: "unknown field", body: `{"connection_ref":"live-connection","write":true}`, origin: "https://studio.example", status: 400},
		{name: "duplicate field", body: `{"connection_ref":"live-connection","connection_ref":"live-connection"}`, origin: "https://studio.example", status: 400},
		{name: "oversized", body: `{"connection_ref":"` + strings.Repeat("a", 1100) + `"}`, origin: "https://studio.example", status: 413},
		{name: "closed", body: `{"connection_ref":"live-connection"}`, origin: "https://studio.example", closed: true, status: 403},
		{name: "retired", body: `{"connection_ref":"live-connection"}`, origin: "https://studio.example", retired: true, status: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newContinuityRig(t, !tc.reader)
			f.advance(t, 4*time.Minute)
			storeBinding, err := f.s.storeBinding()
			if err != nil {
				t.Fatal(err)
			}
			ticket, err := f.s.reconnect(f.ctx, storeBinding, f.owner, f.admission.participant)
			if err != nil {
				t.Fatal(err)
			}
			joined, err := f.s.join(f.ctx, f.s.runtime, f.owner, ticket, "http-join")
			if err != nil {
				t.Fatal(err)
			}
			m := &collaborationHTTPManager{pages: f.s.pages, fence: f.s.fence, clock: f.clock, config: MooringHTTPConfig{PublicOrigin: "https://studio.example"}, docs: make(map[mooring.SessionKey]*collaborationHTTPDocument)}
			d := &collaborationHTTPDocument{manager: m, key: f.s.scope, ctx: t.Context(), owner: f.owner, humans: f.s, connections: make(map[string]*collaborationHTTPConnection)}
			c := &collaborationHTTPConnection{document: d, admission: f.admission, joined: joined, ref: "live-connection", used: true, closed: tc.closed, joinedDelivered: true, auditedOwner: f.r.userID, participantBinding: &mooring.SessionParticipantBinding{}}
			if tc.retired {
				c.participantBinding = nil
			}
			m.docs[f.s.scope] = d
			d.connections[c.ref] = c
			old := f.admission.currentDeadline()
			ctx := context.WithValue(f.ctx, ctxWorkspaceID, f.r.wsID)
			req := httptest.NewRequest("POST", "https://studio.example/api/v1/pages/@admission-page/mooring/session/reauthorize", strings.NewReader(tc.body)).WithContext(ctx)
			req.SetPathValue("slug", "@admission-page")
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			w := &collaborationSessionIOWriter{ResponseRecorder: httptest.NewRecorder(), fence: m.fence}
			m.fence.Wrap(http.HandlerFunc(m.reauthorize)).ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.status == 204 {
				if f.admission.currentDeadline() <= old {
					t.Fatal("success did not refresh original")
				}
				if !w.flushHeld {
					t.Fatal("response escaped ingress lease")
				}
				if tc.reader && f.check(t, true) == nil {
					t.Fatal("reader acquired write")
				}
			} else if f.admission.currentDeadline() != old {
				t.Fatal("failed HTTP request refreshed original")
			}
		})
	}
}

type continuityBlockedFlushWriter struct {
	*collaborationSessionIOWriter
	entered, release chan struct{}
	once             sync.Once
}

func (w *continuityBlockedFlushWriter) FlushError() error {
	w.once.Do(func() { close(w.entered); <-w.release })
	return w.collaborationSessionIOWriter.FlushError()
}

func TestCollaborationHumanContinuitySlowResponseDoesNotBlockDelivery(t *testing.T) {
	f := newContinuityRig(t, true)
	f.advance(t, 4*time.Minute)
	storeBinding, err := f.s.storeBinding()
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := f.s.reconnect(f.ctx, storeBinding, f.owner, f.admission.participant)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := f.s.join(f.ctx, f.s.runtime, f.owner, ticket, "blocked-http-join")
	if err != nil {
		t.Fatal(err)
	}
	m := &collaborationHTTPManager{pages: f.s.pages, fence: f.s.fence, clock: f.clock, config: MooringHTTPConfig{PublicOrigin: "https://studio.example"}, docs: make(map[mooring.SessionKey]*collaborationHTTPDocument)}
	d := &collaborationHTTPDocument{manager: m, key: f.s.scope, ctx: t.Context(), owner: f.owner, humans: f.s, connections: make(map[string]*collaborationHTTPConnection)}
	c := &collaborationHTTPConnection{document: d, admission: f.admission, joined: joined, ref: "live-connection", used: true, joinedDelivered: true, auditedOwner: f.r.userID, participantBinding: &mooring.SessionParticipantBinding{}}
	m.docs[f.s.scope] = d
	d.connections[c.ref] = c
	ctx := context.WithValue(f.ctx, ctxWorkspaceID, f.r.wsID)
	req := httptest.NewRequest("POST", "https://studio.example/reauthorize", strings.NewReader(`{"connection_ref":"live-connection"}`)).WithContext(ctx)
	req.SetPathValue("slug", "@admission-page")
	req.Header.Set("Origin", "https://studio.example")
	w := &continuityBlockedFlushWriter{collaborationSessionIOWriter: &collaborationSessionIOWriter{ResponseRecorder: httptest.NewRecorder(), fence: m.fence}, entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); m.fence.Wrap(http.HandlerFunc(m.reauthorize)).ServeHTTP(w, req) }()
	defer func() { close(w.release); <-done }()
	select {
	case <-w.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("response did not reach flush")
	}
	// Real connection lock ordering and SQL DeliveryStore; no native transport is
	// claimed. A blocked HTTP response must not pin gate renewal/output/close locks.
	if !c.deliveryMu.TryLock() {
		t.Fatal("slow response blocks delivery")
	}
	defer c.deliveryMu.Unlock()
	if !c.mu.TryLock() {
		t.Fatal("slow response blocks output/close")
	}
	defer c.mu.Unlock()
	grant, err := joined.delivery.Renew(f.ctx, f.owner, joined.gate.GateID, "during-blocked-http-response")
	if err != nil || grant.GateID != joined.gate.GateID {
		t.Fatal("delivery renewal blocked by unrelated response", err)
	}
}
