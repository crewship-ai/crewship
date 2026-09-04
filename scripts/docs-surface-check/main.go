// Command docs-surface-check verifies the agent-readable Mintlify surface.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type config struct {
	Contextual *struct {
		Options []json.RawMessage `json:"options"`
	} `json:"contextual"`
	Navigation json.RawMessage `json:"navigation"`
}

var frontmatter = regexp.MustCompile(`(?ms)^---\s*\n(.*?)\n---`)
var titleLine = regexp.MustCompile(`(?m)^title:\s*["']?([^"'\n]+)`)
var descriptionLine = regexp.MustCompile(`(?m)^description:\s*["']?([^"'\n]+)`)
var stabilityLine = regexp.MustCompile(`(?m)^stability:\s*["']?([^"'\s]+)`)
var tagLine = regexp.MustCompile(`(?m)^tag:\s*["']?([^"'\n]+)`)
var llmsLink = regexp.MustCompile(`(?m)^- \[[^]]+\]\([^)]*\)`)

var stabilityVocabulary = map[string]bool{
	"stable":       true,
	"early":        true,
	"experimental": true,
	"deprecated":   true,
	"roadmap":      true,
}

// proseLink matches the two forms a page body uses to point at another page:
// the Markdown target `](/guides/routines)` and the JSX attribute
// `href="/guides/routines"` that <Card>, <Column> and friends take. Both are
// required to start with "/" in the capture, which is what excludes external
// URLs, `mailto:`, same-page `#anchor` links and relative paths — the docs tree
// uses all of those and none of them name a page in this repository.
var proseLink = regexp.MustCompile(`\]\((/[^)\s]*)\)|href=["'](/[^"']*)["']`)

// codeFence matches the opening or closing line of a fenced block, indented or
// not. Mintlify accepts both fence characters.
var codeFence = regexp.MustCompile("^\\s*(?:```|~~~)")

// The constructs that put an anchor on a page, or that stand between a heading's
// Markdown source and the text Mintlify slugs.
var (
	atxHeading      = regexp.MustCompile(`^#{1,6}\s+(.*?)\s*$`)
	customHeadingID = regexp.MustCompile(`\s*\{#([A-Za-z0-9_-]+)\}\s*$`)
	openingTag      = regexp.MustCompile(`<([A-Za-z][A-Za-z0-9]*)\b[^>]*>`)
	attributeID     = regexp.MustCompile(`\bid=(?:"([^"]*)"|'([^']*)')`)
	attributeTitle  = regexp.MustCompile(`\btitle=(?:"([^"]*)"|'([^']*)')`)
	markdownLink    = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	jsxTag          = regexp.MustCompile(`</?[A-Za-z][^>]*>`)
	mdxExpression   = regexp.MustCompile(`\{[^{}]*\}`)
	backslashEscape = regexp.MustCompile(`\\(.)`)
)

type deprecatedTerm struct {
	spelling    string
	replacement string
	pattern     *regexp.Regexp
}

var deprecatedTerms = []deprecatedTerm{
	{spelling: "COORDINATOR", replacement: "LEAD", pattern: regexp.MustCompile(`(?i)\bcoordinator\b`)},
}

var allowedDeprecatedOccurrences = map[string][]string{
	"docs/concepts.mdx": {
		"| **`COORDINATOR`** | **`LEAD`** |",
		"Published prose must not introduce `COORDINATOR` outside this replacement record",
	},
	"docs/manifest/agent.md": {
		"The retired `COORDINATOR` value is refused at validate time",
		"**`COORDINATOR` is retired and refused.**",
	},
	"docs/manifest/workspace.md": {
		"the retired `COORDINATOR` value is rejected",
		"**`COORDINATOR` is retired and rejected everywhere.**",
	},
}

// unnavigatedPages and unnavigatedPrefixes are the pages that live in the docs
// tree on purpose without a docs.json entry. Everything else that is not
// declared is an orphan: Mintlify publishes it, no sidebar reaches it, and —
// because llms.txt is generated from the navigation — no agent can find it
// either. Three pages sat in exactly that state (`cli/onboarding`,
// `api-reference/onboarding`, `manifest/page`), hiding all seven `onboarding`
// commands, because every gate this repository had validated nav→file and
// nothing validated file→nav (#2086).
//
// The allowlist is explicit rather than inferred so that adding a page and
// forgetting the navigation entry is a build failure, while the two genuinely
// internal trees stay quiet:
//
//   - `prd/` is product-requirement and report material written for the repo,
//     not for the published site.
//   - `audit-methodology` documents how the audits are run and is linked from
//     the reports that cite it, not from the sidebar.
//
// One category is not here yet because it does not exist yet: Mintlify reads
// `docs/snippets/` as reusable fragments rather than pages, and a fragment must
// NOT be declared in navigation. The tree has no such directory today, so
// adding the entry now would be blessing a path nothing walks. Whoever adds the
// first snippet adds `snippets/` to unnavigatedPrefixes in the same commit —
// this note is here so that arrives as a one-line change rather than a puzzle.
var unnavigatedPages = map[string]bool{
	"audit-methodology": true,
}

// ux/ holds the UI/UX programme's contract, audits and plan — working
// documents for the people and agents changing the product, not pages for
// the people using it.
var unnavigatedPrefixes = []string{"prd/", "ux/"}

func unnavigatedByDesign(page string) bool {
	if unnavigatedPages[page] {
		return true
	}
	for _, prefix := range unnavigatedPrefixes {
		if strings.HasPrefix(page, prefix) {
			return true
		}
	}
	return false
}

func main() {
	root := flag.String("root", ".", "repository root")
	// Empty by default: the repository-side assertions are hermetic and safe
	// to require on every pull request. Pass -url only where a deployed-site
	// comparison is meaningful — see checkServed for why that is never a PR.
	baseURL := flag.String("url", "", "deployed docs URL; empty skips the deployed-site comparison")
	flag.Parse()
	data, err := os.ReadFile(filepath.Join(*root, "docs/docs.json"))
	if err != nil {
		fail(err)
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		fail(err)
	}
	total, good, bad := descriptionQuality(*root)
	fmt.Printf("docs-surface-check: description quality %d/%d good, %d restate their title\n", good, total, bad)
	stabilityIssues, stabilityPages, err := documentationStability(*root)
	if err != nil {
		fail(err)
	}
	if len(stabilityIssues) > 0 {
		fail(fmt.Errorf("documentation stability labels invalid:\n  %s", strings.Join(stabilityIssues, "\n  ")))
	}
	fmt.Printf("docs-surface-check: stability labels %d/%d valid\n", stabilityPages, stabilityPages)
	if cfg.Contextual == nil || len(cfg.Contextual.Options) == 0 {
		fail(fmt.Errorf("docs/docs.json must declare contextual.options"))
	}

	declared := navigationPages(cfg.Navigation)
	if len(declared) == 0 {
		fail(fmt.Errorf("docs/docs.json declares no navigation pages"))
	}
	missing := []string{}
	for _, page := range declared {
		if !fileExists(filepath.Join(*root, "docs", page+".mdx")) && !fileExists(filepath.Join(*root, "docs", page+".md")) {
			missing = append(missing, page)
		}
	}
	if len(missing) > 0 {
		fail(fmt.Errorf("navigation pages missing from docs tree: %s", strings.Join(missing, ", ")))
	}

	// The other direction. The pass above proves every declared id has a file;
	// it says nothing about a file nobody declared, which is why the inventory
	// could report "0 missing" while three published pages were unreachable.
	orphans, required, err := orphanedPages(*root, declared)
	if err != nil {
		fail(err)
	}
	if len(orphans) > 0 {
		fail(fmt.Errorf("published pages missing from docs/docs.json navigation — unreachable from the sidebar and absent from llms.txt:\n  %s\nAdd each to docs/docs.json, or to unnavigatedPages/unnavigatedPrefixes in scripts/docs-surface-check if it is unlisted on purpose.", strings.Join(orphans, "\n  ")))
	}
	fmt.Printf("docs-surface-check: navigation reachability %d pages require an entry, 0 orphaned\n", required)

	// Third pass: the links written inside the pages, not just the ids
	// docs.json declares. Hermetic like the two above — it reads the same
	// tree — so it belongs on every pull request.
	dead, audit, err := brokenProseLinks(*root)
	if err != nil {
		fail(err)
	}
	// The two failures need different edits — one moves the link to another
	// page, the other rewrites the fragment — so they are reported apart.
	missingPages, missingAnchors := []string{}, []string{}
	for _, d := range dead {
		if d.reason == "" {
			missingPages = append(missingPages, fmt.Sprintf("%s links to %s", d.page, d.target))
			continue
		}
		missingAnchors = append(missingAnchors, fmt.Sprintf("%s links to %s: %s", d.page, d.target, d.reason))
	}
	if len(missingPages) > 0 {
		fail(fmt.Errorf("dead internal links in prose (no such page in the docs tree):\n  %s", strings.Join(missingPages, "\n  ")))
	}
	if len(missingAnchors) > 0 {
		fail(fmt.Errorf("internal links whose heading anchor does not exist on the target page:\n  %s", strings.Join(missingAnchors, "\n  ")))
	}
	fmt.Printf("docs-surface-check: internal prose links %d checked, %d of them anchored, 0 dead\n", audit.links, audit.anchors)

	deprecated, err := deprecatedTerminologyInDocs(*root)
	if err != nil {
		fail(err)
	}
	if len(deprecated) > 0 {
		offenders := make([]string, 0, len(deprecated))
		for _, use := range deprecated {
			offenders = append(offenders, fmt.Sprintf("%s uses %s; use %s", use.page, use.spelling, use.replacement))
		}
		fail(fmt.Errorf("deprecated terminology in published docs:\n  %s", strings.Join(offenders, "\n  ")))
	}
	fmt.Printf("docs-surface-check: deprecated terminology 0 uses across %d denied spelling(s)\n", len(deprecatedTerms))

	wrapped, err := jsxHostileCodeSpans(*root)
	if err != nil {
		fail(err)
	}
	if len(wrapped) > 0 {
		offenders := make([]string, 0, len(wrapped))
		for _, w := range wrapped {
			offenders = append(offenders, fmt.Sprintf("%s:%d: %s — keep the whole code span on one line; MDX reads the wrapped `<` as a JSX tag", w.page, w.line, w.text))
		}
		fail(fmt.Errorf("inline code spans wrapped onto a line starting with `<`:\n  %s", strings.Join(offenders, "\n  ")))
	}
	fmt.Printf("docs-surface-check: no inline code span wraps onto a `<` continuation\n")

	// MDX tag safety. Runs after the content passes because a broken tag fails
	// the whole external docs build, and that build reports as a status the
	// repo does not control — on the commit that introduced the last one it
	// said "skipped: Changes superseded by downstream commit", so nothing
	// surfaced until unrelated PRs merged main days later.
	if err := reportUnguardedMDXTags(*root); err != nil {
		fail(err)
	}

	served, err := checkServed(*baseURL, len(declared))
	if err != nil {
		fail(err)
	}

	servedReport := fmt.Sprintf("%d", served)
	if served < 0 {
		servedReport = "not checked (pass -url to compare the deployed index)"
	}
	fmt.Printf("docs-surface-check: contextual options=%d, navigation pages=%d, llms pages=%s\n", len(cfg.Contextual.Options), len(declared), servedReport)
}

// checkServed compares the deployed llms.txt index against the navigation this
// checkout declares. An empty baseURL skips it entirely and reports -1 pages.
//
// This must never run on a pull request, and the default reflects that. The
// deployed site is generated by Mintlify AFTER a merge, so on any PR that adds
// a page the local navigation leads the deployed index BY CONSTRUCTION —
// `served < declared` is then the correct state of a correct PR, and a gate
// that fails there fires on exactly the change it exists to bless. It also
// makes the check non-hermetic: docs/prd/documentation-contract-testing.md
// requires the deterministic layer to reach no external service, because a
// Mintlify outage would otherwise redden unrelated PRs.
//
// Run it on a schedule instead, where "the deployed index is behind the repo"
// is real drift worth someone's attention rather than an artefact of timing.
func checkServed(baseURL string, declared int) (int, error) {
	if strings.TrimSpace(baseURL) == "" {
		return -1, nil
	}
	llms, err := fetch(baseURL + "/llms.txt")
	if err != nil {
		return 0, err
	}
	// Availability is the whole contract for the full index: it carries page
	// bodies rather than a link list, so there is nothing to count.
	if _, err := fetch(baseURL + "/llms-full.txt"); err != nil {
		return 0, err
	}
	served := len(llmsLink.FindAllString(llms, -1))
	if served < declared {
		return served, fmt.Errorf("llms.txt lists %d pages, docs.json declares %d — the deployed index is behind this checkout", served, declared)
	}
	return served, nil
}

// navigationPages returns every page id docs.json declares.
//
// It reads the STRUCTURE — the string entries of a `pages` array — rather than
// guessing which of the file's strings look like a page. The guess it replaces
// (contains "/", or one of a handful of known prefixes and literals) silently
// dropped every top-level page whose id carries neither: `philosophy`,
// `production-checklist` and `architecture` were declared, never counted, and
// so never checked for existence on disk. A heuristic that decides what to
// verify will always have that failure mode — it cannot report the pages it
// did not recognise.
//
// Nested groups keep their own `pages`, so the walk recurses through anything
// it finds rather than assuming a depth.
func navigationPages(raw json.RawMessage) []string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for key, child := range x {
				if key == "pages" {
					if entries, ok := child.([]any); ok {
						for _, entry := range entries {
							if page, ok := entry.(string); ok {
								seen[page] = true
								continue
							}
							walk(entry) // a nested group, not a page id
						}
						continue
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(value)
	pages := make([]string, 0, len(seen))
	for page := range seen {
		pages = append(pages, page)
	}
	sort.Strings(pages)
	return pages
}

// orphanedPages returns every page in the docs tree that docs.json does not
// declare and the allowlist does not excuse, plus the number of pages that were
// required to be declared.
//
// This is the file→nav direction, and it is the one that was missing. It also
// keeps the llms.txt assertion in checkServed honest: that comparison counts
// what the navigation declares, so a page outside the navigation is invisible
// to it by construction — the deployed index can match docs.json exactly while
// a published page is reachable from neither. Measured on the deployed site
// while the three orphans were live: llms.txt listed 302 pages and docs.json
// declared 302, so `served < declared` was false and the check passed. An
// orphan is subtracted from both sides of that comparison, and cancels.
//
// Pages are collected into a set before counting, because `foo.md` and
// `foo.mdx` name ONE page id. Counting the files would report the same orphan
// twice and inflate the total; Mintlify silently serves one of the two.
func orphanedPages(root string, declared []string) ([]string, int, error) {
	inNav := make(map[string]bool, len(declared))
	for _, page := range declared {
		inNav[page] = true
	}
	docs := filepath.Join(root, "docs")
	required := map[string]bool{}
	err := filepath.Walk(docs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || (!strings.HasSuffix(path, ".mdx") && !strings.HasSuffix(path, ".md")) {
			return nil
		}
		rel, err := filepath.Rel(docs, path)
		if err != nil {
			return err
		}
		page := filepath.ToSlash(rel)
		page = strings.TrimSuffix(strings.TrimSuffix(page, ".mdx"), ".md")
		if unnavigatedByDesign(page) {
			return nil
		}
		required[page] = true
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	orphans := []string{}
	for page := range required {
		if !inNav[page] {
			orphans = append(orphans, page)
		}
	}
	sort.Strings(orphans)
	return orphans, len(required), nil
}

// docsClient bounds the deployed-index requests. http.DefaultClient has no
// deadline, so a server that accepts the connection and then stalls would hang
// the scheduled job until the GitHub Actions timeout kills it — a drift check
// that never reports is worse than one that fails.
var docsClient = &http.Client{Timeout: 30 * time.Second}

func fetch(url string) (string, error) {
	resp, err := docsClient.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

// documentationStability verifies the release contract carried by every MDX
// page. Keeping the vocabulary here makes an unknown label a build failure
// instead of an unrendered typo that silently invents a sixth tier.
func documentationStability(root string) ([]string, int, error) {
	docs := filepath.Join(root, "docs")
	issues := []string{}
	pages := 0
	err := filepath.WalkDir(docs, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".mdx" {
			return nil
		}
		pages++
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		match := frontmatter.FindSubmatch(body)
		if len(match) < 2 {
			issues = append(issues, filepath.ToSlash(rel)+": missing frontmatter and stability label")
			return nil
		}
		label := stabilityLine.FindSubmatch(match[1])
		if len(label) < 2 {
			issues = append(issues, filepath.ToSlash(rel)+": missing stability label")
			return nil
		}
		value := strings.ToLower(string(label[1]))
		if !stabilityVocabulary[value] {
			issues = append(issues, fmt.Sprintf("%s: invalid stability label %q", filepath.ToSlash(rel), value))
			return nil
		}
		tag := tagLine.FindSubmatch(match[1])
		if len(tag) < 2 {
			issues = append(issues, filepath.ToSlash(rel)+": missing rendered stability tag")
			return nil
		}
		if strings.ToLower(strings.TrimSpace(string(tag[1]))) != value {
			issues = append(issues, fmt.Sprintf("%s: rendered tag %q does not match stability %q", filepath.ToSlash(rel), strings.TrimSpace(string(tag[1])), value))
		}
		return nil
	})
	sort.Strings(issues)
	return issues, pages, err
}

// deadLink is one internal link that does not land where it says: the page that
// has to be edited, and the target as it is written there.
type deadLink struct {
	page   string
	target string
	// reason is empty when the target names no page in the tree. Otherwise the
	// page exists and this says why the fragment does not resolve — the two
	// failures need different edits, so the report keeps them apart.
	reason string
}

// linkAudit is what the prose-link pass looked at, so a green run can say how
// much it actually verified rather than just that nothing was wrong.
type linkAudit struct {
	links   int // internal links resolved against the tree
	anchors int // of those, the ones that also named a heading anchor
}

// mintlifySlugKeep is the ASCII punctuation Mintlify keeps verbatim in a heading
// anchor. This is NOT github-slugger, which strips all of it: the deployed site
// publishes `#get-/api/v1/admin/memory/config`, `#runtime-bash-|-python-|-go`,
// `#api-+-cli`, `#archive-layer-pr-#212` and `#4-codex-config-in-$home`. Any
// rune outside ASCII is kept too (`—`, `’`, `→`, `…` all survive).
const mintlifySlugKeep = "_-/&+|<>=#$"

// mintlifySlug turns a heading's RENDERED text into the id Mintlify publishes.
//
// Pinned against docs.crewship.ai rather than against a slugger's source: every
// heading id on the 72 pages this tree links into was fetched and compared, and
// this function reproduces all of them. The rules that fall out:
//
//   - lowercase, then split on spaces and rejoin with "-", so a run of dropped
//     characters between two spaces leaves an empty token and therefore a
//     doubled dash: `Secrets ({{ secrets.<type> }})` publishes as
//     `#secrets--secrets-<type>-`, and the trailing dash is real.
//   - inside a token, "." becomes "-" (`routine.state` → `routine-state`) and
//     leading/trailing dashes are trimmed (`1. Writing memory` → `1-writing…`).
//   - letters, digits, anything non-ASCII and mintlifySlugKeep survive;
//     everything else is dropped.
//
// The angle brackets are the case worth remembering: the heading
// "## `crewship routine result <run_id>`" publishes
// `#crewship-routine-result-<run_id>`, and
// Mintlify's own table of contents links to it as
// `#crewship-routine-result-%3Crun_id%3E`. Both `#crewship-routine-result-run_id`
// and `#crewship-routine-result-run-id` are written in this tree and both are
// dead — the underscore was never the whole story.
func mintlifySlug(text string) string {
	tokens := strings.Split(strings.TrimSpace(strings.ToLower(text)), " ")
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		var slug strings.Builder
		for _, r := range token {
			switch {
			case r >= utf8.RuneSelf || unicode.IsLetter(r) || unicode.IsDigit(r):
				slug.WriteRune(r)
			case strings.ContainsRune(mintlifySlugKeep, r):
				slug.WriteRune(r)
			case r == '.':
				slug.WriteByte('-')
			}
		}
		out = append(out, strings.Trim(slug.String(), "-"))
	}
	return strings.Join(out, "-")
}

// anchorKey reduces an anchor to the form the comparison uses. It is where this
// gate is deliberately conservative, and it ignores exactly two things — the two
// the deployed output could not pin.
//
//  1. How many dashes a run of dropped punctuation leaves. The heading
//     "Server installs (systemd): `self-update --systemd`" publishes
//     `#server-installs-systemd--self-update-systemd` while the structurally
//     identical "Contract: `GET …`" publishes a single dash. Nothing in the
//     rendered text distinguishes them, and no reader distinguishes two anchors
//     by dash count either.
//  2. Quotation marks. Mintlify's smart typography curls them unpredictably:
//     `"Run this routine every day at 9 AM"` publishes
//     `#”run-this-routine-every-day-at-9-am` — opening mark curled, closing one
//     dropped — while `"Validate routine DSL in CI before committing"` on the
//     same page publishes the mirror image. Nobody can write that from the
//     source, so a heading in quotes is a heading this gate cannot address
//     exactly; give it a `{#custom-id}` if it has to be linkable.
//
// The APOSTROPHE is not in that second class and is not ignored: `'` between
// two alphanumerics always becomes `’`, on every page checked. renderProseSegment
// applies it, and `#what’s-next` is compared character for character.
//
// Everything else is exact too: word content, the punctuation Mintlify keeps,
// and the -2/-3 suffix on repeated headings.
func anchorKey(anchor string) string {
	if decoded, err := url.PathUnescape(anchor); err == nil {
		anchor = decoded
	}
	var key strings.Builder
	dash := false
	for _, r := range strings.ToLower(anchor) {
		switch r {
		case '"', '\'', '‘', '“', '”':
			continue
		case '-':
			if dash {
				continue
			}
			dash = true
		default:
			dash = false
		}
		key.WriteRune(r)
	}
	return strings.Trim(key.String(), "-")
}

// anchorsOf returns every anchor a page body publishes, in document order.
//
// Headings are most of it, but not all of it: an explicit `{#custom-id}` wins
// over the slug, `<Accordion title=…>` publishes its title as an id, and a
// literal `<a id=…>` publishes whatever it says. Repeated headings get the
// -2/-3 suffix Mintlify appends, which is why this returns an ordered list
// rather than a set — `### Response` appears 40 times on one API page and each
// one is addressable.
func anchorsOf(body string) []string {
	body = stripFrontmatter(body)
	anchors := []string{}
	used := map[string]int{}
	fenced := false
	for _, line := range strings.Split(body, "\n") {
		if codeFence.MatchString(line) {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		if match := atxHeading.FindStringSubmatch(line); match != nil {
			anchors = append(anchors, headingAnchor(match[1], used))
		}
		if anchor, ok := tagAnchor(line); ok {
			anchors = append(anchors, anchor)
		}
	}
	return anchors
}

// tagAnchor returns the anchor an opening tag publishes: an explicit `id=`
// wherever one is written, and otherwise an `<Accordion title=…>`'s title.
//
// The explicit id has to win. `docs/api-reference/internal.mdx` carries
// `<Accordion title="Resolved exception: …" id="tenant-isolation-on-internal-auth-handlers">`
// and the deployed page uses the id, not the title — reading the title alone
// would report the one link pointing there as dead and send someone to "fix" a
// link that works.
func tagAnchor(line string) (string, bool) {
	tag := openingTag.FindStringSubmatch(line)
	if tag == nil {
		return "", false
	}
	if id := attributeID.FindStringSubmatch(tag[0]); id != nil {
		return firstNonEmpty(id[1:]), true
	}
	if tag[1] != "Accordion" {
		return "", false
	}
	if title := attributeTitle.FindStringSubmatch(tag[0]); title != nil {
		return componentSlug(firstNonEmpty(title[1:])), true
	}
	return "", false
}

// componentSlug is the id a Mintlify component derives from its title, and it is
// NOT mintlifySlug. A component keeps nothing: every run of characters outside
// [a-z0-9] collapses to a single dash, underscores included. The accordion
// titled `Run returns 500 "pipeline: concurrency_key rendered to empty value"`
// publishes `#run-returns-500-pipeline-concurrency-key-rendered-to-empty-value`
// — dash, not underscore — while the heading "`crewship routine result
// <run_id>`" keeps both the underscore and the angle brackets. Two slugs on one
// page, verified against all 66 accordions the fetched pages publish.
func componentSlug(title string) string {
	var slug strings.Builder
	dash := false
	for _, r := range strings.ToLower(html.UnescapeString(title)) {
		if r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			slug.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			slug.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(slug.String(), "-")
}

// headingAnchor is the anchor one heading publishes. used carries the
// duplicate counter for the page; pass an empty map to slug a heading alone.
func headingAnchor(text string, used map[string]int) string {
	if match := customHeadingID.FindStringSubmatch(text); match != nil {
		return match[1]
	}
	base := mintlifySlug(renderHeadingText(text))
	used[base]++
	if n := used[base]; n > 1 {
		return fmt.Sprintf("%s-%d", base, n)
	}
	return base
}

// renderHeadingText rebuilds the text Mintlify renders for a heading, because
// the slug is computed from that and not from the Markdown source.
//
// Inline code is literal — `<run_id>` inside backticks is two angle brackets,
// while outside them it is a JSX tag that renders as nothing. That asymmetry is
// why the segments are handled separately rather than with one pass of
// substitutions.
func renderHeadingText(text string) string {
	var rendered strings.Builder
	for i, segment := range splitCodeSpans(text) {
		if i%2 == 1 {
			rendered.WriteString(segment)
			continue
		}
		rendered.WriteString(renderProseSegment(segment))
	}
	return html.UnescapeString(rendered.String())
}

// renderProseSegment renders the non-code part of a heading: link text without
// its target, no JSX tags, no MDX expressions, escapes resolved.
//
// `## PUT /api/v1/admin/rate-limits/{key}` publishes
// `#put-/api/v1/admin/rate-limits/` because MDX evaluates `{key}` and it renders
// as nothing, while `## GET /api/v1/admin/users/\{userId\}/data` escapes the
// braces and publishes `#get-/api/v1/admin/users/userid/data`. Both spellings
// are in this tree.
func renderProseSegment(segment string) string {
	segment = markdownLink.ReplaceAllString(segment, "$1")
	segment = jsxTag.ReplaceAllString(segment, "")
	// Park the escaped braces so the expression sweep cannot see them.
	segment = strings.ReplaceAll(segment, `\{`, "\x01")
	segment = strings.ReplaceAll(segment, `\}`, "\x02")
	for {
		stripped := mdxExpression.ReplaceAllString(segment, "")
		if stripped == segment {
			break
		}
		segment = stripped
	}
	segment = strings.ReplaceAll(segment, "\x01", "{")
	segment = strings.ReplaceAll(segment, "\x02", "}")
	segment = backslashEscape.ReplaceAllString(segment, "$1")
	segment = strings.ReplaceAll(segment, "**", "")
	return smartApostrophe(segment)
}

// smartApostrophe applies the one part of Mintlify's typography that is
// predictable: an apostrophe between two alphanumerics curls, every time. `What's
// next` publishes `#what’s-next`, and the curled form is in the anchor, so a
// checker that skipped this would have to ignore apostrophes entirely and go
// blind to a whole class of renames.
//
// Quotation marks get no such treatment — see anchorKey for why.
func smartApostrophe(segment string) string {
	if !strings.Contains(segment, "'") {
		return segment
	}
	runes := []rune(segment)
	for i := 1; i < len(runes)-1; i++ {
		if runes[i] != '\'' {
			continue
		}
		if isAlphanumeric(runes[i-1]) && isAlphanumeric(runes[i+1]) {
			runes[i] = '’'
		}
	}
	return string(runes)
}

func isAlphanumeric(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// splitCodeSpans returns alternating prose and inline-code segments: even
// indexes are prose, odd indexes are code.
func splitCodeSpans(text string) []string {
	segments := []string{}
	var current strings.Builder
	for i := 0; i < len(text); {
		if text[i] == '`' {
			for i < len(text) && text[i] == '`' {
				i++
			}
			segments = append(segments, current.String())
			current.Reset()
			continue
		}
		current.WriteByte(text[i])
		i++
	}
	return append(segments, current.String())
}

// stripFrontmatter removes a leading `---` block. It anchors on the start of the
// file rather than on any line, so a `---` horizontal rule in the prose cannot
// swallow half a page.
func stripFrontmatter(body string) string {
	if !strings.HasPrefix(body, "---\n") {
		return body
	}
	rest := body[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return body
	}
	rest = rest[end+len("\n---"):]
	if line := strings.IndexByte(rest, '\n'); line >= 0 {
		return rest[line+1:]
	}
	return ""
}

func firstNonEmpty(values []string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type deprecatedTermUse struct {
	page        string
	spelling    string
	replacement string
}

func deprecatedTerminologyInDocs(root string) ([]deprecatedTermUse, error) {
	docsRoot := filepath.Join(root, "docs")
	offenders := []deprecatedTermUse{}
	err := filepath.Walk(docsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if path == filepath.Join(docsRoot, "prd") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".mdx") && !strings.HasSuffix(path, ".md") {
			return nil
		}
		page := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(body)
		for _, allowed := range allowedDeprecatedOccurrences[page] {
			// Remove only the exact reviewed compatibility wording. Any extra
			// deprecated term on the same page — even on the same line — stays
			// in text and is reported below.
			text = strings.Replace(text, allowed, "", 1)
		}
		for _, term := range deprecatedTerms {
			if term.pattern.MatchString(text) {
				offenders = append(offenders, deprecatedTermUse{page: page, spelling: term.spelling, replacement: term.replacement})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(offenders, func(i, j int) bool {
		if offenders[i].page != offenders[j].page {
			return offenders[i].page < offenders[j].page
		}
		return offenders[i].spelling < offenders[j].spelling
	})
	return offenders, nil
}

type wrappedCodeSpan struct {
	page string
	line int
	text string
}

// wrapsOntoTag reports whether a line is the tail of an inline code span that
// wrapped onto a `<`.
//
// The test is the shape itself — a tag-like token at the very start of the
// line, immediately closed by a backtick — rather than tracking whether a span
// is open across lines. Backtick parity cannot do this job: a legitimate
// ``` “ ` “ ``` (a backtick shown as code) makes any naive count odd, and
// docs/guides/keeper.mdx has exactly that, so a parity-based check reports its
// `</Step>` as an offender. A closing JSX tag never has a backtick welded to
// its `>`, so this rule ignores it without needing an exception.
func wrapsOntoTag(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	for _, marker := range []string{"- ", "* ", "+ "} {
		trimmed = strings.TrimPrefix(trimmed, marker)
	}
	if !strings.HasPrefix(trimmed, "<") {
		return false
	}
	close := strings.Index(trimmed, ">")
	if close < 0 {
		return false
	}
	return strings.HasPrefix(trimmed[close+1:], "`")
}

// jsxHostileCodeSpans finds an inline code span opened on one line and closed
// on the next, where the continuation line begins with `<`.
//
// Markdown renders that fine, MDX does not: the parser reaches the `<` before
// the span is resolved and reads it as the start of a JSX tag, so
// `crewship routine save --author-agent\n<slug|id>` fails with "Unexpected
// character `|` in name". A tag-shaped continuation like `<id>` is just as
// broken — it parses as an unclosed element rather than erroring on a
// character, which is harder to read in the log, not safer.
//
// Why this is a gate and not a one-time fix (#1794 fallout): Mintlify only
// re-parses a page some commit actually touched, so a wrapped span sits
// silently for months and then fails the deploy for whoever next edits that
// file — as it did for three pages here, none of them authored by the PR that
// surfaced them. The cost lands on the wrong person, which is exactly the
// shape a cheap static check should absorb.
func jsxHostileCodeSpans(root string) ([]wrappedCodeSpan, error) {
	docsRoot := filepath.Join(root, "docs")
	offenders := []wrappedCodeSpan{}
	err := filepath.Walk(docsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if path == filepath.Join(docsRoot, "prd") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".mdx") && !strings.HasSuffix(path, ".md") {
			return nil
		}
		page := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Keep the raw lines so a reported position is the one an editor will
		// jump to; stripping frontmatter first shifts every number by the size
		// of a block that varies per page.
		lines := strings.Split(string(body), "\n")
		start := 0
		if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
			for i := 1; i < len(lines); i++ {
				if strings.TrimSpace(lines[i]) == "---" {
					start = i + 1
					break
				}
			}
		}
		// Shares fenceState with the MDX tag pass rather than toggling on a
		// "```" prefix. The naive toggle is wrong on a nested fence — an inner
		// ```yaml inside an outer ```markdown reads as a close, and everything
		// after it is then treated as prose. docs/guides/skills-authoring.mdx
		// has exactly that shape at :80/:83. Two fence implementations that
		// disagree is worse than one, whichever is right.
		var fence fenceState
		for i := start; i < len(lines); i++ {
			line := lines[i]
			if fence.feed(line) || fence.inside() {
				continue
			}
			if wrapsOntoTag(line) {
				offenders = append(offenders, wrappedCodeSpan{page: page, line: i + 1, text: strings.TrimSpace(line)})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(offenders, func(i, j int) bool {
		if offenders[i].page != offenders[j].page {
			return offenders[i].page < offenders[j].page
		}
		return offenders[i].line < offenders[j].line
	})
	return offenders, nil
}

// brokenProseLinks resolves every internal link written inside a page body
// against the docs tree, and returns the ones that do not land where they say.
//
// docs.json's navigation is gated; the links inside the prose were not, so
// `](/guides/does-not-exist)` passed every check this repository had and
// Mintlify's build too. The orientation-layer pages (#1770/#1771) added 28 such
// links and they were verified by a shell loop pasted into a review comment,
// which is a check that exists only while someone remembers to run it (#1774).
//
// A fragment is verified, not resolved away. `/guides/routines#cross-run-state`
// addresses a position inside a page, and a link that lands on the right page
// and the wrong place is broken for the reader — a renamed heading used to ship
// green here (#1794). The page must exist AND the anchor must be one the page
// publishes; see anchorsOf and mintlifySlug for how that namespace is derived,
// and anchorKey for the two things the comparison deliberately ignores.
func brokenProseLinks(root string) ([]deadLink, linkAudit, error) {
	docs := filepath.Join(root, "docs")
	audit := linkAudit{}
	dead := []deadLink{}
	// One page is the target of many links; parse each one once.
	published := map[string][]string{}
	err := filepath.Walk(docs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || (!strings.HasSuffix(path, ".mdx") && !strings.HasSuffix(path, ".md")) {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		page := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		for _, target := range internalLinks(string(body)) {
			audit.links++
			resolved := strings.TrimPrefix(pageOf(target), "/")
			targetPath, ok := docsPage(docs, resolved)
			if !ok {
				dead = append(dead, deadLink{page: page, target: target})
				continue
			}
			fragment := fragmentOf(target)
			if fragment == "" {
				continue
			}
			audit.anchors++
			anchors, ok := published[targetPath]
			if !ok {
				targetBody, err := os.ReadFile(targetPath)
				if err != nil {
					return err
				}
				anchors = anchorsOf(string(targetBody))
				published[targetPath] = anchors
			}
			if hasAnchor(anchors, fragment) {
				continue
			}
			dead = append(dead, deadLink{page: page, target: target, reason: noSuchAnchor(fragment, anchors)})
		}
		return nil
	})
	if err != nil {
		return nil, audit, err
	}
	sort.Slice(dead, func(i, j int) bool {
		if dead[i].page != dead[j].page {
			return dead[i].page < dead[j].page
		}
		return dead[i].target < dead[j].target
	})
	return dead, audit, nil
}

// docsPage returns the file backing a page id, trying both extensions Mintlify
// accepts.
func docsPage(docs, resolved string) (string, bool) {
	for _, ext := range []string{".mdx", ".md"} {
		path := filepath.Join(docs, filepath.FromSlash(resolved)+ext)
		if fileExists(path) {
			return path, true
		}
	}
	return "", false
}

// fragmentOf returns the anchor a link target names, with any trailing query
// stripped. An empty result means the link addresses the page as a whole.
func fragmentOf(target string) string {
	_, fragment, ok := strings.Cut(target, "#")
	if !ok {
		return ""
	}
	fragment, _, _ = strings.Cut(fragment, "?")
	return fragment
}

func hasAnchor(anchors []string, fragment string) bool {
	want := anchorKey(fragment)
	// `page#` and `page#top` both address the top of the document — the second
	// by the HTML fragment-navigation rules, which fall back to the start of the
	// document when nothing carries that id. Neither needs a heading.
	if want == "" || strings.EqualFold(want, "top") {
		return true
	}
	for _, anchor := range anchors {
		if anchorKey(anchor) == want {
			return true
		}
	}
	return false
}

// noSuchAnchor explains a dead fragment and, where one is obvious, names the
// anchor that was probably meant. "27 dead anchors" sends the reader back to
// the hand-rolled loop this gate replaces; "you wrote X, the page publishes Y"
// is a diff someone can apply.
func noSuchAnchor(fragment string, anchors []string) string {
	want := anchorKey(fragment)
	best, bestScore := "", 0
	for _, anchor := range anchors {
		score := commonPrefixLen(want, anchorKey(anchor))
		if score > bestScore {
			best, bestScore = anchor, score
		}
	}
	// Half the written fragment has to agree before a suggestion is more help
	// than noise.
	if best != "" && bestScore*2 >= len(want) {
		return fmt.Sprintf("no heading publishes #%s — nearest is #%s", fragment, best)
	}
	return fmt.Sprintf("no heading publishes #%s", fragment)
}

func commonPrefixLen(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// internalLinks returns every absolute internal target a page body points at,
// in the order they are written.
//
// Fenced blocks are skipped. They hold transcripts and Markdown samples, not
// navigation — the issue asking for this gate quotes `](/guides/does-not-exist)`
// as its own illustration, and a check that reddens on the page explaining it
// is a check that gets deleted rather than obeyed.
func internalLinks(body string) []string {
	targets := []string{}
	fenced := false
	for _, line := range strings.Split(body, "\n") {
		if codeFence.MatchString(line) {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		for _, match := range proseLink.FindAllStringSubmatch(line, -1) {
			if match[1] != "" {
				targets = append(targets, match[1])
				continue
			}
			targets = append(targets, match[2])
		}
	}
	return targets
}

// pageOf reduces a link target to the page id it addresses: fragment and query
// dropped, trailing slash trimmed, and the site root resolved to `index`, which
// is the page Mintlify serves there. Both spellings are legal to write, and a
// gate that reddens on a working link gets argued with rather than obeyed.
func pageOf(target string) string {
	page := target
	if i := strings.IndexAny(page, "#?"); i >= 0 {
		page = page[:i]
	}
	page = strings.TrimSuffix(page, "/")
	if page == "" {
		return "/index"
	}
	return page
}

func descriptionQuality(root string) (total, good, bad int) {
	_ = filepath.Walk(filepath.Join(root, "docs"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".mdx") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		match := frontmatter.FindStringSubmatch(string(data))
		if len(match) != 2 {
			return nil
		}
		title := titleLine.FindStringSubmatch(match[1])
		desc := descriptionLine.FindStringSubmatch(match[1])
		if len(title) != 2 || len(desc) != 2 {
			return nil
		}
		total++
		if strings.TrimSpace(strings.Trim(desc[1], " \t\"'")) == strings.TrimSpace(strings.Trim(title[1], " \t\"'")) {
			bad++
		} else {
			good++
		}
		return nil
	})
	return
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }
func fail(err error)              { fmt.Fprintln(os.Stderr, "docs-surface-check:", err); os.Exit(1) }
