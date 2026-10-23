package mooring

import (
	"net"
	"testing"
)

func TestRuntimePrivateProfileRejectsAmbiguousReady(t *testing.T) {
	parent, child := net.Pipe()
	defer parent.Close()
	defer child.Close()
	peer := &RPC{conn: child}
	done := make(chan error, 1)
	go func() {
		if _, err := peer.Read(); err != nil {
			done <- err
			return
		}
		done <- peer.Write([]byte(`{"type":"runtime_ready","request_id":"host-start","selected":{"protocol_version":"0.4.0","model_version":"1.0.0","engine_version":"0.1.0"},"selected_control_profile":"unsupported","selected_control_profile":"mooring.prepare.v1"}`))
	}()
	rpc := &RPC{conn: parent}
	err := runtimeHandshake(rpc, RuntimeVersions{Protocol: "0.4.0", Model: "1.0.0", Engine: "0.1.0"}, PrivatePrepareProfile)
	if peerErr := <-done; peerErr != nil {
		t.Fatal(peerErr)
	}
	if err == nil {
		t.Fatalf("ambiguous Ready accepted and raised private cap to %d", rpc.frameLimit())
	}
}
