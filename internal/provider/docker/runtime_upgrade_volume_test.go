package docker

import (
	"encoding/json"
	"errors"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeUpgradePreservesUnexpectedVolumes(t *testing.T) {
	p := &Provider{}
	for _, tc := range []struct {
		name, target, volume string
		allowed              bool
	}{
		{"inherited anonymous image volume", "/data", "anonymous", false},
		{"unexpected named volume", "/home/agent", "another-home", false},
		{"crew home", "/home/agent", p.homeVolumeName("crew", "engineering"), true},
		{"crew tools", "/opt/crew-tools", p.toolsVolumeName("crew", "engineering"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := p.verifyReplacementVolumes(t.Context(), []container.MountPoint{{Type: mount.TypeVolume, Name: tc.volume, Destination: tc.target}}, "crew", "engineering")
			if tc.allowed && err != nil || !tc.allowed && !errors.Is(err, provider.ErrRuntimeImageUpdatePending) {
				t.Fatalf("replacement decision: %v", err)
			}
		})
	}
}

func TestCrewRuntimeDataMountContract(t *testing.T) {
	for _, tc := range []struct {
		name, engine string
		selinux      bool
	}{
		{"Docker", "docker", false}, {"rootful Podman", "podman", false}, {"rootless Podman with SELinux", "podman", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			base, err := filepath.EvalSymlinks(base)
			if err != nil {
				t.Fatal(err)
			}
			p := &Provider{cfg: Config{OutputBasePath: base}, detected: DetectResult{Runtime: tc.engine}, selinuxEnabled: tc.selinux}
			src := p.crewDataSource("team", "/output")
			if err := os.MkdirAll(src, 0700); err != nil {
				t.Fatal(err)
			}
			mounts, binds, err := p.crewRuntimeBinds([]mount.Mount{noexecBindMount(src, "/output", "team", "instance")}, false)
			if err != nil {
				t.Fatal(err)
			}
			if tc.engine == "docker" {
				if len(binds) != 0 || len(mounts) != 1 || mounts[0].VolumeOptions.DriverConfig.Options["device"] != src {
					t.Fatalf("unexpected Docker mounts: %v %v", mounts, binds)
				}
				return
			}
			opts := "rw,noexec,nosuid"
			if tc.selinux {
				opts = "rw,z,noexec,nosuid"
			}
			if len(mounts) != 0 || len(binds) != 1 || binds[0] != src+":/output:"+opts {
				t.Fatalf("unexpected Podman mounts: %v %v", mounts, binds)
			}
			installed := container.MountPoint{Type: mount.TypeBind, Source: src, Destination: "/output", RW: true}
			for _, v := range []struct {
				spec  string
				valid bool
			}{
				{binds[0] + ",rprivate,nodev,rbind", true}, {src + ":/output:rw,rprivate", false}, {"anonymous:/output:rw,noexec,nosuid", false}, {src + ":/output:rw,noexec,nosuid,exec", false},
			} {
				if p.verifiedPodmanDataBind(&container.HostConfig{Binds: []string{v.spec}}, installed, "team") != v.valid {
					t.Fatalf("unexpected replacement decision for %s", v.spec)
				}
			}
		})
	}
}

func TestRestartBackoffPreservesUnknownVolume(t *testing.T) {
	cfg := covRTConfig(t)
	var inspected map[string]any
	if err := json.Unmarshal([]byte(covRestartingInspect(covRuntimeRef)), &inspected); err != nil {
		t.Fatal(err)
	}
	inspected["Mounts"] = []map[string]string{{"Type": "volume", "Name": "private", "Destination": "/unknown"}}
	data, err := json.Marshal(inspected)
	if err != nil {
		t.Fatal(err)
	}
	f := &covRT{listBody: covExistingList(string(container.StateRestarting)), inspectBody: string(data)}
	p := f.provider(t, cfg)
	_, err = p.EnsureCrewRuntime(t.Context(), covTeam())
	if !errors.Is(err, provider.ErrRuntimeStorageUnverified) || len(f.stops) != 0 || len(f.deletes) != 0 {
		t.Fatalf("err=%v stops=%v deletes=%v", err, f.stops, f.deletes)
	}
}

func TestPodmanSELinuxRuntimeContractHasDataBinds(t *testing.T) {
	f := &covRT{}
	p := f.provider(t, covRTConfig(t))
	p.detected.Runtime = "podman"
	p.selinuxEnabled = true
	if p.crewRuntimeContractDigest() == "" {
		t.Fatal("missing Podman runtime contract")
	}
	_, host := contractSpec(t, p)
	for _, target := range []string{"/output", "/workspace", "/crew"} {
		found := false
		for _, spec := range host.Binds {
			if strings.HasSuffix(spec, ":"+target+":rw,z,noexec,nosuid") {
				found = true
			}
		}
		if !found {
			t.Fatalf("contract missing %s: %v", target, host.Binds)
		}
	}
}
