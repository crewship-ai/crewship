//go:build integration && linux

package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"github.com/crewship-ai/crewship/internal/provider"
	dockerprovider "github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
)

var codexOuterNativeArtifact = flag.String("codex-outer-native-artifact", "", "Explicit static native Codex artifact for real Docker qualification")

// Release qualification requires an explicit real static Codex artifact. The
// fixture never installs a CLI, reads a login, calls a paid model or pulls an
// image. Its only provider is an isolated host bridge listener.
func TestCodexOuterActualNativeDocker(t *testing.T) {
	native := *codexOuterNativeArtifact
	if native == "" {
		t.Fatal("provide -codex-outer-native-artifact with the real static Codex artifact; wrapper/version output is not qualification")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture Docker %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	_ = docker("image", "inspect", "--format={{.Id}}", "alpine:3")
	dir := t.TempDir()
	label := fmt.Sprintf("crewship-outer-%d-%d", os.Getpid(), time.Now().UnixNano())
	network := label + "-net"
	f, err := os.Open(native)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	artifact, err := managedlaunch.CaptureFile("/opt/native/codex", f)
	if err != nil {
		t.Fatal("real native Codex required", err)
	}
	target, err := os.OpenFile(filepath.Join(dir, "native"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0555)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(target, f); err != nil {
		t.Fatal(err)
	}
	target.Close()
	versionHome := filepath.Join(dir, "version-home")
	if err := os.MkdirAll(filepath.Join(versionHome, "codex"), 0700); err != nil {
		t.Fatal(err)
	}
	versionCmd := exec.CommandContext(ctx, filepath.Join(dir, "native"), "--version")
	versionCmd.Env = []string{"HOME=" + versionHome, "CODEX_HOME=" + filepath.Join(versionHome, "codex"), "PATH=" + managedlaunch.SafePath}
	versionOut, err := versionCmd.Output()
	if err != nil {
		t.Fatal("identify explicit native artifact", err)
	}
	fields := strings.Fields(string(versionOut))
	if len(fields) != 2 {
		t.Fatal("unrecognized native version receipt")
	}
	version := fields[1]
	launcher := filepath.Join(dir, "launcher")
	build := exec.CommandContext(ctx, "go", "build", "-o", launcher, "../../cmd/crewship-sidecar")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build static launcher: %v %s", err, out)
	}
	entrypoint := filepath.Join(dir, "entrypoint.sh")
	if err := os.WriteFile(entrypoint, []byte("#!/bin/sh\nexec /bin/sleep infinity\n"), 0555); err != nil {
		t.Fatal(err)
	}
	dockerfile := "FROM alpine:3\nUSER 0\nRUN addgroup -g 1001 agent && adduser -D -u 1001 -G agent -s /bin/sh agent\nCOPY --chmod=0555 native /opt/native/codex\nENTRYPOINT [\"/bin/sleep\"]\nCMD [\"120\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0600); err != nil {
		t.Fatal(err)
	}
	docker("build", "--network=none", "--pull=false", "-t", label, dir)
	t.Cleanup(func() { exec.Command("docker", "image", "rm", label).Run() })
	image := docker("image", "inspect", "--format={{.Id}}", label)
	sum := sha256.Sum256([]byte(label))
	instance := fmt.Sprintf("%x", sum)
	p, err := dockerprovider.New(ctx, dockerprovider.Config{InstanceID: instance, SidecarBinaryPath: launcher, EntrypointPath: entrypoint, OutputBasePath: dir, ContainerPrefix: label, Network: network}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command("docker", "network", "rm", network).Run() })
	crew := provider.CrewConfig{ID: "outer-fixture", Slug: "fixture", CachedImage: image, MemoryMB: 1024, CPUs: 1}
	t.Cleanup(func() { p.RemoveCrewVolumes(context.Background(), crew.ID, crew.Slug) })
	id, err := p.EnsureCrewRuntime(ctx, crew)
	if err != nil {
		t.Fatal(err)
	}
	// Provider preparation can assign host directories to UID1001. Restore only
	// this fixture tree using the existing scoped ownership-cleanup pattern.
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		name := label + "-cleanup"
		defer exec.Command("docker", "rm", "-f", name).Run()
		cmd := exec.CommandContext(cleanup, "docker", "run", "--rm", "--name", name, "--network=none", "--label=crewship.temp=codex-outer-test", "--label=crewship.test="+label, "--user=0:0", "--cap-drop=ALL", "--cap-add=CHOWN", "--cap-add=DAC_OVERRIDE", "--cap-add=FOWNER", "--mount", "type=bind,src="+dir+",dst=/fixture", "--entrypoint=/bin/sh", image, "-c", fmt.Sprintf("chown -Rh %d:%d /fixture && chmod -R u+rwX /fixture", os.Geteuid(), os.Getegid()))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("restore fixture permissions: %v %s", err, out)
		}
	})
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
	gateway := docker("network", "inspect", "--format={{(index .IPAM.Config 0).Gateway}}", network)
	listener, err := net.Listen("tcp", net.JoinHostPort(gateway, "0"))
	if err != nil {
		t.Fatal(err)
	}
	var passed atomic.Bool
	var calls atomic.Int32
	command := `test "$NATIVE_TEST_TOKEN" = fixture-token && test -f "$NATIVE_TEST_FILE" && printf %s fixture-file | cmp -s "$NATIVE_TEST_FILE" - && sh -c 'test "$NATIVE_TEST_TOKEN" = fixture-token' && printf '%s%s' OUTER_SHELL_ OK`
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		followup := false
		input, _ := body["input"].([]any)
		for _, raw := range input {
			item, _ := raw.(map[string]any)
			if item["type"] == "function_call_output" {
				followup = true
				output, _ := json.Marshal(item["output"])
				if strings.Contains(string(output), "OUTER_SHELL_OK") {
					passed.Store(true)
				}
			}
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(event string, value any) {
			raw, _ := json.Marshal(value)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
		}
		var item map[string]any
		if !followup {
			args, _ := json.Marshal(map[string]any{"cmd": command, "yield_time_ms": 1000, "max_output_tokens": 200})
			item = map[string]any{"type": "function_call", "id": "outer-call", "call_id": "outer-tool", "name": "exec_command", "arguments": string(args), "status": "completed"}
		} else {
			item = map[string]any{"type": "message", "id": "outer-final", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "FIXTURE_DONE", "annotations": []any{}}}}
		}
		response := map[string]any{"id": "outer-response", "status": "completed", "model": "gpt-5-mini", "output": []any{item}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 10, "total_tokens": 20}}
		send("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
		send("response.completed", map[string]any{"type": "response.completed", "response": response})
	})
	fixture := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	fixture.Start()
	defer fixture.Close()
	d := &managedlaunch.Descriptor{Artifact: *artifact, ImageID: image, RevisionID: "fixture-revision", LockSHA256: strings.Repeat("a", 64), Binary: "codex", Version: version, OuterSandbox: true}
	if err := p.AttestCodexOuterSandbox(ctx, id, crew.ID, *d); err != nil {
		t.Fatal("actual runtime rejected", err)
	}
	req := AgentRunRequest{AgentSlug: "fixture", RunID: "outer-native", CLIAdapter: "CODEX_CLI", ToolProfile: "CODING", LLMModel: "gpt-5-mini", UserMessage: "Execute the fixture shell tool once.", managedLaunch: d, codexOuterSandbox: true, ContainerID: id, Credentials: []Credential{{ID: "token", Type: "CLI_TOKEN", EnvVarName: "NATIVE_TEST_TOKEN", PlainValue: "fixture-token"}, {ID: "file", Type: "GENERIC_SECRET", EnvVarName: "NATIVE_TEST_FILE", PlainValue: "fixture-file"}}}
	secretDir := agentSecretsDir(req.AgentSlug, req.RunID)
	script, _, _, err := buildCredFileScript(req.Credentials, secretDir, true)
	if err != nil {
		t.Fatal(err)
	}
	script += "\nmkdir -p /tmp/outer-fixture-home/codex\n"
	writer, err := p.Exec(ctx, provider.ExecConfig{ContainerID: id, User: "1001:1001", Cmd: []string{"sh"}, Stdin: strings.NewReader(script)})
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, writer.Reader)
	writer.Reader.Close()
	running, code, err := p.ExecInspect(ctx, writer.ExecID)
	if err != nil || running || code != 0 {
		t.Fatal("native writer did not complete successfully")
	}
	env, err := appendCredentialFileEnv(BuildEnvVarsSidecar(req, true), req.Credentials, secretDir, true)
	if err != nil {
		t.Fatal(err)
	}
	env = overrideEnv(env, "HOME", "/tmp/outer-fixture-home")
	env = overrideEnv(env, "SHELL", "/bin/sh")
	env = overrideEnv(env, "CODEX_HOME", "/tmp/outer-fixture-home/codex")
	env = overrideEnv(env, "NO_PROXY", gateway+",localhost,127.0.0.1")
	env = overrideEnv(env, "no_proxy", gateway+",localhost,127.0.0.1")
	argv := (codexAdapter{}).BuildCommand(req)
	argv = append([]string{argv[0], "--config", `model_provider="fixture"`, "--config", `model_providers.fixture.name="Fixture"`, "--config", fmt.Sprintf("model_providers.fixture.base_url=%q", fixture.URL), "--config", `model_providers.fixture.wire_api="responses"`, "--config", `model_providers.fixture.requires_openai_auth=false`}, argv[1:]...)
	cfg, err := managedExecConfig(req, argv, env, "/")
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Exec(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	output, readErr := io.ReadAll(io.LimitReader(result.Reader, 1<<20))
	result.Reader.Close()
	running, code, err = p.ExecInspect(ctx, result.ExecID)
	if readErr != nil || err != nil || running || code != 0 || !passed.Load() {
		t.Fatalf("actual native Codex shell failed: code%d running%t calls%d read%v inspect%v\n%s", code, running, calls.Load(), readErr, err, output)
	}
	t.Logf("PASS real Codex shell: native_version=%s image=%s artifact_sha256=%s", version, image, artifact.SHA256)
	// A non-qualified runtime is refused before another model call.
	badName := label + "-unqualified"
	bad := docker("run", "-d", "--name", badName, "--network=none", "--label=crewship.temp=codex-outer-test", "--label=crewship.test="+label, "--user=1001:1001", "--cap-drop=ALL", image, "120")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", bad).Run() })
	before := calls.Load()
	if err := p.AttestCodexOuterSandbox(ctx, bad, crew.ID, *d); err == nil || calls.Load() != before {
		t.Fatal("unqualified runtime reached a model")
	}
	// Receipt includes the immutable staged launcher generation; it is never
	// copied into the agent's writable home or selected through its PATH.
	launcherBytes, err := os.ReadFile(launcher)
	if err != nil {
		t.Fatal(err)
	}
	launcherSum := sha256.Sum256(launcherBytes)
	staged := filepath.Join(dir, ".runtime", fmt.Sprintf("%s-%x", runtimestage.SidecarFileName, launcherSum))
	if _, err := os.Stat(staged); err != nil {
		t.Fatal(err)
	}
	noNNPName := label + "-no-nnp"
	noNNP := docker("run", "-d", "--name", noNNPName, "--network=none", "--label=crewship.temp=codex-outer-test", "--label=crewship.test="+label, "--user=1001:1001", "--cap-drop=ALL", "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,size=16m,mode=1777", "--mount", "type=bind,src="+staged+",dst="+managedlaunch.LauncherPath+",readonly", image, "120")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", noNNP).Run() })
	rejected := req
	rejected.ContainerID = noNNP
	rejected.RunID = "outer-rejected-kernel"
	rejectedCfg, err := managedExecConfig(rejected, argv, env, "/")
	if err != nil {
		t.Fatal(err)
	}
	denied, err := p.Exec(ctx, rejectedCfg)
	if err != nil {
		t.Fatal(err)
	}
	deniedOutput, readErr := io.ReadAll(io.LimitReader(denied.Reader, 16384))
	denied.Reader.Close()
	running, code, err = p.ExecInspect(ctx, denied.ExecID)
	if readErr != nil || err != nil || running || code != 126 || !strings.Contains(string(deniedOutput), "Codex outer isolation: effective UID") || calls.Load() != before {
		t.Fatalf("kernel controls did not refuse before Codex: code%d running%t calls%d read%v inspect%v %s", code, running, calls.Load(), readErr, err, deniedOutput)
	}

}
