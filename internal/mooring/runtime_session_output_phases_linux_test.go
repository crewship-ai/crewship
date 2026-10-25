//go:build linux

package mooring

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Actual inherited-FD transport, MOCK SQL/receipt semantics. The host borrows
// Pending and deliberately withholds completion, as a blocked physical send
// does. A subsequent host request is the barrier permitting the second phase.
func TestSessionOutputPhaseTransportHelper(t *testing.T) {
	mode := ""
	for i, arg := range os.Args {
		if arg == "--output-phase-peer" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
		}
	}
	if mode == "" {
		return
	}
	f := os.NewFile(3, "output-phase-rpc")
	conn, err := net.FileConn(f)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	rpc := &RPC{conn: conn}
	defer rpc.Close()
	write := func(v any) {
		raw, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		if e = rpc.Write(raw); e != nil {
			t.Fatal(e)
		}
	}
	if _, err = rpc.Read(); err != nil {
		t.Fatal(err)
	}
	write(map[string]any{"type": "runtime_ready", "request_id": "host-start", "selected": sessionTestVersions, "selected_control_profile": PrivateSessionProfile})
	rpc.maxFrame = MaxPrivatePrepareRPCFrame
	raw, err := rpc.Read()
	if err != nil {
		t.Fatal(err)
	}
	var open map[string]any
	if json.Unmarshal(raw, &open) != nil {
		t.Fatal("open")
	}
	fence := map[string]any{"generation": "1", "token": "01234567890123456789012345678901"}
	write(map[string]any{"type": "session_opened", "request_id": open["request_id"], "control_profile": PrivateSessionProfile, "scope": open["scope"], "session_id": open["session_id"], "runtime_ref": open["runtime_ref"], "versions": sessionTestVersions, "capabilities": []string{"checkpoint_identity_v1"}, "fence": fence, "confirmed_basis": map[string]any{"revision": "0", "digest": "sha256:" + string(makeDigestZeros())}})
	pending := map[string]any{"type": "changes_received", "request_id": "original-public-request", "scope": open["scope"], "session_id": open["session_id"], "change_ids": []string{"original-change", "second-change"}, "state": "pending"}
	output := map[string]any{"type": "session_participant_output", "request_id": "original-outer-request", "control_profile": PrivateSessionProfile, "scope": open["scope"], "session_id": open["session_id"], "runtime_ref": open["runtime_ref"], "fence": fence, "connection_ref": "connection", "participant_ref": "participant", "gate_id": "gate", "permission_version": "1", "message": pending}
	write(output)
	// No second notification can be emitted until Pending is borrowed. This
	// barrier is an ordinary private prepare request, not an ACK of Pending.
	raw, err = rpc.Read()
	if err != nil {
		t.Fatal(err)
	}
	var barrier map[string]any
	if json.Unmarshal(raw, &barrier) != nil {
		t.Fatal("barrier")
	}
	terminal := map[string]any{"type": "commit_result", "request_id": "original-public-request", "scope": open["scope"], "generation": "1", "revision": "2", "issuer": "crewship", "durability": "node", "changes": []any{
		map[string]any{"change_id": "original-change", "status": "saved", "first_revision": "1", "last_revision": "1", "operations": []any{map[string]any{"op_id": "original-op", "status": "saved", "revision": "1", "issuer": "crewship", "durability": "node"}}},
		map[string]any{"change_id": "second-change", "status": "saved", "first_revision": "2", "last_revision": "2", "operations": []any{map[string]any{"op_id": "second-op", "status": "saved", "revision": "2", "issuer": "crewship", "durability": "node"}}},
	}}
	switch mode {
	case "contradictory-pending":
		pending["state"] = "synchronized"
	case "exact-pending-retry":
	default:
		output["message"] = terminal
	}
	switch mode {
	case "cross-wrapper":
		output["connection_ref"] = "other-connection"
	case "cross-generation":
		output["fence"] = map[string]any{"generation": "2", "token": fence["token"]}
		terminal["generation"] = "2"
	case "cross-change-ids":
		terminal["changes"].([]any)[0].(map[string]any)["change_id"] = "other-change"
	case "reordered-change-ids":
		changes := terminal["changes"].([]any)
		changes[0], changes[1] = changes[1], changes[0]
	case "cross-inner-request":
		terminal["request_id"] = "other-public-request"
	case "ordinary-id-collision":
		write(map[string]any{"type": "owner_acquire", "request_id": "original-outer-request", "scope": open["scope"], "runtime_ref": open["runtime_ref"], "renewal_nonce": "nonce"})
	}
	write(output)
	if mode == "both-held-exact-retries" || mode == "contradictory-terminal" {
		output["message"] = pending
		write(output)
		output["message"] = terminal
		if mode == "contradictory-terminal" {
			terminal["durability"] = "replicated"
		}
		write(output)
	}
	reply := func(barrier map[string]any) {
		write(map[string]any{"type": "checkpoint_validated_causal", "request_id": barrier["request_id"], "scope": open["scope"], "selected": sessionTestVersions, "input_checkpoint_digest": barrier["checkpoint"].(map[string]any)["digest"], "inventory": []any{}, "control_profile": PrivateCausalPrepareProfile})
	}
	reply(barrier)
	if mode == "late-pending-retry" {
		raw, err = rpc.Read()
		if err != nil || json.Unmarshal(raw, &barrier) != nil {
			t.Fatal("late retry barrier", err)
		}
		output["message"] = pending
		write(output)
		output["message"] = terminal
		write(output)
		reply(barrier)
	}
	_, _ = rpc.Read() // Keep the process alive until its owner closes the FD.
}

func TestSessionRuntimePendingAndSavedRemainDistinctWhileDeliveryIsBlocked(t *testing.T) {
	for _, mode := range []string{"pending-then-saved", "terminal-first-completion", "both-held-exact-retries", "late-pending-retry", "exact-pending-retry", "contradictory-pending", "contradictory-terminal", "cross-wrapper", "cross-generation", "cross-change-ids", "reordered-change-ids", "cross-inner-request", "ordinary-id-collision"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSessionOutputPhaseTransportHelper$", "--", "--output-phase-peer", mode)
			s, err := startSessionRuntimeCommand(t.Context(), SessionRuntimeConfig{Runtime: RuntimeConfig{Versions: sessionTestVersions, ControlProfile: PrivateSessionProfile}, Scope: SessionKey{WorkspaceID: "workspace", ArtifactID: "artifact", ApplicationID: "wordcraft"}}, cmd)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if _, err = s.request(ctx, sessionTestOpen(s), 2*time.Second); err != nil {
				t.Fatal(err)
			}
			pending, err := s.NextControl(ctx)
			if err != nil {
				t.Fatal(err)
			}
			pendingRaw, err := pending.Frame()
			if err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			count, bytes := s.count, s.bytes
			s.mu.Unlock()
			if count != 1 || bytes != len(pendingRaw) {
				t.Fatal("borrowed Pending lost its exact count/byte charge", count, bytes)
			}
			barrier, _ := json.Marshal(map[string]any{"type": "validate_checkpoint_causal", "request_id": "release-second-phase", "scope": map[string]any{"workspace_id": s.scope.WorkspaceID, "artifact_id": s.scope.ArtifactID, "application_id": s.scope.ApplicationID}, "selected": sessionTestVersions, "checkpoint": map[string]any{"encoding": "base64", "digest": "sha256:" + string(makeDigestZeros()), "state": ""}, "control_profile": PrivateCausalPrepareProfile})
			_, err = s.request(ctx, barrier, 2*time.Second)
			negative := mode == "contradictory-pending" || mode == "contradictory-terminal" || mode == "cross-wrapper" || mode == "cross-generation" || mode == "cross-change-ids" || mode == "reordered-change-ids" || mode == "cross-inner-request" || mode == "ordinary-id-collision"
			if negative {
				if err == nil || s.Err() == nil {
					t.Fatal("changed phase/wrapper/global identity was not rejected")
				}
				return
			}
			if err != nil {
				t.Fatal("second notification poisoned transport while original Pending remained borrowed", err)
			}
			tokens := []SessionControl{pending}
			wantBytes := len(pendingRaw)
			if mode != "exact-pending-retry" {
				saved, e := s.NextControl(ctx)
				if e != nil {
					t.Fatal(e)
				}
				raw, e := saved.Frame()
				if e != nil {
					t.Fatal(e)
				}
				var envelope struct {
					Message struct {
						Type string `json:"type"`
					} `json:"message"`
				}
				if json.Unmarshal(raw, &envelope) != nil || envelope.Message.Type != "commit_result" {
					t.Fatal("Saved phase missing")
				}
				tokens = append(tokens, saved)
				wantBytes += len(raw)
			}
			s.mu.Lock()
			count, bytes = s.count, s.bytes
			s.mu.Unlock()
			if count != len(tokens) || bytes != wantBytes {
				t.Fatal("phase count/bytes or exact retry accounting changed", count, bytes)
			}
			if mode == "late-pending-retry" {
				if err = s.CompleteControl(pending); err != nil {
					t.Fatal(err)
				}
				var v map[string]any
				if json.Unmarshal(barrier, &v) != nil {
					t.Fatal("barrier")
				}
				v["request_id"] = "release-late-pending-retry"
				lateBarrier, _ := json.Marshal(v)
				if _, err = s.request(ctx, lateBarrier, 2*time.Second); err != nil {
					t.Fatal("late exact Pending retry poisoned retained terminal phase", err)
				}
				late, e := s.NextControl(ctx)
				if e != nil {
					t.Fatal(e)
				}
				lateRaw, e := late.Frame()
				if e != nil || string(lateRaw) != string(pendingRaw) {
					t.Fatal("late Pending retry changed original bytes", e)
				}
				tokens = []SessionControl{tokens[1], late}
				s.mu.Lock()
				count, bytes = s.count, s.bytes
				s.mu.Unlock()
				if count != 2 || bytes != wantBytes {
					t.Fatal("late retry reset or leaked phase accounting", count, bytes)
				}
			}
			if mode == "terminal-first-completion" {
				tokens[0], tokens[1] = tokens[1], tokens[0]
			}
			for i, token := range tokens {
				if err = s.CompleteControl(token); err != nil {
					t.Fatal(err)
				}
				if i+1 < len(tokens) {
					if _, err = tokens[i+1].Frame(); err != nil {
						t.Fatal("completing Pending erased the borrowed Saved phase", err)
					}
				}
			}
			s.mu.Lock()
			count, bytes = s.count, s.bytes
			s.mu.Unlock()
			if count != 0 || bytes != 0 || s.Err() != nil {
				t.Fatal("notification completion leaked or closed transport", count, bytes)
			}
		})
	}
}
