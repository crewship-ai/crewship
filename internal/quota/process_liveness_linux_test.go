//go:build linux

package quota

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMountInspectionDistinguishesLiveMissingAndZombieProcesses(t *testing.T) {
	if inactiveProcess(strconv.Itoa(os.Getpid())) {
		t.Fatal("live userspace process ignored during mount inspection")
	}
	if !inactiveProcess("missing-quota-fixture-process") {
		t.Fatal("absent process treated as mount owner")
	}
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	pid := strconv.Itoa(cmd.Process.Pid)
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := os.ReadFile("/proc/" + pid + "/stat")
		if err != nil {
			t.Fatal(err)
		}
		end := strings.LastIndexByte(string(raw), ')')
		if end >= 0 && strings.HasPrefix(string(raw[end+1:]), " Z ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned child did not reach zombie state")
		}
		time.Sleep(time.Millisecond)
	}
	if !inactiveProcess(pid) {
		t.Fatal("zombie with no userspace mounts blocked cleanup")
	}
}
