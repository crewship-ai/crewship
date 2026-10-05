package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeSurfacePage(t *testing.T, root, rel, body string) {
	t.Helper()
	writeDocsPage(t, root, filepath.Join("docs", rel), body)
}

const validSurfaceConfig = `{"contextual":{"options":["copy"]},"navigation":{"pages":["guides/fixture"]}}`
const validSurfacePage = "---\ntitle: Fixture\ndescription: A useful explanation of the fixture behavior\nstability: stable\ntag: Stable\n---\n\n# Fixture\n\n## Real heading\n\nOwned documentation fixture.\n"

func TestDocsSurfaceCommandValidatesRepositoryAndOptionalServedIndex(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "docs-surface-check")
	args := []string{"build", "-o", binary}
	coverage := os.Getenv("CREWSHIP_TEST_DOCS_SURFACE_COVERAGE_DIR")
	if coverage != "" {
		if !filepath.IsAbs(coverage) {
			t.Fatal("coverage directory must be absolute")
		}
		if err := os.MkdirAll(coverage, 0700); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-cover", "-coverpkg=github.com/crewship-ai/crewship/scripts/docs-surface-check")
	}
	args = append(args, ".")
	if out, err := exec.Command("go", args...).CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	fixture := func() string {
		root := t.TempDir()
		writeSurfacePage(t, root, "guides/fixture.mdx", validSurfacePage)
		writeSurfacePage(t, root, "docs.json", validSurfaceConfig)
		return root
	}
	run := func(root string, code int, want string, args ...string) {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Dir = root
		cmd.Env = os.Environ()
		if coverage != "" {
			cmd.Env = append(cmd.Env, "GOCOVERDIR="+coverage)
		}
		out, err := cmd.CombinedOutput()
		got := 0
		if err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatal(err)
			}
			got = exit.ExitCode()
		}
		if got != code || !strings.Contains(string(out), want) {
			t.Fatalf("command %v exit %d want %d: %v\n%s", args, got, code, err, out)
		}
	}
	root := fixture()
	run(root, 0, "llms pages=not checked")
	run(t.TempDir(), 0, "navigation pages=1", "-root", root)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/llms.txt":
			_, _ = w.Write([]byte("- [Fixture](https://example.test/guides/fixture.md)\n"))
		case "/llms-full.txt":
			_, _ = w.Write([]byte("full body"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	run(root, 0, "llms pages=1", "-url", server.URL)
	run(root, 1, "docs-surface-check:", "-url", server.URL+"/missing")
	for _, tc := range []struct{ name, config, page, extra, want string }{
		{name: "missing-config", want: "docs/docs.json"},
		{name: "invalid-config", config: "not JSON", want: "invalid character"},
		{name: "missing-context", config: `{"navigation":{"pages":["guides/fixture"]}}`, want: "must declare contextual.options"},
		{name: "empty-navigation", config: `{"contextual":{"options":["copy"]},"navigation":{"pages":[]}}`, want: "declares no navigation pages"},
		{name: "missing-navigation-target", config: `{"contextual":{"options":["copy"]},"navigation":{"pages":["guides/missing"]}}`, want: "navigation pages missing"},
		{name: "stability", page: strings.Replace(validSurfacePage, "stability: stable", "stability: mystery", 1), want: "stability labels invalid"},
		{name: "orphan", extra: "orphan", want: "published pages missing"},
		{name: "dead-page", extra: "[missing](/guides/missing)", want: "dead internal links"},
		{name: "dead-anchor", extra: "[missing anchor](/guides/fixture#absent-heading)", want: "heading anchor does not exist"},
		{name: "deprecated-term", extra: "The COORDINATOR runs this.", want: "deprecated terminology"},
		{name: "wrapped-code", extra: "Set `command\n<slug|id>` here.", want: "inline code spans wrapped"},
		{name: "heading-expression", extra: "## Route {token}\n", want: "headings with an unescaped"},
		{name: "mdx-tag", extra: "A bare <slug|id> here.", want: "unguarded MDX tag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fixture()
			if tc.name == "missing-config" {
				if err := os.Remove(filepath.Join(root, "docs/docs.json")); err != nil {
					t.Fatal(err)
				}
			} else if tc.config != "" {
				writeSurfacePage(t, root, "docs.json", tc.config)
			}
			if tc.page != "" {
				writeSurfacePage(t, root, "guides/fixture.mdx", tc.page)
			}
			if tc.extra == "orphan" {
				writeSurfacePage(t, root, "guides/orphan.mdx", validSurfacePage)
			} else if tc.extra != "" {
				writeSurfacePage(t, root, "guides/fixture.mdx", validSurfacePage+tc.extra+"\n")
			}
			run(root, 1, tc.want)
		})
	}
}

func TestDescriptionQualityAndMDXTagAuditsUseActualFiles(t *testing.T) {
	root := t.TempDir()
	writeSurfacePage(t, root, "good.mdx", validSurfacePage)
	writeSurfacePage(t, root, "repeated.mdx", "---\ntitle: 'Repeated'\ndescription: \"Repeated\"\n---\n")
	writeSurfacePage(t, root, "missing-description.mdx", "---\ntitle: Bare\n---\n")
	writeSurfacePage(t, root, "no-frontmatter.mdx", "plain text\n")
	writeSurfacePage(t, root, "ignored.txt", "<slug|id>\n")
	if total, good, bad := descriptionQuality(root); total != 2 || good != 1 || bad != 1 {
		t.Fatalf("description audit: %d %d %d", total, good, bad)
	}
	if total, _, _ := descriptionQuality(t.TempDir()); total != 0 {
		t.Fatal("absent tree counted descriptions")
	}
	writeSurfacePage(t, root, "nested/syntax.md", "`<safe|span>`\n```text\n<inside|fence>\n```\nBare <bad|tag>\n")
	issues, scanned, err := unguardedMDXTags(root)
	if err != nil || scanned != 5 || len(issues) != 1 || issues[0].page != "nested/syntax" || issues[0].line != 5 || issues[0].text != "<bad|tag>" {
		t.Fatalf("tag diagnostics: %+v pages=%d %v", issues, scanned, err)
	}
	if err := reportUnguardedMDXTags(root); err == nil || !strings.Contains(err.Error(), "nested/syntax:5") {
		t.Fatalf("unsafe tags accepted: %v", err)
	}
	writeSurfacePage(t, root, "nested/syntax.md", "`<safe|span>`\n")
	if err := reportUnguardedMDXTags(root); err != nil {
		t.Fatal(err)
	}
	if err := reportUnguardedMDXTags(t.TempDir()); err == nil {
		t.Fatal("missing documentation tree accepted")
	}
}
