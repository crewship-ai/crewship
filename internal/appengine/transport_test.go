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
