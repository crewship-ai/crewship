package main

import (
	"fmt"
	"strings"
	"testing"
)

func credDef(types ...string) map[string]interface{} {
	raw := make([]interface{}, len(types))
	for i, ty := range types {
		raw[i] = map[string]interface{}{"type": ty}
	}
	return map[string]interface{}{"credentials_required": raw}
}

func TestCheckCredentialsRequired_NoneDeclaredOK(t *testing.T) {
	got := checkCredentialsRequired(&fakeDoctorGetter{}, "ws", map[string]interface{}{})
	if len(got) != 1 || got[0].Level != doctorOK || !strings.Contains(got[0].Message, "no credentials declared") {
		t.Errorf("got %+v", got)
	}
}

func TestCheckCredentialsRequired_FetchFailureWarns(t *testing.T) {
	got := checkCredentialsRequired(&fakeDoctorGetter{err: fmt.Errorf("down")}, "ws", credDef("ANTHROPIC"))
	if len(got) != 1 || got[0].Level != doctorWarn || !strings.Contains(got[0].Message, "could not fetch workspace credentials") {
		t.Errorf("got %+v", got)
	}
}

func TestCheckCredentialsRequired_MatchAndMiss(t *testing.T) {
	g := &fakeDoctorGetter{status: 200, body: `[
		{"provider":"anthropic","type":"SECRET","status":"ACTIVE"},
		{"provider":"github","type":"","status":"REVOKED"}
	]`}
	got := checkCredentialsRequired(g, "ws", credDef("anthropic", "GITHUB"))
	if len(got) != 2 {
		t.Fatalf("want 2 checks, got %+v", got)
	}
	byName := map[string]doctorCheck{}
	for _, c := range got {
		byName[c.Name] = c
	}
	// anthropic is ACTIVE (provider match, case-insensitive) → OK.
	if c := byName["credential:ANTHROPIC"]; c.Level != doctorOK {
		t.Errorf("anthropic should match: %+v", c)
	}
	// github cred is REVOKED → not in the active set → FAIL with hint.
	c := byName["credential:GITHUB"]
	if c.Level != doctorFail {
		t.Errorf("github should fail: %+v", c)
	}
	if !strings.Contains(c.Hint, "crewship credential create --type=GITHUB") {
		t.Errorf("hint should include create command: %q", c.Hint)
	}
}

func TestCheckCredentialsRequired_TypeFieldAlsoMatches(t *testing.T) {
	g := &fakeDoctorGetter{status: 200, body: `[
		{"provider":"","type":"SLACK_BOT","status":"ACTIVE"}
	]`}
	got := checkCredentialsRequired(g, "ws", credDef("slack_bot"))
	if len(got) != 1 || got[0].Level != doctorOK {
		t.Errorf("type-field match failed: %+v", got)
	}
}

func TestCheckCredentialsRequired_OnlyInvalidEntriesWarns(t *testing.T) {
	g := &fakeDoctorGetter{status: 200, body: `[]`}
	// Entries with empty type or wrong shape are skipped — nothing
	// checkable remains → the declared-but-unverifiable warning.
	def := map[string]interface{}{"credentials_required": []interface{}{
		map[string]interface{}{"type": ""},
		"not-a-map",
	}}
	got := checkCredentialsRequired(g, "ws", def)
	if len(got) != 1 || got[0].Level != doctorWarn || !strings.Contains(got[0].Message, "no valid credential types") {
		t.Errorf("got %+v", got)
	}
}

func TestFetchActiveCredentialTypes_Non200ReturnsNil(t *testing.T) {
	g := &fakeDoctorGetter{status: 403, body: `{"error":"denied"}`}
	if got := fetchActiveCredentialTypes(g, "ws"); got != nil {
		t.Errorf("expected nil on 403, got %v", got)
	}
}

func TestFetchActiveCredentialTypes_BadJSONReturnsNil(t *testing.T) {
	g := &fakeDoctorGetter{status: 200, body: `{`}
	if got := fetchActiveCredentialTypes(g, "ws"); got != nil {
		t.Errorf("expected nil on decode failure, got %v", got)
	}
}

func TestFetchActiveCredentialTypes_FiltersInactive(t *testing.T) {
	g := &fakeDoctorGetter{status: 200, body: `[
		{"provider":"anthropic","type":"SECRET","status":"ACTIVE"},
		{"provider":"openai","type":"","status":"DISABLED"}
	]`}
	got := fetchActiveCredentialTypes(g, "ws")
	if _, ok := got["ANTHROPIC"]; !ok {
		t.Errorf("active provider missing: %v", got)
	}
	if _, ok := got["SECRET"]; !ok {
		t.Errorf("active type missing: %v", got)
	}
	if _, ok := got["OPENAI"]; ok {
		t.Errorf("inactive provider must be excluded: %v", got)
	}
}
