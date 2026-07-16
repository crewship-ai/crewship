// Package preflight decides, before the daemon boots, whether the host can
// actually run agent containers — and when it can't, produces guidance that
// tells the user the one thing to do next, specific to their OS: start the
// runtime they already have, or install one. The distinction matters for
// onboarding: telling someone with Docker Desktop installed to "install
// Docker" reads as a broken product; telling them `open -a Docker` fixes it
// in five seconds.
package preflight

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider/apple"
	"github.com/crewship-ai/crewship/internal/provider/docker"
)

// RuntimeStatus classifies the host's container-runtime situation.
type RuntimeStatus int

const (
	// RuntimeRunning — a live Docker-compatible socket or Apple Containers
	// answered a probe; agents can start.
	RuntimeRunning RuntimeStatus = iota
	// RuntimeInstalledNotRunning — no live runtime answered, but at least one
	// runtime is installed on this host and just needs to be started.
	RuntimeInstalledNotRunning
	// RuntimeMissing — no live runtime and no installed runtime found.
	RuntimeMissing
)

// InstalledRuntime is a container runtime found on the host that did not
// answer a live probe — installed, but (apparently) not running.
type InstalledRuntime struct {
	Name      string // human-readable, e.g. "Docker Desktop"
	StartHint string // the command that starts it on this host
}

// Result is the outcome of a preflight runtime check.
type Result struct {
	Status    RuntimeStatus
	Installed []InstalledRuntime // populated when Status == RuntimeInstalledNotRunning
}

// Check probes for a live container runtime; when none answers it scans the
// host for installed-but-stopped runtimes so callers can tell the user to
// start — not reinstall — what they already have.
func Check(ctx context.Context) Result {
	if _, err := docker.Detect(ctx); err == nil {
		return Result{Status: RuntimeRunning}
	}
	if _, err := apple.Detect(ctx); err == nil {
		return Result{Status: RuntimeRunning}
	}
	if installed := Installed(); len(installed) > 0 {
		return Result{Status: RuntimeInstalledNotRunning, Installed: installed}
	}
	return Result{Status: RuntimeMissing}
}

// Installed scans PATH and well-known install locations for container
// runtimes present on this host, regardless of whether they are running.
func Installed() []InstalledRuntime {
	return detectInstalled(hostProbe{
		goos:     goruntime.GOOS,
		lookPath: exec.LookPath,
		pathExists: func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		},
	})
}

// hostProbe is the OS-injectable view of the host that detectInstalled
// scans — tests substitute fakes so no real host state leaks in.
type hostProbe struct {
	goos       string
	lookPath   func(file string) (string, error)
	pathExists func(path string) bool
}

// installedCandidate describes one way a runtime shows up on a host: either
// an app/install directory (checked with pathExists) or a CLI binary
// (checked with lookPath).
type installedCandidate struct {
	name      string
	path      string // app bundle / install dir; empty when binary-based
	binary    string // CLI binary on PATH; empty when path-based
	startHint string
}

// installedCandidatesFor returns the runtimes worth scanning for on the
// given OS, in the order they should be reported (most common first).
func installedCandidatesFor(goos string) []installedCandidate {
	switch goos {
	case "darwin":
		return []installedCandidate{
			{name: "Docker Desktop", path: "/Applications/Docker.app", startHint: "open -a Docker"},
			{name: "OrbStack", path: "/Applications/OrbStack.app", startHint: "open -a OrbStack"},
			{name: "Rancher Desktop", path: "/Applications/Rancher Desktop.app", startHint: `open -a "Rancher Desktop"`},
			{name: "Colima", binary: "colima", startHint: "colima start"},
			{name: "Podman", binary: "podman", startHint: "podman machine start"},
			// Apple Containers ships a `container` CLI (macOS 26+).
			{name: "Apple Containers", binary: "container", startHint: "container system start"},
			// NOTE: a bare `docker` CLI on macOS is just a client (brew
			// installs it alongside colima) — not evidence of a runtime.
		}
	case "linux":
		return []installedCandidate{
			{name: "Docker Engine", binary: "docker", startHint: "sudo systemctl start docker"},
			{name: "Podman", binary: "podman", startHint: "systemctl --user enable --now podman.socket"},
			{name: "containerd (nerdctl)", binary: "nerdctl", startHint: "sudo systemctl start containerd"},
		}
	case "windows":
		return []installedCandidate{
			// On Windows the docker CLI lands on PATH with Docker Desktop, so
			// either signal maps to the same fix: start Docker Desktop.
			{name: "Docker Desktop", path: `C:\Program Files\Docker\Docker`, startHint: "start Docker Desktop from the Start menu"},
			{name: "Docker Desktop", binary: "docker", startHint: "start Docker Desktop from the Start menu"},
			{name: "Podman", binary: "podman", startHint: "podman machine start"},
		}
	default:
		return nil
	}
}

func detectInstalled(p hostProbe) []InstalledRuntime {
	var out []InstalledRuntime
	seen := map[string]bool{}
	for _, c := range installedCandidatesFor(p.goos) {
		if seen[c.name] {
			continue
		}
		found := false
		switch {
		case c.path != "":
			found = p.pathExists(c.path)
		case c.binary != "":
			_, err := p.lookPath(c.binary)
			found = err == nil
		}
		if found {
			seen[c.name] = true
			out = append(out, InstalledRuntime{Name: c.name, StartHint: c.startHint})
		}
	}
	return out
}

// guidanceFooter closes every non-running guidance message with the two
// escape hatches that always apply.
const guidanceFooter = "To start without containers (dashboard only, no agents):\n" +
	"  crewship start --no-docker\n\n" +
	"Run 'crewship doctor' for full diagnostics."

// Guidance renders the user-facing preflight message for a non-running
// result. Returns "" when the runtime is running (nothing to say).
func Guidance(goos string, r Result) string {
	switch r.Status {
	case RuntimeRunning:
		return ""
	case RuntimeInstalledNotRunning:
		var b strings.Builder
		b.WriteString("Crewship needs a container runtime to run AI agents.\n")
		b.WriteString("A runtime is installed but not running — start it:\n\n")
		for _, rt := range r.Installed {
			fmt.Fprintf(&b, "  %-16s %s\n", rt.Name+":", rt.StartHint)
		}
		b.WriteString("\nThen run 'crewship start' again.\n\n")
		b.WriteString(guidanceFooter)
		return b.String()
	default:
		return missingGuidance(goos)
	}
}

func missingGuidance(goos string) string {
	var b strings.Builder
	b.WriteString("Crewship needs a container runtime to run AI agents — none was found.\n\n")
	switch goos {
	case "darwin":
		b.WriteString("Install one (macOS):\n" +
			"  Docker Desktop:  https://docs.docker.com/desktop/setup/install/mac-install/\n" +
			"  OrbStack:        brew install --cask orbstack\n" +
			"  Colima:          brew install colima docker && colima start\n" +
			"  Podman:          brew install podman && podman machine init && podman machine start\n")
	case "linux":
		b.WriteString("Install one (Linux):\n" +
			"  Docker Engine:  curl -fsSL https://get.docker.com | sh\n" +
			"                  sudo systemctl enable --now docker\n" +
			"                  sudo usermod -aG docker $USER   # then log out and back in\n" +
			"  Podman:         https://podman.io/docs/installation\n")
	case "windows":
		b.WriteString("Install Docker Desktop for Windows (runs on the WSL 2 backend):\n" +
			"  https://docs.docker.com/desktop/setup/install/windows-install/\n")
	default:
		b.WriteString("Install Docker (or a compatible runtime — Podman, Colima, OrbStack, Rancher Desktop):\n" +
			"  https://docs.docker.com/get-docker/\n")
	}
	b.WriteString("\nThen run 'crewship start' again.\n\n")
	b.WriteString(guidanceFooter)
	return b.String()
}
