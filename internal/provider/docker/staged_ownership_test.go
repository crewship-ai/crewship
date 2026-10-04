//go:build linux

package docker

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestOfflineOwnershipHelperUsesOnlyTrustedStaticArtifact(t *testing.T) {
	for _, mode := range []string{"valid", "tag-replaced", "missing-image-id", "dynamic", "wrong-architecture", "image-volume", "helper-failure"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "trusted-sidecar")
			raw := make([]byte, 64)
			copy(raw, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
			binary.LittleEndian.PutUint16(raw[16:], 2)
			binary.LittleEndian.PutUint16(raw[18:], 62)
			binary.LittleEndian.PutUint32(raw[20:], 1)
			binary.LittleEndian.PutUint16(raw[52:], 64)
			if mode == "dynamic" {
				raw = []byte("not a static native executable")
			}
			if err := os.WriteFile(source, raw, 0555); err != nil {
				t.Fatal(err)
			}
			creates, starts, removes := 0, 0, 0
			mutableReads, pinnedReads := 0, 0
			p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
					mutable := strings.Contains(r.URL.Path, "/images/qualified-image/")
					if mutable {
						mutableReads++
					} else {
						pinnedReads++
						if !strings.Contains(r.URL.Path, "/images/sha256:qualified/") {
							t.Errorf("unexpected image identity: %s", r.URL.Path)
						}
					}
					arch := "amd64"
					if mode == "wrong-architecture" {
						arch = "arm64"
					}
					cfg := map[string]any{"Env": []string{"BASH_ENV=/image-evil", "TOKEN=private"}}
					if mode == "image-volume" {
						cfg["Volumes"] = map[string]any{"/mnt/init": map[string]any{}}
					}
					id := "sha256:qualified"
					if mode == "missing-image-id" {
						id = ""
					}
					// After the first resolution the mutable tag points to an
					// incompatible image with an unqualified automatic volume.
					if mode == "tag-replaced" && mutable && mutableReads > 1 {
						id, arch = "sha256:replacement", "arm64"
						cfg["Volumes"] = map[string]any{"/mnt/init": map[string]any{}}
					}
					json.NewEncoder(w).Encode(map[string]any{"Id": id, "Architecture": arch, "Config": cfg})
				case strings.HasSuffix(r.URL.Path, "/containers/create"):
					creates++
					var req struct {
						container.Config
						HostConfig container.HostConfig
					}
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Fatal(err)
					}
					if req.User != "0:0" || req.Image != "sha256:qualified" || !reflect.DeepEqual(req.Entrypoint, []string{stagedInitializerBinary}) || !reflect.DeepEqual(req.Cmd, []string{"--staged-init", "tree:/mnt/init/0", "tree:/mnt/init/1", "crew:/mnt/init/2", "volume:/mnt/init/3"}) {
						t.Errorf("unsafe executable/config: %+v", req.Config)
					}
					caps := append([]string(nil), req.HostConfig.CapAdd...)
					for i := range caps {
						caps[i] = strings.TrimPrefix(caps[i], "CAP_")
					}
					slices.Sort(caps)
					if req.HostConfig.NetworkMode != "none" || !req.HostConfig.ReadonlyRootfs || !reflect.DeepEqual(req.HostConfig.CapDrop, []string{"ALL"}) || !reflect.DeepEqual(caps, []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID"}) || !reflect.DeepEqual(req.HostConfig.SecurityOpt, []string{"no-new-privileges"}) {
						t.Errorf("offline privilege boundary changed: %+v", req.HostConfig)
					}
					for _, env := range req.Env {
						if strings.Contains(env, "private") || strings.Contains(env, "image-evil") {
							t.Errorf("image environment reached trusted helper: %s", env)
						}
					}
					if len(req.HostConfig.Mounts) != 5 || !req.HostConfig.Mounts[4].ReadOnly || req.HostConfig.Mounts[4].Source == source {
						t.Errorf("artifact not immutable readonly publication: %+v", req.HostConfig.Mounts)
					}
					w.WriteHeader(http.StatusCreated)
					w.Write([]byte(`{"Id":"offline-helper"}`))
				case strings.HasSuffix(r.URL.Path, "/containers/offline-helper/start"):
					starts++
					w.WriteHeader(http.StatusNoContent)
				case strings.HasSuffix(r.URL.Path, "/containers/offline-helper/wait"):
					code := 0
					if mode == "helper-failure" {
						code = 7
					}
					json.NewEncoder(w).Encode(map[string]any{"StatusCode": code})
				case strings.HasSuffix(r.URL.Path, "/containers/offline-helper/logs"):
					w.Write([]byte("private log detail permission denied"))
				case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/offline-helper"):
					removes++
					if r.URL.Query().Get("force") != "1" && r.URL.Query().Get("force") != "true" {
						t.Error("helper cleanup must force remove")
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected Docker request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			defer close()
			p.cfg.SidecarBinaryPath = source
			p.cfg.OutputBasePath = root
			p.cfg.InstanceID = "installation"
			err := p.stagedOwnership(t.Context(), provider.CrewConfig{ID: "crew"}, "qualified-image", crewDirs{output: "/host/output", workspace: "/host/workspace", crew: "/host/crew"}, []string{"owned-home"})
			if os.Geteuid() == 1001 || os.Geteuid() == 1002 {
				if err == nil || creates != 0 || starts != 0 || removes != 0 {
					t.Fatal("workload host UID admitted ownership helper")
				}
				t.Log("unqualified host UID: helper admission denial verified")
				return
			}

			if mode == "valid" || mode == "tag-replaced" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("invalid helper admitted")
			}
			if mutableReads != 1 {
				t.Fatalf("mutable tag resolved %d times", mutableReads)
			}
			if mode == "missing-image-id" && pinnedReads != 0 {
				t.Fatal("missing ID reached artifact qualification/environment lookup")
			}
			if mode == "valid" || mode == "tag-replaced" || mode == "helper-failure" {
				if pinnedReads != 2 {
					t.Fatalf("qualification/environment pinned reads=%d", pinnedReads)
				}
				if creates != 1 || starts != 1 || removes != 1 {
					t.Fatalf("helper lifecycle=%d/%d/%d", creates, starts, removes)
				}
			} else if creates != 0 || starts != 0 || removes != 0 {
				t.Fatalf("unqualified artifact ran image helper: %d/%d/%d", creates, starts, removes)
			}
			if err != nil && strings.Contains(err.Error(), "private log detail") {
				t.Fatal("untrusted helper log leaked")
			}
		})
	}
}
