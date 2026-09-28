//go:build linux

package restrictedruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAuditRejectsBroadenedDaemonConfiguration(t *testing.T) {
	for _, name := range []string{"valid", "host network", "shared pid", "shared ipc", "privileged", "writable root", "capability", "supplementary group", "host gateway", "device", "unbounded memory", "unexpected mount", "unbounded logs", "privilege escalation", "foreign tmpfs owner", "unbounded tmpfs"} {
		t.Run(name, func(t *testing.T) {
			p := testPlan()
			p.Mounts = nil
			l := Limits{128 << 20, 500000000, 48}
			h := map[string]any{"NetworkMode": "none", "IpcMode": "private", "ReadonlyRootfs": true, "Init": true, "Memory": l.MemoryBytes, "MemorySwap": l.MemoryBytes, "NanoCpus": l.NanoCPUs, "PidsLimit": l.PIDs, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges"}, "RestartPolicy": map[string]string{"Name": "no"}, "LogConfig": map[string]string{"Type": "none"}, "Tmpfs": privateTmpfs()}
			row := map[string]any{"Config": map[string]any{"User": "1001:1001", "Entrypoint": []string{"/opt/crewship-runner"}, "Cmd": []string{"hold"}}, "HostConfig": h}
			switch name {
			case "host network":
				h["NetworkMode"] = "host"
			case "shared pid":
				h["PidMode"] = "container:foreign"
			case "shared ipc":
				h["IpcMode"] = "host"
			case "privileged":
				h["Privileged"] = true
			case "writable root":
				h["ReadonlyRootfs"] = false
			case "capability":
				h["CapAdd"] = []string{"SYS_ADMIN"}
			case "supplementary group":
				h["GroupAdd"] = []string{"1002"}
			case "host gateway":
				h["ExtraHosts"] = []string{"host.docker.internal:host-gateway"}
			case "device":
				h["Devices"] = []string{"disk"}
			case "unbounded memory":
				h["Memory"] = 0
			case "unexpected mount":
				row["Mounts"] = []map[string]any{{"Type": "bind", "Destination": "/other", "RW": true}}
			case "privilege escalation":
				h["SecurityOpt"] = []string{"no-new-privileges=false"}
			case "foreign tmpfs owner":
				h["Tmpfs"].(map[string]string)["/broker"] = "rw,uid=1001,mode=0700"
			case "unbounded tmpfs":
				h["Tmpfs"].(map[string]string)["/tmp"] = "rw,mode=1777"
			case "unbounded logs":
				h["LogConfig"] = map[string]string{"Type": "json-file"}
			}
			data, _ := json.Marshal([]any{row})
			dir := t.TempDir()
			response := filepath.Join(dir, "inspect.json")
			if e := os.WriteFile(response, data, 0600); e != nil {
				t.Fatal(e)
			}
			bin := filepath.Join(dir, "docker")
			if e := os.WriteFile(bin, []byte("#!/bin/sh\ncat '"+response+"'\n"), 0700); e != nil {
				t.Fatal(e)
			}
			err := (Docker{Binary: bin}).audit(context.Background(), "id", p, catalogMap{}, l)
			if (err == nil) != (name == "valid") {
				t.Fatalf("audit result: %v", err)
			}
		})
	}
}
