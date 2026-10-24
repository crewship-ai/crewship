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

func fanoutOuterID(t *testing.T, connection, participant, gate string) string {
	t.Helper()
	raw, err := json.Marshal([]string{connection, participant, gate})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return "actor-update:1:" + canonicalDigest(canonical)
}

// Actual inherited Go multiplexer; this child has MOCK document/receipt
// semantics and grants no participant authority or Saved truth.
func TestSessionFanoutTransportHelper(t *testing.T) {
	mode := ""
	for i, a := range os.Args {
		if a == "--fanout-peer" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
		}
	}
	if mode == "" {
		return
	}
	f := os.NewFile(3, "fanout-rpc")
	conn, err := net.FileConn(f)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	rpc := &RPC{conn: conn}
	defer rpc.Close()
	if _, err = rpc.Read(); err != nil {
		t.Fatal(err)
	}
	write := func(v any) {
		raw, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		if e = rpc.Write(raw); e != nil {
			t.Fatal(e)
		}
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
	receipt := map[string]any{"type": "commit_result", "request_id": "actor-submit:1", "scope": open["scope"], "generation": "1", "revision": "1", "issuer": "crewship", "durability": "node", "changes": []any{map[string]any{"change_id": "c", "status": "saved", "first_revision": "1", "last_revision": "1", "operations": []any{map[string]any{"op_id": "o", "status": "saved", "revision": "1", "issuer": "crewship", "durability": "node"}}}}}
	message := map[string]any{"type": "document_update", "request_id": "actor-update:1", "scope": open["scope"], "session_id": open["session_id"], "generation": "1", "from_revision": "0", "to_revision": "1", "encoding": "json", "data": "{}", "receipt": receipt}
	for _, recipient := range []string{"a", "b"} {
		id := fanoutOuterID(t, "connection-"+recipient, "participant-"+recipient, "gate-"+recipient)
		if mode == "collision" {
			id = "actor-update:1"
		}
		output := map[string]any{"type": "session_participant_output", "request_id": id, "control_profile": PrivateSessionProfile, "scope": open["scope"], "session_id": open["session_id"], "runtime_ref": open["runtime_ref"], "fence": fence, "connection_ref": "connection-" + recipient, "participant_ref": "participant-" + recipient, "gate_id": "gate-" + recipient, "permission_version": "1", "message": message}
		write(output)
		if mode == "exact-replay" && recipient == "a" {
			write(output)
		}
	}
	write(map[string]any{"type": "session_opened", "request_id": open["request_id"], "control_profile": PrivateSessionProfile, "scope": open["scope"], "session_id": open["session_id"], "runtime_ref": open["runtime_ref"], "versions": sessionTestVersions, "capabilities": []string{"checkpoint_identity_v1"}, "fence": fence, "confirmed_basis": map[string]any{"revision": "0", "digest": "sha256:" + string(makeDigestZeros())}})
	_, _ = rpc.Read()
}
func TestSessionRuntimeRecipientScopedProducerIDsKeepGlobalTamperGuard(t *testing.T) {
	for _, mode := range []string{"fanout", "exact-replay", "collision"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSessionFanoutTransportHelper$", "--", "--fanout-peer", mode)
			s, err := startSessionRuntimeCommand(t.Context(), SessionRuntimeConfig{Runtime: RuntimeConfig{Versions: sessionTestVersions, ControlProfile: PrivateSessionProfile}, Scope: SessionKey{WorkspaceID: "workspace", ArtifactID: "artifact", ApplicationID: "wordcraft"}}, cmd)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			_, err = s.request(ctx, sessionTestOpen(s), 2*time.Second)
			if mode == "collision" {
				if err == nil {
					t.Fatal("same globalID/different recipient bytes accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// Borrow BOTH before completing either: global in-flight dedup must retain
			// two different recipients with the same inner committed cut identity.
			one, err := s.NextControl(ctx)
			if err != nil {
				t.Fatal(err)
			}
			two, err := s.NextControl(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for i, token := range []SessionControl{one, two} {
				raw, err := token.Frame()
				if err != nil {
					t.Fatal(err)
				}
				var v map[string]any
				if json.Unmarshal(raw, &v) != nil {
					t.Fatal("frame")
				}
				recipient := []string{"a", "b"}[i]
				if v["request_id"] != fanoutOuterID(t, "connection-"+recipient, "participant-"+recipient, "gate-"+recipient) || v["message"].(map[string]any)["request_id"] != "actor-update:1" {
					t.Fatal("wrong outer/inner correlation")
				}
				if err = s.CompleteControl(token); err != nil {
					t.Fatal(err)
				}
			}
			s.mu.Lock()
			count := s.count
			s.mu.Unlock()
			if count != 0 {
				t.Fatal("notification accounting/replay leaked", count)
			}
		})
	}
}
