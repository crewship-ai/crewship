package ws

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// captureLogger returns a logger recording every record (debug and up) as
// one JSON line per call into buf, with no global side effects — unlike
// internal/logging.New, which stamps a process-wide runtime level control
// that a t.Parallel subtest here would race against.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// drainChatEvents reads frames off a client's send channel until it sees a
// "done" chat_event (or times out), returning the chat_event payload types
// seen in order (run_begin and other non-chat_event frames are skipped, as
// in TestHandleSendMessageAgentBusySenderOnly). It also returns the raw
// error-typed payload contents, keyed by their position among the returned
// types, for cases that need to inspect WHICH error string was sent.
func drainChatEvents(t *testing.T, ch <-chan []byte) (types []string, contents []string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case raw := <-ch:
			var msg ServerMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				t.Fatalf("unmarshal frame: %v (%s)", err, raw)
			}
			if msg.Type != "chat_event" {
				continue // run_begin etc.
			}
			pmap, _ := msg.Payload.(map[string]interface{})
			typ, _ := pmap["type"].(string)
			content, _ := pmap["content"].(string)
			types = append(types, typ)
			contents = append(contents, content)
			if typ == "done" {
				return types, contents
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a terminal done; saw so far: %v", types)
		}
	}
}

// TestHandleSendMessage_ProvisioningSentinel is the table-driven regression
// test for the "expected state reported as a failure" bug: a crew whose
// image is being auto-built triggers a control-flow sentinel
// (ws.ErrCrewProvisioning), not a real failure, and the ws layer must not
// treat the two the same way.
//
// Each case comments the specific symptom it prevents.
func TestHandleSendMessage_ProvisioningSentinel(t *testing.T) {
	cases := []struct {
		name string
		// err is what the stub ChatHandler returns from HandleChatMessage.
		err error
		// events are what the handler streams BEFORE returning err — mirrors
		// what chatbridge.HandleChatMessage does on each real path.
		events []ChatEvent

		// wantTypes is the exact, in-order sequence of chat_event payload
		// types the client must receive.
		wantTypes []string
		// wantErrorContains, if set, must be a substring of the (single)
		// error event's content.
		wantErrorContains string
		// wantLogContains/wantLogExcludes assert on the captured log output.
		wantLogContains []string
		wantLogExcludes []string
	}{
		{
			// Symptom prevented: a crew auto-provisioning for the first time
			// (or after its cached image was pruned) used to reach the user
			// as a red "an error occurred processing your message" bubble
			// STACKED UNDER the informative build card, and paged as an
			// ERROR-level "chat message error" log line for a state that is
			// completely expected on a first-run crew.
			name: "provisioning sentinel: non-error event, no fallback, no ERROR log",
			err:  fmt.Errorf("crew %q provisioning kicked off: %w", "cli-test", ErrCrewProvisioning),
			events: []ChatEvent{
				{
					Type:    "crew_provisioning",
					Content: "cli-test's environment is being built — this is a one-time setup step. Resend your message once the build finishes.",
					Metadata: map[string]any{
						"crew_id":   "crew-1",
						"crew_slug": "cli-test",
						"status":    "pending",
					},
				},
			},
			wantTypes:       []string{"crew_provisioning", "done"},
			wantLogContains: []string{"message deferred: crew provisioning in progress"},
			wantLogExcludes: []string{"chat message error", `"level":"ERROR"`},
		},
		{
			// Symptom prevented: this test would also pass on code that
			// swallows EVERY non-nil error from HandleChatMessage — it pins
			// that a REAL failure (enqueue never started, nothing built) still
			// reaches the user as an error and still pages as ERROR.
			name:              "genuine provisioning failure still produces an error",
			err:               fmt.Errorf("auto-provision enqueue failed for crew %q: %w", "cli-test", fmt.Errorf("docker daemon unreachable")),
			events:            nil, // handler stayed silent — the ws fallback must speak
			wantTypes:         []string{"error", "done"},
			wantErrorContains: "an error occurred processing your message",
			wantLogContains:   []string{"chat message error", `"level":"ERROR"`},
		},
		{
			// Symptom prevented (#1386's original bug, re-pinned here): a
			// handler that already streamed its OWN classified error must not
			// have the generic fallback stacked under it — the double red box.
			name: "handler-classified error is not double-fired",
			err:  fmt.Errorf("container provider not configured"),
			events: []ChatEvent{
				{Type: "error", Content: "The agent container failed to start (provisioning error)"},
			},
			wantTypes:         []string{"error", "done"},
			wantErrorContains: "The agent container failed to start (provisioning error)",
			wantLogContains:   []string{"chat message error"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var logBuf bytes.Buffer
			hub := newRunningHub(t, withLogger(captureLogger(&logBuf)))
			hub.SetChannelAuthorizer(allowAllAuthorizer{})
			hub.SetChatHandler(&stubChatHandler{err: tc.err, events: tc.events})

			sender := newClient(t, hub, "u-1")

			body, _ := json.Marshal(sendMessagePayload{ChatID: "s1", Content: "hello"})
			sender.handleSendMessage(ClientMessage{Type: "send_message", Channel: "session:s1", Payload: body})

			types, contents := drainChatEvents(t, sender.send)
			if len(types) != len(tc.wantTypes) {
				t.Fatalf("event types = %v, want %v", types, tc.wantTypes)
			}
			for i, want := range tc.wantTypes {
				if types[i] != want {
					t.Errorf("event[%d].type = %q, want %q (full: %v)", i, types[i], want, types)
				}
			}
			if tc.wantErrorContains != "" {
				var errContent string
				for i, typ := range types {
					if typ == "error" {
						errContent = contents[i]
					}
				}
				if !strings.Contains(errContent, tc.wantErrorContains) {
					t.Errorf("error content = %q, want it to contain %q", errContent, tc.wantErrorContains)
				}
			}

			logged := logBuf.String()
			for _, want := range tc.wantLogContains {
				if !strings.Contains(logged, want) {
					t.Errorf("log output missing %q; got:\n%s", want, logged)
				}
			}
			for _, exclude := range tc.wantLogExcludes {
				if strings.Contains(logged, exclude) {
					t.Errorf("log output must not contain %q; got:\n%s", exclude, logged)
				}
			}
		})
	}
}
