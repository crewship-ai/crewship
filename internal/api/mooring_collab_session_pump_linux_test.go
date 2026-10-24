//go:build linux

package api

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

func pumpFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../mooring/protocol/session-control-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Cases []struct {
			ID      string         `json:"id"`
			Message map[string]any `json:"message"`
		} `json:"cases"`
	}
	if json.Unmarshal(raw, &fixtures) != nil {
		t.Fatal("fixture JSON")
	}
	for _, entry := range fixtures.Cases {
		if entry.ID == "valid-"+name {
			return entry.Message
		}
	}
	t.Fatal("missing fixture", name)
	return nil
}

func pumpConvert[T any](t *testing.T, value any) T {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result T
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// This helper is an actual inherited-FD child with a MOCK application. It has
// no SQL, authentication, WordCraft engine or ability to establish Saved.
func TestCollaborationSessionPumpChild(t *testing.T) {
	mode := ""
	for i, arg := range os.Args {
		if arg == "--pump-peer" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
		}
	}
	if mode == "" {
		return
	}
	f := os.NewFile(3, "pump-control")
	conn, err := net.FileConn(f)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	defer conn.Close()
	read := func() (map[string]any, error) {
		var header [4]byte
		if _, e := io.ReadFull(conn, header[:]); e != nil {
			return nil, e
		}
		n := binary.BigEndian.Uint32(header[:])
		if n == 0 || n > 2<<20 {
			return nil, errors.New("mock frame bound")
		}
		raw := make([]byte, n)
		if _, e := io.ReadFull(conn, raw); e != nil {
			return nil, e
		}
		var value map[string]any
		e := json.Unmarshal(raw, &value)
		return value, e
	}
	write := func(value any) bool {
		raw, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
		if _, e = io.Copy(conn, strings.NewReader(string(header[:])+string(raw))); e != nil {
			return false
		}
		return true
	}
	_, err = read()
	if err != nil {
		return
	}
	if !write(map[string]any{"type": "runtime_ready", "request_id": "host-start", "selected": map[string]any{"protocol_version": "0.4.0", "model_version": "0.2.0", "engine_version": "0.10.0"}, "selected_control_profile": mooring.PrivateSessionProfile}) {
		return
	}
	open, err := read()
	if err != nil {
		return
	}
	scope, ref, session := open["scope"], open["runtime_ref"], open["session_id"]
	if !write(map[string]any{"type": "owner_acquire", "request_id": "acquire", "scope": scope, "runtime_ref": ref, "renewal_nonce": "nonce"}) {
		return
	}
	grant, err := read()
	if err != nil {
		return
	}
	if !write(map[string]any{"type": "session_opened", "request_id": open["request_id"], "control_profile": mooring.PrivateSessionProfile, "scope": scope, "session_id": session, "runtime_ref": ref, "versions": open["versions"], "fence": grant["fence"], "confirmed_basis": map[string]any{"revision": "0", "digest": "sha256:" + strings.Repeat("0", 64)}}) {
		return
	}
	bind := func(value map[string]any, id string) map[string]any {
		value["request_id"], value["scope"], value["session_id"], value["runtime_ref"], value["fence"] = id, scope, session, ref, grant["fence"]
		return value
	}
	a := bind(pumpFixture(t, "SessionSubmitBatch"), "A")
	if !write(a) {
		return
	}
	if mode == "output" && !write(bind(pumpFixture(t, "SessionParticipantOutput"), "notification")) {
		return
	}
	if mode == "count" || mode == "bytes" {
		for i := 0; i < 140; i++ {
			v := bind(pumpFixture(t, "SessionSubmitBatch"), fmt.Sprintf("flood-%d", i))
			if mode == "bytes" {
				v["changes"].([]any)[0].(map[string]any)["operations"].([]any)[0].(map[string]any)["payload"] = map[string]any{"padding": strings.Repeat("x", 230*1024)}
			}
			if !write(v) {
				return
			}
		}
		_, _ = read()
		return
	}
	if !write(bind(pumpFixture(t, "SessionSubmitBatch"), "B")) {
		return
	}
	if !write(bind(pumpFixture(t, "SessionStatusRequest"), "status")) {
		return
	}
	renew := func(id string) map[string]any {
		return map[string]any{"type": "owner_renew", "request_id": id, "scope": scope, "session_id": session, "fence": grant["fence"], "renewal_nonce": id, "permission_version": "1"}
	}
	if !write(renew("renew")) {
		return
	}
	if !write(map[string]any{"type": "delivery_renew", "request_id": "delivery", "scope": scope, "session_id": session, "participant_ref": "participant", "fence": grant["fence"], "permission_version": "1", "renewal_nonce": "delivery-nonce", "gate_id": "gate"}) {
		return
	}
	var mutations []string
	for {
		v, e := read()
		if e != nil {
			return
		}
		id, _ := v["request_id"].(string)
		if id == "renew" {
			if mode == "death" {
				return
			}
			if !write(renew("renew-roundtrip")) {
				return
			}
		}
		if id == "delivery" && !write(renew("delivery-roundtrip")) {
			return
		}
		if v["type"] == "session_commit_result" || v["type"] == "session_status_result" {
			mutations = append(mutations, id)
			if len(mutations) == 3 {
				if strings.Join(mutations, ",") != "A,B,status" {
					t.Fatal("mock observed reordered replies")
				}
				if !write(renew("replies-ordered")) {
					return
				}
			}
		}
	}
}

type pumpTestRig struct {
	runtime *mooring.SessionRuntime
	pump    *collaborationSessionPump
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan error
	opened  chan error
	entered chan struct{}
	release chan struct{}
	renewed chan string
	mu      sync.Mutex
	order   []string
	other   atomic.Int32
}

func newPumpTestRig(t *testing.T, mode, outcome string) *pumpTestRig {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	binaryPath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "mock-session-peer")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	if err = os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+quote(binaryPath)+" -test.run=^TestCollaborationSessionPumpChild$ -- --pump-peer "+quote(mode)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	versions := mooring.RuntimeVersions{Protocol: "0.4.0", Model: "0.2.0", Engine: "0.10.0"}
	runtime, err := mooring.StartSessionRuntime(ctx, mooring.SessionRuntimeConfig{Runtime: mooring.RuntimeConfig{Enabled: true, Binary: wrapper, Versions: versions, ControlProfile: mooring.PrivateSessionProfile}, Scope: mooring.SessionKey{WorkspaceID: "workspace", ArtifactID: "artifact", ApplicationID: "wordcraft"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	r := &pumpTestRig{runtime: runtime, ctx: ctx, cancel: cancel, done: make(chan error, 1), opened: make(chan error, 1), entered: make(chan struct{}), release: make(chan struct{}), renewed: make(chan string, 8)}
	owner := func(id protocol.Identifier, nonce protocol.Identifier, scope protocol.Scope, ref protocol.Identifier) protocol.OwnerGrant {
		return protocol.OwnerGrant{Type: "owner_grant", RequestID: id, Scope: scope, SessionID: "host-session", RuntimeRef: ref, Fence: protocol.Fence{Generation: "1", Token: strings.Repeat("x", 32)}, LeaseDurationMs: "60000", Revision: "0", RenewalNonce: nonce, PermissionVersion: "1"}
	}
	h := collaborationSessionPumpHandlers{
		Acquire: func(_ context.Context, q protocol.OwnerAcquire) (protocol.OwnerGrant, error) {
			return owner(q.RequestID, q.RenewalNonce, q.Scope, protocol.Identifier(runtime.RuntimeRef())), nil
		},
		Renew: func(_ context.Context, q protocol.OwnerRenew) (protocol.OwnerGrant, error) {
			if outcome == "control-panic" {
				panic("private mock management panic")
			}
			r.renewed <- string(q.RequestID)
			return owner(q.RequestID, q.RenewalNonce, q.Scope, protocol.Identifier(runtime.RuntimeRef())), nil
		},
		Delivery: func(_ context.Context, q protocol.DeliveryRenew) (protocol.DeliveryGrant, error) {
			return protocol.DeliveryGrant{Type: "delivery_grant", RequestID: q.RequestID, Scope: q.Scope, SessionID: q.SessionID, ParticipantRef: q.ParticipantRef, Generation: q.Fence.Generation, PermissionVersion: q.PermissionVersion, RenewalNonce: q.RenewalNonce, DurationMs: "1000", GateID: q.GateID}, nil
		},
		Submit: func(ctx context.Context, q protocol.SessionSubmitBatch) (protocol.SessionCommitResult, error) {
			r.mu.Lock()
			r.order = append(r.order, string(q.RequestID))
			r.mu.Unlock()
			if q.RequestID == "A" {
				close(r.entered)
				select {
				case <-ctx.Done():
					return protocol.SessionCommitResult{}, ctx.Err()
				case <-r.release:
				}
			} else {
				r.other.Add(1)
			}
			if q.RequestID == "A" && outcome == "error" {
				return protocol.SessionCommitResult{}, errors.New("uncertain mock SQL result")
			}
			if q.RequestID == "A" && outcome == "panic" {
				panic("mock private callback panic")
			}
			v := pumpFixture(t, "SessionCommitResult")
			v["request_id"], v["scope"], v["session_id"], v["runtime_ref"], v["fence"] = q.RequestID, q.Scope, q.SessionID, q.RuntimeRef, q.Fence
			// Deliberately MOCK application receipt: no Saved claim is established.
			result := pumpConvert[protocol.SessionCommitResult](t, v)
			if outcome == "binding" {
				result.RequestID = "foreign-request"
			}
			if q.RequestID == "A" && (outcome == "unknown" || outcome == "root-unknown") {
				reason := map[string]any{"code": "unknown_outcome", "message": "uncertain", "retry": "same_operation"}
				if outcome == "root-unknown" {
					reason = map[string]any{"code": "atomic_aborted", "root_cause": map[string]any{"code": "unknown_outcome"}}
				}
				raw, _ := json.Marshal(map[string]any{"op_id": "o1", "status": "rejected", "reason": reason})
				result.Receipt.Changes[0].Operations[0] = raw
			}
			return result, nil
		},
		Status: func(_ context.Context, q protocol.SessionStatusRequest) (protocol.SessionStatusResult, error) {
			r.mu.Lock()
			r.order = append(r.order, "status")
			r.mu.Unlock()
			r.other.Add(1)
			v := pumpFixture(t, "SessionStatusResult")
			v["request_id"], v["scope"], v["session_id"], v["runtime_ref"], v["fence"] = q.RequestID, q.Scope, q.SessionID, q.RuntimeRef, q.Fence
			result := pumpConvert[protocol.SessionStatusResult](t, v)
			result.Message.Result = json.RawMessage(`{"op_id":"o1","status":"unknown"}`)
			return result, nil
		},
	}
	if outcome == "missing-renew" {
		h.Renew = nil
	}
	r.pump, err = newCollaborationSessionPump(runtime, h)
	if err != nil {
		t.Fatal(err)
	}
	go func() { r.done <- r.pump.run(ctx) }()
	go func() {
		_, e := runtime.Open(ctx, protocol.SessionOpen{Type: "session_open", RequestID: "open", ControlProfile: mooring.PrivateSessionProfile, Scope: protocol.Scope{WorkspaceID: "workspace", ArtifactID: "artifact", ApplicationID: "wordcraft"}, SessionID: "host-session", RuntimeRef: protocol.Identifier(runtime.RuntimeRef()), Versions: protocol.Versions{ProtocolVersion: "0.4.0", ModelVersion: "0.2.0", EngineVersion: "0.10.0"}})
		r.opened <- e
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(2 * time.Second):
			t.Error("pump did not stop")
		}
	})
	return r
}

func pumpWait[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		t.Fatal("pump wait", ctx.Err())
		var zero T
		return zero
	}
}

func TestCollaborationSessionPumpFIFOAndIndependentOwnerRenewal(t *testing.T) {
	r := newPumpTestRig(t, "normal", "")
	if err := pumpWait(t, r.ctx, r.opened); err != nil {
		t.Fatal(err)
	}
	pumpWait(t, r.ctx, r.entered)
	seen := map[string]bool{}
	for !seen["renew-roundtrip"] || !seen["delivery-roundtrip"] {
		seen[pumpWait(t, r.ctx, r.renewed)] = true
	}
	if r.other.Load() != 0 {
		t.Fatal("B/status passed withheld A")
	}
	close(r.release)
	for pumpWait(t, r.ctx, r.renewed) != "replies-ordered" {
	}
	r.mu.Lock()
	order := strings.Join(r.order, ",")
	r.mu.Unlock()
	if order != "A,B,status" {
		t.Fatal(order)
	}
	if err := r.pump.run(r.ctx); err == nil {
		t.Fatal("same pump acquired a second consumer")
	}
}

func TestCollaborationSessionPumpUncertaintyNeverRunsNextMutation(t *testing.T) {
	for _, outcome := range []string{"error", "unknown", "root-unknown", "binding", "panic"} {
		t.Run(outcome, func(t *testing.T) {
			r := newPumpTestRig(t, "normal", outcome)
			pumpWait(t, r.ctx, r.entered)
			for pumpWait(t, r.ctx, r.renewed) != "renew-roundtrip" {
			}
			close(r.release)
			pumpWait(t, r.ctx, r.runtime.Done())
			if r.other.Load() != 0 {
				t.Fatal("uncertain prefix ran B/status")
			}
			err := pumpWait(t, r.ctx, r.done)
			r.done <- err // Preserve the cleanup join's observation.
			if err != errCollaborationSessionPump {
				t.Fatal("nonopaque pump error", err)
			}
		})
	}
}

func TestCollaborationSessionPumpCancellationDeathAndTransportBounds(t *testing.T) {
	for _, mode := range []string{"cancel", "death", "count", "bytes"} {
		t.Run(mode, func(t *testing.T) {
			r := newPumpTestRig(t, mode, "")
			if mode == "cancel" {
				pumpWait(t, r.ctx, r.entered)
				r.cancel()
			}
			select {
			case <-r.runtime.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("child remained alive")
			}
			if r.other.Load() != 0 {
				t.Fatal("poisoned prefix ran next mutation")
			}
		})
	}
}

func TestCollaborationSessionPumpHasNoDefaultHandler(t *testing.T) {
	if _, err := newCollaborationSessionPump(nil, collaborationSessionPumpHandlers{}); err == nil {
		t.Fatal("nil runtime/handlers accepted")
	}
	if _, err := newCollaborationSessionPump(&mooring.SessionRuntime{}, collaborationSessionPumpHandlers{Submit: func(context.Context, protocol.SessionSubmitBatch) (protocol.SessionCommitResult, error) {
		return protocol.SessionCommitResult{}, nil
	}, Status: func(context.Context, protocol.SessionStatusRequest) (protocol.SessionStatusResult, error) {
		return protocol.SessionStatusResult{}, nil
	}}); err == nil {
		t.Fatal("zero runtime accepted")
	}
	for _, entry := range []struct{ mode, outcome string }{{"normal", "missing-renew"}, {"output", ""}, {"normal", "control-panic"}} {
		t.Run(entry.mode+entry.outcome, func(t *testing.T) {
			r := newPumpTestRig(t, entry.mode, entry.outcome)
			select {
			case <-r.runtime.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("unhandled control left child alive")
			}
			if r.other.Load() != 0 {
				t.Fatal("unhandled control ran B/status")
			}
		})
	}
}
