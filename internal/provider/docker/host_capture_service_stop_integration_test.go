//go:build integration && linux

package docker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/runoutput"
)

var hostCaptureBinaryFlag = flag.String("host-capture-binary", "", "built cmd/crewship for the real capture helper")

// #5081 live qualification. The server unit runs with KillMode=control-group,
// so stopping it delivers SIGTERM to the server and to every protected capture
// helper it started. This test puts the controller and the real cmd/crewship
// helper in a transient systemd scope and lets the actual service manager stop
// it (stage uses TimeoutStopSec=45). The helper must turn that signal into its
// own cancellation: exact stop of the admitted exec and all its descendants,
// the whole-tree receipt and a terminal result, all before the scope is gone.
// That terminal result plus the receipt is what restart recovery settles from
// (orchestrator.RetainedEndConfirmed, work.RecoverRetainedResult). A second
// launch of the same run is still refused, and the workload started once.
//
// The controller is a fixture that dies on the signal at once, so this
// qualifies the helper's own handling of the service signal. The server's
// shutdown sequence (Stop, Alive polling, park reasons) is covered by the
// internal/dispatch unit tests.
//
// It needs direct Docker access (mounts and exec), which the batch job Docker
// gateway refuses: compile with `go test -c -tags integration`, then run
//
//	./docker.test -test.run TestHostCaptureDockerServiceStop \
//	  -host-capture-sidecar=/abs/crewship-sidecar -host-capture-binary=/abs/crewship
//
// with both binaries built CGO_ENABLED=0.
func TestHostCaptureDockerServiceStopRecordsTerminal(t *testing.T) {
	requireHostCaptureQualificationUID(t)
	sidecar := os.Getenv("CREWSHIP_HOST_CAPTURE_SIDECAR")
	if *hostCaptureSidecarFlag != "" {
		sidecar = *hostCaptureSidecarFlag
	}
	binary := os.Getenv("CREWSHIP_HOST_CAPTURE_BINARY")
	if *hostCaptureBinaryFlag != "" {
		binary = *hostCaptureBinaryFlag
	}
	if !filepath.IsAbs(sidecar) || !filepath.IsAbs(binary) {
		t.Fatal("qualification requires absolute CREWSHIP_HOST_CAPTURE_SIDECAR and CREWSHIP_HOST_CAPTURE_BINARY (the real helper)")
	}
	systemdRun, err := exec.LookPath("systemd-run")
	if err != nil {
		t.Fatal("qualification requires systemd-run")
	}
	// Queued test runners may not inherit the session bus address; the user
	// manager of the current UID is still the one to ask.
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		runtime := fmt.Sprintf("/run/user/%d", os.Getuid())
		t.Setenv("XDG_RUNTIME_DIR", runtime)
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+runtime+"/bus")
	}
	if out, err := exec.Command("systemctl", "--user", "is-system-running").CombinedOutput(); err != nil && !bytes.Contains(out, []byte("degraded")) {
		t.Fatalf("qualification requires a user service manager: %v %s", err, out)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	image, err := cli.ImageInspect(ctx, "alpine:3")
	if err != nil {
		t.Fatalf("integration requires local alpine:3: %v", err)
	}
	root, err := os.MkdirTemp("/tmp", "crewship-servicestop-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	entrypoint := filepath.Join(root, "entrypoint.sh")
	if err = os.WriteFile(entrypoint, []byte("#!/bin/sh\nexec \"$@\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := stageRuntimeArtifacts(Config{OutputBasePath: filepath.Join(root, "data"), SidecarBinaryPath: sidecar, EntrypointPath: entrypoint}, quietLogger())
	p := &Provider{client: cli, cfg: cfg, logger: quietLogger()}
	p.detected.Host = cli.DaemonHost()
	workspace, output, crew := filepath.Join(root, "data", "workspace"), filepath.Join(root, "data", "output"), filepath.Join(root, "data", "crew")
	for _, dir := range []string{workspace, output, crew} {
		if err = os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	mounts, err := p.buildMounts("fixture", "", workspace, output, crew)
	if err != nil {
		t.Fatal(err)
	}
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "crewship-servicestop-" + strings.ToLower(rand.Text()), Config: &container.Config{Image: image.ID, User: "1001:1001", Cmd: []string{"sleep", "120"}}, HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Mounts: mounts, Tmpfs: map[string]string{"/tmp": "rw,nosuid,nodev,size=1048576,mode=1777"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, e := cli.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true}); e != nil {
			t.Errorf("remove owned container: %v", e)
		}
	}()
	if _, err = cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	if qualified, err := p.QualifyHostCapture(ctx, created.ID); err != nil || !qualified {
		t.Fatalf("stop helper not qualified: %v %v", qualified, err)
	}
	// The run directory's parent is the capacity root; keep it run-only.
	runs := filepath.Join(root, "runs")
	if err = os.Mkdir(runs, 0700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(runs, "run")
	// A workload that never ends by itself, with a long-lived child and a
	// detached (own session) descendant: only a whole-tree stop ends them.
	command := runoutput.HostCommand{ContainerID: created.ID, User: "1001:1001", Args: []string{"/bin/sh", "-c", `printf x >> /tmp/starts; sleep 300 & setsid sleep 301 & printf 'started\n'; printf '%0200d\n' 0; while :; do sleep 0.05; done`}}
	endpoint, authority, err := p.PrepareHostCapture(ctx, provider.ExecConfig{ContainerID: created.ID, User: "1001:1001"}, directory, []string{"unused"})
	if err != nil {
		t.Fatal(err)
	}
	input := runoutput.HostInput{Authority: authority, Endpoint: endpoint, Directory: directory, Deadline: time.Now().Add(10 * time.Minute), MaxBytes: runoutput.DefaultLimit, Command: command}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	unit := fmt.Sprintf("crewship-servicestop-%d-%d", os.Getpid(), time.Now().UnixNano())
	controller := exec.Command(systemdRun, "--user", "--scope", "--quiet", "--collect", "--unit="+unit,
		"-p", "KillMode=control-group", "-p", "TimeoutStopSec=45",
		executable, "-test.run=^TestHostCaptureDockerSubprocess$", "--", "controller")
	controller.Env = append(os.Environ(), "CREWSHIP_HOST_DOCKER_CHILD=1", "CREWSHIP_HOST_CAPTURE_BINARY="+binary)
	controller.Stdin = bytes.NewReader(data)
	var diagnostic bytes.Buffer
	controller.Stderr = &diagnostic
	if err = controller.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- controller.Wait() }()
	defer func() {
		_ = exec.Command("systemctl", "--user", "kill", "--signal=SIGKILL", unit+".scope").Run()
		select {
		case <-waited:
		case <-time.After(10 * time.Second):
		}
	}()
	eventually := func(what string, check func() bool) {
		t.Helper()
		for !check() {
			select {
			case err := <-waited:
				t.Fatalf("%s: controller scope exited early: %v; stderr=%s", what, err, diagnostic.String())
			case <-ctx.Done():
				files, _ := os.ReadDir(directory)
				identity, _ := os.ReadFile(filepath.Join(directory, "identity.json"))
				events, _ := os.ReadFile(filepath.Join(directory, "output", "events.jsonl"))
				outputs, _ := os.ReadDir(filepath.Join(directory, "output"))
				t.Fatalf("%s: %v; controller stderr=%s files=%v output=%v identity=%s events=%s", what, ctx.Err(), diagnostic.String(), files, outputs, identity, events)
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	eventually("workload output", func() bool {
		data, _ := os.ReadFile(filepath.Join(directory, "output", "events.jsonl"))
		return bytes.Contains(data, []byte(`"kind":"output"`))
	})
	identity, err := runoutput.ReadHostIdentity(directory)
	if err != nil || identity.ExecID == "" {
		t.Fatalf("identity=%+v %v", identity, err)
	}
	if out, err := exec.Command("systemctl", "--user", "show", "-p", "ActiveState", unit+".scope").Output(); err != nil || !bytes.Contains(out, []byte("ActiveState=active")) {
		t.Fatalf("scope not active: %s %v", out, err)
	}

	// The service manager stops the unit: SIGTERM to the whole cgroup, then
	// SIGKILL only after TimeoutStopSec. A helper that dies on the signal
	// leaves the exec running and the capture without a terminal result.
	began := time.Now()
	if out, err := exec.CommandContext(ctx, "systemctl", "--user", "stop", unit+".scope").CombinedOutput(); err != nil {
		t.Fatalf("service stop: %v %s", err, out)
	}
	stopTook := time.Since(began)
	t.Logf("service manager stopped the scope in %s", stopTook)
	if stopTook > 30*time.Second {
		t.Fatalf("stop needed the SIGKILL escalation (%s)", stopTook)
	}

	snapshot, err := runoutput.Inspect(ctx, filepath.Join(directory, "output"))
	if err != nil || !snapshot.Complete || snapshot.Result == nil {
		helperErr, _ := os.ReadFile(filepath.Join(directory, "helper-error"))
		t.Fatalf("capture has no terminal result after service stop: snapshot=%+v err=%v helper=%s", snapshot, err, helperErr)
	}
	if snapshot.Result.Reason != "execution_cancelled" {
		t.Fatalf("terminal reason=%q, want execution_cancelled", snapshot.Result.Reason)
	}
	// Recovery evidence: the whole-tree stop receipt bound to the recorded exec.
	if confirmed, err := runoutput.HostStopConfirmed(directory, identity.ExecID); err != nil || !confirmed {
		t.Fatalf("no whole-tree stop receipt: %v %v", confirmed, err)
	}
	inspected, err := cli.ExecInspect(ctx, identity.ExecID, client.ExecInspectOptions{})
	if err != nil || inspected.Running {
		t.Fatalf("exec still running after service stop: %+v %v", inspected, err)
	}
	// A restarted server observes the run only through what is on disk: it
	// restores the observer from the recorded identity (orchestrator
	// hostRunObserver) and asks the provider whether the exec still runs.
	observer, closeObserver, err := p.RestoreHostCapture(runoutput.HostInput{Directory: directory, Endpoint: endpoint, Authority: authority, Command: runoutput.HostCommand{ContainerID: created.ID, User: "1001:1001"}}, identity.ExecID)
	if err != nil {
		t.Fatalf("restore observer after restart: %v", err)
	}
	running, _, err := observer.Inspect(ctx, identity.ExecID)
	closeObserver.Close()
	if err != nil || running {
		t.Fatalf("restored observer sees running=%v err=%v", running, err)
	}
	// No double execution: a restarted server cannot launch the same run again.
	if _, err = runoutput.StartHost(ctx, input, []string{binary, "--internal-run-capture"}, nil); !errors.Is(err, runoutput.ErrClaimed) {
		t.Fatalf("relaunch after restart admitted: %v", err)
	}
	count, err := p.Exec(ctx, provider.ExecConfig{ContainerID: created.ID, User: "1001:1001", Cmd: []string{"cat", "/tmp/starts"}})
	if err != nil {
		t.Fatal(err)
	}
	starts, err := io.ReadAll(count.Reader)
	count.Reader.Close()
	if err != nil || string(starts) != "x" {
		t.Fatalf("workload launch count=%q err=%v", starts, err)
	}
	// Busybox ps truncates args without a tty; read every cmdline instead.
	ps, err := p.Exec(ctx, provider.ExecConfig{ContainerID: created.ID, User: "1001:1001", Cmd: []string{"/bin/sh", "-c", `for p in /proc/[0-9]*; do tr '\0' ' ' < "$p/cmdline" 2>/dev/null; echo; done`}})
	if err != nil {
		t.Fatal(err)
	}
	procs, _ := io.ReadAll(ps.Reader)
	ps.Reader.Close()
	for _, survivor := range []string{"sleep 300", "sleep 301", "while :"} {
		if bytes.Contains(procs, []byte(survivor)) {
			t.Fatalf("workload descendant %q survived the service stop:\n%s", survivor, procs)
		}
	}
}
