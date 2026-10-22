//go:build linux

package appengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/documents"
)

func TestWorkerSocketAuthenticationBoundsAndCapacity(t *testing.T) {
	dir, err := os.MkdirTemp("", "workbook-socket-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	secret := strings.Repeat("s", 64)
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	runner := runnerFunc(func(ctx context.Context, in []byte, c Command) (EngineResult, error) {
		calls.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return EngineResult{Base: in, Result: in}, nil
	})
	handler, err := NewWorkerHandler(secret, runner, 1)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "worker.sock")
	stop, err := documents.ServePrivateUnix(socket, handler)
	if err != nil {
		t.Fatal(err)
	}
	defer stop(context.Background())
	badFile := filepath.Join(dir, "bad-token")
	os.WriteFile(badFile, []byte(strings.Repeat("b", 64)), 0600)
	bad, err := NewWorkerClient(socket, badFile)
	if err != nil {
		t.Fatal(err)
	}
	command := Command{Kind: "import", Snapshot: json.RawMessage(`{}`)}
	if _, err := bad.Run(t.Context(), []byte(`{}`), command); !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("bad credential: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("unauthenticated work executed")
	}
	client, err := NewWorkerClient(socket, token)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Health(t.Context()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := client.Run(t.Context(), []byte(`{}`), command); done <- e }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker not reached")
	}
	if _, err := client.Run(t.Context(), []byte(`{}`), command); !errors.Is(err, ErrCapacity) {
		t.Fatalf("capacity: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("capacity overflow executed work")
	}
	// Unapproved commands must not acquire a physical slot.
	if _, err := client.Run(t.Context(), []byte(`{}`), Command{Kind: "file.open"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("allowlist: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	req, _ := http.NewRequest("POST", "http://worker/v1/candidate", strings.NewReader(strings.Repeat("x", 2*MaxSnapshotBytes+1)))
	req.Header.Set("X-Crewship-Worker-Service", secret)
	setContractHeaders(req.Header)
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: %d", resp.StatusCode)
	}
	if calls.Load() != 1 {
		t.Fatal("oversize work executed")
	}
}

func TestWorkerClientRejectsNonPrivatePathsAndResponseOverflow(t *testing.T) {
	for _, path := range []string{"relative.sock", "/tmp/../tmp/worker.sock", "http://worker"} {
		if _, err := NewWorkerClient(path, "/tmp/token"); err == nil {
			t.Fatalf("unsafe socket %q", path)
		}
	}
	secret := strings.Repeat("s", 64)
	handler, err := NewWorkerHandler(secret, runnerFunc(func(context.Context, []byte, Command) (EngineResult, error) { return EngineResult{}, nil }), 0)
	if err == nil || handler != nil {
		t.Fatal("zero capacity admitted")
	}
}

func TestWorkerClientBoundedResponseAndNoRedirect(t *testing.T) {
	secret := strings.Repeat("s", 64)
	dir, err := os.MkdirTemp("", "workbook-client-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"overflow", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			socket := filepath.Join(dir, scenario+".sock")
			stop, err := documents.ServePrivateUnix(socket, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "redirect" {
					http.Redirect(w, r, "http://127.0.0.1:1/secret", http.StatusTemporaryRedirect)
					return
				}
				setContractHeaders(w.Header())
				w.WriteHeader(200)
				_, _ = io.Copy(w, io.LimitReader(zeroReader{}, workerResponseLimit+1))
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer stop(context.Background())
			client, err := NewWorkerClient(socket, token)
			if err != nil {
				t.Fatal(err)
			}
			want := ErrEngineUnavailable
			if scenario == "overflow" {
				want = ErrInvalid
			}
			if _, err := client.Run(t.Context(), []byte(`{}`), Command{Kind: "import", Snapshot: json.RawMessage(`{}`)}); !errors.Is(err, want) {
				t.Fatalf("unsafe response accepted: %v", err)
			}
		})
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestWorkerUnconfirmedCleanupQuarantinesUntilServiceRestart(t *testing.T) {
	dir, err := os.MkdirTemp("", "workbook-quarantine-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	secret := strings.Repeat("s", 64)
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	handler, err := NewWorkerHandler(secret, runnerFunc(func(context.Context, []byte, Command) (EngineResult, error) {
		calls.Add(1)
		return EngineResult{}, fmt.Errorf("%w: cleanup unconfirmed", ErrEngineUnavailable)
	}), 1)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "worker.sock")
	stop, err := documents.ServePrivateUnix(socket, handler)
	if err != nil {
		t.Fatal(err)
	}
	defer stop(context.Background())
	client, err := NewWorkerClient(socket, token)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := client.Run(t.Context(), []byte(`{}`), Command{Kind: "import", Snapshot: json.RawMessage(`{}`)}); !errors.Is(err, ErrEngineUnavailable) {
			t.Fatalf("unsafe cleanup outcome: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("work started after cleanup uncertainty: %d", calls.Load())
	}
	if err := client.Health(t.Context()); !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("quarantined worker appeared ready: %v", err)
	}
}

func TestWorkerHTMLSnapshotNearLimitPreservesWireSize(t *testing.T) {
	dir, err := os.MkdirTemp("", "workbook-html-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	token := filepath.Join(dir, "token")
	secret := strings.Repeat("s", 64)
	if err := os.WriteFile(token, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := []byte(`{"value":"` + strings.Repeat("<>&", (MaxSnapshotBytes-32)/3) + `"}`)
	handler, err := NewWorkerHandler(secret, runnerFunc(func(_ context.Context, in []byte, _ Command) (EngineResult, error) {
		return EngineResult{Base: in, Result: in}, nil
	}), 1)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "worker.sock")
	stop, err := documents.ServePrivateUnix(socket, handler)
	if err != nil {
		t.Fatal(err)
	}
	defer stop(context.Background())
	client, err := NewWorkerClient(socket, token)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Run(t.Context(), snapshot, Command{Kind: "import", Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Result) != string(snapshot) {
		t.Fatal("snapshot changed during transport")
	}
}

func TestWorkerClientRejectsWritableOrSubstitutedSocketBeforeSendingSecret(t *testing.T) {
	for _, scenario := range []string{"writable-directory", "writable-socket", "symlink-socket"} {
		t.Run(scenario, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "workbook-peer-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			token := filepath.Join(dir, "token")
			if err := os.WriteFile(token, []byte(strings.Repeat("s", 64)), 0600); err != nil {
				t.Fatal(err)
			}
			socket := filepath.Join(dir, "worker.sock")
			var calls atomic.Int32
			stop, err := documents.ServePrivateUnix(socket, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) }))
			if err != nil {
				t.Fatal(err)
			}
			defer stop(context.Background())
			switch scenario {
			case "writable-directory":
				if err := os.Chmod(dir, 0770); err != nil {
					t.Fatal(err)
				}
			case "writable-socket":
				if err := os.Chmod(socket, 0666); err != nil {
					t.Fatal(err)
				}
			case "symlink-socket":
				alias := filepath.Join(dir, "alias.sock")
				if err := os.Symlink(socket, alias); err != nil {
					t.Fatal(err)
				}
				socket = alias
			}
			client, err := NewWorkerClient(socket, token)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.Health(t.Context()); !errors.Is(err, ErrEngineUnavailable) {
				t.Fatalf("unsafe socket accepted: %v", err)
			}
			if calls.Load() != 0 {
				t.Fatal("secret sent to unsafe listener")
			}
		})
	}
}

func TestWorkerHandshakeRefusesIncompatiblePeers(t *testing.T) {
	dir, err := os.MkdirTemp("", "workbook-handshake-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	secret := strings.Repeat("s", 64)
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	handler, err := NewWorkerHandler(secret, runnerFunc(func(_ context.Context, in []byte, _ Command) (EngineResult, error) {
		calls.Add(1)
		return EngineResult{Base: in, Result: in}, nil
	}), 1)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "worker.sock")
	stop, err := documents.ServePrivateUnix(socket, handler)
	if err != nil {
		t.Fatal(err)
	}
	defer stop(context.Background())
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	raw := &http.Client{Transport: transport}

	// Worker side: an incompatible host is refused before any work runs, and
	// an unauthenticated caller learns nothing about the contract.
	for _, tc := range []struct {
		name             string
		auth             string
		protocol, engine []string
		status           int
		disclosed        bool
	}{
		{"unauthenticated", "wrong", []string{"1"}, []string{GridCraftBinarySHA256}, http.StatusUnauthorized, false},
		{"missing protocol", secret, nil, []string{GridCraftBinarySHA256}, http.StatusPreconditionFailed, true},
		{"missing engine", secret, []string{strconv.Itoa(GridCraftWorkerProtocol)}, nil, http.StatusPreconditionFailed, true},
		{"newer host", secret, []string{strconv.Itoa(GridCraftWorkerProtocolMax + 1)}, []string{GridCraftBinarySHA256}, http.StatusPreconditionFailed, true},
		{"older host", secret, []string{strconv.Itoa(GridCraftWorkerProtocolMin - 1)}, []string{GridCraftBinarySHA256}, http.StatusPreconditionFailed, true},
		{"different engine pin", secret, []string{strconv.Itoa(GridCraftWorkerProtocol)}, []string{strings.Repeat("0", 64)}, http.StatusPreconditionFailed, true},
		{"ambiguous protocol", secret, []string{"1", "1"}, []string{GridCraftBinarySHA256}, http.StatusPreconditionFailed, true},
		{"compatible", secret, []string{strconv.Itoa(GridCraftWorkerProtocol)}, []string{GridCraftBinarySHA256}, http.StatusOK, true},
	} {
		t.Run("worker/"+tc.name, func(t *testing.T) {
			before := calls.Load()
			req, _ := http.NewRequest("POST", "http://worker/v1/candidate", strings.NewReader(`{"snapshot":{},"command":{"kind":"import"}}`))
			req.Header.Set(workerServiceHeader, tc.auth)
			for _, v := range tc.protocol {
				req.Header.Add(workerProtocolHeader, v)
			}
			for _, v := range tc.engine {
				req.Header.Add(workerEngineHeader, v)
			}
			resp, err := raw.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.status)
			}
			if got := resp.Header.Get(workerProtocolHeader) != ""; got != tc.disclosed {
				t.Fatalf("contract disclosed=%v, want %v", got, tc.disclosed)
			}
			ran := calls.Load() != before
			if ran != (tc.status == http.StatusOK) {
				t.Fatalf("work ran=%v for status %d", ran, tc.status)
			}
		})
	}

	// Host side: a worker that declares no, another or an out-of-range
	// contract is unavailable, even when it answers with a valid result.
	for _, tc := range []struct {
		name             string
		protocol, engine string
	}{
		{"undeclared", "", ""},
		{"newer worker", strconv.Itoa(GridCraftWorkerProtocolMax + 1), GridCraftBinarySHA256},
		{"older worker", strconv.Itoa(GridCraftWorkerProtocolMin - 1), GridCraftBinarySHA256},
		{"different engine pin", strconv.Itoa(GridCraftWorkerProtocol), strings.Repeat("0", 64)},
	} {
		t.Run("host/"+tc.name, func(t *testing.T) {
			fake := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".sock")
			var sawProtocol, sawEngine atomic.Value
			stopFake, err := documents.ServePrivateUnix(fake, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sawProtocol.Store(r.Header.Get(workerProtocolHeader))
				sawEngine.Store(r.Header.Get(workerEngineHeader))
				if tc.protocol != "" {
					w.Header().Set(workerProtocolHeader, tc.protocol)
				}
				if tc.engine != "" {
					w.Header().Set(workerEngineHeader, tc.engine)
				}
				if r.URL.Path == "/v1/health" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"base":{},"result":{}}`)
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer stopFake(context.Background())
			client, err := NewWorkerClient(fake, token)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Run(t.Context(), []byte(`{}`), Command{Kind: "import", Snapshot: json.RawMessage(`{}`)}); !errors.Is(err, ErrEngineUnavailable) {
				t.Fatalf("incompatible worker result accepted: %v", err)
			}
			if err := client.Health(t.Context()); !errors.Is(err, ErrEngineUnavailable) {
				t.Fatalf("incompatible worker reported healthy: %v", err)
			}
			if sawProtocol.Load() != strconv.Itoa(GridCraftWorkerProtocol) || sawEngine.Load() != GridCraftBinarySHA256 {
				t.Fatal("host did not declare its contract")
			}
		})
	}
}
