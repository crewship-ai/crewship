//go:build linux

package quota

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestClientRejectsUnprivilegedSocketPeer(t *testing.T) {
	if os.Geteuid() == 0 {
		// SKIP-WAIVER(#2703): a root-owned fake socket cannot represent an unprivileged helper peer; real UID probes require quota_live.
		t.Skip("unprivileged peer assertion")
	}
	socket := filepath.Join(t.TempDir(), "fake.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: socket})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err = (Client{Socket: socket}).Ensure(t.Context(), Key{"crew", "database", "data", 1}, 64<<20, Owner{}); !errors.Is(err, ErrDenied) {
		t.Fatalf("unprivileged helper impersonation accepted: %v", err)
	}
}
func TestQuotaClientIdentityProbe(t *testing.T) {
	socket := os.Getenv("CREWSHIP_QUOTA_CLIENT_PROBE_SOCKET")
	if socket == "" {
		// SKIP-WAIVER(#2703): this helper is invoked only by the quota_live parent under explicit UID1000/1001/1002 with its private socket environment.
		t.Skip("forked identity probe only")
	}
	d, err := (Client{Socket: socket}).Ensure(t.Context(), Key{"synthetic-crew", "socket-probe", "data", 1}, 64<<20, Owner{})
	if os.Geteuid() == 1001 || os.Geteuid() == 1002 {
		if err == nil {
			t.Fatal("agent/broker socket admitted")
		}
		return
	}
	if err != nil || d.Bytes != 64<<20 {
		t.Fatalf("server socket quota request: %v", err)
	}
	if os.Geteuid() == 1000 {
		if _, err = os.ReadDir(d.Mount); err == nil {
			t.Fatal("server UID could inspect private root catalog hostpath")
		}
	}
	if _, err = (Client{Socket: socket}).Ensure(t.Context(), Key{"../host", "socket-probe", "data", 1}, 64<<20, Owner{}); err == nil {
		t.Fatal("caller path accepted by privileged helper")
	}
}

func TestClientHonoursCallerContext(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "silent.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: socket})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	key := Key{"crew", "database", "data", 1}
	c := Client{Socket: socket}
	for name, call := range map[string]func() error{
		"ensure":  func() error { _, err := c.Ensure(ctx, key, 64<<20, Owner{}); return err },
		"verify":  func() error { _, err := c.Verify(ctx, key, 64<<20); return err },
		"remove":  func() error { return c.Remove(ctx, key) },
		"recover": func() error { return c.Recover(ctx) },
		"protect": func() error { return c.Protect(ctx, key, "ref") },
		"release": func() error { return c.Release(ctx, key, "ref") },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled caller still waited on helper: %v", err)
			}
		})
	}
}

type cancellingDeadlineConnection struct {
	net.Conn
	mu        sync.Mutex
	cancel    context.CancelFunc
	cancelled chan struct{}
	deadline  time.Time
}

func (c *cancellingDeadlineConnection) SetDeadline(deadline time.Time) error {
	if deadline.After(time.Now().Add(time.Second)) {
		c.cancel()
		select {
		case <-c.cancelled:
		case <-time.After(100 * time.Millisecond):
		}
	}
	c.mu.Lock()
	c.deadline = deadline
	c.mu.Unlock()
	if !deadline.After(time.Now().Add(time.Second)) {
		close(c.cancelled)
	}
	return nil
}
func TestCancellationCannotBeOverwrittenByHelperDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := &cancellingDeadlineConnection{cancel: cancel, cancelled: make(chan struct{})}
	stop := armHelperDeadline(ctx, c)
	defer stop()
	select {
	case <-c.cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not set a deadline")
	}
	c.mu.Lock()
	deadline := c.deadline
	c.mu.Unlock()
	if deadline.After(time.Now().Add(time.Second)) {
		t.Fatal("bounded deadline overwrote cancellation")
	}
}
