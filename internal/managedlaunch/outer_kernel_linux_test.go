//go:build linux

package managedlaunch

import (
	"strings"
	"testing"
)

func TestCodexOuterKernelControlsFailClosed(t *testing.T) {
	good := "NoNewPrivs:\t1\nSeccomp:\t2\nCapInh:\t0000000000000000\nCapPrm:\t0000000000000000\nCapEff:\t0000000000000000\nCapBnd:\t0000000000000000\nCapAmb:\t0000000000000000\n"
	if err := validateOuterKernelControls(1001, 1001, 1001, 1001, good); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"NoNewPrivs", "Seccomp", "CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		t.Run(field, func(t *testing.T) {
			lines := strings.Split(good, "\n")
			bad := append([]string(nil), lines...)
			for i, line := range bad {
				if strings.HasPrefix(line, field+":") {
					bad[i] = field + ": 0"
					if strings.HasPrefix(field, "Cap") {
						bad[i] = field + ": 0000000000000001"
					}
				}
			}
			if err := validateOuterKernelControls(1001, 1001, 1001, 1001, strings.Join(bad, "\n")); err == nil {
				t.Fatal("accepted missing control")
			}
			filtered := []string{}
			for _, line := range lines {
				if !strings.HasPrefix(line, field+":") {
					filtered = append(filtered, line)
				}
			}
			if err := validateOuterKernelControls(1001, 1001, 1001, 1001, strings.Join(filtered, "\n")); err == nil {
				t.Fatal("accepted missing field")
			}
			for _, extra := range []string{field + ": 0\n", field + ": malformed\n"} {
				if err := validateOuterKernelControls(1001, 1001, 1001, 1001, good+extra); err == nil {
					t.Fatal("accepted duplicate field")
				}
			}
		})
	}
	for _, ids := range [][4]int{{0, 1001, 1001, 1001}, {1001, 0, 1001, 1001}, {1001, 1001, 0, 1001}, {1001, 1001, 1001, 0}} {
		if err := validateOuterKernelControls(ids[0], ids[1], ids[2], ids[3], good); err == nil {
			t.Fatal("accepted wrong process identity")
		}
	}
}

func TestCodexOuterToolPathDoesNotSelectMainExecutableOrHooks(t *testing.T) {
	d := Descriptor{OuterSandbox: true, EnvKeys: []string{"HOME", "MISE_DATA_DIR"}}
	env, err := SelectedEnvironment(d, []string{"PATH=/home/agent/.local/bin:.", "HOME=/fixture", "MISE_DATA_DIR=/opt/mise/data", "LD_PRELOAD=/malicious", "BASH_ENV=/malicious", "ENV=/malicious"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if !strings.HasPrefix(joined, "PATH="+OuterToolPath+"\n") || strings.Contains(joined, "malicious") || strings.Contains(joined, "/home/agent/.local/bin") {
		t.Fatal("unsafe outer child environment")
	}
	d.OuterSandbox = false
	env, err = SelectedEnvironment(d, []string{"HOME=/fixture", "MISE_DATA_DIR=/opt/mise/data"})
	if err != nil || env[0] != "PATH="+SafePath {
		t.Fatal("legacy managed pilot path changed")
	}
}
