package pipeline

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestScriptProcessStop_KillsChildBeforeSideEffect(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid unavailable")
	}
	root := t.TempDir()
	control := filepath.Join(root, "control")
	marker := filepath.Join(root, "unwanted-output")
	cmd := exec.Command("setsid", "sh", "-c", scriptProcessWrapper, "crewship-script", control, "sh", "-c", `trap '' TERM; (sleep 1; touch "$1") & wait`, "test", marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(filepath.Join(control, "pid")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop := exec.Command("sh", "-c", scriptStopWrapper, "crewship-script-stop", control)
	if out, err := stop.CombinedOutput(); err != nil {
		t.Fatalf("stop failed: %v %s", err, out)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("child survived cancellation: %v", err)
	}
}
