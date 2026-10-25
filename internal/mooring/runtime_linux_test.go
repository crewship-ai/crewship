//go:build linux

package mooring

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

var runtimeTestVersions = RuntimeVersions{"0.4.0", "1.0.0", "0.1.0"}

func TestMooringRuntimeHelper(t *testing.T) {
	mode := ""
	for i, arg := range os.Args {
		if arg == "--mooring-test-mode" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
		}
	}
	if mode == "" {
		return
	}
	if mode == "stderr-before-ready" {
		_, _ = os.Stderr.WriteString("mooring runtime stopped: kind=InvalidData\nprivate-document-not-for-logs\n")
		os.Exit(23)
	}
	if _, present := os.LookupEnv("MOORING_PARENT_SECRET"); present {
		t.Fatal("backend secret inherited")
	}
	f := os.NewFile(3, "inherited-rpc")
	conn, err := net.FileConn(f)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	rpc := &RPC{conn: conn}
	defer rpc.Close()
	frame, err := rpc.Read()
	if err != nil {
		t.Fatal(err)
	}
	var h struct {
		Type            string            `json:"type"`
		Request         string            `json:"request_id"`
		Supported       []RuntimeVersions `json:"supported"`
		ControlProfiles []string          `json:"control_profiles"`
	}
	if json.Unmarshal(frame, &h) != nil || h.Type != "hello" || h.Request != "host-start" || len(h.Supported) != 1 {
		t.Fatal("bad host hello")
	}
	if mode == "silent" {
		_, _ = io.Copy(io.Discard, conn)
		return
	}
	v := h.Supported[0]
	if mode == "mismatch" {
		v.Model = "different"
	}
	readyValue := map[string]any{"type": "runtime_ready", "request_id": h.Request, "selected": v}
	selected := ""
	if len(h.ControlProfiles) > 0 && mode != "profile-missing" {
		selected = h.ControlProfiles[0]
	}
	if mode == "profile-wrong" {
		selected = "unrecognized-profile"
	}
	if mode == "profile-unoffered" {
		selected = PrivatePrepareProfile
	}
	if selected != "" {
		readyValue["selected_control_profile"] = selected
	}
	ready, _ := json.Marshal(readyValue)
	if err = rpc.Write(ready); err != nil {
		t.Fatal(err)
	}
	if mode == "stderr-live" {
		_, _ = os.Stderr.WriteString("mooring session lane: frame-read-error kind=Some(UnexpectedEof)\nprivate-document-not-for-logs\n")
	}
	if privatePrepareProfile(selected) {
		rpc.maxFrame = MaxPrivatePrepareRPCFrame
	}
	if mode == "die" {
		if _, err := rpc.Read(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if mode == "echo" {
		for {
			frame, err := rpc.Read()
			if err != nil {
				return
			}
			if err = rpc.Write(frame); err != nil {
				return
			}
		}
	}
	// The actual inherited socket remains alive until the parent closes it or
	// cancellation kills the child. No test-only listener or mock Saved exists.
	_, _ = io.Copy(io.Discard, conn)
}
func helperRuntime(t *testing.T, ctx context.Context, mode string) (*Runtime, error) {
	return helperRuntimeProfile(t, ctx, mode, "")
}
func helperRuntimeProfile(t *testing.T, ctx context.Context, mode, profile string) (*Runtime, error) {
	t.Helper()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMooringRuntimeHelper$", "--", "--mooring-test-mode", mode)
	return startRuntime(ctx, RuntimeConfig{Versions: runtimeTestVersions, ControlProfile: profile}, cmd)
}

func TestRuntimeExchangeActualInheritedChild(t *testing.T) {
	r, err := helperRuntime(t, t.Context(), "echo")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, request := range []string{`{"request_id":"one"}`, `{"request_id":"two","opaque":"český 😀"}`} {
		response, err := r.Exchange(t.Context(), []byte(request))
		if err != nil || string(response) != request {
			t.Fatal("inherited child exchange failed", err)
		}
	}
}
func TestRuntimeDisabledStartsNothing(t *testing.T) {
	_, err := StartRuntime(t.Context(), RuntimeConfig{Binary: "/does/not/exist"})
	if !errors.Is(err, ErrRuntimeDisabled) {
		t.Fatal(err)
	}
}
func TestRuntimeRejectsVersionMismatch(t *testing.T) {
	_, err := helperRuntime(t, t.Context(), "mismatch")
	if !errors.Is(err, ErrRuntimeHandshake) {
		t.Fatal(err)
	}
}
func TestRuntimeCancellationClosesChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r, err := helperRuntime(t, ctx, "wait")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-r.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("child did not stop")
	}
	if err = r.RPC.Write([]byte(`{"type":"presence"}`)); err == nil {
		t.Fatal("dead child accepted content")
	}
	_ = r.Close()
}
func TestRuntimeDeathClosesChannel(t *testing.T) {
	r, err := helperRuntime(t, t.Context(), "die")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.RPC.Write([]byte(`{"test":"exit"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("child did not exit")
	}
	if _, err = r.RPC.Read(); err == nil {
		t.Fatal("dead child delivered content")
	}
	_ = r.Close()
}
func TestRPCRejectsRawBoundsBeforePayload(t *testing.T) {
	for _, size := range []uint32{0, MaxRPCFrame + 1, 1 << 31} {
		t.Run(string(rune(size%100+65)), func(t *testing.T) {
			a, b := net.Pipe()
			defer b.Close()
			rpc := &RPC{conn: a}
			defer rpc.Close()
			go func() { var h [4]byte; binary.BigEndian.PutUint32(h[:], size); _, _ = b.Write(h[:]) }()
			if _, err := rpc.Read(); !errors.Is(err, ErrRPCFrame) {
				t.Fatal(err)
			}
			if err := rpc.Write([]byte(`{}`)); err == nil {
				t.Fatal("invalid incoming frame did not permanently close channel")
			}
		})
	}
}
func TestRPCFragmentedBoundaryAndInvalidJSON(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	rpc := &RPC{conn: a}
	defer rpc.Close()
	body := make([]byte, MaxRPCFrame)
	for i := range body {
		body[i] = ' '
	}
	body[0] = '['
	body[len(body)-1] = ']'
	go func() {
		var h [4]byte
		binary.BigEndian.PutUint32(h[:], uint32(len(body)))
		for _, v := range h {
			_, _ = b.Write([]byte{v})
		}
		_, _ = b.Write(body)
		_, _ = b.Write([]byte{0, 0, 0, 1, 'x'})
	}()
	got, err := rpc.Read()
	if err != nil || len(got) != MaxRPCFrame {
		t.Fatal(len(got), err)
	}
	if _, err = rpc.Read(); !errors.Is(err, ErrRPCFrame) {
		t.Fatal(err)
	}
}

func TestRuntimeReadinessHasDeadline(t *testing.T) {
	start := time.Now()
	_, err := helperRuntime(t, t.Context(), "silent")
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("readiness did not time out: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("readiness timeout unbounded: %v", elapsed)
	}
}

func TestRuntimeChildDoesNotInheritBackendSecrets(t *testing.T) {
	t.Setenv("MOORING_PARENT_SECRET", "synthetic-not-a-real-credential")
	r, err := helperRuntime(t, t.Context(), "wait")
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
}
