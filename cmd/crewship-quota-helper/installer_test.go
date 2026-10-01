//go:build linux

package main

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

const installer = "../../scripts/install-quota-helper.sh"

func readInstaller(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(installer)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// dropIn returns the server drop-in heredoc the installer writes.
func dropIn(t *testing.T, script string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)quota-helper\.conf" <<UNIT\n(.*?)\nUNIT\n`).FindStringSubmatch(script)
	if m == nil {
		t.Fatal("server drop-in heredoc not found")
	}
	return m[1]
}

func TestInstallerServerDropIn(t *testing.T) {
	unit := dropIn(t, readInstaller(t))
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		// Wants=: a helper failure must not stop the whole server; quota
		// services fail closed on their own when the socket is missing.
		{"Wants=crewship-quota-helper@$INSTANCE.service", strings.Contains(unit, "Wants=crewship-quota-helper@$INSTANCE.service")},
		{"no Requires=", !strings.Contains(unit, "Requires=")},
		{"After= ordering kept", strings.Contains(unit, "After=crewship-quota-helper@$INSTANCE.service")},
		// The container prefix names every existing container and volume;
		// changing it at the next restart would orphan all of them.
		{"no CREWSHIP_CONTAINER_PREFIX", !strings.Contains(unit, "CREWSHIP_CONTAINER_PREFIX")},
		{"helper socket configured", strings.Contains(unit, "CREWSHIP_QUOTA_HELPER_SOCKET=/run/crewship-quota/$INSTANCE/helper.sock")},
		{"helper namespace configured", strings.Contains(unit, "CREWSHIP_QUOTA_HELPER_NAMESPACE=$INSTANCE")},
	} {
		if !tc.ok {
			t.Errorf("drop-in: %s\n%s", tc.name, unit)
		}
	}
}

func TestInstallerAcceptsTemplateServerUnits(t *testing.T) {
	script := readInstaller(t)
	m := regexp.MustCompile(`(?m)^SERVER_UNIT_RE='([^']*)'$`).FindStringSubmatch(script)
	if m == nil {
		t.Fatal("SERVER_UNIT_RE not declared")
	}
	for _, tc := range []struct {
		unit string
		ok   bool
	}{
		{"crewship.service", true},
		{"crewship-1.service", true},
		{"crewship-ws@3.service", true},
		{"crewship-ws@stage_1.service", true},
		{"crewship-ws@.service", false},
		{"../evil.service", false},
		{"crewship.service.d", false},
		{"crew ship.service", false},
		{"crewship-ws@3.service;rm", false},
	} {
		err := exec.Command("bash", "-c", `[[ $1 =~ $2 ]]`, "_", tc.unit, m[1]).Run()
		if (err == nil) != tc.ok {
			t.Errorf("unit %q accepted=%v, want %v", tc.unit, err == nil, tc.ok)
		}
	}
}

func TestInstallerChecksPython(t *testing.T) {
	script := readInstaller(t)
	check := strings.Index(script, "python3 ")
	use := strings.Index(script, "python3 -")
	if check < 0 || use < 0 || !regexp.MustCompile(`for tool in [^;]*\bpython3\b`).MatchString(script) {
		t.Fatal("installer uses python3 without checking for it")
	}
}

// sharedInstallHarness runs the installer's shared-artifact block against
// a scratch directory. install and systemctl are shimmed: the test is not
// root, and daemon-reload must not touch the host.
func sharedInstallHarness(t *testing.T, body string) (string, error) {
	t.Helper()
	m := regexp.MustCompile(`(?s)# BEGIN shared-install\n(.*?)# END shared-install\n`).FindStringSubmatch(readInstaller(t))
	if m == nil {
		t.Fatal("shared-install block not found")
	}
	dir := t.TempDir()
	harness := `set -Eeuo pipefail
install() { local a=(); while (($#)); do case "$1" in -o|-g) shift 2;; *) a+=("$1"); shift;; esac; done; command install "${a[@]}"; }
systemctl() { echo "systemctl $*" >> "$D/systemctl.log"; }
` + m[1] + body
	cmd := exec.Command("bash", "-c", harness)
	cmd.Env = append(os.Environ(), "D="+dir)
	out, err := cmd.CombinedOutput()
	t.Logf("%s", out)
	return dir, err
}

func readOr(path, missing string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return missing
	}
	return string(raw)
}

// The helper binary and its unit are shared by every instance on the host.
// A failure part-way must leave the previous pair in place, never a new
// binary under an old unit or the reverse.
func TestInstallerSharedArtifactsAreAtomic(t *testing.T) {
	const setup = `mkdir -p "$D/bin" "$D/unit"; echo old-bin > "$D/bin/helper"; echo old-unit > "$D/unit/helper.service"
echo new-bin > "$D/new-bin"; echo new-unit > "$D/new-unit"
`
	tests := []struct {
		name              string
		body              string
		wantErr           bool
		wantBin, wantUnit string
	}{
		{name: "success replaces both and leaves no backups",
			body:    setup + `arm_shared_rollback; install_shared "$D/new-bin" "$D/bin/helper" 0755 "$D/new-unit" "$D/unit/helper.service" 0644; commit_shared` + "\n",
			wantBin: "new-bin\n", wantUnit: "new-unit\n"},
		{name: "failed staging changes nothing",
			body:    setup + `arm_shared_rollback; install_shared "$D/new-bin" "$D/bin/helper" 0755 "$D/missing" "$D/unit/helper.service" 0644; commit_shared` + "\n",
			wantErr: true, wantBin: "old-bin\n", wantUnit: "old-unit\n"},
		{name: "failure after replacement restores the previous pair",
			body:    setup + `arm_shared_rollback; install_shared "$D/new-bin" "$D/bin/helper" 0755 "$D/new-unit" "$D/unit/helper.service" 0644; false; commit_shared` + "\n",
			wantErr: true, wantBin: "old-bin\n", wantUnit: "old-unit\n"},
		{name: "failure on a fresh host removes the new files",
			body: `mkdir -p "$D/bin" "$D/unit"; echo new-bin > "$D/new-bin"; echo new-unit > "$D/new-unit"
arm_shared_rollback; install_shared "$D/new-bin" "$D/bin/helper" 0755 "$D/new-unit" "$D/unit/helper.service" 0644; false` + "\n",
			wantErr: true, wantBin: "absent", wantUnit: "absent"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := sharedInstallHarness(t, tc.body)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got := readOr(dir+"/bin/helper", "absent"); got != tc.wantBin {
				t.Errorf("binary = %q, want %q", got, tc.wantBin)
			}
			if got := readOr(dir+"/unit/helper.service", "absent"); got != tc.wantUnit {
				t.Errorf("unit = %q, want %q", got, tc.wantUnit)
			}
			for _, sub := range []string{"bin", "unit"} {
				entries, _ := os.ReadDir(dir + "/" + sub)
				for _, e := range entries {
					if e.Name() != "helper" && e.Name() != "helper.service" {
						t.Errorf("leftover %s/%s", sub, e.Name())
					}
				}
			}
			reloaded := strings.Contains(readOr(dir+"/systemctl.log", ""), "daemon-reload")
			if tc.wantErr && !reloaded {
				t.Error("rollback did not reload systemd after restoring the shared files")
			}
		})
	}
}
