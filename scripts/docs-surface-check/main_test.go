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

	dead, audit, err := brokenProseLinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if audit.links != 4 {
		t.Errorf("audit.links = %d, want 4 internal links examined", audit.links)
	}
	want := []deadLink{
		{page: "docs/concepts.mdx", target: "/guides/gone#section"},
		{page: "docs/guides/first-projects.mdx", target: "/guides/does-not-exist"},
	}
	if !slices.Equal(dead, want) {
		t.Fatalf("brokenProseLinks() = %+v\nwant %+v", dead, want)
	}
}

// This test used to assert the defect. It pinned `want none — the fragment is
// not part of the page path` for a link whose heading does not exist, which is
// the bug written down as a specification: a heading rename could not be caught
// because the fragment was resolved away rather than verified (#1794).
//
// A fragment addresses a position inside a page. The page must exist AND the
// anchor must exist, because a link that lands on the right page and the wrong
// place is a link that is broken for the reader.
func TestBrokenProseLinksVerifiesTheAnchorNotJustThePage(t *testing.T) {
	root := t.TempDir()
	writeDocsPage(t, root, "docs/guides/routines.mdx", "# Routines\n\n## Cross-run state\n")
	writeDocsPage(t, root, "docs/concepts.mdx", ""+
		"[live](/guides/routines#cross-run-state)\n"+
		"[renamed](/guides/routines#cross-run-storage)\n")

	dead, _, err := brokenProseLinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 1 {
		t.Fatalf("brokenProseLinks() = %+v, want exactly the renamed anchor", dead)
	}
	if dead[0].target != "/guides/routines#cross-run-storage" {
		t.Fatalf("reported %q, want the link whose heading no longer exists", dead[0].target)
	}
}

// Every pair below was read off docs.crewship.ai: the `heading` is the Markdown
// in this repository, the `want` is the `id` attribute Mintlify put on the
// rendered <h> for it. Nothing here was derived from a slugger's documentation,
// because the slugger Mintlify uses is not the one anybody would guess — it is
// not github-slugger, and it keeps `/ & + | < > = # $ _` and every non-ASCII
// rune that github-slugger strips.
//
// The whole gate rests on this function, in both directions: a slugger that
// drops a character reports working links as dead, and one that keeps too many
// blesses dead ones. Mutate mintlifySlug and this table goes red.
func TestMintlifySlugMatchesTheDeployedSite(t *testing.T) {
	cases := []struct{ heading, want string }{
		// docs/cli/routine.mdx — the case #1794 was written around. The
		// underscore survives AND so do the angle brackets.
		{heading: "`crewship routine result <run_id>`", want: "crewship-routine-result-<run_id>"},
		// docs/guides/routines.mdx — a dropped run between two spaces leaves an
		// empty token, so the doubled dash and the trailing dash are both real.
		{heading: "Secrets (`{{ secrets.<type> }}`)", want: "secrets--secrets-<type>-"},
		{heading: "Cross-run state (`{{ routine.state.<key> }}`)", want: "cross-run-state--routine-state-<key>-"},
		{heading: "`runtime: expr` (wired, token-zero)", want: "runtime-expr-wired-token-zero"},
		{heading: "`foreach` — fan out over an array", want: "foreach-—-fan-out-over-an-array"},
		// An explicit id wins over the slug.
		{heading: "\"Get a daily ops digest of runs, cost, and failures\" {#workspace-digest-template}", want: "workspace-digest-template"},
		// docs/api-reference/admin.mdx — escaped braces render as literal text,
		// unescaped ones are MDX expressions and render as nothing.
		{heading: "GET /api/v1/admin/users/\\{userId\\}/data", want: "get-/api/v1/admin/users/userid/data"},
		{heading: "PUT /api/v1/admin/rate-limits/{key}", want: "put-/api/v1/admin/rate-limits/"},
		{heading: "Stats & users", want: "stats-&-users"},
		{heading: "What's Next", want: "what’s-next"},
		// docs/guides/keeper.mdx
		{heading: "API + CLI", want: "api-+-cli"},
		{heading: "…and taking the human out entirely", want: "…and-taking-the-human-out-entirely"},
		{heading: "Credential tiers (L1–L4)", want: "credential-tiers-l1–l4"},
		// docs/api-reference/overview.mdx
		{heading: "Auth / Signup", want: "auth-/-signup"},
		// docs/guides/install.mdx
		{heading: "curl | bash", want: "curl-|-bash"},
		// docs/guides/consolidate.mdx — `#` survives into the anchor, which is
		// why a link to it has to spell it `%23`.
		{heading: "Archive layer (PR #212)", want: "archive-layer-pr-#212"},
		// docs/guides/mcp-multi-cli.mdx
		{heading: "4. Codex config in `$HOME`, not project root", want: "4-codex-config-in-$home-not-project-root"},
		{heading: "2. Codex doesn't interpolate `${VAR}`", want: "2-codex-doesn’t-interpolate-$var"},
		{heading: "3. OpenCode `{env:VAR}` syntax", want: "3-opencode-envvar-syntax"},
		// docs/guides/agent-memory.mdx — `?` is dropped, `=` and `|` are not.
		{heading: "GET /memory/status?scope=agent|crew", want: "get-/memory/statusscope=agent|crew"},
		{heading: "Pinned facts (`tier=pins`)", want: "pinned-facts-tier=pins"},
		{heading: "1. Writing Memory", want: "1-writing-memory"},
		// docs/guides/credentials.mdx
		{heading: "Settings → Access & Secrets", want: "settings-→-access-&-secrets"},
		// docs/api-reference/internal.mdx — the entity is resolved first.
		{heading: "Network origin &amp; reverse proxies", want: "network-origin-&-reverse-proxies"},
		// docs/api-reference/inbox.mdx
		{heading: "`GET /api/v1/inbox`", want: "get-/api/v1/inbox"},
		// docs/guides/cli-adapters.mdx
		{heading: "What does *not* fail a run", want: "what-does-not-fail-a-run"},
		// docs/manifest/routine.mdx
		{heading: "`runtime: bash | python | go` — rejected at author time", want: "runtime-bash-|-python-|-go-—-rejected-at-author-time"},
		// docs/cli/admin.mdx
		{heading: "`memory-config get`", want: "memory-config-get"},
		// docs/configuration/providers.mdx — a "." inside a token is a dash.
		{heading: "Kubernetes Provider — v0.2 roadmap", want: "kubernetes-provider-—-v0-2-roadmap"},
	}
	for _, c := range cases {
		if got := headingAnchor(c.heading, map[string]int{}); got != c.want {
			t.Errorf("headingAnchor(%q)\n = %q\nwant %q (the id docs.crewship.ai publishes)", c.heading, got, c.want)
		}
	}
}

// A component does not use the heading slugger, and assuming it does is how the
// gate would bless `#…concurrency_key…` while the browser needs
// `#…concurrency-key…`. Both ids below are live on docs.crewship.ai.
func TestComponentSlugIsNotTheHeadingSlug(t *testing.T) {
	title := `Run returns 500 &quot;pipeline: concurrency_key rendered to empty value&quot;`
	const want = "run-returns-500-pipeline-concurrency-key-rendered-to-empty-value"
	if got := componentSlug(title); got != want {
		t.Errorf("componentSlug(%q) = %q, want %q", title, got, want)
	}
	// The same underscore in a heading is kept, on the same site.
	if got := headingAnchor("`crewship routine signal <run_id>`", map[string]int{}); got != "crewship-routine-signal-<run_id>" {
		t.Errorf("headingAnchor kept nothing: %q", got)
	}
}

// anchorsOf has to describe the whole namespace, not just the ATX headings: a
// page whose anchors it under-reports turns working links red, which is how a
// per-PR gate gets switched off.
func TestAnchorsOfCollectsEverythingThatPublishesAnAnchor(t *testing.T) {
	body := "" +
		"---\ntitle: Example\nstability: stable\n---\n" +
		"# Overview\n" +
		"## Response\n" +
		"### Response\n" +
		"### Response\n" +
		"## Pinned {#custom-id}\n" +
		"<Accordion title=\"The 17 credential-shaped patterns\">detail</Accordion>\n" +
		"<Accordion title=\"Ignored because it says so\" id=\"explicit-wins\">detail</Accordion>\n" +
		"<a id=\"hand-written\"></a>\n" +
		"```md\n" +
		"## Not a heading, this is a sample\n" +
		"```\n" +
		"## Last\n"

	got := anchorsOf(body)
	want := []string{
		"overview",
		"response", "response-2", "response-3",
		"custom-id",
		"the-17-credential-shaped-patterns",
		"explicit-wins",
		"hand-written",
		"last",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("anchorsOf() = %v\nwant %v", got, want)
	}
}

// The gate is conservative in exactly two places, and nowhere else. Widening
// this hides real breakage; narrowing it invents red on links that work.
func TestAnchorKeyIgnoresDashRunsAndQuoteMarksOnly(t *testing.T) {
	same := []struct{ reason, a, b string }{
		{
			// docs/guides/upgrades.mdx publishes the doubled dash; an otherwise
			// identical heading two pages over publishes a single one.
			reason: "dash runs",
			a:      "server-installs-systemd--self-update-systemd",
			b:      "server-installs-systemd-self-update-systemd",
		},
		{
			// docs/cli/workspace.mdx publishes the trailing dash for `[…]`.
			reason: "trailing dash",
			a:      "crewship-workspace-member-capabilities-grant-<user-id>-<capability>-",
			b:      "crewship-workspace-member-capabilities-grant-<user-id>-<capability>",
		},
		{
			// Mintlify curls the opening mark on one heading and drops it on the
			// next, so a quoted heading cannot be addressed exactly.
			reason: "quotation marks",
			a:      "”ghost-as-cheap-reference”",
			b:      "ghost-as-cheap-reference",
		},
		{
			reason: "percent-encoding",
			a:      "crewship-routine-result-%3Crun_id%3E",
			b:      "crewship-routine-result-<run_id>",
		},
	}
	for _, c := range same {
		if anchorKey(c.a) != anchorKey(c.b) {
			t.Errorf("%s: anchorKey(%q) = %q and anchorKey(%q) = %q should agree",
				c.reason, c.a, anchorKey(c.a), c.b, anchorKey(c.b))
		}
	}

	differ := []struct{ reason, a, b string }{
		{
			// The whole point of #1794: the underscore was never the only
			// difference, and neither written spelling reaches the heading.
			reason: "angle brackets are real characters",
			a:      "crewship-routine-result-run_id",
			b:      "crewship-routine-result-<run_id>",
		},
		{
			reason: "underscore is not a dash",
			a:      "concurrency_key-rendered-to-empty",
			b:      "concurrency-key-rendered-to-empty",
		},
		{
			// The apostrophe is predictable, so it is compared, not ignored.
			reason: "apostrophes are not quote marks",
			a:      "crewship-—-act-on-crewships-own-nouns",
			b:      "crewship-—-act-on-crewship’s-own-nouns",
		},
		{
			reason: "the duplicate-heading counter",
			a:      "response",
			b:      "response-2",
		},
	}
	for _, c := range differ {
		if anchorKey(c.a) == anchorKey(c.b) {
			t.Errorf("%s: anchorKey(%q) and anchorKey(%q) both = %q; the gate is blind to this rename",
				c.reason, c.a, c.b, anchorKey(c.a))
		}
	}
}

// A dead anchor has to name the page to edit, the fragment as written, and —
// where there is an obvious one — the anchor that was meant. "27 dead anchors"
// is the report that sent #1794 back to the backlog.
func TestDeadAnchorReportNamesTheFragmentAndSuggestsTheRealOne(t *testing.T) {
	root := t.TempDir()
	writeDocsPage(t, root, "docs/cli/routine.mdx", "# Routine\n\n## `crewship routine result <run_id>`\n")
	writeDocsPage(t, root, "docs/guides/routines.mdx", "[result](/cli/routine#crewship-routine-result-run_id)\n")

	dead, _, err := brokenProseLinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 1 {
		t.Fatalf("brokenProseLinks() = %+v, want one dead anchor", dead)
	}
	if !strings.Contains(dead[0].reason, "#crewship-routine-result-run_id") {
		t.Errorf("reason %q does not quote the fragment as written", dead[0].reason)
	}
	if !strings.Contains(dead[0].reason, "#crewship-routine-result-<run_id>") {
		t.Errorf("reason %q does not name the anchor the page publishes", dead[0].reason)
	}
	// A missing page and a missing anchor are different edits; only the second
	// carries a reason.
	writeDocsPage(t, root, "docs/guides/gone.mdx", "[nowhere](/cli/does-not-exist#anything)\n")
	dead, _, err = brokenProseLinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dead {
		if d.target == "/cli/does-not-exist#anything" && d.reason != "" {
			t.Errorf("a missing page was reported as a missing anchor: %+v", d)
		}
	}
}

// The gate exists to be enforced, so it has to hold on the tree it ships with.
// Landing it red is how a check gets skipped, filtered, or deleted.
func TestRepositoryDocsHaveNoBrokenProseLinks(t *testing.T) {
	root := filepath.Join("..", "..")
	dead, audit, err := brokenProseLinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if audit.links == 0 {
		t.Fatal("no internal links examined; the walk did not reach the docs tree")
	}
	if audit.anchors == 0 {
		t.Fatal("no anchored links examined; the fragment half of the gate did nothing")
	}
	for _, d := range dead {
		if d.reason == "" {
			t.Errorf("%s links to %s, which is not a page in the docs tree", d.page, d.target)
			continue
		}
		t.Errorf("%s links to %s: %s", d.page, d.target, d.reason)
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

// `foo.md` and `foo.mdx` are one page id, not two. Counting files rather than
// ids reported the same orphan twice and inflated the population it claimed to
// have checked — a gate that cannot count what it looked at is hard to believe
// about what it found.
func TestOrphanedPagesCountsPageIdsNotFiles(t *testing.T) {
	root := t.TempDir()
	writeDocsPage(t, root, "docs/guides/dual.md", "# Dual\n")
	writeDocsPage(t, root, "docs/guides/dual.mdx", "---\ntitle: Dual\n---\n")

	orphans, required, err := orphanedPages(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if required != 1 {
		t.Errorf("required = %d, want 1 — two files, one page id", required)
	}
	if !slices.Equal(orphans, []string{"guides/dual"}) {
		t.Errorf("orphanedPages() = %v, want the id reported exactly once", orphans)
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
