//go:build normal_template_tag_probe

package testutil

import (
	"strings"
	"testing"
)

func TestNormalTemplateHelperPreservesExplicitTestBuildTags(t *testing.T) {
	plan, reason, err := normalTemplatePlan()
	if err != nil || reason != "" {
		t.Fatalf("helper plan unavailable: %q %v", reason, err)
	}
	if !strings.Contains(plan.tags, "normal_template_tag_probe") {
		t.Fatal("running toolchain did not report explicit test build tags; cannot silently drop them")
	}
}
