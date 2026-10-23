//go:build linux

package mooring

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"testing"
)

func TestRuntimePrivateProfileDoesNotRaiseBootstrapAllocationBound(t *testing.T) {
	parent, child := net.Pipe()
	t.Cleanup(func() { _ = parent.Close(); _ = child.Close() })
	rpc := &RPC{conn: parent}
	peer := &RPC{conn: child}
	done := make(chan error, 1)
	go func() {
		if _, err := peer.Read(); err != nil {
			done <- err
			return
		}
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], MaxRPCFrame+1)
		done <- writeRPCBytes(child, header[:])
		_ = child.Close() // No oversized body is allocated or transmitted.
	}()
	if err := runtimeHandshake(rpc, runtimeTestVersions, PrivatePrepareProfile); !errors.Is(err, ErrRPCFrame) {
		t.Fatal("bootstrap used the private allocation ceiling", err)
	}
	if rpc.frameLimit() != MaxRPCFrame {
		t.Fatal("failed bootstrap changed transport profile")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimePrivateProfileMustBeOfferedAndSelectedExactly(t *testing.T) {
	for _, tc := range []struct{ mode, profile string }{
		{"profile-missing", PrivatePrepareProfile},
		{"profile-wrong", PrivatePrepareProfile},
		{"profile-unoffered", ""},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			r, err := helperRuntimeProfile(t, t.Context(), tc.mode, tc.profile)
			if r != nil {
				_ = r.Close()
			}
			if !errors.Is(err, ErrRuntimeHandshake) {
				t.Fatal("unselected private profile accepted", err)
			}
		})
	}
	_, err := StartRuntime(t.Context(), RuntimeConfig{
		Enabled: true, Binary: "/does/not/exist", Versions: runtimeTestVersions,
		ControlProfile: "unrecognized-profile",
	})
	if !errors.Is(err, ErrRuntimeHandshake) {
		t.Fatal("unknown profile reached process startup", err)
	}
}

func TestRuntimePrivateProfileRaisesOnlyNegotiatedTransportBound(t *testing.T) {
	for _, profile := range []string{"", PrivatePrepareProfile, PrivateCausalPrepareProfile} {
		t.Run(profile, func(t *testing.T) {
			r, err := helperRuntimeProfile(t, t.Context(), "echo", profile)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			want := MaxRPCFrame
			if profile != "" {
				want = MaxPrivatePrepareRPCFrame
			}
			if r.RPC.frameLimit() != want {
				t.Fatal("wrong negotiated transport bound", r.RPC.frameLimit())
			}
			// This helper is a framing peer, not a private semantic dispatcher.
			// The production adapter must validate exactly the four private types.
			request := append([]byte(`{"data":"`), bytes.Repeat([]byte{'x'}, want-11)...)
			request = append(request, []byte(`"}`)...)
			if len(request) != want {
				t.Fatal("bad boundary fixture", len(request), want)
			}
			response, err := r.Exchange(t.Context(), request)
			if err != nil || !bytes.Equal(response, request) {
				t.Fatal("exact framing bound rejected", err)
			}
			if err := r.RPC.Write(append(request, ' ')); !errors.Is(err, ErrRPCFrame) {
				t.Fatal("oversized transport frame accepted", err)
			}
		})
	}
}
