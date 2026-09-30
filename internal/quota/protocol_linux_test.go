//go:build linux

package quota

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestClientRejectsUnprivilegedSocketPeer(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("unprivileged peer assertion")
	}
	socket := filepath.Join(t.TempDir(), "fake.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: socket})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err = (Client{socket}).Ensure(Key{"crew", "database", "data", 1}, 64<<20); !errors.Is(err, ErrDenied) {
		t.Fatalf("unprivileged helper impersonation accepted: %v", err)
	}
}
func TestQuotaClientIdentityProbe(t *testing.T) {
	socket := os.Getenv("CREWSHIP_QUOTA_CLIENT_PROBE_SOCKET")
	if socket == "" {
		t.Skip("forked identity probe only")
	}
	d, err := (Client{socket}).Ensure(Key{"synthetic-crew", "socket-probe", "data", 1}, 64<<20)
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
	if _, err = (Client{socket}).Ensure(Key{"../host", "socket-probe", "data", 1}, 64<<20); err == nil {
		t.Fatal("caller path accepted by privileged helper")
	}
}
func TestLiveSocketPeerIdentityAndPrivateCatalog(t *testing.T) {
	if os.Getenv("CREWSHIP_LIVE_QUOTA_BACKEND") != "1" || os.Geteuid() != 0 {
		t.Skip("requires explicit root helper probe")
	}
	front, err := os.MkdirTemp("/tmp", "crewship-quota-socket-probe-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(front); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(front, 0755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(front, "catalog")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	b, err := NewBackend(root, 128<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(front, "helper.sock")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, socket, 1000, b) }()
	t.Cleanup(func() {
		cancel()
		<-done
		key := Key{"synthetic-crew", "socket-probe", "data", 1}
		if err := b.Remove(key); err != nil {
			t.Error(err)
		}
		b.Close()
	})
	for i := 0; i < 100; i++ {
		if _, err = os.Stat(socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range []uint32{1000, 1001, 1002} {
		cmd := exec.Command(executable, "-test.run=^TestQuotaClientIdentityProbe$", "-test.v")
		cmd.Env = append(os.Environ(), "CREWSHIP_QUOTA_CLIENT_PROBE_SOCKET="+socket)
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid}}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("identity%d: %v %s", uid, err, out)
		}
	}
	t.Log("root peer authenticated; serverUID1000 admitted; agent1001/broker1002 denied; root catalog hostpath private")
}
