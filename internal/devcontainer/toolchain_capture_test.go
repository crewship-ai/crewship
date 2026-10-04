package devcontainer

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

func TestToolchainCaptureIsSharedWithBuildOnlyProvisioning(t *testing.T) {
	rec := &dockerfileRecorder{}
	if err := writeToolchainInventory(context.Background(), "", []string{"claude", "codex"}, nil, rec.exec); err != nil {
		t.Fatal(err)
	}
	steps := strings.Join(rec.steps(), "\n")
	for _, want := range []string{"USER 1001:1001", "timeout -k 1 8", "ulimit -f 8", "DISABLE_AUTOUPDATER=1", toolchainDirectory, "chown -R 0:0"} {
		if !strings.Contains(steps, want) {
			t.Fatalf("build recipe missing %q", want)
		}
	}
}

func TestManagedArtifactCaptureRejectsArchiveSubstitution(t *testing.T) {
	raw := make([]byte, 64)
	copy(raw, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(raw[16:], 2)
	binary.LittleEndian.PutUint16(raw[18:], 62)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint16(raw[52:], 64)
	for _, which := range []string{"valid", "symlink", "agent-owned", "sidecar-owned", "writable", "wrong-name", "script", "duplicate"} {
		t.Run(which, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			data := raw
			if which == "script" {
				data = []byte("#!/usr/bin/env node\n")
			}
			h := &tar.Header{Name: "claude", Typeflag: tar.TypeReg, Mode: 0555, Size: int64(len(data))}
			switch which {
			case "symlink":
				h.Typeflag = tar.TypeSymlink
				h.Linkname = "/home/agent/evil"
				h.Size = 0
				data = nil
			case "agent-owned":
				h.Uid = 1001
			case "sidecar-owned":
				h.Uid = 1002
			case "writable":
				h.Mode = 0777
			case "wrong-name":
				h.Name = "../claude"
			}
			if err := tw.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(data); err != nil {
				t.Fatal(err)
			}
			if which == "duplicate" {
				tw.WriteHeader(h)
				tw.Write(data)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			artifact := captureLaunchArtifact("/opt/native/claude", &buf)
			if which == "valid" || which == "agent-owned" {
				if artifact == nil || len(artifact.SHA256) != 64 {
					t.Fatal("immutable host hash missing")
				}
			} else if artifact != nil {
				t.Fatal("unsafe archive became launch authority")
			}
		})
	}
}

func TestToolchainCapturePreservesCustomBinaryVerification(t *testing.T) {
	rec := &dockerfileRecorder{}
	if err := writeToolchainInventory(context.Background(), "", []string{"custom-binary"}, nil, rec.exec); err != nil {
		t.Fatal(err)
	}
	if len(rec.steps()) != 0 {
		t.Fatal("inventory must not guess a custom binary's version protocol")
	}
}

func TestInspectToolchainNeverStartsImageAndCleansScratch(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprint(malformed), func(t *testing.T) {
			files := map[string]string{"schema": "1", "codex.path": "/opt/mise/data/shims/codex", "codex.version": "codex-cli 0.153.2", "codex.status": "0"}
			if malformed {
				files["schema"] = "unsupported"
			}
			docker := &inventoryDocker{archive: toolchainArchive(t, files)}
			p := NewProvisioner(docker, nil, nil, testLogger())
			got := p.inspectToolchain(context.Background(), "mutable:tag", []string{"codex"})
			if got.ImageID != "sha256:immutable" {
				t.Fatalf("identity=%q", got.ImageID)
			}
			if !malformed && got.Tools[0].Version != "0.153.2" {
				t.Fatalf("inventory=%+v", got)
			}
			if malformed && got.Status != "unavailable" {
				t.Fatalf("malformed data was trusted: %+v", got)
			}
			if len(docker.startedIDs) != 0 {
				t.Fatal("inspection executed image code")
			}
			if len(docker.removedIDs) != 1 {
				t.Fatal("inspection leaked its scratch container")
			}
			created := docker.createdContainers[0]
			if created.config.Image != "sha256:immutable" || created.hostCfg.NetworkMode != "none" || !created.hostCfg.ReadonlyRootfs {
				t.Fatalf("unsafe image inspection: %+v", created)
			}
		})
	}
}

type inventoryDocker struct {
	mockCommitClient
	archive []byte
}

func (d *inventoryDocker) ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	return client.ImageInspectResult{InspectResponse: image.InspectResponse{ID: "sha256:immutable"}}, nil
}
func (d *inventoryDocker) CopyFromContainer(context.Context, string, client.CopyFromContainerOptions) (client.CopyFromContainerResult, error) {
	return client.CopyFromContainerResult{Content: io.NopCloser(bytes.NewReader(d.archive))}, nil
}

func TestToolchainCaptureUsesRuntimeMiseLocationsWithoutUnrelatedEnv(t *testing.T) {
	var agentEnv []string
	exec := func(_ context.Context, _ string, _ []string, user string, env []string) (string, int, error) {
		if user == "1001:1001" {
			agentEnv = append([]string(nil), env...)
		}
		return "", 0, nil
	}
	err := writeToolchainInventory(context.Background(), "fixture", []string{"codex"}, map[string]string{"MISE_DATA_DIR": "/custom/mise-data", "UNRELATED_SECRET": "EXAMPLE-NOT-A-REAL-SECRET"}, exec)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(agentEnv, "\n")
	for _, want := range []string{"MISE_DATA_DIR=/custom/mise-data", "MISE_GLOBAL_CONFIG_FILE=/opt/mise/config/config.toml", "DISABLE_AUTOUPDATER=1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing runtime location %q", want)
		}
	}
	if strings.Contains(joined, "UNRELATED_SECRET") {
		t.Fatal("version probe inherited unrelated configured environment")
	}
}

func TestManagedNativeEvidenceDoesNotChangeLegacyInventory(t *testing.T) {
	for _, mode := range []string{"native", "mise-fails", "shim", "legacy-shim"} {
		t.Run(mode, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			inventory := filepath.Join(dir, "inventory")
			shadow := filepath.Join(dir, "shadow", "codex")
			native := filepath.Join(dir, "native", "codex")
			mise := filepath.Join(dir, "mise")
			for _, p := range []string{inventory, filepath.Dir(shadow), filepath.Dir(native)} {
				if err := os.MkdirAll(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for p, source := range map[string]string{shadow: "#!/bin/sh\necho codex-cli 0.159.0\n", native: "#!/bin/sh\necho codex-cli 0.160.0\n"} {
				if err := os.WriteFile(p, []byte(source), 0700); err != nil {
					t.Fatal(err)
				}
			}
			// Model the Linux image utilities explicitly: host macOS lacks
			// GNU timeout and readlink -f. These immediate fixture programs
			// exercise argv/path evidence, not timeout enforcement.
			utilities := filepath.Join(dir, "utilities")
			if err := os.MkdirAll(utilities, 0700); err != nil {
				t.Fatal(err)
			}
			for name, source := range map[string]string{
				"timeout":  "#!/bin/sh\n[ \"$1\" = -k ] && [ \"$2\" = 1 ] && [ \"$3\" = 8 ] || exit 125\nshift 3\nexec \"$@\"\n",
				"readlink": "#!/bin/sh\n[ \"$1\" = -f ] || exit 125\nif [ -L \"$2\" ]; then /usr/bin/readlink \"$2\"; else printf '%s\\n' \"$2\"; fi\n",
			} {
				if err := os.WriteFile(filepath.Join(utilities, name), []byte(source), 0700); err != nil {
					t.Fatal(err)
				}
			}
			resolved := native
			if mode == "shim" {
				resolved = filepath.Join(dir, "shim")
				if err := os.Symlink(mise, resolved); err != nil {
					t.Fatal(err)
				}
			}
			source := "#!/bin/sh\nif [ \"$1\" = --version ]; then case \"$0\" in */codex) echo codex-cli 0.160.0;; *) echo mise 2026.10.0;; esac; exit 0; fi\nprintf '%s\\n' '" + resolved + "'\n"
			if mode == "mise-fails" {
				source = "#!/bin/sh\nexit 1\n"
			}
			if mode == "legacy-shim" {
				if err := os.Remove(shadow); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(mise, shadow); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(mise, []byte(source), 0700); err != nil {
				t.Fatal(err)
			}
			run := func(ctx context.Context, _ string, cmd []string, user string, env []string) (string, int, error) {
				if user != "1001:1001" {
					return "", 0, nil
				}
				script := strings.ReplaceAll(strings.ReplaceAll(cmd[2], toolchainDirectory, inventory), "/usr/local/bin/mise", mise)
				command := exec.CommandContext(ctx, "sh", "-c", script)
				command.Env = append(os.Environ(), env...)
				out, err := command.CombinedOutput()
				if err != nil {
					return string(out), 1, err
				}
				return string(out), 0, nil
			}
			if err := writeToolchainInventory(context.Background(), "fixture", []string{"codex"}, map[string]string{"PATH": filepath.Dir(shadow) + ":" + utilities + ":/usr/bin:/bin"}, run); err != nil {
				t.Fatal(err)
			}
			baseline := filepath.Join(dir, "baseline")
			if err := os.MkdirAll(baseline, 0700); err != nil {
				t.Fatal(err)
			}
			mainProbe := exec.Command("sh", "-c", `command -v codex > "$1/codex.path"; codex --version > "$1/codex.version"`, "baseline", baseline)
			mainProbe.Env = append(os.Environ(), "PATH="+filepath.Dir(shadow)+":/usr/bin:/bin")
			if out, err := mainProbe.CombinedOutput(); err != nil {
				t.Fatalf("main probe: %v %s", err, out)
			}
			for _, name := range []string{"codex.path", "codex.version"} {
				before, err := os.ReadFile(filepath.Join(baseline, name))
				if err != nil {
					t.Fatal(err)
				}
				after, err := os.ReadFile(filepath.Join(inventory, name))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Fatalf("legacy %s changed: before=%q after=%q", name, before, after)
				}
			}
			read := func(name string) string {
				raw, err := os.ReadFile(filepath.Join(inventory, name))
				if err != nil {
					t.Fatal(err)
				}
				return strings.TrimSpace(string(raw))
			}
			legacyVersion := "codex-cli 0.159.0"
			if mode == "legacy-shim" {
				legacyVersion = "codex-cli 0.160.0"
			}
			if read("codex.path") != shadow || read("codex.version") != legacyVersion || read("codex.status") != "0" {
				t.Fatal("legacy PATH-first evidence changed")
			}
			if mode == "native" || mode == "legacy-shim" {
				if read("codex.native-path") != native || read("codex.native-version") != "codex-cli 0.160.0" {
					t.Fatal("native probe did not use exact executable")
				}
			} else if _, err := os.Stat(filepath.Join(inventory, "codex.native-path")); !os.IsNotExist(err) {
				t.Fatal("failed mise/shim resolution became managed evidence")
			}
		})
	}
}

func TestManagedToolchainCandidateRejectsShimAndMise(t *testing.T) {
	for _, candidate := range []string{"/opt/mise/data/installs/codex/0.160.0/bin/codex", "/opt/mise/data/shims/codex", "/usr/local/bin/mise", "/opt/mise/data/installs/codex/0.159.0/bin/codex"} {
		files := map[string]string{"schema": "1", "codex.path": "/usr/local/bin/mise", "codex.status": "0", "codex.version": "codex-cli 0.160.0", "codex.native-path": candidate, "codex.native-version": "codex-cli 0.160.0", "codex.native-status": "0"}
		got, err := parseToolchainArchive(bytes.NewReader(toolchainArchive(t, files)), []string{"codex"})
		if err != nil {
			t.Fatal(err)
		}
		valid := candidate == "/opt/mise/data/installs/codex/0.160.0/bin/codex"
		if (got.Tools[0].ManagedPath != "") != valid {
			t.Fatalf("candidate=%q evidence=%+v", candidate, got.Tools[0])
		}
		if got.Tools[0].Path != "/usr/local/bin/mise" || got.Tools[0].Version != "0.160.0" {
			t.Fatal("legacy shim observation changed")
		}
	}
	if captureLaunchArtifact("/usr/local/bin/mise", strings.NewReader("irrelevant")) != nil {
		t.Fatal("mise misattributed as CLI")
	}
}
