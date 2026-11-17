//go:build !windows

package crewstart

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

type pathProbeContainer struct {
	provider.ContainerProvider
	path       string
	code       int
	running    bool
	err        error
	inspectErr error
	got        provider.ExecConfig
}

func (p *pathProbeContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	p.got = cfg
	if p.err != nil {
		return nil, p.err
	}
	return &provider.ExecResult{ExecID: "path-probe", Reader: io.NopCloser(strings.NewReader(p.path))}, nil
}
func (p *pathProbeContainer) ExecInspect(context.Context, string) (bool, int, error) {
	return p.running, p.code, p.inspectErr
}

func TestNativeToolExecEnvRepairsLegacyRuntime(t *testing.T) {
	old := "/opt/mise/data/shims:/opt/custom-feature/bin:/opt/mise/data/installs/glab/latest/bin:/usr/bin:/bin"
	p := &pathProbeContainer{path: old}
	input := []string{"TERM=xterm-256color", "HOME=/home/agent"}
	got, err := NativeToolExecEnv(context.Background(), p, "legacy-runtime", input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !reflect.DeepEqual(got[:2], input) {
		t.Fatalf("metadata or env isolation changed: %v", got)
	}
	path := strings.TrimPrefix(got[2], "PATH=")
	for _, dir := range []string{"/opt/crew-tools/bin", "/opt/mise/data/shims", "/opt/custom-feature/bin", "/opt/mise/data/installs/glab/latest/bin", "/usr/bin", "/bin"} {
		if strings.Count(":"+path+":", ":"+dir+":") != 1 {
			t.Errorf("lost or duplicated inherited/native path %s: %s", dir, path)
		}
	}
	if p.got.User != "1001:1001" || len(p.got.Env) != 0 || !reflect.DeepEqual(p.got.Cmd, []string{"/bin/sh", "-c", `printf '%s' "$PATH"`}) {
		t.Fatalf("probe must read PATH only, without a run env: %+v", p.got)
	}
	if len(input) != 2 {
		t.Fatal("mutated caller environment")
	}
}
func TestNativeToolExecEnvRejectsIncompleteProbe(t *testing.T) {
	for name, p := range map[string]*pathProbeContainer{
		"exec": {err: errors.New("transport")}, "inspect": {inspectErr: errors.New("inspect")},
		"running": {running: true}, "failed": {code: 1}, "oversize": {path: strings.Repeat("x", 32769)},
		"nul": {path: "/bin\x00"}, "multiple lines": {path: "/bin\nUNRELATED=value"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NativeToolExecEnv(context.Background(), p, "legacy", nil); err == nil {
				t.Fatal("accepted incomplete PATH probe")
			}
		})
	}
}

func TestNativeToolExecEnvLegacyVolumeExecutable(t *testing.T) {
	volume := t.TempDir()
	binary := "crewship-legacy-path-regression"
	if err := os.WriteFile(filepath.Join(volume, binary), []byte("#!/bin/sh\nprintf 'legacy-tool-v1\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	p := &pathProbeContainer{path: "/opt/mise/data/shims:/opt/feature/bin:/usr/bin:/bin"}
	env, err := NativeToolExecEnv(context.Background(), p, "old-runtime", []string{"TERM=xterm-256color", "HOME=/home/agent"})
	if err != nil {
		t.Fatal(err)
	}
	// Materialize only the volume mount in a temp directory; never write host
	// /opt. A missing shared crew-tools component must still fail lookup.
	for i, item := range env {
		if strings.HasPrefix(item, "PATH=") {
			parts := strings.Split(strings.TrimPrefix(item, "PATH="), ":")
			for j, part := range parts {
				if part == "/opt/crew-tools/bin" {
					parts[j] = volume
				}
			}
			env[i] = "PATH=" + strings.Join(parts, ":")
		}
	}
	cmd := exec.Command("/bin/sh", "-c", binary+" --version")
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != "legacy-tool-v1\n" {
		t.Fatalf("legacy volume tool: %v %q", err, out)
	}
}
