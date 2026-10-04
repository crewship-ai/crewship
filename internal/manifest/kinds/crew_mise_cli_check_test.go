package kinds

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCrewMiseCLICheckRoundTrip(t *testing.T) {
	var doc CrewDocument
	if err := yaml.Unmarshal([]byte("spec:\n  mise:\n    ai_cli_check: required\n    tools:\n      codex: latest\n"), &doc); err != nil {
		t.Fatal(err)
	}
	raw, err := doc.miseJSON()
	if err != nil || !strings.Contains(raw, `"ai_cli_check":"required"`) {
		t.Fatalf("policy lost: %s %v", raw, err)
	}
	decoded, err := parseMiseConfigJSON(raw)
	if err != nil || decoded.AICLICheck != "required" {
		t.Fatalf("decode: %+v %v", decoded, err)
	}
	wire, err := yaml.Marshal(decoded)
	if err != nil || !strings.Contains(string(wire), "ai_cli_check: required") {
		t.Fatalf("export lost policy: %s %v", wire, err)
	}
	for _, bad := range []string{`{"ai_cli_check":true}`, `{"ai_cli_check":"unknown"}`} {
		if _, err := parseMiseConfigJSON(bad); err == nil {
			t.Fatal("invalid policy exported", bad)
		}
	}
	doc.Spec.Mise.AICLICheck = "unknown"
	if _, err := doc.miseJSON(); err == nil {
		t.Fatal("invalid policy accepted")
	}
}
