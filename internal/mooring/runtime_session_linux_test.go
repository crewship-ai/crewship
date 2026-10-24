//go:build linux

package mooring

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

var sessionTestVersions = RuntimeVersions{"0.4.0", "0.2.0", "0.10.0"}

// This is an inherited-FD transport peer, not an owner authority or document engine.
func TestSessionTransportHelper(t *testing.T) {
	mode := ""
	for i, a := range os.Args {
		if a == "--session-peer" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
		}
	}
	if mode == "" {
		return
	}
	f := os.NewFile(3, "session-rpc")
	conn, err := net.FileConn(f)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	rpc := &RPC{conn: conn}
	defer rpc.Close()
	raw, err := rpc.Read()
	if err != nil {
		t.Fatal(err)
	}
	var hello map[string]any
	if json.Unmarshal(raw, &hello) != nil {
		t.Fatal("hello")
	}
	ready, _ := json.Marshal(map[string]any{"type": "runtime_ready", "request_id": "host-start", "selected": sessionTestVersions, "selected_control_profile": PrivateSessionProfile})
	if err = rpc.Write(ready); err != nil {
		t.Fatal(err)
	}
	rpc.maxFrame = MaxPrivatePrepareRPCFrame
	var open map[string]any
	raw, err = rpc.Read()
	if err != nil {
		return
	}
	if json.Unmarshal(raw, &open) != nil {
		t.Fatal("open")
	}
	write := func(v map[string]any) {
		b, _ := json.Marshal(v)
		if rpc.Write(b) != nil {
			return
		}
	}
	owner := map[string]any{"type": "owner_acquire", "request_id": "acquire", "scope": open["scope"], "runtime_ref": open["runtime_ref"], "renewal_nonce": "nonce"}
	if mode == "wrong-scope" {
		owner["scope"] = map[string]any{"workspace_id": "wrong", "artifact_id": "artifact", "application_id": "wordcraft"}
	}
	if mode == "unsolicited" {
		write(map[string]any{"type": "session_opened", "capabilities": []string{"checkpoint_identity_v1"}, "request_id": "not-pending", "control_profile": PrivateSessionProfile, "scope": open["scope"], "session_id": open["session_id"], "runtime_ref": open["runtime_ref"], "versions": sessionTestVersions, "fence": map[string]any{"generation": "1", "token": "01234567890123456789012345678901"}, "confirmed_basis": map[string]any{"revision": "0", "digest": "sha256:" + string(makeDigestZeros())}})
	} else {
		write(owner)
	}
	if mode == "flood" {
		for i := 0; i < sessionControlCount+1; i++ {
			owner["request_id"] = "request-" + time.Duration(i).String()
			write(owner)
		}
	}
	for {
		raw, err = rpc.Read()
		if err != nil {
			return
		}
		var v map[string]any
		if json.Unmarshal(raw, &v) != nil {
			return
		}
		switch v["type"] {
		case "owner_grant":
			if mode == "activation" {
				continue
			}
			write(map[string]any{"type": "session_opened", "capabilities": []string{"checkpoint_identity_v1"}, "request_id": open["request_id"], "control_profile": PrivateSessionProfile, "scope": open["scope"], "session_id": open["session_id"], "runtime_ref": open["runtime_ref"], "versions": sessionTestVersions, "fence": v["fence"], "confirmed_basis": map[string]any{"revision": "0", "digest": "sha256:" + string(makeDigestZeros())}})

		case "session_restore":
			if mode != "activation" {
				return
			}
			cp := v["checkpoint"].(map[string]any)
			result := map[string]any{"type": "session_restored", "request_id": v["request_id"], "control_profile": PrivateSessionProfile, "scope": v["scope"], "session_id": v["session_id"], "runtime_ref": v["runtime_ref"], "versions": v["versions"], "fence": v["fence"], "confirmed_basis": map[string]any{"revision": cp["revision"], "digest": cp["digest"]}}
			write(result)
			result["type"] = "session_opened"
			result["capabilities"] = []string{"checkpoint_identity_v1"}
			result["request_id"] = open["request_id"]
			write(result)
		case "session_describe_checkpoint":
			described := map[string]any{"type": "session_checkpoint_described", "request_id": v["request_id"], "control_profile": PrivateSessionProfile, "scope": v["scope"], "session_id": v["session_id"], "runtime_ref": v["runtime_ref"], "versions": v["versions"], "input_digest": v["checkpoint"].(map[string]any)["digest"], "description": map[string]any{"format_version": "wordcraft-0.10.0/yrs-0.28.0/v2", "versions": sessionTestVersions, "assets": []any{}}}
			if mode == "descriptor-wrong-digest" {
				described["input_digest"] = "sha256:" + string(makeDigestZeros())
			}
			write(described)
		case "validate_checkpoint_causal":
			write(map[string]any{"type": "checkpoint_validated_causal", "request_id": v["request_id"], "scope": v["scope"], "selected": sessionTestVersions, "input_checkpoint_digest": v["checkpoint"].(map[string]any)["digest"], "inventory": []any{}, "control_profile": PrivateCausalPrepareProfile})
		}
	}
}
func makeDigestZeros() []byte {
	b := make([]byte, 64)
	for i := range b {
		b[i] = '0'
	}
	return b
}
func sessionPeer(t *testing.T, mode string) *SessionRuntime {
	return sessionPeerKey(t, mode, SessionKey{WorkspaceID: "workspace", ArtifactID: "artifact", ApplicationID: "wordcraft"})
}
func sessionPeerKey(t *testing.T, mode string, key SessionKey) *SessionRuntime {
	t.Helper()
	ctx := t.Context()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSessionTransportHelper$", "--", "--session-peer", mode)
	s, err := startSessionRuntimeCommand(ctx, SessionRuntimeConfig{Runtime: RuntimeConfig{Versions: sessionTestVersions, ControlProfile: PrivateSessionProfile}, Scope: key}, cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func sessionTestOpen(s *SessionRuntime) []byte {
	b, _ := json.Marshal(map[string]any{"type": "session_open", "required_capabilities": []string{"checkpoint_identity_v1"}, "request_id": "open", "control_profile": PrivateSessionProfile, "scope": map[string]any{"workspace_id": s.scope.WorkspaceID, "artifact_id": s.scope.ArtifactID, "application_id": s.scope.ApplicationID}, "session_id": "host-session", "runtime_ref": s.RuntimeRef(), "versions": sessionTestVersions})
	return b
}
func TestSessionRuntimeInheritedPrepareProgressWhileOpenAwaitsOwner(t *testing.T) {
	s := sessionPeer(t, "normal")
	opened := make(chan error, 1)
	go func() { _, err := s.request(t.Context(), sessionTestOpen(s), 3*time.Second); opened <- err }()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	control, err := s.NextControl(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteControl(control); !errors.Is(err, ErrSessionRuntime) {
		t.Fatal("request disappeared", err)
	}
	// The actor's outstanding Open does not own the stream reader or writer.
	request, _ := json.Marshal(map[string]any{"type": "validate_checkpoint_causal", "request_id": "prepare-validation", "scope": map[string]any{"workspace_id": "workspace", "artifact_id": "artifact", "application_id": "wordcraft"}, "selected": sessionTestVersions, "checkpoint": map[string]any{"encoding": "base64", "digest": "sha256:" + string(makeDigestZeros()), "state": ""}, "control_profile": PrivateCausalPrepareProfile})
	if _, err = s.request(ctx, request, time.Second); err != nil {
		t.Fatal("prepare blocked behind actor", err)
	}
	select {
	case err := <-opened:
		t.Fatal("opened without grant", err)
	default:
	}
	frame, _ := control.Frame()
	var acquire map[string]any
	_ = json.Unmarshal(frame, &acquire)
	grant, _ := json.Marshal(map[string]any{"type": "owner_grant", "request_id": acquire["request_id"], "scope": acquire["scope"], "runtime_ref": s.RuntimeRef(), "session_id": "host-session", "renewal_nonce": "nonce", "fence": map[string]any{"generation": "1", "token": "01234567890123456789012345678901"}, "lease_duration_ms": "60000", "revision": "0", "permission_version": "1"})
	if err = s.ReplyControl(ctx, control, grant); err != nil {
		t.Fatal(err)
	}
	if err = <-opened; err != nil {
		t.Fatal(err)
	}
	if _, err = control.Frame(); !errors.Is(err, ErrSessionRuntime) {
		t.Fatal("consumed token reused")
	}
}
func TestSessionRuntimeWrongScopeAndOverflowCloseActualChild(t *testing.T) {
	for _, mode := range []string{"wrong-scope", "flood", "unsolicited"} {
		t.Run(mode, func(t *testing.T) {
			s := sessionPeer(t, mode)
			go func() { _, _ = s.request(t.Context(), sessionTestOpen(s), 3*time.Second) }()
			select {
			case <-s.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("bad stream remained live")
			}
			if s.Err() == nil {
				t.Fatal("missing poison cause")
			}
			if s.process.cmd.ProcessState == nil {
				t.Fatal("Done before actual process exit")
			}
		})
	}
}
func TestSessionRuntimeCanceledSentRequestPoisonsAndWakesControls(t *testing.T) {
	s := sessionPeer(t, "normal")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := s.request(ctx, sessionTestOpen(s), 3*time.Second); done <- err }()
	control, err := s.NextControl(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err = <-done; err == nil {
		t.Fatal("cancellation accepted")
	}
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("child alive")
	}
	if _, err = control.Frame(); err == nil {
		t.Fatal("poisoned token readable")
	}
	if _, err = s.NextControl(t.Context()); err == nil {
		t.Fatal("poison did not wake control consumer")
	}
}

func TestSessionRuntimeQueuedCancellationDoesNotPoisonPeer(t *testing.T) {
	s := sessionPeer(t, "normal")
	<-s.writer // A different host writer owns the bounded lane.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := s.request(ctx, sessionTestOpen(s), time.Second)
	s.writer <- struct{}{}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	s.mu.Lock()
	closed, pending, count, bytes, id := s.closed, len(s.pending), s.count, s.bytes, s.sessionID
	s.mu.Unlock()
	if closed || pending != 0 || count != 0 || bytes != 0 || id != "" {
		t.Fatal("queued cancel changed live transport", closed, pending, count, bytes, id)
	}
	// The same exact session-open intent can now actually enter the stream.
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	go func() { _, _ = s.request(ctx2, sessionTestOpen(s), time.Second) }()
	c, err := s.NextControl(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Frame(); err != nil {
		t.Fatal(err)
	}
	cancel2()
}
func TestSessionRuntimeFactoryRejectsLegacyOrForgedProcess(t *testing.T) {
	if _, err := NewSessionCausalAdapter(nil, SessionKey{}, sessionTestVersions); err == nil {
		t.Fatal("nil provenance accepted")
	}
	if _, err := NewSessionCausalAdapter(&SessionRuntime{}, SessionKey{}, sessionTestVersions); err == nil {
		t.Fatal("copied configuration accepted")
	}
	if _, err := StartRuntime(t.Context(), RuntimeConfig{Enabled: true, Binary: "/does/not/exist", Versions: sessionTestVersions, ControlProfile: PrivateSessionProfile}); !errors.Is(err, ErrRuntimeHandshake) {
		t.Fatal("legacy accepted session profile", err)
	}
	s := sessionPeer(t, "normal")
	if _, err := NewSessionCausalAdapter(s, s.scope, sessionTestVersions); err != nil {
		t.Fatal(err)
	}
	wrong := s.scope
	wrong.ArtifactID = "other"
	if _, err := NewSessionCausalAdapter(s, wrong, sessionTestVersions); err == nil {
		t.Fatal("cross scope factory")
	}
	if _, err := NewCausalRPCPrepareAdapter(s.process, s.scope, sessionTestVersions); err == nil {
		t.Fatal("legacy reader exposed on session process")
	}
}

func TestSessionRuntimeMandatoryIdentityCapabilityBeforeAnyOwnerRequest(t *testing.T) {
	s := sessionPeer(t, "normal")
	var open map[string]any
	if json.Unmarshal(sessionTestOpen(s), &open) != nil {
		t.Fatal("fixture")
	}
	delete(open, "required_capabilities")
	raw, _ := json.Marshal(open)
	if _, err := s.request(t.Context(), raw, time.Second); err == nil {
		t.Fatal("missing capability admitted")
	}
	if s.sessionID != "" || s.openSent {
		t.Fatal("invalid Open reached transport")
	}
}
func TestSessionRuntimeWrongDescriptorDigestPoisonsBeforeInitialization(t *testing.T) {
	s := sessionPeer(t, "descriptor-wrong-digest")
	go func() { _, _ = s.request(t.Context(), sessionTestOpen(s), 3*time.Second) }()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := s.NextControl(ctx); err != nil {
		t.Fatal(err)
	}
	adapter, err := NewSessionCausalAdapter(s, s.scope, s.versions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.DescribeCheckpoint(ctx, []byte("actual retained input")); err == nil {
		t.Fatal("foreign descriptor returned")
	}
	select {
	case <-s.Done():
	case <-ctx.Done():
		t.Fatal("uncertain descriptor process stayed alive")
	}
}
