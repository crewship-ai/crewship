//go:build integration && linux

package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"github.com/crewship-ai/crewship/internal/provider"
	dockerprovider "github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
)

// Real static launcher + production admission/command builder + Docker exec.
// No provider credentials, upstream CLI compatibility claim, or registry pull.
func TestManagedLaunchRealDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("Docker %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	base := docker("image", "inspect", "--format={{.Id}}", "alpine:3")
	label := fmt.Sprintf("crewship-launch-%d-%d", os.Getpid(), time.Now().UnixNano())
	baseTag := label + "-base"
	docker("tag", base, baseTag)
	t.Cleanup(func() { exec.Command("docker", "image", "rm", baseTag).Run() })
	dir := t.TempDir()
	build := func(target, source string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "go", "build", "-o", target, source)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v %s", err, out)
		}
	}
	cliSource := filepath.Join(dir, "cli.go")
	if err := os.WriteFile(cliSource, []byte(`package main
import("fmt";"os";"strings")
func main(){for _,e:=range os.Environ(){if strings.HasPrefix(e,"LD_")||strings.HasPrefix(e,"NODE_OPTIONS=")||strings.HasPrefix(e,"IMAGE_INJECTION=")||strings.HasPrefix(e,"TMUX="){os.Exit(90)}};if strings.Contains(os.Getenv("PATH"),"/home"){os.Exit(91)};fmt.Println("IMAGE_NATIVE_v2")}
`), 0600); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(dir, "native")
	build(native, cliSource)
	launcher := filepath.Join(dir, "launcher")
	build(launcher, "../../cmd/crewship-sidecar")
	t.Setenv("CREWSHIP_MANAGED_LAUNCH_CREWS", "pilot-crew")
	p, err := dockerprovider.New(ctx, dockerprovider.Config{SidecarBinaryPath: launcher, OutputBasePath: dir, ContainerPrefix: "managed-launch-fixture"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	launcherBytes, err := os.ReadFile(launcher)
	if err != nil {
		t.Fatal(err)
	}
	launcherHash := sha256.Sum256(launcherBytes)
	stagedLauncher := filepath.Join(dir, ".runtime", fmt.Sprintf("%s-%x", runtimestage.SidecarFileName, launcherHash))
	if _, err := os.Stat(stagedLauncher); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"Dockerfile": "FROM " + baseTag + "\nUSER 0\nCOPY --chmod=0555 native /opt/native/claude\nCOPY --chown=1001:1001 --chmod=0755 native /opt/native/codex\nRUN mkdir -p /home/agent/.local/bin && chown -R 1001:1001 /home/agent\nENV PATH=/home/agent/.local/bin:/usr/local/bin:/usr/bin:/bin NODE_OPTIONS=evil LD_PRELOAD=/home/agent/evil.so IMAGE_INJECTION=evil TMUX=untrusted-socket\nUSER 1001:1001\nENTRYPOINT [\"/bin/sleep\"]\nCMD [\"120\"]\n",
	}
	for name, raw := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	docker("build", "--network=none", "--pull=false", "-t", label, dir)
	t.Cleanup(func() { exec.Command("docker", "image", "rm", label).Run() })
	imageID := docker("image", "inspect", "--format={{.Id}}", label)
	volume := label + "-home"
	docker("volume", "create", volume)
	t.Cleanup(func() { exec.Command("docker", "volume", "rm", volume).Run() })
	containerID := docker("run", "-d", "--name", label, "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--memory=512m", "--cpus=1", "--pids-limit=64", "--mount", "type=volume,src="+volume+",dst=/home/agent", "--mount", "type=bind,src="+stagedLauncher+",dst="+managedlaunch.LauncherPath+",readonly", imageID)
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", containerID).Run() })
	docker("exec", containerID, "/bin/sh", "-c", `printf '#!/bin/sh\necho OLD_HOME_CLI_v1\n' > /home/agent/.local/bin/claude; cp /home/agent/.local/bin/claude /home/agent/.local/bin/codex; cp /home/agent/.local/bin/claude /home/agent/.local/bin/node; cp /home/agent/.local/bin/claude /home/agent/.local/bin/tmux; chmod 755 /home/agent/.local/bin/*`)
	raw, err := os.ReadFile(native)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_MANAGED_LAUNCH_CREWS", "pilot-crew")
	for _, adapter := range []string{"CODEX_CLI"} {
		t.Run(adapter, func(t *testing.T) {
			o := New(p, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			d := launchDescriptor()
			d.ImageID = imageID
			if adapter == "CODEX_CLI" {
				d.Binary = "codex"
				d.Version = "0.153.2"
			}
			d.Path = "/opt/native/" + d.Binary
			artifact, err := managedlaunch.Capture(d.Path, raw)
			if err != nil {
				t.Fatal(err)
			}
			d.Artifact = *artifact
			o.SetManagedLaunchResolver(func(context.Context, string, string, string) (*managedlaunch.Descriptor, error) { return d, nil })
			use := provider.NewRuntimeUse("pilot-crew", containerID, func() {})
			defer use.Release()
			req := AgentRunRequest{CrewID: "pilot-crew", WorkspaceID: "fixture", ContainerID: containerID, CLIAdapter: adapter, RunID: "real-launch", UserMessage: "offline", ToolProfile: "MINIMAL"}
			if err := o.admitManagedLaunch(ctx, &req, use); err != nil {
				t.Fatal(err)
			}
			execute := func(wantExit int) string {
				t.Helper()
				cfg, err := o.buildExecCommand(ctx, req, BuildCLICommand(req), []string{"HOME=/home/agent", "PATH=/home/agent/.local/bin", "NODE_OPTIONS=evil", "LD_AUDIT=evil"}, "/home/agent")
				if err != nil {
					t.Fatal(err)
				}
				result, err := p.Exec(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				output, err := io.ReadAll(result.Reader)
				result.Reader.Close()
				if err != nil {
					t.Fatal(err)
				}
				exit, err := provider.WaitExecExit(ctx, p, result.ExecID, 10*time.Second)
				if err != nil || exit != wantExit {
					t.Fatalf("exit=%d err=%v output=%s", exit, err, output)
				}
				return strings.TrimSpace(string(output))
			}
			if got := execute(0); got != "IMAGE_NATIVE_v2" {
				t.Fatalf("home/interpreter/tmux shadowing=%q", got)
			}
			req.managedLaunch.SHA256 = strings.Repeat("0", 64)
			if got := execute(126); !strings.Contains(got, "hash mismatch") {
				t.Fatalf("hash failure=%q", got)
			}
			payload := execCommandPayload(req, journalCmdView{}, "start", nil)
			encoded, _ := json.Marshal(payload)
			if !strings.Contains(string(encoded), d.Version) {
				t.Fatal("lock version absent from provenance")
			}
		})
	}
	// Upgrade the installation source atomically, then construct the new host
	// provider. The bound old generation remains untouched and is refused by
	// the new provider before FIRST exec, even though it is still static ELF.
	next := append(append([]byte(nil), launcherBytes...), []byte("new-build-generation")...)
	replacement := launcher + ".next"
	if err := os.WriteFile(replacement, next, 0555); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, launcher); err != nil {
		t.Fatal(err)
	}
	newProvider, err := dockerprovider.New(ctx, dockerprovider.Config{SidecarBinaryPath: launcher, OutputBasePath: dir, ContainerPrefix: "managed-launch-fixture"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	d := launchDescriptor()
	d.ImageID = imageID
	artifact, err := managedlaunch.Capture(d.Path, raw)
	if err == nil {
		d.Artifact = *artifact
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := newProvider.AttestManagedLaunch(ctx, containerID, *d); err == nil {
		t.Fatal("managed launch accepted a stale bind-mounted launcher inode after atomic staging")
	}
	preserved, err := os.ReadFile(stagedLauncher)
	if err != nil || string(preserved) != string(launcherBytes) {
		t.Fatal("upgrade overwrote referenced old launcher")
	}
	nextHash := sha256.Sum256(next)
	nextPath := filepath.Join(dir, ".runtime", fmt.Sprintf("%s-%x", runtimestage.SidecarFileName, nextHash))
	newContainer := docker("run", "-d", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--mount", "type=volume,src="+volume+",dst=/home/agent", "--mount", "type=bind,src="+nextPath+",dst="+managedlaunch.LauncherPath+",readonly", imageID)
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", newContainer).Run() })
	if err := newProvider.AttestManagedLaunch(ctx, newContainer, *d); err != nil {
		t.Fatal(err)
	}
	_, keys, err := managedlaunch.Environment([]string{"HOME=/home/agent"})
	if err != nil {
		t.Fatal(err)
	}
	d.EnvKeys = keys
	encoded, _ := json.Marshal(d)
	if out := docker("exec", "--env", "HOME=/home/agent", "--env", "PATH="+managedlaunch.SafePath, newContainer, managedlaunch.LauncherPath, "--managed-launch", base64.RawURLEncoding.EncodeToString(encoded), "--version"); out != "IMAGE_NATIVE_v2" {
		t.Fatalf("new launcher generation: %q", out)
	}
}
