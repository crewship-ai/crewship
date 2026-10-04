//go:build integration && linux

package stagedstart

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/stagedstart/testfixture"
	"github.com/moby/moby/client"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This exercises the standalone static modes before any provider selector can
// activate them. Missing Docker or the local fixture image is a failure.
func TestKeeperControlRealDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	docker := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	}
	must := func(args ...string) []byte {
		t.Helper()
		out, err := docker(args...)
		if err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, out)
		}
		return out
	}
	f := testfixture.New(t, ctx, []byte("#!/bin/sh\nset -eu\ntest \"$1\" = --bootstrap-only\necho bootstrapped >> /tmp/bootstrap-count\n"))
	root, binary, script := f.Root, f.Binary, f.Bootstrap
	memoryDir := filepath.Join(root, "memory")
	if err := os.Mkdir(memoryDir, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(memoryDir, 0777); err != nil {
		t.Fatal(err)
	}
	f.WriteCanaries(t, memoryDir)
	probe := filepath.Join(root, "peerprobe")
	probeBuild := exec.CommandContext(ctx, "go", "build", "-o", probe, "./testdata/peerprobe")
	probeBuild.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if out, err := probeBuild.CombinedOutput(); err != nil {
		t.Fatalf("probe build: %v %s", err, out)
	}
	if err := os.Chmod(probe, 0555); err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(must("create", "--user", "1002:1002", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--no-healthcheck", "--pids-limit", "100", "--memory", "128m", "--network", f.Prefix, "--env", "ENV=", "--tmpfs", "/tmp:rw,mode=1777", "--mount", "type=bind,src="+binary+",dst="+Binary+",readonly", "--mount", "type=bind,src="+script+",dst="+Bootstrap+",readonly", "--mount", "type=bind,src="+probe+",dst=/peerprobe,readonly", "--mount", "type=bind,src="+memoryDir+",dst=/crew/shared/.memory", "--entrypoint", Binary, f.ImageID, "--staged-keeper")))
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if out, err := exec.CommandContext(cleanup, "docker", "rm", "-f", id).CombinedOutput(); err != nil {
			t.Errorf("exact fixture cleanup: %v %s", err, out)
		}
	}()
	must("start", id)
	control := func(operation, nonce string) Status {
		t.Helper()
		out := must("exec", "--user", "1002:1002", id, Binary, "--staged-control", operation, nonce, "fixture-challenge")
		var status Status
		if err := json.Unmarshal(out, &status); err != nil || status.Challenge != "fixture-challenge" || status.Error != "" {
			t.Fatalf("control response: %v %s", err, out)
		}
		return status
	}
	var initial Status
	for {
		out, err := docker("exec", "--user", "1002:1002", id, Binary, "--staged-control", "status", "", "fixture-challenge")
		if err == nil && json.Unmarshal(out, &initial) == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("keeper not available", err)
		case <-time.After(25 * time.Millisecond):
		}
	}
	candidate, err := f.Client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f.Calibrate(t, candidate.Container, memoryDir)
	for _, name := range testfixture.Canaries {
		if _, err := os.Stat(filepath.Join(memoryDir, name+"-"+candidate.Container.Config.Hostname)); !os.IsNotExist(err) {
			t.Fatalf("keeper executed image startup before release: %s %v", name, err)
		}
	}
	must("exec", "--user", "0:0", id, "/peerprobe", "deny")
	must("exec", "--user", "1001:1001", id, "/peerprobe", "deny")
	flood := exec.CommandContext(ctx, "docker", "exec", "--user", "1001:1001", id, "/peerprobe", "flood")
	output, err := flood.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := flood.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || scanner.Text() != "READY" {
		t.Fatal("malicious peer flood did not start")
	}
	for i := 0; i < 12; i++ {
		if state := control("status", ""); state.Phase != "staging" {
			t.Fatal("malicious peer flood changed keeper state", state)
		}
	}
	if err := flood.Wait(); err != nil {
		t.Fatal("peer flood failed", err)
	}
	if initial.Phase != "staging" || len(initial.Nonce) < 32 {
		t.Fatalf("initial generation: %+v", initial)
	}
	deny := func(args ...string) {
		t.Helper()
		out, err := docker(append([]string{"exec", "--user", "1001:1001", id, Binary}, args...)...)
		if err == nil {
			t.Fatalf("untrusted operation admitted: %v %s", args, out)
		}
	}
	deny("--staged-control", "seal", initial.Nonce, "fixture-challenge")
	deny("--staged-bootstrap", initial.Nonce)
	deny("--staged-exec", initial.Nonce, "/bin/sh", "-c", "touch /tmp/forbidden")
	if out, err := docker("exec", "--user", "1002:1002", id, "/bin/sh", "-c", "test ! -e /tmp/forbidden && test ! -e /tmp/bootstrap-count"); err != nil {
		t.Fatalf("denial started image workload: %v %s", err, out)
	}
	if state := control("seal", initial.Nonce); state.Phase != "sealed" {
		t.Fatal(state)
	}
	if state := control("reserve", initial.Nonce); state.Phase != "reserved" {
		t.Fatal(state)
	}
	must("exec", "--user", "1001:1001", id, Binary, "--staged-bootstrap", initial.Nonce)
	if state := control("status", ""); state.Phase != "ready" {
		t.Fatal(state)
	}
	deny("--staged-bootstrap", initial.Nonce)
	out := must("exec", "--user", "1001:1001", "--env", "ENV=/tmp/untrusted-shell", "--env", "LD_PRELOAD=/tmp/untrusted-loader", id, Binary, "--staged-exec", initial.Nonce, "/bin/sh", "-c", "test -z \"${ENV+x}\"; test -z \"${LD_PRELOAD+x}\"; cat /tmp/bootstrap-count")
	if strings.TrimSpace(string(out)) != "bootstrapped" {
		t.Fatalf("bootstrap executed more than once: %s", out)
	}
	must("restart", id)
	restarted := control("status", "")
	if restarted.Nonce == initial.Nonce || restarted.Phase != "staging" {
		t.Fatalf("restart generation unchanged: %+v", restarted)
	}
	deny("--staged-bootstrap", initial.Nonce)
	deny("--staged-exec", initial.Nonce, "/bin/sh", "-c", "touch /tmp/forbidden")
}
