package docker

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// The /secrets tmpfs is the mount that makes file-delivered credentials
// possible at all: the crew rootfs is read-only, so without it every run
// carrying an SSH key or a token aborts at the mkdir preflight.
//
// Docker and Podman have OPPOSITE constraints on how to express it, and the
// collision is total rather than cosmetic. Verified by running the real
// HostConfig against real daemons rather than reasoned about:
//
//	docker 28.0.4 (CI, ubuntu-latest) — accepts uid=/gid= in the option-string
//	  path, and rejects them inside mount.TmpfsOptions.Options. That asymmetry
//	  is why the spec rides HostConfig.Tmpfs at all (see secretsTmpfsSpec).
//
//	podman 4.9.3 (CI, ubuntu-latest, rootful AND rootless) and podman 6.0.2
//	  (applehv, macOS 26) — reject uid= outright:
//	    container create: unknown mount option "uid=1001": invalid mount option
//	  Two majors apart, two hosts, two privilege modes, identical refusal. Every
//	  crew container failed at CREATE. Crewship did not partially work on
//	  Podman; it did not work at all, on a runtime the README, PRIVACY.md and
//	  `crewship doctor` all advertise.
//
// And dropping the uid/gid options alone is not a fix. Measured on podman
// 6.0.2, same container, three specs:
//
//	mode=0700,uid=1001,gid=1001 → create refused
//	mode=0700                   → mounts root:root; agent write DENIED
//	mode=1777                   → mounts root:root; agent write OK, size honoured
//
// A tmpfs is mounted by the guest as root, the crew container runs as uid 1001,
// and CapDrop: ALL means no exec — not even one asking for user 0 — has
// CAP_CHOWN to fix it afterwards. So on Podman the mount root is either
// agent-writable at 1777 or useless.
//
// This is the same conclusion the Apple provider reached independently for the
// same underlying reason (no uid/gid mount directive in its CLI), and its
// secretsMountSpec has carried mode=1777 since it shipped. Two of the three
// runtimes we support now agree; Docker is the outlier that can do better, and
// keeps doing better.
//
// What 1777 costs, precisely — the same accounting apple_create_args.go gives:
// with one shared agent uid the practical delta is small, because every agent
// already runs as 1001 and could already read a 0700 mount root owned by 1001.
// What changes is that the sidecar uid (1002) can now LIST /secrets and see
// agent slugs. It still cannot read a credential: writeCredentialFiles chmods
// each per-agent directory to 0700 and each file to 0400/0600 as uid 1001, so
// the secrets themselves keep their protection. The sticky bit stops one uid
// unlinking another's entries. Closing the last of the gap needs a uid/gid
// mount directive Podman's compat API does not have.
func TestSecretsTmpfsSpecFor(t *testing.T) {
	t.Parallel()

	t.Run("docker keeps the agent-owned mount root", func(t *testing.T) {
		t.Parallel()
		spec := secretsTmpfsSpecFor("docker")
		for _, want := range []string{"uid=1001", "gid=1001", "mode=0700"} {
			if !strings.Contains(spec, want) {
				t.Errorf("docker spec %q lost %q — that is the stronger ownership we only get to keep on Docker", spec, want)
			}
		}
	})

	// The load-bearing assertion. uid=/gid= present is not a weaker spec on
	// Podman, it is a crew that never starts.
	t.Run("podman carries no uid or gid directive", func(t *testing.T) {
		t.Parallel()
		spec := secretsTmpfsSpecFor("podman")
		if strings.Contains(spec, "uid=") || strings.Contains(spec, "gid=") {
			t.Fatalf("podman spec %q still carries a uid/gid directive; podman refuses the CREATE outright and no crew starts", spec)
		}
	})

	// Asserting "no uid=" alone would pass on a spec that drops the options and
	// leaves the mount root at 0700 root:root — which creates cleanly and then
	// fails every credential-bearing run, one layer later and much harder to
	// diagnose. The mount root must be agent-writable, and sticky so that
	// world-writable does not mean agents can unlink each other's directories.
	t.Run("podman mount root is agent-writable and sticky", func(t *testing.T) {
		t.Parallel()
		spec := secretsTmpfsSpecFor("podman")
		mode := tmpfsOption(spec, "mode")
		if mode == "" {
			t.Fatalf("podman spec %q sets no mode; a tmpfs defaults to 1777 by accident rather than by decision", spec)
		}
		// Parsed as octal and checked bit by bit, so "1777" is asserted for
		// what it MEANS rather than as a string that could be reproduced by a
		// wrong-but-similar value.
		bits, err := parseOctalMode(mode)
		if err != nil {
			t.Fatalf("podman spec %q has an unparseable mode %q: %v", spec, mode, err)
		}
		if bits&0o002 == 0 {
			t.Errorf("podman mount root mode %s is not world-writable; the agent runs as uid 1001 and the tmpfs is mounted by root, so it could not create /secrets/<slug>", mode)
		}
		if bits&0o1000 == 0 {
			t.Errorf("podman mount root mode %s has no sticky bit; on a world-writable directory that lets one agent unlink another's credential directory", mode)
		}
	})

	// The hardening that has nothing to do with ownership has to survive the
	// substitution. A spec that fixed the create and lost noexec would let a
	// credential file staged in /secrets be executed directly.
	t.Run("both specs keep the size cap and the exec/suid guards", func(t *testing.T) {
		t.Parallel()
		for _, rt := range []string{"docker", "podman"} {
			spec := secretsTmpfsSpecFor(rt)
			for _, want := range []string{"noexec", "nosuid", "size=16m"} {
				if !strings.Contains(spec, want) {
					t.Errorf("%s spec %q lost %q", rt, spec, want)
				}
			}
		}
	})

	// An unrecognised runtime gets Docker's spec: it is the stronger one, and
	// every runtime that reaches this code answered a Docker-API socket. A
	// runtime that turns out to share Podman's restriction fails loudly at
	// create with the exact message quoted above, which is a far better outcome
	// than silently handing every runtime the weaker mount root on the chance
	// that one of them needs it.
	t.Run("an unknown runtime gets the stronger docker spec", func(t *testing.T) {
		t.Parallel()
		for _, rt := range []string{"", "colima", "orbstack", "rancher", "nerdctl"} {
			if got := secretsTmpfsSpecFor(rt); got != secretsTmpfsSpec {
				t.Errorf("runtime %q got %q, want the docker spec %q", rt, got, secretsTmpfsSpec)
			}
		}
	})
}

// TestBuildCrewContainerConfigUsesRuntimeSecretsSpec pins the wiring rather than
// the constant. The selection helper being correct buys nothing if
// buildCrewContainerConfig goes on emitting the Docker spec unconditionally —
// which is exactly the state this fixes, and exactly the kind of gap a
// constants-only test leaves open.
func TestBuildCrewContainerConfigUsesRuntimeSecretsSpec(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		runtime string
		want    string
	}{
		{"docker", secretsTmpfsSpec},
		{"podman", secretsTmpfsSpecPodman},
	} {
		t.Run(tc.runtime, func(t *testing.T) {
			t.Parallel()
			p := newSecretsSpecProvider(t, tc.runtime)
			_, hostCfg, err := p.buildCrewContainerConfig(
				context.Background(),
				provider.CrewConfig{ID: "crew-id", Slug: "crew"},
				"crewship-crew", "debian:bookworm-slim", "", 1024, 1,
				crewDirs{output: t.TempDir(), workspace: t.TempDir(), crew: t.TempDir()},
			)
			if err != nil {
				t.Fatalf("build config: %v", err)
			}
			if got := hostCfg.Tmpfs["/secrets"]; got != tc.want {
				t.Errorf("on %s the crew HostConfig mounts /secrets as %q, want %q", tc.runtime, got, tc.want)
			}
		})
	}
}

// newSecretsSpecProvider builds a Provider with a detected runtime and the two
// mandatory bind-mount paths, over the package's fake daemon —
// buildCrewContainerConfig inspects the image to expand containerEnv, so it
// needs a client even though nothing here depends on the answer.
func newSecretsSpecProvider(t *testing.T, runtime string) *Provider {
	t.Helper()
	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/images/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"sha256:test","Config":{"Env":["PATH=/usr/bin"]}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	t.Cleanup(cleanup)

	base := t.TempDir()
	sidecar := filepath.Join(base, "crewship-sidecar")
	if err := os.WriteFile(sidecar, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}, 0o755); err != nil {
		t.Fatalf("write sidecar stub: %v", err)
	}
	entrypoint := filepath.Join(base, "entrypoint.sh")
	if err := os.WriteFile(entrypoint, []byte("#!/bin/sh\nexec sleep infinity\n"), 0o755); err != nil {
		t.Fatalf("write entrypoint stub: %v", err)
	}
	p.cfg = Config{SidecarBinaryPath: sidecar, EntrypointPath: entrypoint, OutputBasePath: base}
	p.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	p.detected = DetectResult{Runtime: runtime}
	return p
}
