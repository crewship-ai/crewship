//go:build integration && (linux || darwin)

package devcontainer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Explicit packaging qualification using the same mise planning, installer and
// build gate as provisioning. Supply a Linux Node 22 base image with curl and sh. The test creates
// an owned container, agent UID/GID 1001 and the current mise installer. Only public downloads and --version are used.
func TestCurrentCLIImageRecipes(t *testing.T) {
	image := os.Getenv("CREWSHIP_CLI_QUALIFICATION_IMAGE")
	if image == "" {
		t.Fatal("set CREWSHIP_CLI_QUALIFICATION_IMAGE to the Linux Node 22 base image")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	inspected, err := docker.ImageInspect(ctx, image)
	if err != nil {
		t.Fatal(err)
	}
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:       tempContainerName(),
		Config:     &container.Config{Image: inspected.ID, User: "0:0", Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"720"}, Env: []string{"HOME=/home/agent", "PATH=/usr/local/bin:/usr/bin:/bin"}},
		HostConfig: &container.HostConfig{CapDrop: []string{"ALL"}, CapAdd: []string{"CHOWN", "DAC_OVERRIDE", "FOWNER"}, SecurityOpt: []string{"no-new-privileges"}, Resources: container.Resources{Memory: 2 << 30, NanoCPUs: 2e9}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := docker.ContainerRemove(clean, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			t.Error(err)
		}
	})
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	installer := NewInstaller(docker, testLogger())
	run := installer.execInContainerAsUser
	out, code, err := run(ctx, created.ID, []string{"sh", "-c", "groupadd -g 1001 agent && useradd -u 1001 -g 1001 -m agent"}, "0:0", nil)
	if err != nil || code != 0 {
		t.Fatalf("agent setup: %v %d %s", err, code, out)
	}
	if err := InstallMise(ctx, created.ID, run); err != nil {
		t.Fatal(err)
	}
	adapters := []string{"CLAUDE_CODE", "CODEX_CLI", "GEMINI_CLI", "OPENCODE"}
	plan, err := EnsureAdapterCLIs(&Config{}, "", adapters)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseMiseConfig(plan.MiseConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallMiseTools(ctx, created.ID, cfg, run); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBinariesResolve(ctx, created.ID, plan.Binaries, "", run); err != nil {
		t.Fatal(err)
	}
	env := append([]string{}, miseAgentEnv...)
	env = append(env, "HOME=/home/agent", "PATH="+strings.Join(AgentToolPathDirs, ":")+":/usr/local/bin:/usr/bin:/bin")
	for _, adapter := range adapters {
		cli, _ := AdapterCLIFor(adapter)
		out, code, err := run(ctx, created.ID, []string{cli.Binary, "--version"}, "1001:1001", env)
		if err != nil || code != 0 {
			t.Fatalf("%s version: %v %d %s", cli.Binary, err, code, out)
		}
		if version := ObservedToolVersion(cli.Binary, out); version != cli.Version {
			t.Fatalf("%s actual %q, selected %s", cli.Binary, out, cli.Version)
		}
		t.Logf("image recipe %s: %s", cli.Binary, strings.TrimSpace(out))
	}
}

// Qualify mise's actual registry recipes when the host's Docker gateway does
// not permit the image inspection needed by TestCurrentCLIImageRecipes.
// All configuration, installs, caches and homes live in one temporary root.
func TestCurrentMiseCLIRecipes(t *testing.T) {
	binary := os.Getenv("CREWSHIP_CLI_QUALIFICATION_MISE")
	if binary == "" {
		t.Fatal("set CREWSHIP_CLI_QUALIFICATION_MISE to the isolated current mise binary")
	}
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	adapters := []string{"CLAUDE_CODE", "CODEX_CLI", "GEMINI_CLI", "OPENCODE"}
	plan, err := EnsureAdapterCLIs(&Config{}, "", adapters)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseMiseConfig(plan.MiseConfig)
	if err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configFile, []byte(cfg.ToTOML()), 0600); err != nil {
		t.Fatal(err)
	}
	npmConfig := filepath.Join(root, "empty-npmrc")
	if err := os.WriteFile(npmConfig, nil, 0600); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + filepath.Join(root, "home"), "PATH=" + filepath.Dir(binary) + ":/usr/local/bin:/usr/bin:/bin", "CI=1", "XDG_CONFIG_HOME=" + configDir, "XDG_DATA_HOME=" + filepath.Join(root, "xdg-data"), "XDG_CACHE_HOME=" + filepath.Join(root, "xdg-cache"), "MISE_CONFIG_DIR=" + configDir, "MISE_GLOBAL_CONFIG_FILE=" + configFile, "MISE_DATA_DIR=" + filepath.Join(root, "data"), "MISE_CACHE_DIR=" + filepath.Join(root, "cache"), "MISE_STATE_DIR=" + filepath.Join(root, "state"), "NPM_CONFIG_USERCONFIG=" + npmConfig, "NPM_CONFIG_GLOBALCONFIG=" + npmConfig}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	run := func(args ...string) string {
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir, cmd.Env = root, env
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if err == syscall.ESRCH {
				return os.ErrProcessDone
			}
			return err
		}
		cmd.WaitDelay = 2 * time.Second
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("mise %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	t.Log(strings.TrimSpace(run("--version")))
	run("install", "--yes")
	run("reshim")
	for _, adapter := range adapters {
		cli, _ := AdapterCLIFor(adapter)
		out := run("exec", "--", cli.Binary, "--version")
		if got := ObservedToolVersion(cli.Binary, out); got != cli.Version {
			t.Fatalf("mise %s actual %q, selected %s", cli.Binary, out, cli.Version)
		}
		t.Logf("mise recipe %s: %s", cli.Binary, strings.TrimSpace(out))
	}
}
