package main

import (
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
