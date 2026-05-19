package manifest

import (
	"strings"
	"testing"
)

func TestMarshalDocument_DoesNotMutateCallersSpec(t *testing.T) {
	// MarshalDocument receives Document by value, but Document.Spec
	// is a pointer. Clearing fields directly through that pointer
	// would permanently strip credentials and skill bodies from the
	// caller's in-memory manifest. Regression for the round-3
	// review finding.
	originalCreds := []Credential{
		{EnvVar: "MY_KEY", Provider: "ANTHROPIC", Type: "API_KEY"},
	}
	originalSkills := []Skill{
		{Slug: "x", Inline: "---\nname: x\ndescription: y\n---\nbody"},
	}
	doc := Document{
		APIVersion: APIVersion,
		Kind:       KindCrew,
		Metadata:   Metadata{Name: "T", Slug: "t"},
		Spec: &CrewSpec{
			Credentials: originalCreds,
			Skills:      originalSkills,
			Agents:      []Agent{{Slug: "a", Name: "A", AgentRole: "LEAD", Prompt: "x"}},
		},
	}

	out, err := MarshalDocument(doc, ExportOptions{
		IncludeCredentials: false,
		IncludeSkillBodies: false,
	})
	if err != nil {
		t.Fatalf("MarshalDocument: %v", err)
	}
	if strings.Contains(out, "MY_KEY") {
		t.Error("credentials should be stripped when IncludeCredentials=false")
	}
	if strings.Contains(out, "body") {
		t.Error("skill body should be stripped when IncludeSkillBodies=false")
	}

	// The caller's spec must be untouched.
	if len(doc.Spec.Credentials) != 1 || doc.Spec.Credentials[0].EnvVar != "MY_KEY" {
		t.Errorf("MarshalDocument mutated caller's credentials: %+v", doc.Spec.Credentials)
	}
	if len(doc.Spec.Skills) != 1 || doc.Spec.Skills[0].Inline == "" {
		t.Errorf("MarshalDocument mutated caller's skill body: %+v", doc.Spec.Skills)
	}
}
