//go:build linux && mooring_rust_integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

type mooringObservedPage struct {
	token  mooring.SessionControl
	output protocol.SessionParticipantOutput
}

// Only the first actual native read_result is delayed. The original checked
// output handler remains mandatory; no grant, gate, token or native frame is
// manufactured. Configure runs before the pump goroutine, avoiding a handler race.
func mooringPauseFirstNativePage() (func(*collaborationSessionPumpHandlers), <-chan mooringObservedPage, func()) {
	seen := make(chan mooringObservedPage, 1)
	release := make(chan struct{})
	var once, first sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	configure := func(handlers *collaborationSessionPumpHandlers) {
		original := handlers.OutputControl
		handlers.OutputControl = func(ctx context.Context, token mooring.SessionControl, output protocol.SessionParticipantOutput) error {
			var header struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(output.Message, &header) == nil && header.Type == "read_result" {
				pause := false
				first.Do(func() { pause = true })
				if pause {
					seen <- mooringObservedPage{token, output}
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
			return original(ctx, token, output)
		}
	}
	return configure, seen, resume
}

func startMooringActualPageCall(t *testing.T, x *collaborationAgentTestFixture, m *collaborationHTTPManager) (context.CancelFunc, <-chan *agentToolBufferedWriter) {
	t.Helper()
	r := x.request()
	r.Method = http.MethodGet
	r.URL.RawQuery = "page=1&max_bytes=65536"
	ctx, cancel := context.WithCancel(r.Context())
	done := make(chan *agentToolBufferedWriter, 1)
	go func() {
		w := newAgentToolBufferedWriter(false)
		m.agentTool(w, r.WithContext(ctx))
		done <- w
	}()
	t.Cleanup(cancel)
	return cancel, done
}

func awaitMooringActualPage(t *testing.T, done <-chan *agentToolBufferedWriter) *agentToolBufferedWriter {
	t.Helper()
	select {
	case w := <-done:
		return w
	case <-time.After(3 * time.Second):
		t.Fatal("actual page handler did not finish within existing test bound")
		return nil
	}
}

func awaitMooringNativePage(t *testing.T, seen <-chan mooringObservedPage) mooringObservedPage {
	t.Helper()
	select {
	case event := <-seen:
		return event
	case <-time.After(3 * time.Second):
		t.Fatal("actual native page did not arrive")
		return mooringObservedPage{}
	}
}

func testMooringActualAgentPageTimeout(t *testing.T) {
	configure, seen, resume := mooringPauseFirstNativePage()
	defer resume()
	withMooringAgentToolHostFixtureOutput(t, func(x *collaborationAgentTestFixture, m *collaborationHTTPManager, a *collaborationLiveAgent) {
		cancel, done := startMooringActualPageCall(t, x, m)
		event := awaitMooringNativePage(t, seen)
		cancel() // Deterministic caller cancellation after native response, before delivery.
		w := awaitMooringActualPage(t, done)
		if w.Code != 503 || bytes.Contains(w.Body.Bytes(), []byte("native_json")) || w.flushed {
			t.Fatal("abandoned caller received native content")
		}
		a.mu.Lock()
		pending := a.pageRead
		valid := pending != nil && pending.abandoned && !pending.done && pending.raw == nil
		a.mu.Unlock()
		if !valid {
			t.Fatal("uncertain read correlation was freed or retained private content")
		}
		_, blocked := startMooringActualPageCall(t, x, m)
		if w := awaitMooringActualPage(t, blocked); w.Code != 503 || bytes.Contains(w.Body.Bytes(), []byte("native_json")) {
			t.Fatal("next caller reused the still-unresolved read")
		}
		resume()
		select {
		case <-pending.ready:
		case <-time.After(3 * time.Second):
			t.Fatal("late native read did not retire its exact tombstone")
		}
		a.mu.Lock()
		discarded := pending.done && pending.abandoned && pending.raw == nil
		a.mu.Unlock()
		if !discarded {
			t.Fatal("late abandoned native payload retained")
		}
		_, next := startMooringActualPageCall(t, x, m)
		if w := awaitMooringActualPage(t, next); w.Code != 200 || !w.flushed || !bytes.Contains(w.Body.Bytes(), []byte("native_json")) {
			t.Fatal("fresh read could not pass native pump and physical flush after late response")
		}
		// The next DATA response passed the same serial worker, so the old
		// notification must already be completed exactly once. Duplicate completion
		// must fail without poisoning the healthy runtime or releasing credit twice.
		if a.document.cell.runtime.CompleteControl(event.token) == nil || a.document.cell.runtime.Err() != nil {
			t.Fatal("late output token was not completed exactly once")
		}
	}, configure, "read")
}

func testMooringActualAgentPageCorrelation(t *testing.T) {
	configure, seen, resume := mooringPauseFirstNativePage()
	defer resume()
	withMooringAgentToolHostFixtureOutput(t, func(x *collaborationAgentTestFixture, m *collaborationHTTPManager, a *collaborationLiveAgent) {
		_, done := startMooringActualPageCall(t, x, m)
		event := awaitMooringNativePage(t, seen)
		faults := []struct {
			name   string
			change func(*protocol.SessionParticipantOutput)
		}{
			{"outer-request", func(q *protocol.SessionParticipantOutput) { q.RequestID = "unrelated-read" }},
			{"connection", func(q *protocol.SessionParticipantOutput) { q.ConnectionRef = "other-connection" }},
			{"participant", func(q *protocol.SessionParticipantOutput) { q.ParticipantRef = "other-participant" }},
			{"gate", func(q *protocol.SessionParticipantOutput) { q.GateID = "other-gate" }},
			{"permission", func(q *protocol.SessionParticipantOutput) { q.PermissionVersion = "18446744073709551615" }},
			{"scope", func(q *protocol.SessionParticipantOutput) { q.Scope.WorkspaceID = "other-workspace" }},
			{"session", func(q *protocol.SessionParticipantOutput) { q.SessionID = "other-session" }},
			{"runtime", func(q *protocol.SessionParticipantOutput) { q.RuntimeRef = "other-runtime" }},
			{"fence", func(q *protocol.SessionParticipantOutput) { q.Fence.Generation = "18446744073709551615" }},
		}
		for _, fault := range faults {
			q := event.output
			fault.change(&q)
			if a.output(t.Context(), event.token, q) == nil {
				t.Fatal("negative host correlation accepted", fault.name)
			}
		}
		for _, field := range []string{"request_id", "revision", "oversize"} {
			q := event.output
			var message map[string]json.RawMessage
			if json.Unmarshal(q.Message, &message) != nil {
				t.Fatal("actual output was not JSON")
			}
			switch field {
			case "request_id":
				message[field] = json.RawMessage(`"unrelated-read"`)
			case "revision":
				message[field] = json.RawMessage(`"18446744073709551615"`)
			case "oversize":
				message["items"], _ = json.Marshal([]any{map[string]any{"target_id": "synthetic-fault", "model_type": "synthetic-fault", "revision": "1", "data": map[string]any{"native_json": strings.Repeat("x", 70_000)}}})
			}
			q.Message, _ = json.Marshal(message)
			if a.output(t.Context(), event.token, q) == nil {
				t.Fatal("negative inner correlation/budget accepted", field)
			}
		}
		a.mu.Lock()
		unchanged := a.pageRead != nil && !a.pageRead.done && a.pageRead.raw == nil
		a.mu.Unlock()
		if !unchanged {
			t.Fatal("negative controlled host input consumed original native read")
		}
		resume()
		if w := awaitMooringActualPage(t, done); w.Code != 200 || !w.flushed || !bytes.Contains(w.Body.Bytes(), []byte("native_json")) {
			t.Fatal("negative controls prevented actual original native response and physical flush")
		}
	}, configure, "read")
}

func testMooringActualAgentPageRevocation(t *testing.T) {
	configure, seen, resume := mooringPauseFirstNativePage()
	defer resume()
	withMooringAgentToolHostFixtureOutput(t, func(x *collaborationAgentTestFixture, m *collaborationHTTPManager, a *collaborationLiveAgent) {
		cancel, done := startMooringActualPageCall(t, x, m)
		event := awaitMooringNativePage(t, seen)
		mutation := pagesGrantCall(t, x.f.pages, "DELETE", "/api/v1/pages/agent-shared-page/grants?subject_type=agent&subject="+x.f.a.AgentID+"&level=read", x.key.WorkspaceID, x.f.a.HumanUserID, "MEMBER", "agent-shared-page", "")
		if mutation.Code != 200 {
			t.Fatal("canonical read revocation failed", mutation.Code)
		}
		var closed bool
		if err := x.f.rig.db.QueryRow(`SELECT closed FROM mooring_delivery_gates WHERE id=?`, a.gate.GateID).Scan(&closed); err != nil || !closed {
			t.Fatal("canonical trigger did not close exact read gate", err)
		}
		// Independently exercise the existing final physical-send guard with the
		// actual native response while its ordinary dispatcher is paused. This
		// negative control must refuse BEFORE writing or flushing any bytes.
		final := newAgentToolBufferedWriter(false)
		if err := a.sendStatus(x.request(), final, event.output.Message, http.StatusOK); err == nil || final.Body.Len() != 0 || final.flushed {
			t.Fatal("final current-rights guard delivered revoked native content")
		}
		resume()
		// Wait for the real native reply to pass the original output handler and
		// retire the closed participant; never reopen a SQL gate or disable triggers.
		deadline := time.Now().Add(3 * time.Second)
		for {
			a.mu.Lock()
			retired := a.closed && a.retired != nil
			a.mu.Unlock()
			if retired {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("native page dispatcher did not retire revoked participant")
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
		w := awaitMooringActualPage(t, done)
		if bytes.Contains(w.Body.Bytes(), []byte("native_json")) || w.flushed {
			t.Fatal("ordinary native page dispatcher delivered content after revocation")
		}
		a.mu.Lock()
		retained := a.pageRead != nil && a.pageRead.raw != nil
		a.mu.Unlock()
		if retained {
			t.Fatal("revoked read retained a native payload")
		}
	}, configure, "read")
}
