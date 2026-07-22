// Package update implements a non-blocking "is there a newer release?" check
// against the project's GitHub Releases API. The result is cached on disk so
// repeated invocations within the TTL hit the local file system instead of
// the network.
//
// The check is intentionally best-effort: any error (offline, rate-limited,
// schema change in the GitHub API) returns nil + a logged warning. A failed
// update check must never break the user's CLI session.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const (
	// LatestReleaseURL is the GitHub API endpoint for the most recent
	// non-prerelease release. Pre-release/beta channels get their own check
	// via LatestPreReleaseURL.
	LatestReleaseURL = "https://api.github.com/repos/crewship-ai/crewship/releases/latest"

	// LatestReleasesURL returns the 5 most recent releases including
	// pre-releases. We use this when the local build's version itself has a
	// pre-release suffix (e.g. v0.1.0-beta.1) so beta users see beta.2, not
	// the older "latest stable" v0.0.x.
	LatestReleasesURL = "https://api.github.com/repos/crewship-ai/crewship/releases?per_page=5"

	// CacheTTL is how long a successful check is reused before we hit the
	// network again. 24h matches Homebrew's auto-update cadence and keeps
	// the rate-limit footprint at <1 request/user/day for unauthenticated
	// GitHub API calls (60/hour limit).
	CacheTTL = 24 * time.Hour

	// requestTimeout caps the network call. Slow boots are worse than missed
	// notifications; 5s is generous for an HTTPS GET to api.github.com.
	requestTimeout = 5 * time.Second
)

// latestNightlyListURL lists enough recent releases to reliably contain the
// newest nightly-<date>-r<n> pre-release even when a stable cut lands in
// between: nightlies are pruned after 7 days (nightly.yml) but can land many
// times a day, so 30 is comfortably more than a single day's worth. A var
// (not const) so tests can point it at a local httptest server.
var latestNightlyListURL = "https://api.github.com/repos/crewship-ai/crewship/releases?per_page=30"

// nightlyVersionPattern matches the nightly release/version format cut by
// nightly.yml: nightly-<UTC-date>-r<workflow-run-number>, e.g.
// "nightly-20260714-r552". The date is fixed-width (YYYYMMDD) and the run
// number strictly increases per push to main, so lexical date comparison
// followed by numeric run comparison sorts builds correctly.
var nightlyVersionPattern = regexp.MustCompile(`^nightly-(\d{8})-r(\d+)$`)

// nightlyVersion is a parsed nightly-<date>-r<n> version, orderable by date
// then run number.
type nightlyVersion struct {
	date string
	run  int
}

// parseNightlyVersion parses the nightly-<date>-r<n> format. Anything else
// (a semver tag, "dev", a hand-built version string) reports ok=false — the
// caller falls back to semver handling or the incomparable-version path.
func parseNightlyVersion(v string) (nightlyVersion, bool) {
	m := nightlyVersionPattern.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return nightlyVersion{}, false
	}
	run, err := strconv.Atoi(m[2])
	if err != nil {
		return nightlyVersion{}, false
	}
	return nightlyVersion{date: m[1], run: run}, true
}

// compareNightlyVersion orders two nightly versions by date then run number,
// returning -1/0/1 like strings.Compare.
func compareNightlyVersion(a, b nightlyVersion) int {
	if a.date != b.date {
		if a.date < b.date {
			return -1
		}
		return 1
	}
	switch {
	case a.run < b.run:
		return -1
	case a.run > b.run:
		return 1
	default:
		return 0
	}
}

// IncomparableVersionError reports an installed version string that is
// neither a recognized semver release nor a nightly-<date>-r<n> build — most
// likely a local build (`go run`, a hand-rolled `make build`) that embedded
// no version at all. It is never a "the check is broken" condition: callers
// should tell the operator plainly rather than surface a raw parse error
// (#1291 — the previous "invalid current version" message was mistaken for
// nightly builds specifically, since those ARE comparable and simply weren't
// recognized).
type IncomparableVersionError struct {
	Version string
}

func (e *IncomparableVersionError) Error() string {
	return fmt.Sprintf(
		"installed version %q cannot be compared against a release — it looks like a local build, not a crewship release or nightly build",
		e.Version,
	)
}

// checkNightly compares a nightly current version against the most recently
// published nightly-* release. Unlike the stable/pre-release path this never
// consults the on-disk cache: nightlies are cut many times a day and pruned
// after 7 days, so a 24h-stale "latest nightly" could already be gone.
func checkNightly(ctx context.Context, currentVersion string, current nightlyVersion) (*Result, error) {
	tag, notes, htmlURL, err := fetchLatestNightly(ctx, latestNightlyListURL)
	if err != nil {
		return nil, err
	}
	latest, ok := parseNightlyVersion(tag)
	if !ok {
		return nil, fmt.Errorf("latest nightly tag %q does not match the nightly-<date>-r<n> format", tag)
	}
	return &Result{
		Current:   currentVersion,
		Latest:    tag,
		Newer:     compareNightlyVersion(latest, current) > 0,
		URL:       htmlURL,
		Notes:     notes,
		CheckedAt: time.Now().UTC(),
	}, nil
}

// Result captures everything the caller needs to render an update banner.
// Newer is the only field a UI consumer needs to gate the banner on; the
// rest power the message body.
type Result struct {
	Current   string    `json:"current"`
	Latest    string    `json:"latest"`
	Newer     bool      `json:"newer"`
	URL       string    `json:"url,omitempty"`
	Notes     string    `json:"notes,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// cacheFile lives under the user's data dir so it survives binary upgrades
// but not full uninstalls. Falls back to a per-OS temp dir if the home dir
// is unavailable (CI, sandboxed environments).
func cacheFile() (string, error) {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".crewship", "cache", "latest_release.json"), nil
	}
	return filepath.Join(os.TempDir(), "crewship-update-check.json"), nil
}

// Check returns the latest-release information for the given current version,
// or nil if the check was skipped. Errors are returned for unrecoverable
// problems (cache write failure with no fallback); transient network/parse
// failures yield a nil result and a non-nil error that callers can log and
// drop.
func Check(ctx context.Context, currentVersion string) (*Result, error) {
	// Skip in development builds — `go run` and unreleased binaries report
	// "dev" via ldflags. There's no meaningful comparison and a banner
	// telling a developer "you're behind" on their own working tree is just
	// noise.
	if currentVersion == "" || currentVersion == "dev" {
		return nil, nil
	}
	if os.Getenv("CREWSHIP_SKIP_UPDATE_CHECK") == "1" {
		return nil, nil
	}

	if nv, ok := parseNightlyVersion(currentVersion); ok {
		return checkNightly(ctx, currentVersion, nv)
	}

	// normalizeVersion adds the leading "v" that golang.org/x/mod/semver
	// requires. Tags ship as v0.1.0 already, but package.json carries 0.1.0
	// and we want the same code path to work for both.
	current := normalizeVersion(currentVersion)
	if !semver.IsValid(current) {
		return nil, &IncomparableVersionError{Version: currentVersion}
	}

	if cached := readCache(); cached != nil && time.Since(cached.CheckedAt) < CacheTTL {
		// Refresh the comparison against the *current* binary's version —
		// the cached "latest" is still accurate but the "newer" flag must be
		// recomputed because the operator may have just upgraded.
		cached.Current = currentVersion
		cached.Newer = semver.Compare(normalizeVersion(cached.Latest), current) > 0
		return cached, nil
	}

	// Pick the appropriate endpoint based on whether the local build is a
	// pre-release. Beta users care about newer betas, not the previous
	// stable release.
	url := LatestReleaseURL
	if semver.Prerelease(current) != "" {
		url = LatestReleasesURL
	}

	latest, notes, htmlURL, err := fetchLatest(ctx, url)
	if err != nil {
		return nil, err
	}

	result := &Result{
		Current:   currentVersion,
		Latest:    latest,
		Newer:     semver.Compare(normalizeVersion(latest), current) > 0,
		URL:       htmlURL,
		Notes:     notes,
		CheckedAt: time.Now().UTC(),
	}
	writeCache(result)
	return result, nil
}

// CheckExplicit is the update check behind an EXPLICIT `crewship self-update`
// (or `--check`): it always hits the network — no 24h disk cache — and
// ignores CREWSHIP_SKIP_UPDATE_CHECK (that env mutes the passive boot banner,
// not a command the user typed on purpose). Dev builds still short-circuit:
// there's no released version to compare against. Compare with Check, which
// powers the cached, opt-out-able background banner.
func CheckExplicit(ctx context.Context, currentVersion string) (*Result, error) {
	if currentVersion == "" || currentVersion == "dev" {
		return nil, nil
	}
	if nv, ok := parseNightlyVersion(currentVersion); ok {
		return checkNightly(ctx, currentVersion, nv)
	}
	current := normalizeVersion(currentVersion)
	if !semver.IsValid(current) {
		return nil, &IncomparableVersionError{Version: currentVersion}
	}
	url := LatestReleaseURL
	if semver.Prerelease(current) != "" {
		url = LatestReleasesURL
	}
	latest, notes, htmlURL, err := fetchLatest(ctx, url)
	if err != nil {
		return nil, err
	}
	// Guard the fetched tag: semver.Compare treats an invalid version as
	// less than any valid one, so a non-semver tag (e.g. a `nightly-*`
	// release surfacing on the list endpoint) must NOT be reported as a
	// newer release to upgrade to. Refuse rather than offer a bogus target.
	normLatest := normalizeVersion(latest)
	if !semver.IsValid(normLatest) {
		return nil, fmt.Errorf("latest release tag %q is not valid semver", latest)
	}
	return &Result{
		Current:   currentVersion,
		Latest:    latest,
		Newer:     semver.Compare(normLatest, current) > 0,
		URL:       htmlURL,
		Notes:     notes,
		CheckedAt: time.Now().UTC(),
	}, nil
}

// fetchLatest hits the GitHub Releases API. When `url` is the single-release
// endpoint we get a JSON object; when it's the list endpoint we get an
// array and pick the first entry (GitHub returns them newest-first).
func fetchLatest(ctx context.Context, url string) (tag, notes, htmlURL string, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "crewship-update-check")
	// GitHub's unauthenticated API quota is 60 req/h *per IP*. Multiple
	// Crewship instances behind a corporate NAT collectively exhaust that
	// budget on simultaneous cold boots. Setting GITHUB_TOKEN (any PAT or
	// fine-grained token with public-repo read scope) bumps the quota to
	// 5000/h per token. Empty value preserves the unauthenticated path.
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		// 404 on the /releases/latest endpoint just means no non-prerelease
		// has been published yet. Treat as a soft no-op rather than an error
		// so first-beta deployments don't log scary messages.
		if resp.StatusCode == http.StatusNotFound {
			return "", "", "", errors.New("no published release")
		}
		return "", "", "", fmt.Errorf("github API status %d", resp.StatusCode)
	}

	type release struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
		Draft   bool   `json:"draft"`
	}

	// Try array first (list endpoint), fall back to single object.
	var list []release
	if err := json.Unmarshal(body, &list); err == nil && len(list) > 0 {
		for _, r := range list {
			if r.Draft {
				continue
			}
			return r.TagName, truncateNotes(r.Body), r.HTMLURL, nil
		}
		return "", "", "", errors.New("no non-draft release in list")
	}

	var single release
	if err := json.Unmarshal(body, &single); err != nil {
		return "", "", "", fmt.Errorf("parse release JSON: %w", err)
	}
	if single.TagName == "" {
		return "", "", "", errors.New("release has empty tag_name")
	}
	return single.TagName, truncateNotes(single.Body), single.HTMLURL, nil
}

// fetchLatestNightly hits a GitHub releases-list endpoint and returns the
// most recent nightly-<date>-r<n> entry. The list is newest-first, but a
// stable release can land between nightly cuts, so this scans past non-
// nightly (and draft) entries rather than trusting index 0.
func fetchLatestNightly(ctx context.Context, url string) (tag, notes, htmlURL string, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "crewship-update-check")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return "", "", "", errors.New("no published release")
		}
		return "", "", "", fmt.Errorf("github API status %d", resp.StatusCode)
	}

	type release struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
		Draft   bool   `json:"draft"`
	}
	var list []release
	if err := json.Unmarshal(body, &list); err != nil {
		return "", "", "", fmt.Errorf("parse release list JSON: %w", err)
	}
	for _, r := range list {
		if r.Draft {
			continue
		}
		if _, ok := parseNightlyVersion(r.TagName); ok {
			return r.TagName, truncateNotes(r.Body), r.HTMLURL, nil
		}
	}
	return "", "", "", errors.New("no nightly-<date>-r<n> release found")
}

// truncateNotes keeps release notes short for the CLI banner. Full notes
// are always one click away via the release URL.
func truncateNotes(body string) string {
	if len(body) > 500 {
		return body[:500] + "..."
	}
	return body
}

// normalizeVersion ensures a leading "v" so semver.Compare doesn't reject
// values that came from package.json or a goreleaser ldflag without the
// prefix.
func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if v[0] != 'v' {
		return "v" + v
	}
	return v
}

// readCache returns the cached Result if present and well-formed, nil
// otherwise. A corrupted cache file is treated as no-cache rather than an
// error — the next Check will overwrite it.
func readCache() *Result {
	path, err := cacheFile()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return nil
	}
	if r.Latest == "" || r.CheckedAt.IsZero() {
		return nil
	}
	return &r
}

func writeCache(r *Result) {
	path, err := cacheFile()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(r)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

// FormatBanner returns the multi-line CLI banner to print when Newer is true.
// Empty string when Newer is false so callers can unconditionally call it.
// The hint chooses between `brew upgrade` and `docker pull` based on how the
// binary was installed, which we can't perfectly detect — we show both.
func FormatBanner(r *Result) string {
	if r == nil || !r.Newer {
		return ""
	}
	return fmt.Sprintf(
		"\n  ┌─ Update available ─────────────────────────────────────────┐\n"+
			"  │  Crewship %s → %s\n"+
			"  │  brew upgrade crewship\n"+
			"  │  docker pull ghcr.io/crewship-ai/crewship:latest\n"+
			"  │  %s\n"+
			"  └────────────────────────────────────────────────────────────┘\n",
		r.Current, r.Latest, r.URL,
	)
}
