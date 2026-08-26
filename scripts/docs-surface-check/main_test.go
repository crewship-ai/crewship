package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The deployed-site comparison must be opt-in. Without this, the check reached
// docs.crewship.ai on every pull request and failed whenever the local
// navigation declared a page Mintlify had not published yet — which is the
// state of every PR that adds documentation.
func TestServedCheckIsSkippedWithoutURL(t *testing.T) {
	served, err := checkServed("", 279)
	if err != nil {
		t.Fatalf("checkServed(\"\", …) = %v; an unset URL must skip the deployed comparison, not fail", err)
	}
	if served != -1 {
		t.Errorf("served = %d, want -1 to mark the comparison as not run", served)
	}
}

func TestServedCheckMakesNoRequestWithoutURL(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	defer srv.Close()

	if _, err := checkServed("", 1); err != nil {
		t.Fatal(err)
	}
	if reached {
		t.Fatal("checkServed made an HTTP request with no URL configured; the repository-side gate must stay hermetic")
	}
}

// navigationPages must read the structure, not guess which strings look like a
// page. The predicate it replaced required a "/" or one of a few known
// prefixes, so `philosophy`, `production-checklist` and `architecture` — all
// declared at the top level of the real docs.json — were never counted and
// therefore never checked for existence. The count went 279 → 282 on the real
// file when this was fixed.
func TestNavigationPagesReadsStructureNotStringShape(t *testing.T) {
	nav := []byte(`{
	  "tabs": [
	    {
	      "tab": "Docs",
	      "icon": "book-open",
	      "groups": [
	        {
	          "group": "Get Started",
	          "pages": ["index", "quickstart", "philosophy", "production-checklist", "architecture"]
	        },
	        {
	          "group": "Guides",
	          "pages": [
	            "guides/first-projects",
	            {"group": "Nested", "pages": ["guides/deep/one"]}
	          ]
	        }
	      ]
	    }
	  ]
	}`)

	got := navigationPages(nav)
	want := []string{
		"architecture", "guides/deep/one", "guides/first-projects", "index",
		"philosophy", "production-checklist", "quickstart",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("navigationPages() = %v\nwant %v", got, want)
	}
	// Group labels, tab names and icons live outside `pages` and must not be
	// mistaken for page ids — that is the other half of a shape-based guess.
	for _, notAPage := range []string{"Docs", "book-open", "Get Started", "Guides", "Nested"} {
		if slices.Contains(got, notAPage) {
			t.Errorf("%q is navigation chrome, not a page id, but it was collected", notAPage)
		}
	}
}

// With a URL it still has to do its job: a deployed index that is behind the
// checkout is real drift, and the message must say which side is short.
func TestServedCheckReportsDeployedDrift(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/llms.txt") {
			_, _ = w.Write([]byte("# Docs\n\n- [One](https://example.test/one)\n- [Two](https://example.test/two)\n"))
			return
		}
		_, _ = w.Write([]byte("full index body"))
	}))
	defer srv.Close()

	if _, err := checkServed(srv.URL, 2); err != nil {
		t.Fatalf("checkServed with a caught-up index = %v, want nil", err)
	}

	served, err := checkServed(srv.URL, 5)
	if err == nil {
		t.Fatal("checkServed with a lagging index = nil, want an error")
	}
	if served != 2 {
		t.Errorf("served = %d, want 2", served)
	}
	if !strings.Contains(err.Error(), "lists 2 pages") || !strings.Contains(err.Error(), "declares 5") {
		t.Errorf("error does not name both counts: %v", err)
	}
}

// A page id declared in docs.json is gated; a link written in a page body was
// not. `](/guides/does-not-exist)` shipped green through every check we had —
// Mintlify does not fail its build on it either — so the only thing standing
// between a typo'd link and production was a reviewer running a throwaway
// shell loop by hand (#1774).
func TestInternalLinksCollectsOnlyResolvableInternalTargets(t *testing.T) {
	body := "" +
		"See the [routines guide](/guides/routines) and the " +
		"[section](/guides/routines#cross-run-state).\n" +
		"An [external link](https://example.test/guides/routines) is not ours.\n" +
		"A [same-page anchor](#recap) has no page to resolve.\n" +
		"A [mail link](mailto:hi@example.test) is not a page.\n" +
		"A [relative link](../guides/routines) is not an absolute target.\n" +
		"<Card title=\"Install\" href=\"/guides/install\">Start here</Card>\n" +
		"<Card title=\"Docs\" href='/index'>Home</Card>\n"

	got := internalLinks(body)
	want := []string{
		"/guides/routines",
		"/guides/routines#cross-run-state",
		"/guides/install",
		"/index",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("internalLinks() = %v\nwant %v", got, want)
	}
}

// A fenced block is a transcript, not navigation. The issue that asked for this
// gate quotes `](/guides/does-not-exist)` as its own example of the bug; a page
// documenting the gate would otherwise redden it, and a gate that fires on the
// prose describing it is a gate someone deletes.
func TestInternalLinksSkipsFencedCodeBlocks(t *testing.T) {
	body := "" +
		"Real link: [routines](/guides/routines)\n" +
		"```md\n" +
		"[a typo'd link](/guides/does-not-exist)\n" +
		"```\n" +
		"~~~text\n" +
		"[another one](/nope)\n" +
		"~~~\n" +
		"After the fence: [install](/guides/install)\n"

	got := internalLinks(body)
	want := []string{"/guides/routines", "/guides/install"}
	if !slices.Equal(got, want) {
		t.Fatalf("internalLinks() = %v\nwant %v", got, want)
	}
}

// The offender report has to name BOTH sides: the page that has to be edited
// and the target that does not resolve. "3 dead links" sends the reader back to
// the same hand-rolled loop this check replaces.
func TestBrokenProseLinksNamesPageAndTarget(t *testing.T) {
	root := t.TempDir()
	writeDocsPage(t, root, "docs/guides/routines.mdx", "# Routines\n")
	writeDocsPage(t, root, "docs/index.md", "# Home\n")
	writeDocsPage(t, root, "docs/guides/first-projects.mdx", ""+
		"[good](/guides/routines) [also good](/index)\n"+
		"[dead](/guides/does-not-exist)\n"+
		"[external](https://example.test/nope) [anchor](#top)\n")
	writeDocsPage(t, root, "docs/concepts.mdx", "[dead too](/guides/gone#section)\n")

	dead, checked, err := brokenProseLinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if checked != 4 {
		t.Errorf("checked = %d, want 4 internal links examined", checked)
	}
	want := []deadLink{
		{page: "docs/concepts.mdx", target: "/guides/gone#section"},
		{page: "docs/guides/first-projects.mdx", target: "/guides/does-not-exist"},
	}
	if !slices.Equal(dead, want) {
		t.Fatalf("brokenProseLinks() = %+v\nwant %+v", dead, want)
	}
}

// Fragments address a position inside a page; the page is what has to exist.
// `/guides/routines#cross-run-state` must not be reported merely because no
// file is named `routines#cross-run-state`.
func TestBrokenProseLinksResolvesAnchorsToTheirPage(t *testing.T) {
	root := t.TempDir()
	writeDocsPage(t, root, "docs/guides/routines.mdx", "# Routines\n")
	writeDocsPage(t, root, "docs/concepts.mdx", "[jump](/guides/routines#cross-run-state)\n")

	dead, _, err := brokenProseLinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 0 {
		t.Fatalf("brokenProseLinks() = %+v, want none — the fragment is not part of the page path", dead)
	}
}

// The gate exists to be enforced, so it has to hold on the tree it ships with.
// Landing it red is how a check gets skipped, filtered, or deleted.
func TestRepositoryDocsHaveNoBrokenProseLinks(t *testing.T) {
	root := filepath.Join("..", "..")
	dead, checked, err := brokenProseLinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no internal links examined; the walk did not reach the docs tree")
	}
	for _, d := range dead {
		t.Errorf("%s links to %s, which is not a page in the docs tree", d.page, d.target)
	}
}

func TestRepositoryDocsHaveValidStabilityLabels(t *testing.T) {
	root := filepath.Join("..", "..")
	issues, pages, err := documentationStability(root)
	if err != nil {
		t.Fatal(err)
	}
	if pages == 0 {
		t.Fatal("no MDX pages examined; the walk did not reach the docs tree")
	}
	for _, issue := range issues {
		t.Error(issue)
	}
}

func TestDocumentationStabilityRejectsUnknownAndMismatchedLabels(t *testing.T) {
	root := t.TempDir()
	writeDocsPage(t, root, "docs/good.mdx", "---\ntitle: Good\nstability: early\ntag: Early\n---\n")
	writeDocsPage(t, root, "docs/unknown.mdx", "---\ntitle: Unknown\nstability: preview\ntag: Preview\n---\n")
	writeDocsPage(t, root, "docs/mismatch.mdx", "---\ntitle: Mismatch\nstability: stable\ntag: Experimental\n---\n")

	issues, pages, err := documentationStability(root)
	if err != nil {
		t.Fatal(err)
	}
	if pages != 3 {
		t.Fatalf("pages = %d, want 3", pages)
	}
	want := []string{
		`docs/mismatch.mdx: rendered tag "Experimental" does not match stability "stable"`,
		`docs/unknown.mdx: invalid stability label "preview"`,
	}
	if !slices.Equal(issues, want) {
		t.Fatalf("issues = %v\nwant %v", issues, want)
	}
}

func TestDeprecatedTerminologyNamesPageSpellingAndReplacement(t *testing.T) {
	root := t.TempDir()
	writeDocsPage(t, root, "docs/guides/delegation.mdx", "A coordinator assigns the work.\n")

	offenders, err := deprecatedTerminologyInDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []deprecatedTermUse{{
		page:        "docs/guides/delegation.mdx",
		spelling:    "COORDINATOR",
		replacement: "LEAD",
	}}
	if !slices.Equal(offenders, want) {
		t.Fatalf("deprecatedTerminologyInDocs() = %+v\nwant %+v", offenders, want)
	}
}

func TestDeprecatedTerminologyRejectsUnrelatedUseOnCompatibilityPage(t *testing.T) {
	root := t.TempDir()
	writeDocsPage(t, root, "docs/concepts.mdx", ""+
		"| **`COORDINATOR`** | **`LEAD`** | Deprecated 2026-04-16 |\n"+
		"A coordinator assigns the work.\n")

	offenders, err := deprecatedTerminologyInDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []deprecatedTermUse{{
		page:        "docs/concepts.mdx",
		spelling:    "COORDINATOR",
		replacement: "LEAD",
	}}
	if !slices.Equal(offenders, want) {
		t.Fatalf("deprecatedTerminologyInDocs() = %+v\nwant %+v", offenders, want)
	}
}

func TestRepositoryDocsHaveNoDeprecatedTerminology(t *testing.T) {
	root := filepath.Join("..", "..")
	offenders, err := deprecatedTerminologyInDocs(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, use := range offenders {
		t.Errorf("%s uses deprecated %s; use %s", use.page, use.spelling, use.replacement)
	}
}

// Two spellings of a live page that a naive path join turns into a false
// positive: the site root, which Mintlify serves from `index`, and a trailing
// slash. Both are legal to write, and a gate that reddens on a working link is
// a gate that gets argued with rather than obeyed.
func TestBrokenProseLinksAcceptsRootAndTrailingSlash(t *testing.T) {
	root := t.TempDir()
	writeDocsPage(t, root, "docs/index.mdx", "# Home\n")
	writeDocsPage(t, root, "docs/guides/routines.mdx", "# Routines\n")
	writeDocsPage(t, root, "docs/concepts.mdx", ""+
		"[home](/) [home again](/#top)\n"+
		"[routines](/guides/routines/)\n")

	dead, _, err := brokenProseLinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 0 {
		t.Fatalf("brokenProseLinks() = %+v, want none — these all resolve to a real page", dead)
	}
}

// Both directions have to be gated, and only one of them was. The nav→file pass
// proves a declared id has a file behind it; a file nobody declared is
// unreachable from the sidebar AND absent from llms.txt, so neither a reader
// nor an agent can find it — and no gate said a word. `cli/onboarding`,
// `api-reference/onboarding` and `manifest/page` shipped published and orphaned,
// with the CLI page hiding all seven `onboarding` commands (#2086).
func TestOrphanedPagesReportsFilesTheNavigationDoesNotDeclare(t *testing.T) {
	root := t.TempDir()
	writeDocsPage(t, root, "docs/cli/page.mdx", "---\ntitle: Page\n---\n")
	writeDocsPage(t, root, "docs/cli/onboarding.mdx", "---\ntitle: Onboarding\n---\n")
	writeDocsPage(t, root, "docs/manifest/page.md", "# kind: Page\n")

	orphans, reachable, err := orphanedPages(root, []string{"cli/page"})
	if err != nil {
		t.Fatal(err)
	}
	if reachable != 3 {
		t.Errorf("reachable = %d, want 3 — every non-allowlisted page must be required to appear in the navigation", reachable)
	}
	want := []string{"cli/onboarding", "manifest/page"}
	if !slices.Equal(orphans, want) {
		t.Errorf("orphanedPages() = %v, want %v", orphans, want)
	}
}

// The allowlist is the whole reason the gate is enforceable: `prd/` and the
// audit methodology are written for the repository, not the published site, so
// a check that reported them would be turned off within a week.
func TestOrphanedPagesExcusesTheDeliberatelyUnlistedTrees(t *testing.T) {
	root := t.TempDir()
	writeDocsPage(t, root, "docs/audit-methodology.md", "# How the audits run\n")
	writeDocsPage(t, root, "docs/prd/pages.md", "# Pages PRD\n")
	writeDocsPage(t, root, "docs/prd/reports/release-1-0.md", "# Report\n")

	orphans, reachable, err := orphanedPages(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 0 {
		t.Errorf("orphanedPages() = %v, want none — these are unlisted on purpose", orphans)
	}
	if reachable != 0 {
		t.Errorf("reachable = %d, want 0 — allowlisted pages are not counted as navigation obligations", reachable)
	}
}

// The gate has to hold on the tree it ships with, like every other check here.
// This is the assertion that was red before the three navigation entries were
// added, and it is what keeps the next orphan from shipping.
func TestRepositoryDocsHaveNoOrphanedPages(t *testing.T) {
	root := filepath.Join("..", "..")
	data, err := os.ReadFile(filepath.Join(root, "docs/docs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	orphans, reachable, err := orphanedPages(root, navigationPages(cfg.Navigation))
	if err != nil {
		t.Fatal(err)
	}
	if reachable == 0 {
		t.Fatal("no pages examined; the walk did not reach the docs tree")
	}
	for _, page := range orphans {
		t.Errorf("docs/%s is published but declared nowhere in docs/docs.json — unreachable from the sidebar and absent from llms.txt", page)
	}
}

func writeDocsPage(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
