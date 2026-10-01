//go:build linux && quota_live

package quota

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Uses only its own root-created fixture and explicitly dropped child UIDs.
func TestLiveSocketPeerIdentityAndPrivateCatalog(t *testing.T) {
	if os.Getenv("CREWSHIP_LIVE_QUOTA_BACKEND") != "1" || os.Geteuid() != 0 {
		t.Fatal("requires explicit root helper probe")
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
		if err := b.Remove(context.Background(), key); err != nil {
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
