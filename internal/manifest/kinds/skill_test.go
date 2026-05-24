package kinds

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// ── Fake client (Skill-specific) ─────────────────────────────────────────
//
// Skill touches:
//
//	GET  /api/v1/skills                                       — list (Lookup + Export)
//	POST /api/v1/workspaces/{wsID}/skills/import              — upsert
//
// A focused fake keeps the test surface obvious. Mirrors the
// crewTemplateFakeClient pattern — we deliberately don't share state
// with the crew_template fake because the fixture shapes differ and
// intertwining them would make every kind's schema change ripple into
// neighbouring tests.
type skillFakeClient struct {
	wsID string

	// skills is the in-fake registry, keyed by slug.
	skills map[string]SkillRemote

	// importCallback fires whenever POST /skills/import is invoked.
	// Tests use it to capture the body the manifest sent so they can
	// assert on the wire shape.
	importCallback func(path string, body any)

	// importStatus overrides the import endpoint's response code
	// (default 201). Used by error-path tests to make Exec surface a
	// server failure.
	importStatus int

	// listGetErr forces GET /api/v1/skills to return a non-2xx status;
	// lets us assert error propagation from LookupSkillRemoteBySlug
	// and ExportSkills.
	listGetErr bool

	calls []string
}

func newSkillFake() *skillFakeClient {
	return &skillFakeClient{
		wsID:   "ws_test",
		skills: map[string]SkillRemote{},
	}
}

func (f *skillFakeClient) WorkspaceID() string { return f.wsID }

func (f *skillFakeClient) record(method, path string) {
	f.calls = append(f.calls, method+" "+path)
}

func skillJSONResp(status int, v any) *internalapi.Response {
	data, _ := json.Marshal(v)
	return &internalapi.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(data)),
	}
}

func (f *skillFakeClient) Get(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("GET", path)
	if path == "/api/v1/skills" {
		if f.listGetErr {
			return skillJSONResp(500, map[string]any{"error": "boom"}), nil
		}
		out := make([]SkillRemote, 0, len(f.skills))
		for _, s := range f.skills {
			out = append(out, s)
		}
		return skillJSONResp(200, out), nil
	}
	return skillJSONResp(404, map[string]any{"error": "not found"}), nil
}

func (f *skillFakeClient) Post(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("POST", path)
	if strings.HasSuffix(path, "/skills/import") {
		if f.importCallback != nil {
			f.importCallback(path, body)
		}
		status := f.importStatus
		if status == 0 {
			status = 201
		}
		// Materialise the imported row in the fake so a follow-on
		// Plan against the same client returns Unchanged (mirrors the
		// real server's post-commit state).
		if status >= 200 && status < 300 {
			if m, ok := body.(map[string]any); ok {
				// Heuristic: pull a slug from the URL fragment if the
				// caller set one in fixtures; otherwise no-op. The real
				// import handler derives the slug from parsed
				// front-matter, which is more than we can reproduce
				// here. Tests that need post-import state pre-seed the
				// fake registry instead.
				_ = m
			}
		}
		return skillJSONResp(status, map[string]any{
			"skill_id": "sk_test",
			"slug":     "stub",
			"created":  true,
		}), nil
	}
	return skillJSONResp(404, map[string]any{"error": "not found"}), nil
}

func (f *skillFakeClient) Patch(_ context.Context, _ string, _ any) (*internalapi.Response, error) {
	return skillJSONResp(404, nil), nil
}
func (f *skillFakeClient) Put(_ context.Context, _ string, _ any) (*internalapi.Response, error) {
	return skillJSONResp(404, nil), nil
}
func (f *skillFakeClient) Delete(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("DELETE", path)
	return skillJSONResp(404, nil), nil
}

// makeSkillDoc returns a minimal valid inline-bodied document for
// happy-path tests. Inline body chosen to exercise both YAML
// frontmatter and a few content lines — keeps the size well below the
// 8 KiB cap.
func makeSkillDoc() *SkillDocument {
	return &SkillDocument{
		APIVersion: skillAPIVersion,
		Kind:       skillKind,
		Metadata: internalapi.Metadata{
			Name: "Network Probe",
			Slug: "network-probe",
		},
		Spec: SkillSpec{
			DisplayName: "Network Probe",
			Category:    "networking",
			Description: "TCP/UDP probes against allow-listed hosts",
			Icon:        "network",
			Inline: `---
name: network-probe
description: TCP/UDP probes
---

# Network Probe

Use this when investigating reachability.
`,
		},
	}
}

// ── 1. Validate: happy path ───────────────────────────────────────────────

func TestSkill_Validate_HappyPathInline(t *testing.T) {
	doc := makeSkillDoc()
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// Validate must mirror inline → resolved so Plan can treat both
	// body shapes uniformly.
	if doc.Resolved() == "" {
		t.Error("Validate should mirror inline into resolved (got empty)")
	}
}

func TestSkill_Validate_HappyPathPath(t *testing.T) {
	doc := makeSkillDoc()
	doc.Spec.Inline = ""
	doc.Spec.Path = "./skills/network-probe/SKILL.md"
	// Simulate what the bundle loader would do: pre-populate resolved.
	doc.SetResolved("---\nname: network-probe\n---\n# Body\n")

	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestSkill_Validate_HappyPathSource(t *testing.T) {
	doc := makeSkillDoc()
	doc.Spec.Inline = ""
	doc.Spec.Source = "https://github.com/anthropics/skills/raw/main/SKILL.md"

	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// ── 2. Validate: error paths ──────────────────────────────────────────────

func TestSkill_Validate_MissingName(t *testing.T) {
	doc := makeSkillDoc()
	doc.Metadata.Name = ""
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "metadata.name is required") {
		t.Fatalf("want name-required error, got %v", err)
	}
}

func TestSkill_Validate_MissingSlug(t *testing.T) {
	doc := makeSkillDoc()
	doc.Metadata.Slug = ""
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "metadata.slug is required") {
		t.Fatalf("want slug-required error, got %v", err)
	}
}

func TestSkill_Validate_InvalidSlug(t *testing.T) {
	cases := []struct {
		name string
		slug string
	}{
		{"uppercase letters", "NetworkProbe"},
		{"leading hyphen", "-probe"},
		{"space", "network probe"},
		{"period", "network.probe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := makeSkillDoc()
			doc.Metadata.Slug = tc.slug
			err := doc.Validate(internalapi.WorkspaceContext{})
			if err == nil {
				t.Fatalf("want validation error for slug %q, got nil", tc.slug)
			}
		})
	}
}

func TestSkill_Validate_MissingDescription(t *testing.T) {
	doc := makeSkillDoc()
	doc.Spec.Description = ""
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "spec.description is required") {
		t.Fatalf("want description-required error, got %v", err)
	}
}

func TestSkill_Validate_NoSourceDeclared(t *testing.T) {
	doc := makeSkillDoc()
	doc.Spec.Inline = ""
	doc.Spec.Path = ""
	doc.Spec.Source = ""
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("want exactly-one-of error, got %v", err)
	}
	if !strings.Contains(err.Error(), "got none") {
		t.Errorf("want 'got none' qualifier in error, got %v", err)
	}
}

func TestSkill_Validate_MultipleSourcesDeclared(t *testing.T) {
	doc := makeSkillDoc()
	// inline is already set; add a second source.
	doc.Spec.Path = "./other/SKILL.md"
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("want exactly-one-of error, got %v", err)
	}
	// Error should name BOTH offenders so the user fixes the right
	// one without a second round-trip.
	if !strings.Contains(err.Error(), "inline") || !strings.Contains(err.Error(), "path") {
		t.Errorf("want both 'inline' and 'path' in error, got %v", err)
	}
}

func TestSkill_Validate_InlineTooLarge(t *testing.T) {
	doc := makeSkillDoc()
	// One byte over the cap. strings.Repeat is the cheapest way to
	// build a large body without dropping a fixture file.
	doc.Spec.Inline = strings.Repeat("x", maxSkillInlineBytes+1)
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "bytes (max") {
		t.Fatalf("want size-cap error, got %v", err)
	}
	// Suggest the remediation (`path:`) in the error so the user
	// doesn't have to read the docs.
	if !strings.Contains(err.Error(), "spec.path") {
		t.Errorf("want spec.path remediation in error, got %v", err)
	}
}

func TestSkill_Validate_BadSourceURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"http scheme rejected", "http://example.com/SKILL.md", "https scheme"},
		{"missing host", "https:///SKILL.md", "missing a host"},
		{"unparseable", "https://%%%", "not a valid URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := makeSkillDoc()
			doc.Spec.Inline = ""
			doc.Spec.Source = tc.url
			err := doc.Validate(internalapi.WorkspaceContext{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q in error, got %v", tc.want, err)
			}
		})
	}
}

func TestSkill_Validate_PathWithoutResolvedBody(t *testing.T) {
	// Hand-constructed document with `path:` but no SetResolved call —
	// the case a unit test or a hand-built bundle would hit. The error
	// should make the remediation explicit.
	doc := makeSkillDoc()
	doc.Spec.Inline = ""
	doc.Spec.Path = "./skills/network-probe/SKILL.md"
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "SetResolved") {
		t.Fatalf("want SetResolved hint in error, got %v", err)
	}
}

func TestSkill_Validate_WrongAPIVersion(t *testing.T) {
	doc := makeSkillDoc()
	doc.APIVersion = "crewship/v2"
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "apiVersion") {
		t.Fatalf("want apiVersion error, got %v", err)
	}
}

func TestSkill_Validate_WrongKind(t *testing.T) {
	doc := makeSkillDoc()
	doc.Kind = "Recipe"
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("want kind error, got %v", err)
	}
}

// ── 3. Plan: Create when remote is nil ────────────────────────────────────

func TestSkill_Plan_CreateWhenRemoteNil(t *testing.T) {
	doc := makeSkillDoc()
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	client := newSkillFake()
	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].Action != internalapi.ActionCreate {
		t.Errorf("want ActionCreate, got %s", items[0].Action)
	}
	if items[0].Kind != skillKindName {
		t.Errorf("want kind %q, got %q", skillKindName, items[0].Kind)
	}
	if items[0].Exec == nil {
		t.Fatal("Create item must have non-nil Exec")
	}

	// Run Exec and capture the body to assert wire shape.
	var capturedPath string
	var capturedBody map[string]any
	client.importCallback = func(path string, body any) {
		capturedPath = path
		if m, ok := body.(map[string]any); ok {
			capturedBody = m
		}
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if capturedPath != "/api/v1/workspaces/ws_test/skills/import" {
		t.Errorf("import path = %q, want workspace-scoped", capturedPath)
	}
	if capturedBody == nil {
		t.Fatal("import body was nil")
	}
	// inline body → `content` field
	if got, _ := capturedBody["content"].(string); !strings.Contains(got, "Network Probe") {
		t.Errorf("content field missing or wrong: %q", got)
	}
	if _, hasURL := capturedBody["url"]; hasURL {
		t.Error("inline-bodied import must not send a url field")
	}
}

func TestSkill_Plan_CreateFromSourceURL(t *testing.T) {
	doc := makeSkillDoc()
	doc.Spec.Inline = ""
	doc.Spec.Source = "https://example.com/SKILL.md"
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	client := newSkillFake()
	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionCreate {
		t.Fatalf("want single Create, got %+v", items)
	}

	var capturedBody map[string]any
	client.importCallback = func(_ string, body any) {
		if m, ok := body.(map[string]any); ok {
			capturedBody = m
		}
	}
	if err := items[0].Exec(context.Background(), client); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got, _ := capturedBody["url"].(string); got != "https://example.com/SKILL.md" {
		t.Errorf("url field = %q, want the source URL", got)
	}
	if _, hasContent := capturedBody["content"]; hasContent {
		t.Error("source-imported skill must not send a content field")
	}
}

// ── 4. Plan: Update when fields drift ─────────────────────────────────────

func TestSkill_Plan_UpdateWhenDrifted(t *testing.T) {
	doc := makeSkillDoc()
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	client := newSkillFake()
	remote := &SkillRemote{
		ID:          "sk_existing",
		Name:        "Network Probe",
		Slug:        "network-probe",
		DisplayName: "Network Probe",
		// Drifted: description has changed since the last apply.
		Description: "stale description",
		Category:    "networking",
		Icon:        "network",
		Source:      "CUSTOM",
	}
	items, err := doc.Plan(context.Background(), client, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want single Update, got %+v", items)
	}
	if items[0].Exec == nil {
		t.Fatal("Update item must have non-nil Exec")
	}
}

// ── 5. Plan: body-declared docs always emit Update ───────────────────────
//
// Pre-fix this test asserted "Unchanged when metadata matches". That
// was wrong: SkillRemote carries no body content or hash, so the
// manifest layer can't tell whether an inline/path/source body has
// changed. Returning Unchanged silently dropped body edits — the
// operator's `crewship apply` ran clean while the server kept the
// old body. The new behaviour emits Update whenever the doc declares
// a body source, regardless of whether the metadata matches. Repeated
// applies of an unchanged manifest now POST every time; loud over
// silently-wrong. See the comment block in Plan() at the hasBodySource
// branch.
func TestSkill_Plan_BodyDeclared_AlwaysUpdate(t *testing.T) {
	doc := makeSkillDoc()
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	client := newSkillFake()
	// Metadata matches the doc exactly — pre-fix this triggered
	// Unchanged. Now Update because the doc declares an inline
	// body and we can't verify the server's stored body matches.
	remote := &SkillRemote{
		ID:          "sk_existing",
		Name:        "Network Probe",
		Slug:        "network-probe",
		DisplayName: "Network Probe",
		Description: "TCP/UDP probes against allow-listed hosts",
		Category:    "networking",
		Icon:        "network",
		Source:      "CUSTOM",
	}
	items, err := doc.Plan(context.Background(), client, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("want single Update (body declared), got %+v", items)
	}
	if items[0].Exec == nil {
		t.Error("Update item must have non-nil Exec")
	}
}

// ── 6. Plan: BUNDLED rows refused ─────────────────────────────────────────

func TestSkill_Plan_RefusesBundledRemote(t *testing.T) {
	doc := makeSkillDoc()
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	client := newSkillFake()
	remote := &SkillRemote{
		ID: "sk_bundled", Slug: "network-probe", Source: "BUNDLED",
	}
	_, err := doc.Plan(context.Background(), client, remote)
	if err == nil || !strings.Contains(err.Error(), "BUNDLED") {
		t.Fatalf("want BUNDLED-refusal error, got %v", err)
	}
}

// ── 7. Plan: missing workspace ID errors ──────────────────────────────────

func TestSkill_Plan_RequiresWorkspaceID(t *testing.T) {
	doc := makeSkillDoc()
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	client := newSkillFake()
	client.wsID = "" // simulate an unset client
	_, err := doc.Plan(context.Background(), client, nil)
	if err == nil || !strings.Contains(err.Error(), "workspace_id") {
		t.Fatalf("want workspace_id error, got %v", err)
	}
}

// ── 8. Plan: server error surfaces via Exec ───────────────────────────────

func TestSkill_Plan_ImportErrorSurfacesFromExec(t *testing.T) {
	doc := makeSkillDoc()
	if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	client := newSkillFake()
	client.importStatus = 400 // import handler validation failure
	items, err := doc.Plan(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if items[0].Action != internalapi.ActionCreate {
		t.Fatalf("expected ActionCreate before server failure, got %s", items[0].Action)
	}
	err = items[0].Exec(context.Background(), client)
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("want 400 error from Exec, got %v", err)
	}
}

// ── 9. LookupSkillRemoteBySlug ───────────────────────────────────────────

func TestLookupSkillRemoteBySlug_Found(t *testing.T) {
	client := newSkillFake()
	client.skills["network-probe"] = SkillRemote{
		ID: "sk_a", Slug: "network-probe", Name: "Network Probe", Source: "CUSTOM",
	}
	client.skills["http-client"] = SkillRemote{
		ID: "sk_b", Slug: "http-client", Name: "HTTP Client", Source: "CUSTOM",
	}

	got, err := LookupSkillRemoteBySlug(context.Background(), client, "network-probe")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil {
		t.Fatal("want non-nil remote, got nil")
	}
	if got.ID != "sk_a" {
		t.Errorf("ID = %q, want sk_a", got.ID)
	}
}

func TestLookupSkillRemoteBySlug_NotFound(t *testing.T) {
	client := newSkillFake()
	client.skills["other"] = SkillRemote{ID: "sk_other", Slug: "other"}

	got, err := LookupSkillRemoteBySlug(context.Background(), client, "network-probe")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Errorf("want nil remote for missing slug, got %+v", got)
	}
}

func TestLookupSkillRemoteBySlug_EmptySlugRejected(t *testing.T) {
	client := newSkillFake()
	_, err := LookupSkillRemoteBySlug(context.Background(), client, "")
	if err == nil || !strings.Contains(err.Error(), "slug is required") {
		t.Fatalf("want slug-required error, got %v", err)
	}
}

func TestLookupSkillRemoteBySlug_ListError(t *testing.T) {
	client := newSkillFake()
	client.listGetErr = true
	_, err := LookupSkillRemoteBySlug(context.Background(), client, "any")
	if err == nil {
		t.Fatal("want error from list failure, got nil")
	}
}

// ── 10. ExportSkills ─────────────────────────────────────────────────────

func TestExportSkills_SkipsBundled(t *testing.T) {
	client := newSkillFake()
	client.skills["bundled-one"] = SkillRemote{
		ID: "sk_b1", Slug: "bundled-one", Name: "Bundled One", Source: "BUNDLED",
	}
	client.skills["custom-one"] = SkillRemote{
		ID: "sk_c1", Slug: "custom-one", Name: "Custom One",
		DisplayName: "Custom One", Description: "desc-1", Category: "x",
		Source: "CUSTOM",
	}
	client.skills["custom-two"] = SkillRemote{
		ID: "sk_c2", Slug: "custom-two", Name: "Custom Two",
		DisplayName: "Custom Two", Description: "desc-2", Category: "y",
		Source: "CUSTOM",
	}

	docs, err := ExportSkills(context.Background(), client)
	if err != nil {
		t.Fatalf("ExportSkills: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("want 2 docs (BUNDLED skipped), got %d", len(docs))
	}
	// Sorted by slug — assert order is stable.
	if docs[0].Metadata.Slug != "custom-one" || docs[1].Metadata.Slug != "custom-two" {
		t.Errorf("want sorted slugs, got %q, %q", docs[0].Metadata.Slug, docs[1].Metadata.Slug)
	}
	for _, d := range docs {
		if d.Kind != skillKind {
			t.Errorf("kind = %q, want %q", d.Kind, skillKind)
		}
		if d.APIVersion != skillAPIVersion {
			t.Errorf("apiVersion = %q, want %q", d.APIVersion, skillAPIVersion)
		}
		// Body source intentionally empty — see doc comment on
		// ExportSkills. Tests that wire in a `skill pull` companion
		// will need to update this expectation.
		if d.Spec.Inline != "" || d.Spec.Path != "" || d.Spec.Source != "" {
			t.Errorf("exported doc %s should have empty body source, got inline=%q path=%q source=%q",
				d.Metadata.Slug, d.Spec.Inline, d.Spec.Path, d.Spec.Source)
		}
	}
}

func TestExportSkills_EmptyRegistry(t *testing.T) {
	client := newSkillFake()
	docs, err := ExportSkills(context.Background(), client)
	if err != nil {
		t.Fatalf("ExportSkills: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("want 0 docs from empty registry, got %d", len(docs))
	}
}
