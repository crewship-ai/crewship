package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// The runtime privilege audit names every security-relevant field a crew
// configuration save can move (old/new are both built from this), and none
// of the private ones (environment values, commands, image/registry).
func TestCrewPrivilegeAuditRecordsSecurityFields(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      []string
	}{
		{"empty", ``, []string{`"privileged":false`, `"cap_add":[]`, `"init":false`, `"mounts":[]`}},
		{"full", `{"image":"registry.internal.invalid/private:1","privileged":true,"init":true,
			"capAdd":["net_bind_service"],"containerEnv":{"API_TOKEN":"do-not-log"},"postStartCommand":"echo do-not-log",
			"mounts":[{"source":"crew-cache","target":"/cache","type":"volume"}]}`,
			[]string{`"privileged":true`, `"cap_add":["NET_BIND_SERVICE"]`, `"init":true`,
				`"mounts":[{"source":"crew-cache","target":"/cache","type":"volume"}]`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(crewPrivilegeAudit(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(string(raw), want) {
					t.Errorf("privilege audit missing %s: %s", want, raw)
				}
			}
			for _, forbidden := range []string{"do-not-log", "API_TOKEN", "registry.internal.invalid"} {
				if strings.Contains(string(raw), forbidden) {
					t.Errorf("privilege audit leaks %s: %s", forbidden, raw)
				}
			}
		})
	}
}
