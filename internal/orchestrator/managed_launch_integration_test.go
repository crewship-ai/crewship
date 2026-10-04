//go:build integration && linux

package orchestrator

import (
	"bufio"
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
import("fmt";"os";"strings";"time";"os/exec")
func main(){for _,e:=range os.Environ(){if strings.HasPrefix(e,"LD_")||strings.HasPrefix(e,"NODE_OPTIONS=")||strings.HasPrefix(e,"IMAGE_INJECTION=")||strings.HasPrefix(e,"TMUX="){os.Exit(90)}};if strings.Contains(os.Getenv("PATH"),"/home"){os.Exit(91)};if len(os.Args)>1 && os.Args[1]=="--child" {time.Sleep(120*time.Second);return};if len(os.Args)>1 && os.Args[1]=="--hold" {exe,_:=os.Executable();child:=exec.Command(exe,"--child");if err:=child.Start();err!=nil{panic(err)};fmt.Printf("HOLD_READY %d\n",child.Process.Pid);child.Wait();return};fmt.Println("IMAGE_NATIVE_v2")}
`), 0600); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(dir, "native")
	build(native, cliSource)
	launcher := filepath.Join(dir, "launcher")
	build(launcher, "../../cmd/crewship-sidecar")
	t.Setenv("CREWSHIP_MANAGED_LAUNCH_CREWS", "pilot-crew")
	entrypoint := filepath.Join(dir, "entrypoint.sh")
	if err := os.WriteFile(entrypoint, []byte("#!/bin/sh\nexec /bin/sleep infinity\n"), 0555); err != nil {
		t.Fatal(err)
	}
	network := label + "-net"
	t.Cleanup(func() { exec.Command("docker", "network", "rm", network).Run() })
	p, err := dockerprovider.New(ctx, dockerprovider.Config{SidecarBinaryPath: launcher, EntrypointPath: entrypoint, OutputBasePath: dir, ContainerPrefix: label, Network: network}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	// Provider preparation intentionally assigns owned crew data to UID1001.
	// Return this fixture's host tree to the test user before TempDir cleanup.
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		name := label + "-ownership-cleanup"
		defer exec.Command("docker", "rm", "-f", name).Run()
		command := exec.CommandContext(clean, "docker", "run", "--rm", "--name", name, "--network=none", "--label=crewship.temp=managed-launch-test", "--label=crewship.test="+label, "--user=0:0", "--cap-drop=ALL", "--cap-add=CHOWN", "--cap-add=DAC_OVERRIDE", "--cap-add=FOWNER", "--mount", "type=bind,src="+dir+",dst=/fixture", "--entrypoint=/bin/sh", imageID, "-c", fmt.Sprintf("chown -Rh %d:%d /fixture && chmod -R u+rwX /fixture", os.Geteuid(), os.Getegid()))
		if out, err := command.CombinedOutput(); err != nil {
			t.Errorf("restore owned fixture permissions: %v %s", err, out)
		}
	})

	volume := label + "-home"
	docker("volume", "create", volume)
	t.Cleanup(func() { exec.Command("docker", "volume", "rm", volume).Run() })
	// Exercise production provider creation: real mounts, tmpfs, init and
	// capability policy, with a fixture entrypoint that makes no model calls.
	crew := provider.CrewConfig{ID: "pilot-crew", Slug: "fixture", CachedImage: imageID, MemoryMB: 512, CPUs: 1}
	t.Cleanup(func() { p.RemoveCrewVolumes(context.Background(), crew.ID, crew.Slug) })
	containerID, err := p.EnsureCrewRuntime(ctx, crew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", containerID).Run() })
	docker("exec", containerID, "/bin/sh", "-c", `printf '#!/bin/sh\necho OLD_HOME_CLI_v1\n' > /home/agent/.local/bin/claude; cp /home/agent/.local/bin/claude /home/agent/.local/bin/codex; cp /home/agent/.local/bin/claude /home/agent/.local/bin/node; cp /home/agent/.local/bin/claude /home/agent/.local/bin/tmux; printf '#!/bin/sh\nexit 1\n' > /home/agent/.local/bin/tmux; chmod 755 /home/agent/.local/bin/*`)
	raw, err := os.ReadFile(native)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_MANAGED_LAUNCH_CREWS", "pilot-crew")
	for _, adapter := range []string{"CODEX_CLI"} {
		t.Run(adapter, func(t *testing.T) {
			state := newLockedMemState()
			o := New(p, state, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
			attempt := 0
			execute := func(wantExit int) string {
				attempt++
				req.RunID = fmt.Sprintf("real-launch-%d", attempt)
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
			t.Run("stop-and-recovery-liveness", func(t *testing.T) {
				req.RunID = "real-lifecycle"
				cfg, err := managedExecConfig(req, []string{"codex", "--hold"}, []string{"HOME=/home/agent"}, "/home/agent")
				if err != nil {
					t.Fatal(err)
				}
				persisted, _ := json.Marshal(RunState{ID: req.RunID, AgentSlug: "fixture", ContainerID: containerID, ManagedLaunch: req.managedLaunch})
				if err := state.Set(ctx, "agent_runs", req.RunID, persisted); err != nil {
					t.Fatal(err)
				}
				result, err := p.Exec(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer result.Reader.Close()
				ready := make(chan string, 1)
				go func() {
					scanner := bufio.NewScanner(result.Reader)
					if scanner.Scan() {
						ready <- scanner.Text()
					} else {
						ready <- "no ready output"
					}
				}()
				var childPID string
				select {
				case line := <-ready:
					fields := strings.Fields(line)
					if len(fields) != 2 || fields[0] != "HOLD_READY" {
						t.Fatal(line)
					}
					childPID = fields[1]
				case <-time.After(10 * time.Second):
					t.Fatal("native did not start")
				}
				location := RunLocation{ContainerID: containerID, AgentSlug: "fixture", RunID: req.RunID}
				// Recover from durable evidence with selection disabled and no mode hint.
				t.Setenv("CREWSHIP_MANAGED_LAUNCH_CREWS", "")
				recovered := New(p, state, slog.New(slog.NewTextHandler(io.Discard, nil)))
				t.Logf("kernel/probe diagnostic: %s", docker("exec", containerID, "/bin/sh", "-c", `cat /tmp/crewship-direct-real-lifecycle.pid 2>/dev/null; read pid stamp < /tmp/crewship-direct-real-lifecycle.pid; cat /proc/$pid/stat 2>/dev/null; /bin/kill -0 -- "-$pid"; echo group_with_dashdash=$?; /bin/kill -0 "-$pid"; echo group_without_dashdash=$?; ps -o pid,ppid,pgid,args || true`))
				script, scriptErr := recovered.managedRunProbe(ctx, location, false)
				probeOut, probeErr := recovered.probeExec(ctx, containerID, script)
				t.Logf("managed probe mode=%v output=%q error=%v", scriptErr, probeOut, probeErr)
				if alive, err := recovered.RunIsAliveAt(ctx, location); err != nil || !alive {
					t.Fatalf("managed live process invisible to recovery: alive=%v err=%v", alive, err)
				}
				identityPath := managedlaunch.DirectRunPIDFile(req.RunID)
				identity := docker("exec", containerID, "cat", identityPath)
				docker("exec", containerID, "/bin/sh", "-c", `read pid stamp < /tmp/crewship-direct-real-lifecycle.pid; printf '%s %s\n' "$pid" "$((stamp + 1))" > /tmp/crewship-direct-real-lifecycle.pid`)
				if _, err := recovered.RunIsAliveAt(ctx, location); err == nil {
					t.Fatal("PID/starttime mismatch treated as presence or absence")
				}
				if gone, err := recovered.StopRunAt(ctx, location); err == nil || gone {
					t.Fatal("PID/starttime mismatch signalled or confirmed stopped")
				}
				docker("exec", containerID, "/bin/sh", "-c", "printf '%s\\n' '"+identity+"' > "+identityPath)
				if alive, err := recovered.RunIsAliveAt(ctx, location); err != nil || !alive {
					t.Fatal("mismatched identity stop killed live process group")
				}
				docker("exec", containerID, "rm", identityPath)
				if alive, err := recovered.RunIsAliveAt(ctx, location); err == nil || alive {
					t.Fatal("missing managed PID falsely confirmed absence/presence")
				}
				if gone, err := recovered.StopRunAt(ctx, location); err == nil || gone {
					t.Fatal("missing managed PID falsely confirmed stop")
				}
				// Recover the known kernel identity so this owned fixture can be stopped.
				docker("exec", containerID, "/bin/sh", "-c", "printf '%s\\n' '"+identity+"' > "+identityPath+"; chmod 600 "+identityPath)
				deadline := time.Now().Add(5 * time.Second)
				for {
					gone, err := recovered.StopRunAt(ctx, location)
					if err != nil {
						t.Fatal(err)
					}
					if gone {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("stop did not confirm disappearance")
					}
					time.Sleep(25 * time.Millisecond)
				}
				if alive, err := recovered.RunIsAliveAt(ctx, location); err != nil || alive {
					t.Fatalf("stopped process still alive: %v %v", alive, err)
				}
				code, err := provider.WaitExecExit(ctx, p, result.ExecID, 5*time.Second)
				if err != nil || code == 0 {
					t.Fatalf("group signal did not terminate native: %d %v", code, err)
				}
				if got := docker("exec", containerID, "/bin/sh", "-c", "if /bin/kill -0 "+childPID+" 2>/dev/null; then echo PRESENT; else echo ABSENT; fi"); got != "ABSENT" {
					t.Fatal("child survived process-group stop")
				}
				duplicate, err := p.Exec(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				out, err := io.ReadAll(duplicate.Reader)
				duplicate.Reader.Close()
				if err != nil {
					t.Fatal(err)
				}
				code, err = provider.WaitExecExit(ctx, p, duplicate.ExecID, 5*time.Second)
				if err != nil || code != 126 || !strings.Contains(string(out), "exclusive run identity") || strings.Contains(string(out), "HOLD_READY") {
					t.Fatalf("same RunID re-executed: %d %v %s", code, err, out)
				}
				stamp := docker("exec", containerID, "cat", managedlaunch.DirectRunPIDFile(req.RunID))
				if len(strings.Fields(stamp)) != 2 {
					t.Fatalf("invalid lifecycle identity: %q", stamp)
				}
			})
			t.Run("existing-identity-refuses-before-native", func(t *testing.T) {
				req.RunID = "real-spoofed"
				docker("exec", containerID, "/bin/sh", "-c", "printf keep > /tmp/identity-target; ln -s /tmp/identity-target /tmp/crewship-direct-real-spoofed.pid")
				cfg, err := managedExecConfig(req, []string{"codex", "--version"}, []string{"HOME=/home/agent"}, "/home/agent")
				if err != nil {
					t.Fatal(err)
				}
				result, err := p.Exec(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				out, err := io.ReadAll(result.Reader)
				result.Reader.Close()
				if err != nil {
					t.Fatal(err)
				}
				code, err := provider.WaitExecExit(ctx, p, result.ExecID, 5*time.Second)
				if err != nil || code != 126 || !strings.Contains(string(out), "exclusive run identity") || strings.Contains(string(out), "IMAGE_NATIVE") {
					t.Fatalf("spoof identity admitted: %d %v %s", code, err, out)
				}
				if got := docker("exec", containerID, "cat", "/tmp/identity-target"); got != "keep" {
					t.Fatal("symlink target overwritten")
				}
			})
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
	newContainer := docker("run", "-d", "--network=none", "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,mode=1777,size=16m", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--mount", "type=volume,src="+volume+",dst=/home/agent", "--mount", "type=bind,src="+nextPath+",dst="+managedlaunch.LauncherPath+",readonly", imageID)
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", newContainer).Run() })
	if err := newProvider.AttestManagedLaunch(ctx, newContainer, *d); err != nil {
		t.Fatal(err)
	}
	_, keys, err := managedlaunch.Environment([]string{"HOME=/home/agent"})
	if err != nil {
		t.Fatal(err)
	}
	d.EnvKeys = keys
	d.RunID = "new-generation"
	encoded, _ := json.Marshal(d)
	if out := docker("exec", "--env", "HOME=/home/agent", "--env", "PATH="+managedlaunch.SafePath, newContainer, managedlaunch.LauncherPath, "--managed-launch", base64.RawURLEncoding.EncodeToString(encoded), "--version"); out != "IMAGE_NATIVE_v2" {
		t.Fatalf("new launcher generation: %q", out)
	}
}
