package devcontainer

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
)

type rootlessCommitMock struct {
	mockCommitClient
	socket string
}

func (*rootlessCommitMock) ServerVersion(context.Context, client.ServerVersionOptions) (client.ServerVersionResult, error) {
	return client.ServerVersionResult{Components: []system.ComponentVersion{{Name: "Podman Engine"}}}, nil
}

func (m *rootlessCommitMock) DaemonHost() string { return "unix://" + m.socket }

// Pass an already-open listener to a child, as systemd does. The process
// writing the response and the process that opened the listener have distinct
// OOM settings. Only the child's setting can reproduce the create/exec fix.
func TestRootlessPodmanProvisionCreate(t *testing.T) {
	const helper = "CREWSHIP_TEST_PODMAN_SERVICE_OOM"
	if value := os.Getenv(helper); value != "" {
		f := os.NewFile(3, "activation socket")
		l, err := net.FileListener(f)
		if err != nil {
			t.Fatal(err)
		}
		if os.Getuid() == 0 {
			if err = syscall.Setgid(65534); err != nil {
				t.Fatal(err)
			}
			if err = syscall.Setuid(65534); err != nil {
				t.Fatal(err)
			}
		}
		if err = os.WriteFile("/proc/self/oom_score_adj", []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		t.Fatal(http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/info" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Libpod-Api-Version", "6.2.0")
			fmt.Fprint(w, `{"SecurityOptions":["name=rootless"]}`)
		})))
	}
	current, err := os.ReadFile("/proc/self/oom_score_adj")
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := strconv.Atoi(strings.TrimSpace(string(current)))
	if err != nil {
		t.Fatal(err)
	}
	want := min(1000, max(0, baseline)+237)
	socket := filepath.Join(t.TempDir(), "podman.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if os.Getuid() == 0 {
		if err = os.Chown(socket, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	fd, err := listener.File()
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRootlessPodmanProvisionCreate$")
	cmd.Env = append(os.Environ(), helper+"="+strconv.Itoa(want))
	cmd.ExtraFiles = []*os.File{fd}
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	m := &rootlessCommitMock{socket: socket}
	p := NewProvisioner(m, nil, nil, testLogger())
	if _, err = p.createTempContainer(context.Background(), offlineBaseImage); err != nil {
		t.Fatal(err)
	}
	if got := m.createdContainers[0].hostCfg.OomScoreAdj; got != want {
		t.Fatalf("create OomScoreAdj = %d, want service value %d", got, want)
	}
}
