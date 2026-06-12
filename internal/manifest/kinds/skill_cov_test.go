package kinds

// Coverage-focused tests for skill.go: the previously-untested
// skillDocumentDiffers / equalNonEmpty diff helpers, sourceLabel,
// buildImportBody, the listSkills shape tolerance, and skillExec.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

func skillCovDoc() SkillDocument {
	return SkillDocument{
		APIVersion: skillAPIVersion,
		Kind:       skillKind,
		Metadata:   internalapi.Metadata{Name: "Research", Slug: "research"},
		Spec: SkillSpec{
			DisplayName: "Research",
			Category:    "analysis",
			Description: "Find things out",
			Icon:        "search",
		},
	}
}

func TestSkillCov_DocumentDiffers(t *testing.T) {
	t.Parallel()
	base := skillCovDoc()
	matching := &SkillRemote{
		Slug: "research", DisplayName: "Research",
		Description: "Find things out", Category: "analysis", Icon: "search",
	}
	if skillDocumentDiffers(&base, matching) {
		t.Error("matching decoration should not differ")
	}

	cases := []struct {
		name   string
		mutate func(*SkillRemote)
	}{
		{"display_name", func(r *SkillRemote) { r.DisplayName = "Other" }},
		{"description", func(r *SkillRemote) { r.Description = "Other" }},
		{"category", func(r *SkillRemote) { r.Category = "other" }},
		{"icon", func(r *SkillRemote) { r.Icon = "other" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := *matching
			tc.mutate(&r)
			if !skillDocumentDiffers(&base, &r) {
				t.Errorf("%s drift should differ", tc.name)
			}
		})
	}

	// DisplayName falls back to metadata.name when spec leaves it blank.
	noDisplay := base
	noDisplay.Spec.DisplayName = ""
	r := *matching
	r.DisplayName = "Research" // == metadata.name
	if skillDocumentDiffers(&noDisplay, &r) {
		t.Error("metadata.name fallback should match remote display_name")
	}

	// Blank declared decoration means "no opinion".
	blank := base
	blank.Spec.Category = ""
	blank.Spec.Icon = ""
	r2 := *matching
	r2.Category = "server-side"
	r2.Icon = "server-icon"
	if skillDocumentDiffers(&blank, &r2) {
		t.Error("blank declared fields should not flag drift")
	}
}

func TestSkillCov_EqualNonEmpty(t *testing.T) {
	t.Parallel()
	if !equalNonEmpty("", "anything") {
		t.Error("empty declared = no opinion")
	}
	if !equalNonEmpty("x", "x") {
		t.Error("equal values should match")
	}
	if equalNonEmpty("x", "y") {
		t.Error("different non-empty values should not match")
	}
}

func TestSkillCov_SourceLabel(t *testing.T) {
	t.Parallel()
	d := skillCovDoc()
	d.Spec.Inline = "body"
	if got := d.sourceLabel(); got != "inline body" {
		t.Errorf("inline: %q", got)
	}
	d.Spec.Inline = ""
	d.Spec.Path = "skills/research.md"
	if got := d.sourceLabel(); got != "path skills/research.md" {
		t.Errorf("path: %q", got)
	}
	d.Spec.Path = ""
	d.Spec.Source = "https://example.com/SKILL.md"
	if got := d.sourceLabel(); got != "url https://example.com/SKILL.md" {
		t.Errorf("source: %q", got)
	}
	d.Spec.Source = ""
	if got := d.sourceLabel(); got != "(no source)" {
		t.Errorf("none: %q", got)
	}
}

func TestSkillCov_BuildImportBody(t *testing.T) {
	t.Parallel()
	d := skillCovDoc()

	// source wins
	d.Spec.Source = "https://example.com/SKILL.md"
	d.Spec.AllowUnsafeLicense = true
	body, err := d.buildImportBody()
	if err != nil || body["url"] != "https://example.com/SKILL.md" || body["allow_unsafe_license"] != true {
		t.Fatalf("source: body=%v err=%v", body, err)
	}

	// resolved content
	d.Spec.Source = ""
	d.SetResolved("# SKILL")
	body, err = d.buildImportBody()
	if err != nil || body["content"] != "# SKILL" {
		t.Fatalf("content: body=%v err=%v", body, err)
	}

	// nothing → error
	d.SetResolved("")
	if _, err := d.buildImportBody(); err == nil || !strings.Contains(err.Error(), "no body to import") {
		t.Fatalf("none: got %v", err)
	}
}

func TestSkillCov_ListSkills(t *testing.T) {
	t.Parallel()
	path := "/api/v1/skills"

	t.Run("transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {err: errors.New("down")}})
		if _, err := listSkills(context.Background(), c); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("non-2xx", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {status: 500, body: "x"}})
		if _, err := listSkills(context.Background(), c); err == nil || !strings.Contains(err.Error(), "list skills") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("bad body", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {badBody: true}})
		if _, err := listSkills(context.Background(), c); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("empty body → nil", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: ""}})
		out, err := listSkills(context.Background(), c)
		if err != nil || out != nil {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
	t.Run("flat", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `[{"id":"s1","slug":"research"}]`}})
		out, err := listSkills(context.Background(), c)
		if err != nil || len(out) != 1 || out[0].Slug != "research" {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
	t.Run("wrapped", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `{"skills":[{"id":"s1","slug":"research"}]}`}})
		out, err := listSkills(context.Background(), c)
		if err != nil || len(out) != 1 || out[0].Slug != "research" {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
	t.Run("undecodable", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `42`}})
		if _, err := listSkills(context.Background(), c); err == nil || !strings.Contains(err.Error(), "decode /api/v1/skills") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestSkillCov_SkillExec(t *testing.T) {
	t.Parallel()
	c := newCovClient(map[string]covRoute{
		"POST /x":   {status: 201, body: `{}`},
		"PATCH /x":  {status: 200, body: `{}`},
		"PUT /x":    {status: 200, body: `{}`},
		"DELETE /x": {status: 204},
		"POST /bad": {status: 422, body: "rejected"},
		"POST /err": {err: errors.New("down")},
		"POST /nil": {nilResp: true},
	})

	for _, m := range []string{"POST", "PATCH", "PUT", "DELETE"} {
		if err := skillExec(context.Background(), c, m, "/x", nil); err != nil {
			t.Errorf("%s /x: %v", m, err)
		}
	}
	if err := skillExec(context.Background(), c, "BREW", "/x", nil); err == nil || !strings.Contains(err.Error(), "unsupported HTTP method") {
		t.Errorf("BREW: %v", err)
	}
	if err := skillExec(context.Background(), c, "POST", "/bad", nil); err == nil || !strings.Contains(err.Error(), "HTTP 422") || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("422: %v", err)
	}
	if err := skillExec(context.Background(), c, "POST", "/err", nil); err == nil || !strings.Contains(err.Error(), "down") {
		t.Errorf("transport: %v", err)
	}
	if err := skillExec(context.Background(), c, "POST", "/nil", nil); err == nil || !strings.Contains(err.Error(), "nil response") {
		t.Errorf("nil resp: %v", err)
	}
}

func TestSkillCov_DisplayOrName(t *testing.T) {
	t.Parallel()
	if got := displayOrName(SkillRemote{DisplayName: "Pretty", Name: "raw"}); got != "Pretty" {
		t.Errorf("got %q", got)
	}
	if got := displayOrName(SkillRemote{Name: "raw"}); got != "raw" {
		t.Errorf("got %q", got)
	}
}

// A doc with a resolved body but no declared source reaches the
// decoration-only diff: matching remote → Unchanged, drifted → Update.
func TestSkillCov_Plan_DecorationOnlyDiff(t *testing.T) {
	t.Parallel()
	c := newCovClient(nil)

	d := skillCovDoc()
	d.SetResolved("# SKILL body")

	matching := &SkillRemote{
		ID: "s1", Slug: "research", Source: "CUSTOM",
		DisplayName: "Research", Description: "Find things out",
		Category: "analysis", Icon: "search",
	}
	items, err := d.Plan(context.Background(), c, matching)
	if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionUnchanged {
		t.Fatalf("unchanged: items=%+v err=%v", items, err)
	}
	if items[0].Exec != nil {
		t.Error("unchanged item must not carry Exec")
	}

	drifted := *matching
	drifted.Icon = "other"
	items, err = d.Plan(context.Background(), c, &drifted)
	if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("update: items=%+v err=%v", items, err)
	}
}

func TestSkillCov_Plan_Guards(t *testing.T) {
	t.Parallel()
	d := skillCovDoc()
	d.Spec.Inline = "# body"

	noWS := newCovClient(nil)
	noWS.ws = ""
	if _, err := d.Plan(context.Background(), noWS, nil); err == nil || !strings.Contains(err.Error(), "workspace_id not set") {
		t.Fatalf("no ws: got %v", err)
	}

	c := newCovClient(nil)
	bundled := &SkillRemote{Slug: "research", Source: "BUNDLED"}
	if _, err := d.Plan(context.Background(), c, bundled); err == nil || !strings.Contains(err.Error(), "BUNDLED") {
		t.Fatalf("bundled: got %v", err)
	}
}
