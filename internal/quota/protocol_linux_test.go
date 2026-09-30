//go:build linux

package quota

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
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
	if _, err = (Client{Socket: socket}).Ensure(Key{"crew", "database", "data", 1}, 64<<20); !errors.Is(err, ErrDenied) {
		t.Fatalf("unprivileged helper impersonation accepted: %v", err)
	}
}
func TestQuotaClientIdentityProbe(t *testing.T) {
	socket := os.Getenv("CREWSHIP_QUOTA_CLIENT_PROBE_SOCKET")
	if socket == "" {
		// SKIP-WAIVER(#2703): this helper is invoked only by the quota_live parent under explicit UID1000/1001/1002 with its private socket environment.
		t.Skip("forked identity probe only")
	}
	d, err := (Client{Socket: socket}).Ensure(Key{"synthetic-crew", "socket-probe", "data", 1}, 64<<20)
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
	if _, err = (Client{Socket: socket}).Ensure(Key{"../host", "socket-probe", "data", 1}, 64<<20); err == nil {
		t.Fatal("caller path accepted by privileged helper")
	}
}
