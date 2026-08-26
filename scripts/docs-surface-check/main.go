// Command docs-surface-check verifies the agent-readable Mintlify surface.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
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
		"LEAD | AGENT | COORDINATOR (server default: AGENT)",
		"One of `LEAD` \\| `AGENT` \\| `COORDINATOR`.",
		"**`COORDINATOR` is effectively unsupported — prefer `AGENT`/`LEAD`**",
		"**`COORDINATOR` is asymmetric — and effectively unsupported.**",
		"still admits `COORDINATOR`, but:",
		"`COORDINATOR` survives in the",
	},
	"docs/manifest/workspace.md": {
		"`COORDINATOR` is rejected in the nested form",
		"**`COORDINATOR` is not valid in nested bundles.**",
		"accepts `COORDINATOR` in its own front-end validator",
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
var unnavigatedPages = map[string]bool{
	"audit-methodology": true,
}

var unnavigatedPrefixes = []string{"prd/"}

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
	orphans, reachable, err := orphanedPages(*root, declared)
	if err != nil {
		fail(err)
	}
	if len(orphans) > 0 {
		fail(fmt.Errorf("published pages missing from docs/docs.json navigation — unreachable from the sidebar and absent from llms.txt:\n  %s\nAdd each to docs/docs.json, or to unnavigatedPages/unnavigatedPrefixes in scripts/docs-surface-check if it is unlisted on purpose.", strings.Join(orphans, "\n  ")))
	}
	fmt.Printf("docs-surface-check: navigation reachability %d/%d pages declared, 0 orphaned\n", reachable, reachable)

	// Third pass: the links written inside the pages, not just the ids
	// docs.json declares. Hermetic like the two above — it reads the same
	// tree — so it belongs on every pull request.
	dead, checkedLinks, err := brokenProseLinks(*root)
	if err != nil {
		fail(err)
	}
	if len(dead) > 0 {
		offenders := make([]string, 0, len(dead))
		for _, d := range dead {
			offenders = append(offenders, fmt.Sprintf("%s links to %s", d.page, d.target))
		}
		fail(fmt.Errorf("dead internal links in prose (no such page in the docs tree):\n  %s", strings.Join(offenders, "\n  ")))
	}
	fmt.Printf("docs-surface-check: internal prose links %d checked, 0 dead\n", checkedLinks)

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
// a published page is reachable from neither.
func orphanedPages(root string, declared []string) ([]string, int, error) {
	inNav := make(map[string]bool, len(declared))
	for _, page := range declared {
		inNav[page] = true
	}
	docs := filepath.Join(root, "docs")
	orphans := []string{}
	reachable := 0
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
		reachable++
		if !inNav[page] {
			orphans = append(orphans, page)
		}
		return nil
	})
	if err != nil {
		return nil, reachable, err
	}
	sort.Strings(orphans)
	return orphans, reachable, nil
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

// deadLink is one internal link whose target is not a page in the docs tree:
// the page that has to be edited, and the target as it is written there.
type deadLink struct {
	page   string
	target string
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

// brokenProseLinks resolves every internal link written inside a page body
// against the docs tree, and returns the ones that do not land on a file.
//
// docs.json's navigation is gated; the links inside the prose were not, so
// `](/guides/does-not-exist)` passed every check this repository had and
// Mintlify's build too. The orientation-layer pages (#1770/#1771) added 28 such
// links and they were verified by a shell loop pasted into a review comment,
// which is a check that exists only while someone remembers to run it (#1774).
//
// Fragments are dropped before resolution: `/guides/routines#cross-run-state`
// addresses a position inside a page, and the page is what must exist. Whether
// the *heading* behind that fragment still exists is deliberately NOT checked
// here — see the note in docs/prd/documentation-contract-testing.md. It needs a
// slugger that agrees with Mintlify's character-for-character, and the real
// tree shows both failure directions: `#crewship-routine-result-run_id` is the
// live anchor for the heading "crewship routine result <run_id>", so a slugger
// that drops the underscore reports the working link and blesses the dead
// `#crewship-routine-result-run-id` written two pages over. A gate that lands
// with a pile of ambiguous offenders is a gate someone turns off.
func brokenProseLinks(root string) ([]deadLink, int, error) {
	docs := filepath.Join(root, "docs")
	checked := 0
	dead := []deadLink{}
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
			checked++
			resolved := strings.TrimPrefix(pageOf(target), "/")
			if fileExists(filepath.Join(docs, filepath.FromSlash(resolved)+".mdx")) ||
				fileExists(filepath.Join(docs, filepath.FromSlash(resolved)+".md")) {
				continue
			}
			dead = append(dead, deadLink{page: page, target: target})
		}
		return nil
	})
	if err != nil {
		return nil, checked, err
	}
	sort.Slice(dead, func(i, j int) bool {
		if dead[i].page != dead[j].page {
			return dead[i].page < dead[j].page
		}
		return dead[i].target < dead[j].target
	})
	return dead, checked, nil
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
