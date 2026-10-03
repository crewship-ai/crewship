package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/moby/moby/api/types/container"
)

func sandboxTestSpec() provider.SandboxSpec {
	return provider.SandboxSpec{Lifetime: time.Minute, ID: "qualification-a", ImageID: "sha256:" + strings.Repeat("a", 64), MemoryBytes: 128 << 20, NanoCPUs: 1e9, PIDs: 32}
}
func TestSandboxRefusesUnsupportedBeforeDocker(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*provider.SandboxSpec)
		owner string
	}{
		{"missing owner", func(*provider.SandboxSpec) {}, ""},
		{"unbounded lifetime", func(s *provider.SandboxSpec) { s.Lifetime = 0 }, "installation"},
		{"mutable image", func(s *provider.SandboxSpec) { s.ImageID = "ubuntu:latest" }, "installation"},
		{"mounts", func(s *provider.SandboxSpec) {
			s.Mounts = []provider.SandboxMount{{ResourceID: "volume", Target: "/data"}}
		}, "installation"},
		{"unbounded memory", func(s *provider.SandboxSpec) { s.MemoryBytes = 0 }, "installation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) { t.Errorf("unexpected daemon call %s", r.URL.Path) })
			defer close()
			p.cfg.InstanceID = tc.owner
			spec := sandboxTestSpec()
			tc.edit(&spec)
			if _, err := p.CreateSandbox(context.Background(), spec); err == nil {
				t.Fatal("accepted unsupported request")
			}
		})
	}
}
func TestSandboxForeignOperationsAreRefused(t *testing.T) {
	for _, op := range []string{"inspect", "exec", "remove"} {
		t.Run(op, func(t *testing.T) {
			p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("foreign mutation %s", r.Method)
				}
				json.NewEncoder(w).Encode(map[string]any{"Id": "foreign", "Image": sandboxTestSpec().ImageID, "Config": map[string]any{"Labels": map[string]string{"crewship.instance-id": "other", "crewship.sandbox-id": "qualification-a", "crewship.sandbox-kind": "offline-v1"}}})
			})
			defer close()
			p.cfg.InstanceID = "installation"
			ref := provider.SandboxRef{ID: "qualification-a", RuntimeID: "foreign", ImageID: sandboxTestSpec().ImageID}
			var err error
			switch op {
			case "inspect":
				_, err = p.InspectSandbox(context.Background(), ref)
			case "exec":
				_, err = p.ExecSandbox(context.Background(), ref, provider.SandboxExec{Command: []string{"true"}, Timeout: time.Second, OutputLimit: 256})
			case "remove":
				err = p.RemoveSandbox(context.Background(), ref)
			}
			if !errors.Is(err, provider.ErrSandboxDenied) {
				t.Fatalf("foreign access: %v", err)
			}
		})
	}
}
func TestSandboxRejectsImplicitImageVolumes(t *testing.T) {
	p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/images/") {
			t.Errorf("unexpected call %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"Id": sandboxTestSpec().ImageID, "Config": map[string]any{"Volumes": map[string]any{"/data": map[string]any{}}}})
	})
	defer close()
	p.cfg.InstanceID = "installation"
	if _, err := p.CreateSandbox(context.Background(), sandboxTestSpec()); !errors.Is(err, provider.ErrSandboxDenied) {
		t.Fatalf("implicit volume: %v", err)
	}
}
func TestSandboxOutputIsBounded(t *testing.T) {
	b := &sandboxOutput{limit: 4}
	if _, err := b.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("de")); !errors.Is(err, provider.ErrSandboxOutputLimit) {
		t.Fatal(err)
	}
	if b.String() != "abc" {
		t.Fatal("limit modified prior output")
	}
}

func TestOfflineSandboxAuditRejectsWeakenedConfiguration(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*container.InspectResponse)
	}{
		{"host network", func(c *container.InspectResponse) { c.HostConfig.NetworkMode = "host" }},
		{"capability", func(c *container.InspectResponse) { c.HostConfig.CapAdd = []string{"NET_ADMIN"} }},
		{"missing no-new-privileges", func(c *container.InspectResponse) { c.HostConfig.SecurityOpt = nil }},
		{"host pid", func(c *container.InspectResponse) { c.HostConfig.PidMode = "host" }},
		{"implicit mount", func(c *container.InspectResponse) {
			c.Mounts = []container.MountPoint{{Source: "/host", Destination: "/data"}}
		}},
		{"unbounded pids", func(c *container.InspectResponse) { c.HostConfig.PidsLimit = nil }},
		{"extra tmpfs", func(c *container.InspectResponse) { c.HostConfig.Tmpfs["/extra"] = "rw" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pids := int64(64)
			c := container.InspectResponse{HostConfig: &container.HostConfig{NetworkMode: "none", IpcMode: "private", CgroupnsMode: "private", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"}, Resources: container.Resources{Memory: 128 << 20, MemorySwap: 128 << 20, NanoCPUs: 1e9, PidsLimit: &pids}, Tmpfs: map[string]string{"/home/agent": "rw,nosuid,nodev,size=67108864,uid=1001,gid=1001,mode=0700", "/tmp": "rw,nosuid,nodev,noexec,size=16777216,mode=1777"}}, Config: &container.Config{User: "1002:1002"}}
			if err := auditOfflineSandbox(c); err != nil {
				t.Fatal("valid fixture rejected", err)
			}
			tc.mutate(&c)
			if !errors.Is(auditOfflineSandbox(c), provider.ErrSandboxDenied) {
				t.Fatal("weakened configuration accepted")
			}
		})
	}
}
