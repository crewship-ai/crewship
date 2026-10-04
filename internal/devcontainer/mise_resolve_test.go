package devcontainer

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestResolverInputNeverIncludesEnvironmentOrHostPaths(t *testing.T) {
	cfg := &MiseConfig{Tools: map[string]string{"node": "22.0.0"}, Env: map[string]string{"PRIVATE_TOKEN": "DO_NOT_DELIVER"}, Lock: &MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": "lockfile_version = 3", ".mise/locks/npm-tool/package.json": "{}"}}}
	raw, err := resolverInputArchive(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("DO_NOT_DELIVER")) || bytes.Contains(raw, []byte("PRIVATE_TOKEN")) {
		t.Fatal("resolver received config environment")
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	files := map[string]string{}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if h.Uid != 1001 || h.Gid != 1001 || !strings.HasPrefix(h.Name, "crewship-resolver") {
			t.Fatalf("unexpected ownership/path %+v", h)
		}
		if h.Typeflag == tar.TypeReg {
			data, e := io.ReadAll(tr)
			if e != nil {
				t.Fatal(e)
			}
			files[h.Name] = string(data)
		}
	}
	if files["crewship-resolver/config/config.toml"] != "[tools]\nnode = \"22.0.0\"\n" || files["crewship-resolver/config/.mise/locks/npm-tool/package.json"] != "{}" {
		t.Fatalf("incomplete input: %v", files)
	}
}

func TestResolverArchiveRejectsTraversalLinksDuplicatesAndOversize(t *testing.T) {
	cases := []struct {
		name    string
		headers []*tar.Header
		want    bool
	}{
		{"valid", []*tar.Header{{Name: "config/", Typeflag: tar.TypeDir}, {Name: "config/config.toml", Size: 1}, {Name: "config/mise.lock", Size: 1}, {Name: "config/.mise/locks/npm/aube-lock.yaml", Size: 1}}, true},
		{"traversal", []*tar.Header{{Name: "config/../mise.lock", Size: 1}}, false},
		{"symlink", []*tar.Header{{Name: "config/mise.lock", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}, false},
		{"hardlink", []*tar.Header{{Name: "config/mise.lock", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"}}, false},
		{"duplicate", []*tar.Header{{Name: "config/mise.lock", Size: 1}, {Name: "config/mise.lock", Size: 1}}, false},
		{"oversize", []*tar.Header{{Name: "config/mise.lock", Size: maxMiseLockFileBytes + 1}}, false},
		{"unexpected", []*tar.Header{{Name: "config/mise.lock", Size: 1}, {Name: "config/credentials", Size: 1}}, false},
		{"missing", []*tar.Header{{Name: "config/config.toml", Size: 1}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var data bytes.Buffer
			tw := tar.NewWriter(&data)
			for _, h := range tc.headers {
				if h.Typeflag == 0 {
					h.Typeflag = tar.TypeReg
				}
				if err := tw.WriteHeader(h); err != nil {
					t.Fatal(err)
				}
				if h.Size > 0 {
					if _, err := io.WriteString(tw, strings.Repeat("x", int(h.Size))); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			b, e := readResolverLockArchive(&data)
			if (e == nil) != tc.want {
				t.Fatalf("bundle=%+v err=%v", b, e)
			}
		})
	}
}

func TestResolverOutputBound(t *testing.T) {
	b := &resolverOutput{limit: 3}
	if _, e := b.Write([]byte("abc")); e != nil {
		t.Fatal(e)
	}
	if _, e := b.Write([]byte("d")); e == nil || b.String() != "abc" {
		t.Fatal("output cap failed")
	}
}

func TestMiseResolverAuditRejectsWeakenedBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*container.InspectResponse)
	}{
		{"root", func(c *container.InspectResponse) { c.Config.User = "0" }},
		{"host network", func(c *container.InspectResponse) { c.HostConfig.NetworkMode = "host" }},
		{"writable root", func(c *container.InspectResponse) { c.HostConfig.ReadonlyRootfs = false }},
		{"mount", func(c *container.InspectResponse) {
			c.Mounts = []container.MountPoint{{Source: "/host", Destination: "/work"}}
		}},
		{"image healthcheck", func(c *container.InspectResponse) { c.Config.Healthcheck = nil }},
		{"capability", func(c *container.InspectResponse) { c.HostConfig.CapAdd = []string{"SYS_ADMIN"} }},
		{"lifetime", func(c *container.InspectResponse) { c.Config.Cmd = []string{"infinity"} }},
		{"unbounded memory", func(c *container.InspectResponse) { c.HostConfig.Memory = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pids := int64(128)
			c := container.InspectResponse{Image: "sha256:fixture", Config: &container.Config{User: "1002:1002", Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"360"}, Healthcheck: &container.HealthConfig{Test: []string{"NONE"}}, Labels: map[string]string{"crewship.mise-resolver": "fixture"}}, HostConfig: &container.HostConfig{NetworkMode: "bridge", IpcMode: "private", CgroupnsMode: "private", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"}, Resources: container.Resources{Memory: 512 << 20, MemorySwap: 512 << 20, NanoCPUs: 1e9, PidsLimit: &pids}, Tmpfs: map[string]string{"/tmp": "rw,nosuid,nodev,size=268435456,uid=1001,gid=1001,mode=0700"}}}
			if e := auditMiseResolver(c, "sha256:fixture", "fixture"); e != nil {
				t.Fatal(e)
			}
			tc.edit(&c)
			if auditMiseResolver(c, "sha256:fixture", "fixture") == nil {
				t.Fatal("weakened builder accepted")
			}
		})
	}
}
