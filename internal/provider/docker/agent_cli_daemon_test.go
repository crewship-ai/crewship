package docker

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"

	"github.com/crewship-ai/crewship/internal/buildinfo"
	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
)

// Only stateful UNIT fixtures use these declared Docker responses. Real
// executing-inode and real-container qualification have independent tests.
type agentSDKFake struct {
	t        *testing.T
	artifact *runtimestage.AgentCLIArtifact
	commit   string
	fault    string
}

func (s *agentSDKFake) serve(w http.ResponseWriter, r *http.Request, d *sidecarFakeDaemon) bool {
	uri := r.URL.Path
	reply := func(value any) {
		if data, ok := value.(map[string]any); ok && strings.Contains(uri, "/containers/") && strings.HasSuffix(uri, "/json") {
			host := data["HostConfig"].(map[string]any)
			if s.fault == "privileged" {
				host["Privileged"] = true
			}
			if s.fault == "capability" {
				host["CapAdd"] = []string{"SYS_ADMIN"}
			}
			if s.fault == "missing-no-new-privileges" {
				host["SecurityOpt"] = []string{}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(value)
	}
	switch {
	case strings.Contains(uri, "/containers/") && strings.HasSuffix(uri, "/json"):
		c := d.containers[extractContainerID(uri)]
		if c == nil || c.labels["crewship.agent-cli-sha256"] == "" {
			return false
		}
		mounts := []map[string]any{{"Type": "bind", "Source": s.artifact.Path, "Destination": "/usr/local/bin/crewship", "RW": s.fault == "writable-sdk"}}
		if s.fault == "writable-sdk-alias" {
			mounts = append(mounts, map[string]any{"Type": "bind", "Source": s.artifact.Path, "Destination": "/tmp/writable-sdk", "RW": true})
		}
		for _, target := range []string{"/workspace", "/output", "/crew", "/home/agent", "/opt/crew-tools"} {
			name := "fixture-sdk-" + strings.ReplaceAll(target, "/", "-")
			mounts = append(mounts, map[string]any{"Type": "volume", "Name": name, "Source": "/var/lib/docker/volumes/" + name + "/_data", "Destination": target, "RW": true})
		}
		reply(map[string]any{"Id": c.id, "Name": "/" + c.name, "Image": "sha256:" + strings.Repeat("a", 64), "State": map[string]any{"Status": c.status, "Running": c.status == "running"}, "Config": map[string]any{"Image": "default/runtime:latest", "User": "1001:1001", "Labels": c.labels}, "HostConfig": map[string]any{"ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges"}, "Tmpfs": map[string]string{"/secrets": secretsTmpfsSpec}}, "Mounts": mounts})
		return true
	case r.Method == http.MethodHead && strings.HasSuffix(uri, "/archive"):
		target := r.URL.Query().Get("path")
		mode := os.ModeDir | 0755
		if target == "/usr/local/bin/crewship" {
			mode = 0555
		}
		stat := container.PathStat{Name: path.Base(target), Mode: mode}
		if s.fault == "parent-alias" && target == "/usr/local/bin" {
			stat.LinkTarget = "/home/agent"
		}
		raw, _ := json.Marshal(stat)
		w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(raw))
		w.WriteHeader(http.StatusOK)
		return true
	case strings.Contains(uri, "/volumes/fixture-sdk-"):
		name := path.Base(uri)
		volume := map[string]any{"Name": name, "Driver": "local", "Mountpoint": "/var/lib/docker/volumes/" + name + "/_data"}
		if s.fault == "writable-staging-volume" {
			volume["Options"] = map[string]string{"type": "none", "o": "bind", "device": filepath.Dir(s.artifact.Path)}
		}
		if s.fault == "writable-sdk-volume-alias" {
			volume["Options"] = map[string]string{"type": "none", "o": "bind", "device": s.artifact.Path}
		}
		reply(volume)
		return true
	case strings.Contains(uri, "/images/") && strings.HasSuffix(uri, "/json"):
		arch := s.artifact.Arch
		if s.fault == "image-architecture" {
			if arch == "amd64" {
				arch = "arm64"
			} else {
				arch = "amd64"
			}
		}
		reply(map[string]any{"Id": "sha256:" + strings.Repeat("a", 64), "Os": "linux", "Architecture": arch, "Config": map[string]any{"Env": []string{}}, "RepoDigests": []string{}})
		return true
	case strings.HasSuffix(uri, "/exec") && r.Method == http.MethodPost:
		var body struct {
			Cmd []string `json:"Cmd"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			return false
		}
		if len(body.Cmd) == 2 && body.Cmd[0] == "/usr/local/bin/crewship" && body.Cmd[1] == "agent-runtime-probe" {
			reply(map[string]any{"Id": "fixture-sdk-exec"})
			return true
		}
	case strings.HasSuffix(uri, "/exec/fixture-sdk-exec/start"):
		raw, _ := json.Marshal(buildinfo.AgentRuntimeProbe{Protocol: 1, SHA256: s.artifact.SHA256, Version: "fixture", Commit: s.commit, OS: "linux", Arch: s.artifact.Arch, Commands: []string{"commands", "version"}})
		if s.fault == "damaged-probe" {
			raw = []byte(`{"protocol":`)
		}
		covHijackUpgrade(s.t, w, r, covStdcopyFrame(1, string(raw)))
		return true
	case strings.HasSuffix(uri, "/exec/fixture-sdk-exec/json"):
		reply(map[string]any{"Running": false, "ExitCode": 0})
		return true
	}
	return false
}
