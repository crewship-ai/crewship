//go:build integration && linux

package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
	dockerprovider "github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/runoutput"
)

type retainedDockerControllerInput struct {
	Input             runoutput.HostInput
	Run               orchestrator.RunState
	StatePath, Binary string
}

// The production orchestrator starts os.Executable. This test-only mode uses
// the same production Docker executor and capture engine as cmd/crewship;
// the separate provider acceptance also executes the built command itself.
func init() {
	if len(os.Args) != 2 || os.Args[1] != "--internal-run-capture" {
		return
	}
	input, err := runoutput.ReadHostInput(os.Stdin)
	if err != nil {
		os.Exit(125)
	}
	executor, closer, err := dockerprovider.NewHostCaptureExecutor(input)
	if err != nil {
		os.Exit(125)
	}
	_, err = runoutput.CaptureHost(context.Background(), input, executor)
	_ = closer.Close()
	if err != nil {
		os.Exit(125)
	}
	os.Exit(0)
}

type retainedDockerProvider struct {
	*mockContainer
	cli   *client.Client
	input runoutput.HostInput
}

func (*retainedDockerProvider) SupportsHostCapture() bool { return true }

// Transport fixture qualification is separate from the real production staged
// helper qualification exercised by the Docker provider integration suite.
func (*retainedDockerProvider) QualifyHostCapture(context.Context, string) (bool, error) {
	return true, nil
}
func (*retainedDockerProvider) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{ExecID: "synthetic-preflight", Reader: io.NopCloser(strings.NewReader("crewship-preflight-done\n"))}, nil
}
func (p *retainedDockerProvider) PrepareHostCapture(ctx context.Context, cfg provider.ExecConfig, directory string, _ []string) (string, json.RawMessage, error) {
	if cfg.ContainerID != p.input.Command.ContainerID || cfg.User != "1001:1001" || directory != p.input.Directory {
		return "", nil, fmt.Errorf("fixture binding mismatch")
	}
	// Preflights are deliberately synthetic; only this real cwd creation is
	// required before the literal CLI command is admitted to actual Docker.
	home := ""
	for _, value := range cfg.Env {
		if strings.HasPrefix(value, "HOME=") {
			home = strings.TrimPrefix(value, "HOME=")
		}
	}
	if !strings.HasPrefix(home, "/crew/") {
		return "", nil, fmt.Errorf("fixture HOME missing")
	}
	made, err := p.cli.ExecCreate(ctx, cfg.ContainerID, client.ExecCreateOptions{User: "1001:1001", Cmd: []string{"mkdir", "-p", cfg.WorkingDir, home}, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return "", nil, err
	}
	attached, err := p.cli.ExecAttach(ctx, made.ID, client.ExecAttachOptions{})
	if err != nil {
		return "", nil, err
	}
	_, err = io.Copy(io.Discard, attached.Reader)
	attached.Close()
	if err != nil {
		return "", nil, err
	}
	return p.input.Endpoint, p.input.Authority, nil
}
func (*retainedDockerProvider) RestoreHostCapture(runoutput.HostInput, string) (runoutput.HostExecutor, io.Closer, error) {
	return nil, nil, fmt.Errorf("fixture does not stop execution")
}

func TestRetainedDockerControllerChild(t *testing.T) {
	if os.Getenv("CREWSHIP_RETAINED_DOCKER_CHILD") != "1" {
		return
	}
	var spec retainedDockerControllerInput
	if err := json.NewDecoder(os.Stdin).Decode(&spec); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(121)
	}
	state, err := bbolt.New(spec.StatePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(122)
	}
	cli, err := client.New(client.WithHost(spec.Input.Endpoint))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(123)
	}
	p := &retainedDockerProvider{mockContainer: &mockContainer{}, cli: cli, input: spec.Input}
	o := orchestrator.New(p, state, logging.New("error", "json", nil))
	o.SetHostOutputRoot(filepath.Dir(spec.Input.Directory))
	o.SetSidecarEnabled(false)
	req := orchestrator.AgentRunRequest{AgentID: spec.Run.AgentID, AgentSlug: spec.Run.AgentSlug, WorkspaceID: spec.Run.WorkspaceID, RunID: spec.Run.ID, ContainerID: spec.Run.ContainerID, CLIAdapter: "CLAUDE_CODE", UserMessage: "literal $(never); ' quoted", TimeoutSecs: 45, SkipSidecar: true, SkipConvHistory: true}
	req.OnExecStarted = func(execID string) {
		if err := os.WriteFile(filepath.Join(filepath.Dir(spec.StatePath), "controller-ready"), []byte(execID), 0600); err != nil {
			os.Exit(124)
		}
	}
	if err = o.RunAgent(context.Background(), req, nil); err != nil {
		fmt.Fprintln(os.Stderr, "RunAgent:", err)
		os.Exit(125)
	}
	os.Exit(0)
}

// This acceptance proves a killed controller's persisted bbolt capability is
// projected by a newly constructed Server after real Docker execution finishes
// in its absence. RunAgent admission, literal launch and protected transport
// are real; the provider preflight setup is narrowly synthetic.
func TestRetainedDockerServerRestartProjectsActualProviderExit(t *testing.T) {
	if os.Getenv("CREWSHIP_HOST_CAPTURE_REQUIRE_UNPRIVILEGED") == "1" {
		uid := os.Geteuid()
		if uid == 0 || uid == 1001 || uid == 1002 {
			t.Fatalf("required protected host recovery needs distinct unprivileged controller UID; got %d", uid)
		}
	}
	binary := os.Getenv("CREWSHIP_HOST_CAPTURE_BINARY")
	if !filepath.IsAbs(binary) {
		t.Fatal("integration requires absolute CREWSHIP_HOST_CAPTURE_BINARY built from cmd/crewship")
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
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("/tmp", "crewship-retained-restart-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	script := filepath.Join(root, "fixture-claude")
	scriptData := `#!/bin/sh
test "$(id -u)" = 1001 || exit 90
case "$HOME" in /crew/*) ;; *) exit 91 ;; esac
test -d "$HOME" || exit 93
case "$PWD" in /output/*) ;; *) exit 92 ;; esac
printf x >> /tmp/starts
printf 'before\n'
printf '%0200d\n' 0
while [ ! -e /tmp/release ]; do sleep 0.02; done
printf 'after\n'
printf 'diagnostic\n' >&2
printf '%s\n' '{"type":"result","subtype":"success","is_error":true,"result":"fixture denied","usage":{"input_tokens":31,"output_tokens":7}}'
exit 7
`
	if err = os.WriteFile(script, []byte(scriptData), 0755); err != nil {
		t.Fatal(err)
	}
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "crewship-retained-" + strings.ToLower(rand.Text()), Config: &container.Config{Image: image.ID, User: "1001:1001", Cmd: []string{"sleep", "90"}, Labels: map[string]string{"ai.crewship.retained-test": "owned"}}, HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Mounts: []mount.Mount{{Type: mount.TypeBind, Source: script, Target: "/usr/local/bin/claude", ReadOnly: true}}, Tmpfs: map[string]string{"/tmp": "rw,nosuid,nodev,size=1048576,mode=1777", "/crew": "rw,nosuid,nodev,size=1048576,mode=1777", "/output": "rw,nosuid,nodev,size=1048576,mode=1777"}}})
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
	inspected, err := cli.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	outputRoot := filepath.Join(root, "capture")
	if err = os.Mkdir(outputRoot, 0700); err != nil {
		t.Fatal(err)
	}
	runID := "retained-docker-restart"
	directory := filepath.Join(outputRoot, runID)
	// Exact immutable daemon identity, no ancestor spool binds or fencing in this isolated
	// network-none container. No provider.New startup reconciliation is invoked.
	authority, err := json.Marshal(map[string]any{"version": 1, "container_id": created.ID, "image_id": inspected.Container.Image, "started_at": inspected.Container.State.StartedAt, "directory": directory, "fence": false, "crew_id": "", "crew_slug": "", "stop_args": []string{"unused-normal-completion"}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(45 * time.Second)
	input := runoutput.HostInput{Endpoint: cli.DaemonHost(), Authority: authority, Directory: directory, Deadline: deadline, MaxBytes: runoutput.DefaultLimit, Command: runoutput.HostCommand{ContainerID: created.ID, User: "1001:1001", Args: []string{"/bin/sh", "-c", `test "$(id -u)" = 1001 || exit 90; printf x >> /tmp/starts; printf 'before\n'; printf '%0200d\n' 0; while [ ! -e /tmp/release ]; do sleep 0.02; done; printf 'after\n'; printf 'diagnostic\n' >&2; printf '%s\n' '{"type":"result","subtype":"success","is_error":true,"result":"fixture denied","usage":{"input_tokens":31,"output_tokens":7}}'; exit 7`}}}
	run := orchestrator.RunState{ID: runID, AgentID: "retained-agent", AgentSlug: "retained", WorkspaceID: "retained-ws", ContainerID: created.ID, Status: "running", StartedAt: time.Now().UTC(), Output: &orchestrator.RunOutputState{Version: 2, Directory: filepath.Join(directory, "output"), Deadline: deadline, Adapter: "CLAUDE_CODE", Endpoint: input.Endpoint, Authority: authority}}
	statePath := filepath.Join(root, "state.db")
	data, err := json.Marshal(retainedDockerControllerInput{Input: input, Run: run, StatePath: statePath, Binary: binary})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	controller := exec.Command(executable, "-test.run=^TestRetainedDockerControllerChild$")
	controller.Env = []string{"CREWSHIP_RETAINED_DOCKER_CHILD=1"}
	controller.Stdin = bytes.NewReader(data)
	var diagnostic bytes.Buffer
	controller.Stderr = &diagnostic
	if err = controller.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = controller.Process.Kill(); _ = controller.Wait() }()
	wait := func(check func() bool) {
		t.Helper()
		for !check() {
			select {
			case <-ctx.Done():
				t.Fatalf("timed out: %v controller=%s", ctx.Err(), diagnostic.String())
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	wait(func() bool { _, err := os.Stat(filepath.Join(root, "controller-ready")); return err == nil })
	wait(func() bool {
		data, _ := os.ReadFile(filepath.Join(directory, "output", "events.jsonl"))
		return bytes.Contains(data, []byte(`"kind":"output"`))
	})
	identity, err := runoutput.ReadHostIdentity(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = controller.Wait()
	running, err := cli.ExecInspect(ctx, identity.ExecID, client.ExecInspectOptions{})
	if err != nil || !running.Running {
		t.Fatalf("execution lost with controller: %+v %v", running, err)
	}
	release, err := cli.ExecCreate(ctx, created.ID, client.ExecCreateOptions{User: "1001:1001", Cmd: []string{"touch", "/tmp/release"}, AttachStdout: true, AttachStderr: true})
	if err != nil {
		t.Fatal(err)
	}
	attached, err := cli.ExecAttach(ctx, release.ID, client.ExecAttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, attached.Reader)
	attached.Close()
	wait(func() bool {
		snapshot, err := runoutput.Inspect(ctx, filepath.Join(directory, "output"))
		return err == nil && snapshot.Complete
	})
	state, err := bbolt.New(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	cfg := config.Default()
	cfg.Auth.JWTSecret = "test-secret-for-server-routes-test-32"
	cfg.Storage.RunOutputPath = outputRoot
	db := openTestDB(t)
	mustExec(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('retained-ws','Recovery','retained-ws')`)
	mustExec(t, db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('retained-agent','retained-ws','Agent','retained','RUNNING')`)
	s := New(cfg, logging.New("error", "json", nil), &Deps{Container: &mockContainer{}, State: state, DB: db})
	defer s.StopBackground()
	if err = s.flushRetainedOutput(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.flushRetainedOutput(ctx); err != nil {
		t.Fatal(err)
	}
	var lines, failed, completed int
	if err = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND entry_type='exec.output_chunk'`, runID).Scan(&lines); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND entry_type='run.failed' AND json_extract(payload,'$.exit_code')=7 AND json_extract(payload,'$.metadata.in_band_error')=1`, runID).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND entry_type='run.completed'`, runID).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if lines < 4 || failed != 1 || completed != 0 {
		t.Fatalf("lines=%d failed7=%d completed=%d", lines, failed, completed)
	}
	raw, err := state.Get(ctx, "agent_runs", runID)
	if err != nil {
		t.Fatal(err)
	}
	var recovered orchestrator.RunState
	if err = json.Unmarshal(raw, &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.Status != "error" || recovered.ExecID != identity.ExecID {
		t.Fatalf("recovered=%+v", recovered)
	}
}
