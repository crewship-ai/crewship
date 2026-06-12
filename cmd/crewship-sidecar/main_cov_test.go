package main

import (
	"bufio"
	"flag"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// withStdin swaps os.Stdin for the read end of a pipe carrying `data` and
// restores it on cleanup.
func withStdin(t *testing.T, data string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		r.Close()
	})
	go func() {
		_, _ = w.WriteString(data)
		w.Close()
	}()
}

func TestReadStdin_ReadsUntilEOF(t *testing.T) {
	// Larger than the internal 4096-byte chunk so the loop iterates.
	payload := strings.Repeat("x", 4096*2+123)
	withStdin(t, payload)

	got, err := readStdin()
	if err != nil {
		t.Fatalf("readStdin: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("readStdin returned %d bytes, want %d", len(got), len(payload))
	}
}

func TestReadStdin_PropagatesReadError(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	r.Close() // reading a closed file errors with non-EOF
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })

	if _, err := readStdin(); err == nil {
		t.Fatal("expected error reading from closed stdin")
	}
}

// runMain executes main() in-process: flags reset, stdin scripted, stdout
// captured. Once the sidecar reports SIDECAR_READY, a SIGTERM triggers the
// graceful-shutdown path and main returns.
func runMain(t *testing.T, stdin string) {
	t.Helper()

	withStdin(t, stdin)

	// Fresh FlagSet so repeated main() calls don't trip "flag redefined".
	oldArgs, oldFlags := os.Args, flag.CommandLine
	os.Args = []string{"crewship-sidecar", "-addr", "127.0.0.1:0"}
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	t.Cleanup(func() {
		os.Args = oldArgs
		flag.CommandLine = oldFlags
	})

	// Capture stdout so the SIDECAR_READY line can gate the SIGTERM.
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = wOut
	t.Cleanup(func() {
		os.Stdout = oldStdout
		rOut.Close()
	})

	ready := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(rOut)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "SIDECAR_READY") {
				ready <- sc.Text()
				return
			}
		}
		close(ready)
	}()

	done := make(chan struct{})
	go func() {
		main()
		close(done)
	}()

	select {
	case line, ok := <-ready:
		if !ok {
			t.Fatal("stdout closed before SIDECAR_READY")
		}
		if !strings.Contains(line, "SIDECAR_READY") {
			t.Fatalf("unexpected readiness line %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("sidecar never reported SIDECAR_READY")
	}

	// Graceful shutdown via the signal handler main installs.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("main did not exit after SIGTERM")
	}
}

// Full main() lifecycle with the modern object-format stdin payload.
func TestMain_ObjectInputLifecycle(t *testing.T) {
	runMain(t, `{"credentials":[{"provider":"anthropic","value":"sk-test"}],"network_policy":{}}`)
}

// Legacy array-of-credentials stdin payload takes the fallback parse path.
func TestMain_LegacyArrayInputLifecycle(t *testing.T) {
	runMain(t, `[{"provider":"openai","value":"sk-legacy"}]`)
}

// --version must print the version banner and exit 0 — exercised in a
// subprocess because it calls os.Exit. Bad stdin must exit 1.
func TestMain_SubprocessExitPaths(t *testing.T) {
	if os.Getenv("SIDECAR_MAIN_BEHAVIOR") != "" {
		os.Args = []string{"crewship-sidecar"}
		if os.Getenv("SIDECAR_MAIN_BEHAVIOR") == "version" {
			os.Args = append(os.Args, "-version")
		}
		flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
		main()
		return
	}

	t.Run("version exits zero", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=TestMain_SubprocessExitPaths")
		cmd.Env = append(os.Environ(), "SIDECAR_MAIN_BEHAVIOR=version")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("version subprocess failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "crewship-sidecar version") {
			t.Errorf("output %q missing version banner", out)
		}
	})

	t.Run("unparsable stdin exits one", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=TestMain_SubprocessExitPaths")
		cmd.Env = append(os.Environ(), "SIDECAR_MAIN_BEHAVIOR=badstdin")
		cmd.Stdin = strings.NewReader("{definitely not json")
		out, err := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		if !asExitError(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("expected exit code 1, got err=%v\n%s", err, out)
		}
		if !strings.Contains(string(out), "failed to parse stdin") {
			t.Errorf("output %q missing parse failure log", out)
		}
	})
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}
