package docker

import (
	"archive/tar"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedLaunchRejectsRuntimeDriftAndMountAliases(t *testing.T) {
	for _, which := range []string{"valid", "writable-root", "wrong-image", "privileged", "binary-overlay", "readonly-overlay", "launcher-overlay", "missing-launcher", "symlink-parent", "dynamic-launcher"} {
		t.Run(which, func(t *testing.T) {
			image := "sha256:" + strings.Repeat("a", 64)
			launcher := filepath.Join(t.TempDir(), "launcher")
			raw := make([]byte, 64)
			copy(raw, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
			binary.LittleEndian.PutUint16(raw[16:], 2)
			binary.LittleEndian.PutUint16(raw[18:], 62)
			binary.LittleEndian.PutUint32(raw[20:], 1)
			binary.LittleEndian.PutUint16(raw[52:], 64)
			if which == "dynamic-launcher" {
				raw = []byte("not a static ELF")
			}
			if err := os.WriteFile(launcher, raw, 0555); err != nil {
				t.Fatal(err)
			}
			root := true
			privileged := false
			if which == "writable-root" {
				root = false
			}
			if which == "privileged" {
				privileged = true
			}
			actualImage := image
			if which == "wrong-image" {
				actualImage = "sha256:" + strings.Repeat("b", 64)
			}
			mounts := []map[string]any{{"Type": "bind", "Source": launcher, "Destination": "/usr/local/bin/crewship-sidecar", "RW": false}}
			switch which {
			case "binary-overlay", "readonly-overlay":
				mounts = append(mounts, map[string]any{"Type": "bind", "Source": "/foreign", "Destination": "/opt/native", "RW": which == "binary-overlay"})
			case "launcher-overlay":
				mounts = append(mounts, map[string]any{"Type": "bind", "Source": "/foreign", "Destination": "/usr", "RW": false})
			case "missing-launcher":
				mounts = nil
			}
			p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "HEAD" {
					stat := map[string]any{"name": filepath.Base(r.URL.Query().Get("path")), "size": 0, "mode": uint32(os.ModeDir | 0755), "mtime": "2026-10-04T00:00:00Z", "linkTarget": ""}
					if which == "symlink-parent" {
						stat["linkTarget"] = "/home/agent/alias"
					}
					raw, _ := json.Marshal(stat)
					w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(raw))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/archive") {
					tw := tar.NewWriter(w)
					tw.WriteHeader(&tar.Header{Name: "crewship-sidecar", Typeflag: tar.TypeReg, Mode: 0555, Size: int64(len(raw))})
					tw.Write(raw)
					tw.Close()
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"Id": "c1", "Image": actualImage, "State": map[string]any{"Running": true}, "HostConfig": map[string]any{"ReadonlyRootfs": root, "Privileged": privileged, "SecurityOpt": []string{"no-new-privileges"}}, "Mounts": mounts})
			})
			defer close()
			p.cfg.SidecarBinaryPath = launcher
			d := managedlaunch.Descriptor{Artifact: managedlaunch.Artifact{Path: "/opt/native/claude", SHA256: strings.Repeat("b", 64), Format: "static_elf"}, ImageID: image, RevisionID: "r1", LockSHA256: strings.Repeat("c", 64), Binary: "claude", Version: "2.1.288"}
			err := p.AttestManagedLaunch(context.Background(), "c1", d)
			if which == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unsafe runtime admitted")
			}
		})
	}
}
