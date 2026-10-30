package chatbridge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/ws"
)

type ipcOutcomeResolver struct {
	*capResolver
	ipc *IPCResolver
}

func (r *ipcOutcomeResolver) UpdateRunOutcome(ctx context.Context, id, status string, exit *int, message *string, metadata map[string]interface{}, completed string) (string, error) {
	return r.ipc.UpdateRunOutcome(ctx, id, status, exit, message, metadata, completed)
}

type legacyOutcomeResolver struct {
	ChatResolver
	HumanChatResolver
}

// The actual internal API may acknowledge an already-COMPLETED run or a work
// projection instead of the requested CANCELLED state. HTTP200 alone is not a
// cancellation receipt. Exercise the production IPC parser from the real Bridge.
func TestHandleChatMessageCancellationRequiresAcknowledgedOutcome(t *testing.T) {
	for _, scenario := range []string{"cancelled", "completed", "malformed", "wrong-id", "empty", "oversized", "legacy-resolver", "non-explicit-cause"} {
		t.Run(scenario, func(t *testing.T) {
			var received atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if r.Method != http.MethodPatch || json.NewDecoder(r.Body).Decode(&request) != nil || request["status"] != "CANCELLED" {
					t.Errorf("unexpected update %s %s", r.Method, r.URL.Path)
				}
				received.Store(true)
				id := path.Base(r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				switch scenario {
				case "malformed":
					_, _ = w.Write([]byte(`{"status":`))
				case "wrong-id":
					_ = json.NewEncoder(w).Encode(map[string]string{"id": "other-run", "status": "CANCELLED"})
				case "empty":
				case "oversized":
					_, _ = w.Write([]byte(strings.Repeat(" ", 8193)))
				default:
					status := "CANCELLED"
					if scenario == "completed" {
						status = "COMPLETED"
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "status": status})
				}
			}))
			defer server.Close()
			base := &capResolver{info: baseInfo()}
			var resolver ChatResolver = &ipcOutcomeResolver{capResolver: base, ipc: NewIPCResolver(server.URL, "synthetic", slog.Default())}
			if scenario == "legacy-resolver" {
				resolver = &legacyOutcomeResolver{ChatResolver: base, HumanChatResolver: base}
			}
			ctr := &scriptedContainer{agentOutput: claudeSuccessOutput(0), exitCode: 0}
			bridge := testBridgeWithContainer(t, resolver, ctr)
			// Only the hub's explicit stop cause may be confirmed; a run
			// context ended by anything else (e.g. server shutdown) is
			// recorded CANCELLED but never confirmed to a requester.
			cause := ws.ErrExplicitStopRequested
			if scenario == "non-explicit-cause" {
				cause = errors.New("hub shutting down")
			}
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			err := bridge.HandleChatMessage(ctx, "user-1", "cancel-receipt", "hi", func(e ws.ChatEvent) {
				if e.Type == "text" {
					cancel(cause)
				}
			})
			if got := errors.Is(err, ws.ErrRunCancelled); got != (scenario == "cancelled") {
				t.Fatalf("sentinel=%v scenario=%s err=%v", got, scenario, err)
			}
			if scenario != "legacy-resolver" && !received.Load() {
				t.Fatal("real IPC update not exercised")
			}
		})
	}
}

// A stop that races an already terminal legacy execution must preserve the
// ordinary accumulator/exit classification rather than overwriting it CANCELLED.
type lateStopOutcomeContainer struct{ *scriptedContainer }

func (c *lateStopOutcomeContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, c.exitCode, nil
}

func TestHandleChatMessageCancelRacePreservesNaturalOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, output, status string
		exit                 int
	}{
		{"success", claudeSuccessOutput(0), "COMPLETED", 0},
		{"failure", claudeSuccessOutput(0), "FAILED", 1},
		{"in-band-failure", `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"failed"}` + "\n", "FAILED", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &capResolver{info: baseInfo()}
			container := &lateStopOutcomeContainer{scriptedContainer: &scriptedContainer{agentOutput: tc.output, exitCode: tc.exit}}
			bridge := testBridgeWithContainer(t, resolver, container)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			err := bridge.HandleChatMessage(ctx, "user-1", "late-stop-"+tc.name, "hello", func(e ws.ChatEvent) {
				if e.Type == "result" {
					cancel()
				}
			})
			if errors.Is(err, ws.ErrRunCancelled) {
				t.Fatalf("natural %s was confirmed cancelled: %v", tc.name, err)
			}
			if len(resolver.runUpdates) != 1 || resolver.runUpdates[0].status != tc.status {
				t.Fatalf("natural %s terminal updates=%+v err=%v", tc.name, resolver.runUpdates, err)
			}
		})
	}
}
