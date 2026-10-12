//go:build integration && unix

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
	"log/slog"
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

var hostCaptureSidecarFlag = flag.String("host-capture-sidecar", "", "built sidecar for actual Docker stop qualification")

func requireHostCaptureQualificationUID(t *testing.T) {
	t.Helper()
	if os.Getenv("CREWSHIP_HOST_CAPTURE_REQUIRE_UNPRIVILEGED") == "1" {
		uid := os.Geteuid()
		if uid == 0 || uid == 1001 || uid == 1002 {
			t.Fatalf("required host capture qualification needs a distinct unprivileged controller UID; got %d", uid)
		}
	}
}

// The child uses the production adapter; only the controller fixture differs
// from the production server. The helper receives secrets via its stdin pipe.
func TestHostCaptureDockerSubprocess(t *testing.T) {
	if os.Getenv("CREWSHIP_HOST_DOCKER_CHILD") != "1" {
		return
	}
	input, err := runoutput.ReadHostInput(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read input:", err)
		os.Exit(121)
	}
	mode := os.Args[len(os.Args)-1]
	if mode == "helper" {
		executor, closer, err := NewHostCaptureExecutor(input)
		if err != nil {
			_ = os.WriteFile(filepath.Join(input.Directory, "helper-error"), []byte(err.Error()), 0600)
			os.Exit(122)
		}
		defer closer.Close()
		_, err = runoutput.CaptureHost(context.Background(), input, executor)
		if err != nil {
			_ = os.WriteFile(filepath.Join(input.Directory, "helper-error"), []byte(err.Error()), 0600)
			os.Exit(123)
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(124)
	}
	helperArgs := []string{executable, "-test.run=^TestHostCaptureDockerSubprocess$", "--", "helper"}
	helperEnv := []string{"CREWSHIP_HOST_DOCKER_CHILD=1"}
	if binary := os.Getenv("CREWSHIP_HOST_CAPTURE_BINARY"); binary != "" {
		helperArgs = []string{binary, "--internal-run-capture"}
		helperEnv = nil
	}
	_, err = runoutput.StartHost(context.Background(), input, helperArgs, helperEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start helper:", err)
		os.Exit(125)
	}
	select {}
}

func TestHostCaptureDockerSurvivesControllerSIGKILL(t *testing.T) {
	requireHostCaptureQualificationUID(t)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if _, err = cli.Ping(ctx, client.PingOptions{}); err != nil {
		t.Fatalf("integration requires Docker: %v", err)
	}
	image, err := cli.ImageInspect(ctx, "alpine:3")
	if err != nil {
		t.Fatalf("integration requires local alpine:3: %v", err)
	}
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "crewship-hostcapture-" + strings.ToLower(rand.Text()), Config: &container.Config{Image: image.ID, User: "1001:1001", Cmd: []string{"sleep", "60"}}, HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Tmpfs: map[string]string{"/tmp": "rw,nosuid,nodev,size=1048576,mode=1777"}}})
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
	p := &Provider{client: cli, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	p.detected.Host = cli.DaemonHost()
	fixtureRoot, err := os.MkdirTemp("/tmp", "crewship-hostcapture-docker-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(fixtureRoot)
	directory := filepath.Join(fixtureRoot, "run")
	command := runoutput.HostCommand{ContainerID: created.ID, User: "1001:1001", Args: []string{"/bin/sh", "-c", `test "$(id -u)" = 1001 || exit 90; test ! -e "$1" || exit 91; printf x >> /tmp/starts; printf 'before\n'; printf '%0200d\n' 0; while [ ! -e /tmp/release ]; do sleep 0.02; done; printf 'after\n'; printf 'diagnostic\n' >&2; exit 7`, "fixture", directory}}
	endpoint, authority, err := p.PrepareHostCapture(ctx, provider.ExecConfig{ContainerID: created.ID, User: "1001:1001"}, directory, []string{"unused-normal-completion"})
	if err != nil {
		t.Fatal(err)
	}
	input := runoutput.HostInput{Authority: authority, Endpoint: endpoint, Directory: directory, Deadline: time.Now().Add(30 * time.Second), MaxBytes: runoutput.DefaultLimit, Command: command}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	controller := exec.Command(executable, "-test.run=^TestHostCaptureDockerSubprocess$", "--", "controller")
	controller.Env = []string{"CREWSHIP_HOST_DOCKER_CHILD=1"}
	if binary := os.Getenv("CREWSHIP_HOST_CAPTURE_BINARY"); binary != "" {
		if !filepath.IsAbs(binary) {
			t.Fatal("CREWSHIP_HOST_CAPTURE_BINARY must be absolute")
		}
		controller.Env = append(controller.Env, "CREWSHIP_HOST_CAPTURE_BINARY="+binary)
		t.Log("using real cmd/crewship hidden capture helper:", binary)
	}
	controller.Stdin = bytes.NewReader(data)
	var diagnostic bytes.Buffer
	controller.Stderr = &diagnostic
	if err = controller.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = controller.Process.Kill(); _ = controller.Wait() }()
	wait := func(check func() bool) {
		t.Helper()
		for {
			if check() {
				return
			}
			if data, err := os.ReadFile(filepath.Join(directory, "helper-error")); err == nil {
				t.Fatalf("helper failed: %s", data)
			}
			select {
			case <-ctx.Done():
				files, _ := os.ReadDir(directory)
				identity, _ := os.ReadFile(filepath.Join(directory, "identity.json"))
				events, _ := os.ReadFile(filepath.Join(directory, "output", "events.jsonl"))
				t.Fatalf("capture timed out: %v; controller stderr=%s files=%v identity=%s events=%s", ctx.Err(), diagnostic.String(), files, identity, events)
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	wait(func() bool {
		data, _ := os.ReadFile(filepath.Join(directory, "output", "events.jsonl"))
		return bytes.Contains(data, []byte(`"kind":"output"`))
	})
	identity, err := runoutput.ReadHostIdentity(directory)
	if err != nil || identity.ExecID == "" {
		t.Fatalf("identity=%+v %v", identity, err)
	}
	if err = controller.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = controller.Wait()
	// Docker exec still exists while its controller is absent.
	inspected, err := cli.ExecInspect(ctx, identity.ExecID, client.ExecInspectOptions{})
	if err != nil || !inspected.Running {
		t.Fatalf("controller kill stopped exec: %+v %v", inspected, err)
	}
	release, err := p.Exec(ctx, provider.ExecConfig{ContainerID: created.ID, User: "1001:1001", Cmd: []string{"touch", "/tmp/release"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, release.Reader)
	release.Reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		snapshot, err := runoutput.Inspect(ctx, filepath.Join(directory, "output"))
		return err == nil && snapshot.Complete
	})
	snapshot, err := runoutput.Inspect(ctx, filepath.Join(directory, "output"))
	if err != nil || snapshot.Result == nil || snapshot.Result.ExitCode != 7 {
		t.Fatalf("terminal=%+v err=%v", snapshot, err)
	}
	var stdout, stderr strings.Builder
	_, complete, err := runoutput.ReadCommitted(filepath.Join(directory, "output"), 0, func(record runoutput.Record) error {
		if record.Kind == "output" {
			if record.Stream == "stdout" {
				stdout.Write(record.Data)
			} else {
				stderr.Write(record.Data)
			}
		}
		return nil
	})
	if err != nil || !complete || !strings.Contains(stdout.String(), "before\n") || !strings.Contains(stdout.String(), "after\n") || stderr.String() != "diagnostic\n" {
		t.Fatalf("retained streams stdout=%q stderr=%q complete=%v err=%v", stdout.String(), stderr.String(), complete, err)
	}
	if _, err = runoutput.StartHost(ctx, input, []string{executable}, nil); !errors.Is(err, runoutput.ErrClaimed) {
		t.Fatalf("duplicate launch admitted: %v", err)
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
}

// Stop is a separate qualification from natural completion. In particular,
// host UID1000 must authenticate namespace/start identity of workload UID1001
// and invoke the pidfd helper without broad pkill/container-stop fallbacks.
func TestHostCaptureDockerStopsOnlyAdmittedExec(t *testing.T) {
	runHostCaptureDockerQualification(t, "current", true)
}

func TestHostCaptureDockerQualifiesTrustedStopHelper(t *testing.T) {
	for _, scenario := range []string{"current", "alias", "old", "mismatched"} {
		t.Run(scenario, func(t *testing.T) { runHostCaptureDockerQualification(t, scenario, false) })
	}
}

func runHostCaptureDockerQualification(t *testing.T, scenario string, stopTarget bool) {
	t.Helper()
	requireHostCaptureQualificationUID(t)
	sidecar := os.Getenv("CREWSHIP_HOST_CAPTURE_SIDECAR")
	if *hostCaptureSidecarFlag != "" {
		sidecar = *hostCaptureSidecarFlag
	}
	if !filepath.IsAbs(sidecar) {
		t.Fatal("integration requires absolute CREWSHIP_HOST_CAPTURE_SIDECAR")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	image, err := cli.ImageInspect(ctx, "alpine:3")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("/tmp", "crewship-hoststop-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	entrypoint := filepath.Join(root, "entrypoint.sh")
	if err = os.WriteFile(entrypoint, []byte("#!/bin/sh\nexec \"$@\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := stageRuntimeArtifacts(Config{OutputBasePath: filepath.Join(root, "data"), SidecarBinaryPath: sidecar, EntrypointPath: entrypoint}, quietLogger())
	if scenario == "old" {
		if err = os.WriteFile(cfg.SidecarBinaryPath, []byte("#!/bin/sh\nexit 2\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
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
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "crewship-hoststop-" + strings.ToLower(rand.Text()), Config: &container.Config{Image: image.ID, User: "1001:1001", Cmd: []string{"sleep", "60"}}, HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Mounts: mounts}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := cli.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			t.Error(err)
		}
	}()
	if _, err = cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "run")
	if scenario == "mismatched" {
		// Atomic staging replaces the host path while the existing container keeps
		// the previous bind inode. It must not qualify by path spelling alone.
		replacement := cfg.SidecarBinaryPath + ".replacement"
		if err = os.WriteFile(replacement, []byte("#!/bin/sh\nexit 2\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.Rename(replacement, cfg.SidecarBinaryPath); err != nil {
			t.Fatal(err)
		}
	}
	if scenario == "alias" {
		alias := filepath.Join(root, "helper-alias")
		if err = os.Symlink(cfg.SidecarBinaryPath, alias); err != nil {
			t.Fatal(err)
		}
		p.cfg.SidecarBinaryPath = alias
	}
	qualified, err := p.QualifyHostCapture(ctx, created.ID)
	wantQualified := scenario == "current" || scenario == "alias"
	if err != nil || qualified != wantQualified {
		t.Fatalf("production-staged readonly helper scenario=%s qualified=%v want=%v err=%v", scenario, qualified, wantQualified, err)
	}
	if !stopTarget {
		return
	}
	endpoint, authority, err := p.PrepareHostCapture(ctx, provider.ExecConfig{ContainerID: created.ID, User: "1001:1001"}, directory, []string{"unused"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	input := runoutput.HostInput{Endpoint: endpoint, Authority: authority, Directory: directory, Deadline: time.Now().Add(20 * time.Second), MaxBytes: runoutput.DefaultLimit, Command: runoutput.HostCommand{ContainerID: created.ID, User: "1001:1001", Args: []string{"sleep", "60"}}}
	executor, closer, err := NewHostCaptureExecutor(input)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	execution, err := executor.Start(ctx, input.Command)
	if err != nil {
		t.Fatal(err)
	}
	defer execution.Stdout.Close()
	defer execution.Stderr.Close()
	other, err := cli.ExecCreate(ctx, created.ID, client.ExecCreateOptions{User: "1001:1001", Cmd: []string{"sleep", "60"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cli.ExecStart(ctx, other.ID, client.ExecStartOptions{Detach: true}); err != nil {
		t.Fatal(err)
	}
	if err = executor.Stop(ctx, execution.ID); err != nil {
		t.Fatalf("actual UID%d exact stop qualification failed: %v", os.Geteuid(), err)
	}
	running, _, err := executor.Inspect(ctx, execution.ID)
	if err != nil || running {
		t.Fatalf("target still running=%v err=%v", running, err)
	}
	untouched, err := cli.ExecInspect(ctx, other.ID, client.ExecInspectOptions{})
	if err != nil || !untouched.Running {
		t.Fatalf("unrelated exec affected: %+v err=%v", untouched, err)
	}
}
