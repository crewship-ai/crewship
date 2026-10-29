package mooring

import (
	"strings"
	"testing"
)

func TestRuntimeReadyRejectsAmbiguousAndOpenShapes(t *testing.T) {
	base := `{"type":"runtime_ready","request_id":"host-start","selected":{"protocol_version":"0.4.0","model_version":"1.0.0","engine_version":"0.1.0"},"selected_control_profile":"mooring.prepare.v1"}`
	versions := RuntimeVersions{"0.4.0", "1.0.0", "0.1.0"}
	if !validateRuntimeReady([]byte(base), versions, PrivatePrepareProfile) {
		t.Fatal("valid private readiness rejected")
	}
	for name, raw := range map[string]string{
		"duplicate profile": strings.TrimSuffix(base, "}") + `,"selected_control_profile":"mooring.prepare.v1"}`,
		"escaped duplicate": strings.TrimSuffix(base, "}") + `,"\u0074ype":"runtime_ready"}`,
		"unknown outer":     strings.TrimSuffix(base, "}") + `,"extension":true}`,
		"case variant":      strings.Replace(base, `"type"`, `"Type"`, 1),
		"nested duplicate":  strings.Replace(base, `"model_version":"1.0.0"`, `"model_version":"1.0.0","model_version":"1.0.0"`, 1),
		"unknown nested":    strings.Replace(base, `"model_version":"1.0.0"`, `"model_version":"1.0.0","extension":false`, 1),
		"missing field":     strings.Replace(base, `,"engine_version":"0.1.0"`, "", 1),
		"invalid version":   strings.Replace(base, `"0.1.0"`, `"0.01.0"`, 1),
		"surrogate":         strings.Replace(base, `"0.1.0"`, `"\ud800"`, 1),
		"trailing":          base + `{}`,
		"wrong value kind":  strings.Replace(base, `"request_id":"host-start"`, `"request_id":{}`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if validateRuntimeReady([]byte(raw), versions, PrivatePrepareProfile) {
				t.Fatal("invalid or ambiguous readiness accepted")
			}
		})
	}
	old := strings.Replace(base, `,"selected_control_profile":"mooring.prepare.v1"`, "", 1)
	if !validateRuntimeReady([]byte(old), versions, "") || validateRuntimeReady([]byte(base), versions, "") || validateRuntimeReady([]byte(old), versions, PrivatePrepareProfile) {
		t.Fatal("default and private negotiation were confused")
	}
}
