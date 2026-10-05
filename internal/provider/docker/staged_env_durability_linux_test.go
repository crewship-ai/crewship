//go:build linux

package docker

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"github.com/moby/moby/api/types/container"
	"golang.org/x/sys/unix"

	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
)

// Inject a real fsync EIO in a disposable subprocess. This exercises unchanged
// production calls, rather than introducing a test-only filesystem hook. The
// parent retries through a fresh controller after the faulted process exits.
func TestStagedCleanupDurabilityBarrier(t *testing.T) {
	if raw := os.Getenv("CREWSHIP_TEST_CLEANUP_SYNC_OBJECT"); raw != "" {
		var c container.InspectResponse
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			t.Fatal(err)
		}
		p := &Provider{cfg: Config{OutputBasePath: os.Getenv("CREWSHIP_TEST_CLEANUP_SYNC_PATH"), InstanceID: "installation"}}
		journal := filepath.Join(p.cfg.OutputBasePath, runtimestage.DirName, "staged-env-cleanup", c.ID+".json")
		dir, err := os.Open(p.cfg.OutputBasePath)
		if err != nil {
			t.Fatal(err)
		}
		defer dir.Close()
		// Never return the filtered thread to the Go scheduler's thread pool.
		// This test goroutine and its subprocess terminate with the filter.
		runtime.LockOSThread()
		filter := []unix.SockFilter{
			{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
			{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: uint32(unix.SYS_FSYNC)},
			{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EIO)},
			{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
		}
		program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			t.Fatal(err)
		}
		if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0); err != nil {
			t.Fatal(err)
		}
		runtime.KeepAlive(filter)
		if err := unix.Fsync(int(dir.Fd())); !errors.Is(err, unix.EIO) {
			t.Fatalf("fault calibration did not deny an actual fsync: %v", err)
		}
		if err := p.finishStagedCleanup(c); !errors.Is(err, unix.EIO) {
			t.Fatalf("secret deletion durability failure not propagated: %v", err)
		}
		if _, err := os.Stat(journal); err != nil {
			t.Fatalf("cleanup intent lost before secret deletion durability: %v", err)
		}
		return
	}
	for _, absent := range []bool{false, true} {
		name := "unlink"
		if absent {
			name = "already-absent"
		}
		t.Run(name, func(t *testing.T) {
			p, c, target, journal := cleanupFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("recovery mutated Docker: %s", r.Method)
				}
				w.WriteHeader(http.StatusNotFound)
			})
			if err := p.publishStagedCleanup(c); err != nil {
				t.Fatal(err)
			}
			if absent {
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.CommandContext(t.Context(), exe, "-test.run=^TestStagedCleanupDurabilityBarrier$", "-test.count=1", "-test.timeout=30s")
			child.Env = append(os.Environ(), "CREWSHIP_TEST_CLEANUP_SYNC_OBJECT="+string(raw), "CREWSHIP_TEST_CLEANUP_SYNC_PATH="+p.cfg.OutputBasePath)
			if out, err := child.CombinedOutput(); err != nil {
				t.Fatalf("faulted cleanup process: %v\n%s", err, out)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("secret unlink did not happen: %v", err)
			}
			if _, err := os.Stat(journal); err != nil {
				t.Fatalf("durable intent not retained for a new controller: %v", err)
			}
			recovered := &Provider{cfg: p.cfg, client: p.client}
			if err := recovered.reconcileStagedEnvCleanup(t.Context(), 64); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(journal); !os.IsNotExist(err) {
				t.Fatalf("successful recovery retained intent: %v", err)
			}
		})
	}
}
