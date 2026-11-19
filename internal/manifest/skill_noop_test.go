package manifest

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/skills"
)

const legacyNoopSkillMD = "---\nname: probe\ndescription: Use when probing a host.\n---\n# Probe\n\nCheck reachability.\n"

// storedImportOf is the row a server holds after importing body.
func storedImportOf(t *testing.T, body string) *skills.StoredSkill {
	t.Helper()
	f, err := skills.ContentImportFields(body)
	if err != nil {
		t.Fatal(err)
	}
	ptr := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	return &skills.StoredSkill{
		Slug: f.Slug, Name: f.DisplayName, DisplayName: f.DisplayName, Description: ptr(f.Description),
		Version: f.Version, Author: ptr(f.Author), Category: f.Category, Icon: ptr(f.Icon),
		CredentialRequirements: ptr(f.CredentialRequirements), Tags: ptr(f.Tags), Content: ptr(f.Content),
		Vendor: ptr(f.Vendor), SPDXLicense: ptr(f.SPDXLicense), License: ptr(f.License),
		ScanStatus: f.ScanStatus, DescriptionQuality: ptr(f.DescriptionQuality),
	}
}

// #5334: the combined-manifest planner plans an existing, unchanged skill as
// unchanged with no write; a changed one as an update; a new one as a create.
func TestPlanSkill_UnchangedContentIsNoop(t *testing.T) {
	changed := strings.Replace(legacyNoopSkillMD, "Check reachability.", "Check reachability!", 1)
	cases := []struct {
		name   string
		skill  Skill
		stored *skills.StoredSkill
		found  bool
		want   Action
	}{
		{"new skill", Skill{Slug: "probe", Inline: legacyNoopSkillMD}, nil, false, ActionCreate},
		{"identical body", Skill{Slug: "probe", Inline: legacyNoopSkillMD}, storedImportOf(t, legacyNoopSkillMD), true, ActionUnchanged},
		{"one byte changed", Skill{Slug: "probe", Inline: changed}, storedImportOf(t, legacyNoopSkillMD), true, ActionUpdate},
		{"row unreadable", Skill{Slug: "probe", Inline: legacyNoopSkillMD}, nil, true, ActionUpdate},
		{"url source", Skill{Slug: "probe", Source: "https://example.com/SKILL.md"}, storedImportOf(t, legacyNoopSkillMD), true, ActionUpdate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pb := &planBuilder{plan: &Plan{}, client: &Client{}}
			pb.storedSkillFn = func(slug string) (*skills.StoredSkill, bool) {
				if slug != "probe" {
					t.Fatalf("looked up %q", slug)
				}
				return tc.stored, tc.found
			}
			s := tc.skill
			s.SetResolved(s.Inline)
			if err := pb.planSkill(context.Background(), &s, "workspace"); err != nil {
				t.Fatal(err)
			}
			if len(pb.plan.Items) != 1 || pb.plan.Items[0].Action != tc.want {
				t.Fatalf("want %v, got %+v", tc.want, pb.plan.Items)
			}
			if (pb.plan.Items[0].exec == nil) != (tc.want == ActionUnchanged) {
				t.Fatalf("%v item exec=%v", tc.want, pb.plan.Items[0].exec != nil)
			}
		})
	}
}
