//go:build integration

package devcontainer

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// Required native packaging qualification from a pinned, owned mise image.
// Real CLI lock resolution/application, clean install and upgrade use only
// public tool downloads and --version, without credentials or model requests.
func TestCodexNativePackaging(t *testing.T) {
	image := os.Getenv("CREWSHIP_CODEX_QUALIFICATION_IMAGE")
	if image == "" {
		t.Fatal("set CREWSHIP_CODEX_QUALIFICATION_IMAGE for this explicit gate")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	binaries := t.TempDir()
	cliBinary := filepath.Join(binaries, "crewship")
	launcher := filepath.Join(binaries, "launcher")
	for _, buildSpec := range []struct{ target, source string }{{cliBinary, "../../cmd/crewship"}, {launcher, "../../cmd/crewship-sidecar"}} {
		build := exec.CommandContext(ctx, "go", "build", "-o", buildSpec.target, buildSpec.source)
		if buildSpec.target == launcher {
			build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
		}
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build qualification binary: %v %s", err, out)
		}
	}
	for _, scenario := range []struct{ name, previous string }{{"clean", ""}, {"upgrade", "0.160.0"}} {
		t.Run(scenario.name, func(t *testing.T) { testCodexNativePackaging(t, ctx, image, cliBinary, launcher, scenario.previous) })
	}
}

func testCodexNativePackaging(t *testing.T, ctx context.Context, image, cliBinary, launcher, previous string) {
	const version = "0.169.0"
	var exportFiles map[string][]byte
	// Export only after the later Docker cleanup callbacks have succeeded.
	t.Cleanup(func() {
		if t.Failed() || exportFiles == nil {
			return
		}
		root := os.Getenv("CREWSHIP_TEST_MISE_RECEIPTS_DIR")
		if root == "" {
			return
		}
		if !filepath.IsAbs(root) {
			t.Error("qualification output must be absolute")
			return
		}
		name := "clean"
		if previous != "" {
			name = "upgrade"
		}
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Error(err)
			return
		}
		for name, data := range exportFiles {
			file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				t.Error(err)
				continue
			}
			if _, err := file.Write(data); err != nil {
				t.Error(err)
			}
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	inspected, err := docker.ImageInspect(ctx, image)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Os != "linux" || inspected.Architecture != "amd64" {
		t.Fatal("native packaging qualification requires Linux amd64")
	}
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:       tempContainerName(),
		Config:     &container.Config{Image: inspected.ID, User: "0:0", Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"480"}, Env: []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/home/agent"}},
		HostConfig: &container.HostConfig{CapDrop: []string{"ALL"}, CapAdd: []string{"CHOWN", "DAC_OVERRIDE", "FOWNER"}, SecurityOpt: []string{"no-new-privileges"}, Resources: container.Resources{Memory: 512 << 20, NanoCPUs: 1e9}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var committed, runtimeID string
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := docker.ContainerRemove(clean, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			t.Error(err)
		}
		if runtimeID != "" {
			if _, err := docker.ContainerRemove(clean, runtimeID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
				t.Error(err)
			}
		}
		if committed != "" {
			if _, err := docker.ImageRemove(clean, committed, client.ImageRemoveOptions{}); err != nil {
				t.Error(err)
			}
		}
	})
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	installer := NewInstaller(docker, testLogger())
	run := installer.execInContainerAsUser
	setup := `mkdir -p /home/agent; chown 1001:1001 /home/agent`
	if out, code, err := run(ctx, created.ID, []string{"sh", "-c", setup}, "0:0", nil); err != nil || code != 0 {
		t.Fatalf("setup: %v %d %s", err, code, out)
	}
	work := t.TempDir()
	cliHome := filepath.Join(work, "home")
	if err := os.Mkdir(cliHome, 0700); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(work, "cli.yaml")
	if err := os.WriteFile(profile, []byte("server: http://127.0.0.1:1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cli := func(args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(ctx, cliBinary, args...)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "CREWSHIP_") && !strings.HasPrefix(entry, "HOME=") && !strings.HasPrefix(entry, "XDG_CONFIG_HOME=") {
				command.Env = append(command.Env, entry)
			}
		}
		command.Env = append(command.Env, "HOME="+cliHome, "XDG_CONFIG_HOME="+cliHome, "CREWSHIP_CONFIG="+profile, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		command.Dir = work
		out, err := command.Output()
		if err != nil {
			if failed, ok := err.(*exec.ExitError); ok {
				t.Fatalf("native lock CLI %q: %v %s", args, err, failed.Stderr)
			}
			t.Fatal(err)
		}
		return out
	}
	resolve := func(pin string, previousLock *MiseLockBundle) (*MiseConfig, *MiseResolution, []byte, []byte, []byte) {
		t.Helper()
		input, err := json.Marshal(&MiseConfig{Tools: map[string]string{"codex": pin}, Lock: previousLock})
		if err != nil {
			t.Fatal(err)
		}
		configFile := filepath.Join(work, "input-"+pin+".json")
		proposalFile := filepath.Join(work, "proposal-"+pin+".json")
		if err := os.WriteFile(configFile, input, 0600); err != nil {
			t.Fatal(err)
		}
		args := []string{"crew", "provision", "lock-resolve", configFile, "--image", inspected.ID}
		if previousLock != nil {
			args = append(args, "--bump")
		}
		proposal := cli(args...)
		var plan MiseResolution
		if err := json.Unmarshal(proposal, &plan); err != nil {
			t.Fatal("native resolver returned invalid JSON", err)
		}
		if plan.ImageID != inspected.ID || plan.Platform != "linux-x64" || plan.MiseVersion == "" || plan.LockSHA256 == "" {
			t.Fatal("native resolver omitted exact builder evidence")
		}
		if err := os.WriteFile(proposalFile, proposal, 0600); err != nil {
			t.Fatal(err)
		}
		applied := cli("crew", "provision", "lock-apply", configFile, proposalFile)
		cfg, err := ParseMiseConfig(string(applied))
		if err != nil {
			t.Fatal(err)
		}
		if got, err := LockedToolVersion(cfg.Lock, "codex"); err != nil || got != pin {
			t.Fatalf("native CLI lock selected %s: %v", got, err)
		}
		return cfg, &plan, input, proposal, applied
	}
	var oldLock *MiseLockBundle
	if previous != "" {
		old, _, _, _, _ := resolve(previous, nil)
		if err := InstallMiseTools(ctx, created.ID, old, run); err != nil {
			t.Fatal(err)
		}
		if out, code, err := run(ctx, created.ID, []string{"/opt/mise/data/shims/codex", "--version"}, "1001:1001", append(append([]string(nil), miseAgentEnv...), "HOME=/home/agent")); err != nil || code != 0 || !strings.Contains(out, "codex-cli "+previous) {
			t.Fatalf("prior native installation: %v %d %s", err, code, out)
		}
		oldLock = old.Lock
	}
	cfg, plan, input, proposal, applied := resolve(version, oldLock)
	if previous != "" && plan.PreviousLockSHA256 != miseLockDigest(oldLock) {
		t.Fatal("upgrade proposal lost its previous lock binding")
	}
	if err := InstallMiseTools(ctx, created.ID, cfg, run); err != nil {
		t.Fatal(err)
	}
	if err := writeToolchainInventory(ctx, created.ID, []string{"codex"}, nil, run); err != nil {
		t.Fatal(err)
	}
	result, err := docker.ContainerCommit(ctx, created.ID, client.ContainerCommitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	committed = result.ID
	p := NewProvisioner(docker, installer, nil, testLogger())
	got := p.inspectToolchain(ctx, committed, []string{"codex"})
	if got.ImageID != committed || len(got.Tools) != 1 {
		t.Fatalf("inventory: %+v", got)
	}
	tool := got.Tools[0]
	if tool.Status != "observed" || tool.Version != version || tool.LaunchArtifact == nil || tool.Path != "/opt/mise/data/shims/codex" || tool.ManagedVersion != version || !strings.HasPrefix(tool.ManagedPath, "/opt/mise/data/installs/") {
		t.Fatalf("native artifact: %+v", tool)
	}
	runtime, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: tempContainerName(), Config: &container.Config{Image: committed, User: "1001:1001", Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"120"}, Env: []string{"HOME=/home/agent", "PATH=/usr/local/bin:/usr/bin:/bin", "LD_PRELOAD=/home/agent/evil.so", "NODE_OPTIONS=evil"}},
		HostConfig: &container.HostConfig{ReadonlyRootfs: true, Tmpfs: map[string]string{"/tmp": "rw,noexec,nosuid,mode=1777,size=16m"}, NetworkMode: "none", CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Mounts: []mount.Mount{{Type: mount.TypeBind, Source: launcher, Target: managedlaunch.LauncherPath, ReadOnly: true}}, Resources: container.Resources{Memory: 1 << 30, NanoCPUs: 1e9}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeID = runtime.ID
	if _, err := docker.ContainerStart(ctx, runtimeID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	descriptor := managedlaunch.Descriptor{Artifact: *tool.LaunchArtifact, ImageID: committed, RevisionID: "packaging-fixture", LockSHA256: strings.TrimPrefix(miseLockDigest(cfg.Lock), "sha256:"), Binary: "codex", Version: version, RunID: "packaging-fixture", EnvKeys: []string{"HOME"}}
	encoded, _ := json.Marshal(descriptor)
	if out, code, err := run(ctx, runtimeID, []string{managedlaunch.LauncherPath, "--managed-launch", base64.RawURLEncoding.EncodeToString(encoded), "--version"}, "1001:1001", []string{"HOME=/home/agent"}); err != nil || code != 0 || !strings.Contains(out, "codex-cli "+version) {
		t.Fatalf("native managed launcher: %v %d %s", err, code, out)
	}
	source, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	clientBytes, err := os.ReadFile(cliBinary)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := json.MarshalIndent(map[string]any{"schema_version": 1, "scope": "native_mise_packaging", "source_sha": strings.TrimSpace(string(source)), "image_id": committed, "resolver_image_id": inspected.ID, "mise_version": plan.MiseVersion, "version": version, "previous_version": previous, "lock_sha256": plan.LockSHA256, "native_cli_sha256": fmt.Sprintf("%x", sha256.Sum256(clientBytes)), "tool": tool, "authentication": "not_tested"}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	exportFiles = map[string][]byte{"input.json": input, "resolution.json": proposal, "locked.json": applied, "receipt.json": receipt}
	t.Logf("native CLI packaging qualified: version=%s previous=%s image=%s lock=%s mise=%s artifact=%+v", version, previous, committed, plan.LockSHA256, plan.MiseVersion, tool.LaunchArtifact)
}
