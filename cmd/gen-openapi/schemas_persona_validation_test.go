package main

import (
	"regexp"
	"strings"
	"testing"
)

func TestPersonaRequestRejectsEmptyContent(t *testing.T) {
	doc := loadSpec(t)
	components := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"RemainingAgentPersonaRequestV1", "RemainingCrewPersonaRequestV1"} {
		schema := components[name].(map[string]any)
		required, _ := schema["required"].([]any)
		if len(required) != 1 || required[0] != "content" {
			t.Fatalf("%s must require content: %v", name, required)
		}
		content := schema["properties"].(map[string]any)["content"].(map[string]any)
		if content["minLength"] != float64(1) {
			t.Fatalf("%s minLength=%v", name, content["minLength"])
		}
		pattern := regexp.MustCompile(content["pattern"].(string))
		for _, input := range []string{"", " \t\r\n\v\f", "\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000", "\ufeff", "a", " \nHi\t", "\u200b"} {
			if got, want := pattern.MatchString(input), strings.TrimSpace(input) != ""; got != want {
				t.Errorf("%s rejects/accepts %q incorrectly: got=%v want=%v", name, input, got, want)
			}
		}
	}
	for _, path := range []string{"/api/v1/agents/{agentId}/persona", "/api/v1/crews/{crewId}/persona"} {
		content := errorContentOf(t, doc, path, "put", "400")
		if _, ok := content["application/json"]; !ok {
			t.Fatalf("%s 400 must use application/json", path)
		}
	}
}
