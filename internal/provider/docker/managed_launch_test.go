package docker

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedLaunchRejectsRuntimeDriftAndMountAliases(t *testing.T) {
	for _, which := range []string{"valid", "writable-root", "wrong-image", "privileged", "binary-overlay", "readonly-overlay", "launcher-overlay", "missing-launcher", "symlink-parent", "dynamic-launcher", "stale-launcher", "launcher-symlink", "launcher-duplicate", "fixed-launcher"} {
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
			if which != "fixed-launcher" {
				sum := sha256.Sum256(raw)
				immutable := filepath.Join(filepath.Dir(launcher), fmt.Sprintf("crewship-sidecar-%x", sum))
				if err := os.Rename(launcher, immutable); err != nil {
					t.Fatal(err)
				}
				launcher = immutable
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
				if r.Method == "HEAD" || strings.HasSuffix(r.URL.Path, "/archive") {
					stat := map[string]any{"name": filepath.Base(r.URL.Query().Get("path")), "size": 0, "mode": uint32(os.ModeDir | 0755), "mtime": "2026-10-04T00:00:00Z", "linkTarget": ""}
					if which == "symlink-parent" {
						stat["linkTarget"] = "/home/agent/alias"
					}
					raw, _ := json.Marshal(stat)
					w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(raw))
					if r.Method == "HEAD" {
						return
					}
				}
				if strings.HasSuffix(r.URL.Path, "/archive") {
					tw := tar.NewWriter(w)
					data := append([]byte(nil), raw...)
					if which == "stale-launcher" {
						data[63] ^= 1
					}
					h := &tar.Header{Name: "crewship-sidecar", Typeflag: tar.TypeReg, Mode: 0555, Size: int64(len(data))}
					if which == "launcher-symlink" {
						h.Typeflag = tar.TypeSymlink
						h.Linkname = "/home/agent/evil"
						h.Size = 0
						data = nil
					}
					tw.WriteHeader(h)
					tw.Write(data)
					if which == "launcher-duplicate" {
						tw.WriteHeader(h)
						tw.Write(data)
					}
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
					info, _ := os.Lstat(launcher)
					parent, _ := os.Lstat(filepath.Dir(launcher))
					t.Fatalf("%v launcher=%s info=%+v parent=%+v expected=%s", err, launcher, info, parent, p.ExpectedSidecarHash())
				}
			} else if err == nil {
				t.Fatal("unsafe runtime admitted")
			}
		})
	}
}

func TestManagedLauncherStagingNeverReplacesAddressedInode(t *testing.T) {
	t.Setenv("CREWSHIP_MANAGED_LAUNCH_CREWS", "pilot")
	dir := t.TempDir()
	source := filepath.Join(t.TempDir(), "launcher")
	if err := os.WriteFile(source, []byte("first artifact"), 0555); err != nil {
		t.Fatal(err)
	}
	first := stageRuntimeArtifacts(Config{SidecarBinaryPath: source, OutputBasePath: dir}, quietLogger())
	before, err := os.Stat(first.SidecarBinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	again := stageRuntimeArtifacts(Config{SidecarBinaryPath: source, OutputBasePath: dir}, quietLogger())
	after, err := os.Stat(again.SidecarBinaryPath)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("staging replaced an existing immutable inode")
	}
	if err := os.Chmod(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("second artifact"), 0555); err != nil {
		t.Fatal(err)
	}
	next := stageRuntimeArtifacts(Config{SidecarBinaryPath: source, OutputBasePath: dir}, quietLogger())
	if next.SidecarBinaryPath == first.SidecarBinaryPath {
		t.Fatal("new generation reused old bind source")
	}
	raw, err := os.ReadFile(first.SidecarBinaryPath)
	if err != nil || string(raw) != "first artifact" {
		t.Fatal("old generation lost")
	}
	if err := os.Chmod(first.SidecarBinaryPath, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := stageManagedLauncher(first.SidecarBinaryPath, dir); err == nil {
		t.Fatal("writable addressed source accepted")
	}
}
