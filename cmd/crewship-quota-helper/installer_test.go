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
