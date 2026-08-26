//go:build conformance

// This file closes the coverage hole behind the four-month onboarding
// regression: agent chat has zero automated coverage because every CI path
// that could start a real sidecar either sets CREWSHIP_SKIP_SIDECAR=1 or is
// gated behind an unconfigured API-key secret, so it skips silently. An
// onboarding-created crew ran on bare debian:bookworm-slim — no `claude`
// CLI, no uid 1001 — until it was driven by hand and the agent died with
// exit 127, `claude: No such file or directory` (internal/database/
// crew_devcontainer_default.go documents the incident and the fix: a
// READ-time default so every crew-creation path gets a usable devcontainer
// config instead of NULL falling through to bare Debian).
//
// The enabling fact that makes this testable with NO API key and NO LLM:
// internal/sidecar/proxy.go's handleLocal (~line 543) serves /health from
// in-memory Proxy state that is all zero-valued by construction —
// credential count, network mode, hashes — and startSidecar
// (internal/orchestrator/exec_sidecar.go) never requires a credential to
// launch the sidecar. So a real container, a real bind-mounted
// crewship-sidecar binary and a real HTTP /health probe are all reachable
// with nothing behind an unconfigured secret.
//
// # What this test actually boots
//
// Unlike runtime_conformance_test.go (same package), this harness does NOT
// bind-mount a four-byte fake-ELF stub for the sidecar. That shortcut is
// correct for a harness whose subject is the container RUNTIME (HostConfig
// fidelity) — nothing in it execs the stub. Here the sidecar actually
// answering /health, and `claude` actually running, ARE the subject, so a
// fake binary would prove nothing. The sidecar is a real `go build
// ./cmd/crewship-sidecar` linux binary, and the image under test is the
// product's own devcontainer.Provisioner output for
// database.DefaultCrewDevcontainerConfig — the exact config that ships to
// every onboarding-created crew today — not a hand-picked pre-baked image.
//
// # Running it
//
//	go test -tags conformance -run TestOnboardingDefaultBoots -v ./internal/provider/docker/
//
// First run on a cold Docker daemon downloads two devcontainer features
// (common-utils, claude-code) and builds+commits crewship-cache:<hash>,
// which costs real minutes. Every run after that — including every
// following PR on the same long-lived crewship-dev workstation daemon,
// since the default config's hash never changes — hits Provision's own
// image-tag cache and skips straight to "does the container work",
// finishing in well under the ~90s per-PR budget. See the runtime this
// file's author measured, logged as the final line of the test.
package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
)

func TestOnboardingDefaultBoots(t *testing.T) {
	testStart := time.Now()
	// Generous enough to cover a cold feature-download + BuildKit build on a
	// daemon that has never provisioned this config before; a warm-cache run
	// (the steady state on a long-lived workstation daemon) finishes in a
	// small fraction of this.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	p, cleanupProvider := newOnboardingBootProvider(ctx, t)
	defer cleanupProvider()

	// --- provision the REAL default devcontainer config -------------------
	//
	// This is the chokepoint under test: database.DefaultCrewDevcontainerConfig
	// is the literal JSON every onboarding-created crew provisions with today
	// (crew_templates.go, services/onboarding.go, recipes.go,
	// internal_status.go all resolve through EffectiveCrewDevcontainerConfig
	// to this constant when their own devcontainer_config column is NULL).
	// Running it through the product's own devcontainer.Provisioner — the
	// same Provision() call internal/api/crew_provisioning.go wires up for a
	// real Docker daemon — is what makes this an end-to-end proof rather than
	// an assertion about a JSON string.
	dcCfg, err := devcontainer.ParseBytes([]byte(database.DefaultCrewDevcontainerConfig))
	if err != nil {
		t.Fatalf("parse database.DefaultCrewDevcontainerConfig: %v", err)
	}

	featureCacheDir, err := os.MkdirTemp("", "crewship-onboarding-boot-features-")
	if err != nil {
		t.Fatalf("create feature cache dir: %v", err)
	}
	defer os.RemoveAll(featureCacheDir)

	featureDL := devcontainer.NewFeatureDownloader(featureCacheDir, p.logger)
	installer := devcontainer.NewInstaller(p.client, p.logger)
	provisioner := devcontainer.NewProvisioner(p.client, installer, featureDL, p.logger)

	provisionStart := time.Now()
	result, err := provisioner.Provision(ctx, dcCfg.Image, dcCfg, "" /* no mise config in the default */)
	if err != nil {
		t.Fatalf("provision the default onboarding devcontainer config: %v", err)
	}
	t.Logf("provision took %s (tag=%s, config_hash=%s) — a large first number and a tiny one on every run after is exactly the cache behaviour the per-PR budget depends on",
		time.Since(provisionStart), result.CachedImage, result.ConfigHash)
	if result.CachedImage == "" {
		// Provision() returns an empty tag only when the config has no
		// features/postCreate/containerEnv/mise to bake in. The default config
		// has two features, so this would mean the constant changed underneath
		// this test in a way that stops it proving anything.
		t.Fatalf("provisioning the default config produced no cached image — the config has features, so this should never be empty")
	}
	image := result.CachedImage

	// containerEnv merge precedence mirrors internal/api/crew_runtime_config.go
	// (~line 142) and internal/chatbridge/crew_config.go: feature-aggregated
	// containerEnv from the provisioning requirements first, then the
	// devcontainer.json's own containerEnv overrides — because the runtime
	// reads both through provider.CrewConfig.ContainerEnv and this is the
	// path that puts the default's PATH override in front of the agent's
	// non-login exec.
	mergedEnv := map[string]string{}
	for k, v := range result.Requirements.ContainerEnv {
		mergedEnv[k] = v
	}
	for k, v := range dcCfg.ContainerEnv {
		mergedEnv[k] = v
	}

	crew := provider.CrewConfig{
		ID:           "onboarding-boot-crew-id",
		Slug:         "onboarding-boot",
		MemoryMB:     conformanceMemoryMB,
		CPUs:         conformanceCPUs,
		CachedImage:  image,
		ContainerEnv: mergedEnv,
		LoginPath:    result.Requirements.LoginPath,
	}

	dirs, err := p.prepareCrewDirs(crew)
	if err != nil {
		t.Fatalf("prepare crew dirs: %v", err)
	}
	// Same product-owned ownership step runtime_conformance_test.go uses:
	// creates the two named volumes and chowns them plus the host binds to
	// 1001:1001 through the root init container, exactly as EnsureCrewRuntime
	// does before create.
	ensureCrewVolumesOwned(ctx, t, p, image, crew, dirs)

	name := p.CrewContainerName(crew.ID, crew.Slug)
	_, _ = p.client.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})

	cfg, hostCfg, err := p.buildCrewContainerConfig(ctx, crew, name, image, "", conformanceMemoryMB, conformanceCPUs, dirs)
	if err != nil {
		t.Fatalf("build crew container config: %v", err)
	}

	created, err := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           cfg,
		HostConfig:       hostCfg,
		NetworkingConfig: &dockernetwork.NetworkingConfig{},
		Name:             name,
	})
	if err != nil {
		t.Fatalf("create crew container from the provisioned onboarding image: %v", err)
	}
	if os.Getenv("CREWSHIP_CONFORMANCE_KEEP") == "" {
		defer func() {
			rmCtx, rmCancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer rmCancel()
			_, _ = p.client.ContainerRemove(rmCtx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}()
	}

	if _, err := p.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start crew container from the provisioned onboarding image: %v", err)
	}
	waitForPID1(ctx, t, p, created.ID)

	// --- assertion 1: uid 1001 exists in the image's user database --------
	//
	// The base image (mcr.microsoft.com/devcontainers/javascript-node) ships
	// uid 1000 as `node`, NOT uid 1001. The runtime execs every agent command
	// as 1001:1001 unconditionally (assembleCrewSpec, docker_container.go).
	// Without the common-utils feature creating uid 1001 `agent`, every exec
	// into the crew container fails at the Docker API level before a single
	// shell runs — `getent` succeeding here is the precondition every
	// assertion below depends on, and it is exactly the invariant a bare
	// debian:bookworm-slim (the historical onboarding fallback) does not
	// hold: that image has no uid 1001 at all.
	out, code, err := execInContainerAs(ctx, p, created.ID, "", "getent passwd 1001")
	if err != nil {
		t.Fatalf("exec getent passwd 1001: %v", err)
	}
	if code != 0 || strings.TrimSpace(out) == "" {
		t.Fatalf("getent passwd 1001 did not resolve (exit=%d output=%q): common-utils is supposed to create uid 1001 `agent` — the base image only ships uid 1000 `node`", code, out)
	}
	t.Logf("getent passwd 1001 -> %s", strings.TrimSpace(out))

	// --- assertion 2: claude --version succeeds as uid 1001 ----------------
	//
	// This is the literal fact the regression broke. The historical failure,
	// reproduced end to end through the CLI and cited in
	// crew_devcontainer_default.go: exit code 127, "claude: No such file or
	// directory" — a bare debian:bookworm-slim has no Claude Code CLI on
	// PATH at all. Running as an EXPLICIT User: "1001:1001" (not the empty
	// default) means a missing uid 1001 would fail this exec at Docker's own
	// user-resolution step, before the command even runs — a second,
	// independent confirmation of assertion 1.
	out, code, err = execInContainerAs(ctx, p, created.ID, "1001:1001", "claude --version")
	if err != nil {
		t.Fatalf("exec claude --version as uid 1001: %v", err)
	}
	if code != 0 {
		t.Fatalf("`claude --version` exited %d as uid 1001 (output=%q) — this is the exit-code-127 \"claude: No such file or directory\" regression: an onboarding crew provisioned without the claude-code feature (or falling through to bare debian:bookworm-slim) has no agent CLI on PATH", code, out)
	}
	t.Logf("claude --version (as uid 1001) -> %s", strings.TrimSpace(out))

	// --- assertion 3: the sidecar's /health answers 200 --------------------
	//
	// No credentials are supplied — {} on stdin — because
	// internal/sidecar/proxy.go's handleLocal serves /health purely from
	// in-memory Proxy state (credential counts, network mode, hashes), all of
	// which are well-defined zero values on a freshly booted sidecar. This is
	// the exact probe sidecarLaunchScript (internal/orchestrator/
	// exec_sidecar.go, ~line 99) performs after launch, reproduced here
	// because that function is unexported in a different package and a
	// _test.go file under internal/provider/docker/ cannot change its own
	// package identity to reach it. See onboardingSidecarLaunchScript's doc
	// comment for exactly which two lines are reproduced and why the log-cap
	// machinery is deliberately left out (already covered by
	// TestSidecarLaunchScript_* in internal/orchestrator).
	credsB64 := "e30=" // base64("{}") — no credentials, no LLM, nothing behind a secret.
	launchScript := onboardingSidecarLaunchScript(credsB64)
	// User 1002:1002 matches production's startSidecar exactly (exec_sidecar.go
	// ~line 1048): the sidecar runs at a UID the agent (1001) cannot read
	// /proc/<pid>/mem for, so credentials in its heap survive a curious agent.
	out, code, err = execInContainerAs(ctx, p, created.ID, "1002:1002", launchScript)
	if err != nil {
		t.Fatalf("exec sidecar launch script: %v", err)
	}
	if code != 0 {
		t.Fatalf("sidecar did not become healthy after launch (exit=%d, output=%q) — this is precisely what the old wget/curl-only health probe silently missed on debian:bookworm-slim (#... see exec_sidecar.go), reported here as a hard failure instead of a false-negative CI green", code, out)
	}

	// Independent of the launch script's own --health-check exit code: fetch
	// /health over real HTTP from inside the container and check the actual
	// status line, so this test does not merely trust the same binary's
	// self-report twice.
	out, code, err = execInContainerAs(ctx, p, created.ID, "", `curl -sf -o /dev/null -w '%{http_code}' http://127.0.0.1:9119/health`)
	if err != nil {
		t.Fatalf("exec curl against sidecar /health: %v", err)
	}
	if code != 0 || strings.TrimSpace(out) != "200" {
		t.Fatalf("sidecar /health did not answer 200 (exit=%d, http_status=%q) — agent chat has zero automated coverage of this path today because every CI leg that could reach it sets CREWSHIP_SKIP_SIDECAR=1 or needs an unset API-key secret", code, out)
	}
	t.Logf("GET /health -> 200")

	t.Logf("TestOnboardingDefaultBoots total wall time: %s", time.Since(testStart))
}

// newOnboardingBootProvider builds a real Provider with a REAL
// crewship-sidecar binary bind-mounted in — deliberately not the four-byte
// fake-ELF stub newConformanceProvider (runtime_conformance_test.go) uses.
// That stub is correct there because nothing in that harness execs the
// sidecar; it is wrong here because the sidecar actually answering /health
// is the entire point of this file.
//
// Structure otherwise mirrors newConformanceProvider on purpose — same
// os.MkdirTemp-not-t.TempDir reasoning (reclaimBindOwnership needs the
// temp root to still exist when it runs, and t.Cleanup is LIFO), same
// bail-on-construction-failure semantics (this file is behind a build tag,
// so a caller here asked for a real run; skipping because no runtime was
// reachable would be exactly the silent non-coverage this file exists to
// end).
func newOnboardingBootProvider(ctx context.Context, t *testing.T) (*Provider, func()) {
	t.Helper()

	base, err := os.MkdirTemp("", "crewship-onboarding-boot-")
	if err != nil {
		t.Fatalf("create onboarding-boot temp dir: %v", err)
	}
	bail := func(format string, args ...any) {
		t.Helper()
		_ = os.RemoveAll(base)
		t.Fatalf(format, args...)
	}

	sidecarBin := buildRealSidecarBinary(t)

	entrypoint := filepath.Join(base, "entrypoint.sh")
	src, err := os.ReadFile(repoFile(t, "scripts", "entrypoint.sh"))
	if err != nil {
		bail("read entrypoint: %v", err)
	}
	if err := os.WriteFile(entrypoint, src, 0o755); err != nil {
		bail("stage entrypoint: %v", err)
	}

	cfg := Config{
		OutputBasePath:    filepath.Join(base, "output"),
		ContainerPrefix:   "crewship-obc", // "onboarding boot conformance" — distinct from the real crewship-1-* prefix on this box
		SidecarBinaryPath: sidecarBin,
		EntrypointPath:    entrypoint,
	}
	p, err := New(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		_ = os.RemoveAll(base)
		t.Fatalf("no container runtime reachable: %v — start one, or point DOCKER_HOST at it", err)
	}

	t.Cleanup(func() {
		if os.Getenv("CREWSHIP_CONFORMANCE_KEEP") != "" {
			t.Logf("CREWSHIP_CONFORMANCE_KEEP set: leaving %s in place", base)
			return
		}
		// debian:bookworm-slim rather than the provisioned onboarding image:
		// reclaimBindOwnership only needs an already-local image with a POSIX
		// shell and chown(1), and debian:bookworm-slim is already resident on
		// every host this package's other conformance tests run against —
		// reusing it here costs no extra pull.
		reclaimBindOwnership(t, p.detected.Host, "debian:bookworm-slim", base)
		_ = p.client.Close()
		if err := os.RemoveAll(base); err != nil {
			t.Logf("could not remove %s (leaking it rather than failing the run): %v", base, err)
		}
	})

	return p, func() { _ = p.client.Close() }
}

// buildRealSidecarBinary builds a real linux crewship-sidecar binary into its
// own throwaway t.TempDir(). Unlike the crew bind dirs under `base` in
// newOnboardingBootProvider, this file is never a target the product chowns
// (it is bind-mounted read-only at /usr/local/bin/crewship-sidecar and
// nothing execs a chown against it), so a plain t.TempDir() is safe here —
// none of the reclaimBindOwnership ordering concerns that force os.MkdirTemp
// for the crew bind root apply to it.
//
// CGO_ENABLED=0 / GOOS=linux / GOARCH=<host> mirrors the Makefile's
// build:sidecar target and dev.sh's equivalent bash: always a static Linux
// binary regardless of host OS, because it is bind-mounted into a Linux
// container and exec'd there. This test runs on the crewship-dev workstation
// (Linux/amd64), so GOARCH is read off the host rather than hardcoded, and
// no cross-compilation toolchain is required for that case.
func buildRealSidecarBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "crewship-sidecar")

	root := repoFile(t)
	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", bin, "./cmd/crewship-sidecar")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH="+goruntime.GOARCH,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./cmd/crewship-sidecar: %v\n%s", err, out)
	}
	return bin
}

// onboardingSidecarLaunchScript reproduces the two load-bearing lines of
// orchestrator.sidecarLaunchScript (internal/orchestrator/exec_sidecar.go,
// ~line 99) that this test needs: credentials arrive on stdin as base64
// (never argv, so they never show up in `ps` / /proc/<pid>/cmdline), the
// sidecar is backgrounded so the exec's stdio can close without SIGPIPE-ing
// it, and readiness is decided by the sidecar BINARY's own --health-check
// flag against the real port — not by this script parsing anything. It
// cannot call sidecarLaunchScript directly: that function is unexported in
// package orchestrator, and a _test.go file living in
// internal/provider/docker/ cannot declare itself part of that package. The
// log-size-cap background loop (sidecarLogTrimOnce / the `while sleep …`
// loop in the real function) is deliberately omitted — it is about bounding
// /tmp under long crew lifetimes, has nothing to do with whether the sidecar
// answers /health, and is already exercised directly by
// TestSidecarLaunchScript_* in internal/orchestrator against the real
// shipped shell.
func onboardingSidecarLaunchScript(credsB64 string) string {
	return fmt.Sprintf(
		`echo '%[1]s' | base64 -d | crewship-sidecar --addr 127.0.0.1:9119 >/dev/null 2>/tmp/sidecar.log &`+"\n"+
			`sleep 0.5`+"\n"+
			`crewship-sidecar --health-check --addr 127.0.0.1:9119`,
		credsB64,
	)
}

// execInContainerAs runs a shell snippet as an explicit user (unlike
// execInContainer in runtime_conformance_test.go, which always uses an empty
// User to exercise ContainerUser resolution) and returns its output alongside
// the real exit code from ExecInspect — the same running/exitCode contract
// production's own startSidecar and writeCredentialFiles check
// (internal/orchestrator/exec_sidecar.go), rather than trusting "the exec
// call didn't error" the way a naive caller might.
func execInContainerAs(ctx context.Context, p *Provider, id, user, script string) (string, int, error) {
	res, err := p.Exec(ctx, provider.ExecConfig{
		ContainerID: id,
		Cmd:         []string{"sh", "-c", script},
		User:        user,
	})
	if err != nil {
		return "", -1, err
	}
	defer res.Reader.Close()
	out, err := io.ReadAll(res.Reader)
	if err != nil {
		return "", -1, fmt.Errorf("read exec stream: %w", err)
	}
	// Reading to EOF means Docker has closed the exec pipe, i.e. the process
	// has exited, so ExecInspect reports the final code without racing it —
	// the same ordering writeCredentialFiles relies on (exec_sidecar.go).
	running, exitCode, err := p.ExecInspect(ctx, res.ExecID)
	if err != nil {
		return string(out), -1, fmt.Errorf("inspect exec: %w", err)
	}
	if running {
		return string(out), -1, fmt.Errorf("exec %s reported still running after EOF", res.ExecID)
	}
	return string(out), exitCode, nil
}
