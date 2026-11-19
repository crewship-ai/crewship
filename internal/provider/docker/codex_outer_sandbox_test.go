package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

func TestCodexOuterRuntimeRejectsMissingControls(t *testing.T) {
	for _, which := range []string{"valid", "uid", "owner", "crew", "rootfs", "privileged", "capdrop", "capadd", "devices", "nnp", "seccomp-unconfined", "apparmor", "pid", "ipc", "network", "userns", "uts", "cgroupns", "volumes-from", "extra-bind", "extra-volume", "tmpfs", "missing-mount", "missing-secrets", "missing-tmp", "unsafe-secrets", "volume-device", "volume-options", "volume-owner", "volume-driver", "wrong-image", "not-running", "agent-cli", "agent-cli-unconfigured", "agent-cli-missing", "agent-cli-rw", "agent-cli-source", "agent-cli-duplicate"} {
		t.Run(which, func(t *testing.T) {
			root := t.TempDir()
			crew := "outer-crew"
			slug := "fixture"
			instance := "fixture-install"
			image := "sha256:" + strings.Repeat("a", 64)
			for _, parts := range [][]string{{crew}, {"workspaces", crew}, {"crews", crew}} {
				if err := os.MkdirAll(filepath.Join(append([]string{root}, parts...)...), 0700); err != nil {
					t.Fatal(err)
				}
			}
			labels := map[string]string{crewCrewIDLabel: crew, crewCrewLabel: slug, crewKindLabel: crewRuntimeKind, resourcelifecycle.InstanceLabel: instance}
			config := map[string]any{"User": "1001:1001", "Labels": labels}
			host := map[string]any{"ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges"}, "Tmpfs": map[string]string{"/tmp": "rw", "/secrets": secretsTmpfsSpec}}
			state := map[string]any{"Running": true}
			metadata := map[string]any{"Id": "c1", "Image": image, "Config": config, "State": state, "HostConfig": host, "AppArmorProfile": "docker-default"}
			volumes := map[string]map[string]any{}
			mounts := []map[string]any{{"Type": "bind", "Source": "/trusted/launcher", "Destination": managedlaunch.LauncherPath, "RW": false}, {"Type": "bind", "Source": "/trusted/entrypoint", "Destination": "/usr/local/bin/entrypoint.sh", "RW": false}}
			var p *Provider
			p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/volumes/") {
					json.NewEncoder(w).Encode(volumes[filepath.Base(r.URL.Path)])
					return
				}
				metadata["Mounts"] = mounts
				json.NewEncoder(w).Encode(metadata)
			})
			defer close()
			p.cfg.InstanceID = instance
			p.cfg.OutputBasePath = root
			p.cfg.SidecarBinaryPath = "/trusted/launcher"
			p.cfg.EntrypointPath = "/trusted/entrypoint"
			for destination, parts := range map[string][]string{"/output": {crew}, "/workspace": {"workspaces", crew}, "/crew": {"crews", crew}} {
				name := strings.ReplaceAll(destination, "/", "") + "-volume"
				volumes[name] = map[string]any{"Name": name, "Driver": "local", "Labels": labels, "Options": map[string]string{"type": "none", "device": filepath.Join(append([]string{root}, parts...)...), "o": noexecBindMountOpts}}
				mounts = append(mounts, map[string]any{"Type": "volume", "Name": name, "Destination": destination, "RW": true})
			}
			for destination, name := range map[string]string{"/home/agent": p.homeVolumeName(crew, slug), "/opt/crew-tools": p.toolsVolumeName(crew, slug)} {
				volumes[name] = map[string]any{"Name": name, "Driver": "local"}
				mounts = append(mounts, map[string]any{"Type": "volume", "Name": name, "Destination": destination, "RW": true})
			}
			switch which {
			case "uid":
				config["User"] = "0:0"
			case "owner":
				labels[resourcelifecycle.InstanceLabel] = "other"
			case "crew":
				labels[crewCrewIDLabel] = "other"
			case "rootfs":
				host["ReadonlyRootfs"] = false
			case "privileged":
				host["Privileged"] = true
			case "capdrop":
				host["CapDrop"] = []string{}
			case "capadd":
				host["CapAdd"] = []string{"SYS_ADMIN"}
			case "devices":
				host["Devices"] = []map[string]any{{"PathOnHost": "/dev/sda", "PathInContainer": "/dev/sda", "CgroupPermissions": "rwm"}}
			case "nnp":
				host["SecurityOpt"] = []string{}
			case "seccomp-unconfined":
				host["SecurityOpt"] = []string{"no-new-privileges", "seccomp=unconfined"}
			case "apparmor":
				metadata["AppArmorProfile"] = "unconfined"
			case "pid":
				host["PidMode"] = "host"
			case "ipc":
				host["IpcMode"] = "host"
			case "network":
				host["NetworkMode"] = "host"
			case "userns":
				host["UsernsMode"] = "host"
			case "uts":
				host["UTSMode"] = "host"
			case "cgroupns":
				host["CgroupnsMode"] = "host"
			case "volumes-from":
				host["VolumesFrom"] = []string{"foreign"}
			case "extra-bind":
				mounts = append(mounts, map[string]any{"Type": "bind", "Source": "/var/run/docker.sock", "Destination": "/var/run/docker.sock", "RW": false})
			case "extra-volume":
				mounts = append(mounts, map[string]any{"Type": "volume", "Name": "foreign", "Destination": "/foreign", "RW": false})
				volumes["foreign"] = map[string]any{"Driver": "local"}
			case "tmpfs":
				host["Tmpfs"] = map[string]string{"/opt": "rw"}
			case "missing-secrets":
				delete(host["Tmpfs"].(map[string]string), "/secrets")
			case "missing-tmp":
				delete(host["Tmpfs"].(map[string]string), "/tmp")
			case "unsafe-secrets":
				host["Tmpfs"].(map[string]string)["/secrets"] = "rw,mode=0777"
			case "missing-mount":
				mounts = mounts[1:]
			case "volume-device":
				volumes["crew-volume"]["Options"].(map[string]string)["device"] = "/"
			case "volume-options":
				volumes["crew-volume"]["Options"].(map[string]string)["o"] = "bind"
			case "volume-owner":
				copyLabels := map[string]string{crewCrewIDLabel: "other", resourcelifecycle.InstanceLabel: instance}
				volumes["crew-volume"]["Labels"] = copyLabels
			case "volume-driver":
				volumes["crew-volume"]["Driver"] = "foreign"
			case "agent-cli":
				p.cfg.agentCLIArtifact = &runtimestage.AgentCLIArtifact{Path: "/trusted/agent-cli"}
				mounts = append(mounts, map[string]any{"Type": "bind", "Source": "/trusted/agent-cli", "Destination": "/usr/local/bin/crewship", "RW": false})
			case "agent-cli-unconfigured":
				mounts = append(mounts, map[string]any{"Type": "bind", "Source": "/trusted/agent-cli", "Destination": "/usr/local/bin/crewship", "RW": false})
			case "agent-cli-missing":
				p.cfg.agentCLIArtifact = &runtimestage.AgentCLIArtifact{Path: "/trusted/agent-cli"}
			case "agent-cli-rw":
				p.cfg.agentCLIArtifact = &runtimestage.AgentCLIArtifact{Path: "/trusted/agent-cli"}
				mounts = append(mounts, map[string]any{"Type": "bind", "Source": "/trusted/agent-cli", "Destination": "/usr/local/bin/crewship", "RW": true})
			case "agent-cli-source":
				p.cfg.agentCLIArtifact = &runtimestage.AgentCLIArtifact{Path: "/trusted/agent-cli"}
				mounts = append(mounts, map[string]any{"Type": "bind", "Source": "/tmp/forged", "Destination": "/usr/local/bin/crewship", "RW": false})
			case "agent-cli-duplicate":
				p.cfg.agentCLIArtifact = &runtimestage.AgentCLIArtifact{Path: "/trusted/agent-cli"}
				mounts = append(mounts, map[string]any{"Type": "bind", "Source": "/trusted/agent-cli", "Destination": "/usr/local/bin/crewship", "RW": false}, map[string]any{"Type": "bind", "Source": "/trusted/agent-cli", "Destination": "/usr/local/bin/crewship", "RW": false})
			case "wrong-image":
				metadata["Image"] = "sha256:" + strings.Repeat("b", 64)
			case "not-running":
				state["Running"] = false
			}
			d := managedlaunch.Descriptor{ImageID: image}
			err := p.attestCodexOuterMetadata(context.Background(), "c1", crew, d)
			if (err == nil) != (which == "valid" || which == "agent-cli") {
				t.Fatalf("%s qualification: %v", which, err)
			}
		})
	}
}
