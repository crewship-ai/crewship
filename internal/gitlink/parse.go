// Package gitlink turns a pasted pull-request / merge-request URL into a
// provider-qualified reference, and fetches that reference's current state
// through the provider's REST API.
//
// It is the "link-first" half of the Git integration: a user or an agent
// pastes a URL onto an issue, Crewship recognises it and attaches what the
// provider says about it. No webhooks, no publicly reachable instance, and
// the same shape for GitHub and GitLab — which is what makes it work for a
// self-hosted Crewship talking to a self-hosted forge.
//
// The package is a leaf: std-lib plus internal/httpsafe. It holds no database
// and no credential store; the caller resolves a token and hands it to Fetch.
package gitlink

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Provider is the forge a link points at. The values match the credential
// provider enum in internal/credprovider so the resolver can look a token up
// by the same string it stored.
type Provider string

const (
	ProviderGitHub Provider = "GITHUB"
	ProviderGitLab Provider = "GITLAB"
)

// Kind discriminates what an external link points at. Only pull requests /
// merge requests are recognised today; the type exists so a later commit or
// branch link is a new constant rather than a new table.
type Kind string

// KindPullRequest covers both a GitHub pull request and a GitLab merge
// request — one concept, two vendor names.
const KindPullRequest Kind = "pull_request"

// ErrUnsupportedURL is returned by Parse for anything that is not a
// recognised pull-request / merge-request URL. Callers should surface the
// wrapped message: it names which part of the shape was wrong.
var ErrUnsupportedURL = errors.New("gitlink: not a recognised pull-request or merge-request URL")

// maxOwnerSegments caps how deep a GitLab group path may nest. GitLab's own
// limit is 20 levels; the cap here exists so a paste with a thousand slashes
// cannot become a thousand-segment provider API path.
const maxOwnerSegments = 24

// Ref is a parsed link: which forge, which repository, which pull request.
type Ref struct {
	Provider Provider
	// Host is the lowercased host, port included when the URL carried one.
	// For GitHub this is the WEB host (github.com), not the API host.
	Host string
	// Owner is a GitHub owner ("crewship-ai") or a GitLab group path,
	// which may nest ("acme/platform/backend").
	Owner  string
	Repo   string
	Number int
	Kind   Kind
	// Scheme is the scheme the URL was pasted with. Preserved rather than
	// forced to https because a self-hosted forge on a plain-http intranet
	// address is a real deployment; whether we are willing to DIAL it is a
	// separate decision, made by the Client, not here.
	Scheme string
	// URL is the canonical web URL rebuilt from the parse — the same PR
	// pasted as /pull/7, /pull/7/files and /pull/7#comment all normalise to
	// one string.
	URL string
}

// Parse recognises a pull-request / merge-request URL.
//
// # How self-hosted GitHub is told apart from self-hosted GitLab
//
// Not by the host. `github.com` and `gitlab.com` are two hosts out of an
// unbounded set: GitHub Enterprise Server and self-managed GitLab both live on
// whatever name the operator chose, and `code.acme.example` is as likely to be
// one as the other. A hostname allowlist would recognise the two SaaS hosts
// and fail every self-hosted instance, which is precisely the deployment this
// feature exists for. Probing the host to find out (GET /api/v4/version, then
// /api/v3) would turn a paste into an unauthenticated fingerprint request
// against a user-named host — a worse idea than the problem it solves.
//
// The discriminator is the URL PATH GRAMMAR, which is a product-level constant
// rather than a deployment choice:
//
//	GitHub   /{owner}/{repo}/pull/{n}
//	GitLab   /{group}/…/{repo}/-/merge_requests/{n}      (and the pre-11.0
//	         /{group}/…/{repo}/merge_requests/{n} form)
//
// Every GitHub instance, SaaS or Enterprise, serves `/pull/`; no GitLab
// instance does, and vice versa for `/merge_requests/`. The grammars do not
// overlap, so one host serving both products still yields the right answer per
// URL. The cost of being wrong is also bounded in the right direction: a
// misdetected provider produces a 404 from an API that does not exist, not a
// request to the wrong place with the right token.
//
// The other half of the grammar is a security control. GitLab groups nest and
// GitHub owners do not, so "everything before the repo segment" is the owner
// on GitLab and exactly one segment on GitHub — which means the segment count
// is checked, not inferred. Segments are then validated against a conservative
// character set, because owner and repo are concatenated into the provider API
// path: `..%2f..%2fadmin` as a repo name would otherwise walk out of
// /repos/{owner}/{repo}/ into a different endpoint.
func Parse(raw string) (Ref, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Ref{}, fmt.Errorf("%w: empty", ErrUnsupportedURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Ref{}, fmt.Errorf("%w: %v", ErrUnsupportedURL, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return Ref{}, fmt.Errorf("%w: scheme %q is not http(s)", ErrUnsupportedURL, u.Scheme)
	}
	if u.User != nil {
		// A pasted URL carrying a token would end up in the DB, the audit
		// log and the CLI's stdout. Refuse before any of that.
		return Ref{}, fmt.Errorf("%w: the URL carries credentials (user:password@host) — paste the plain link", ErrUnsupportedURL)
	}
	host := normalizeHost(strings.ToLower(u.Host), scheme)
	if host == "" {
		return Ref{}, fmt.Errorf("%w: missing host", ErrUnsupportedURL)
	}

	// u.Path is already percent-decoded. Splitting the DECODED path is what
	// makes "..%2f..%2fadmin" show up as a segment containing slashes and
	// dots rather than as an opaque repo name, so the validation below sees
	// it. Never split u.RawPath here.
	segs := splitPath(u.Path)

	switch {
	case indexOf(segs, "pull") >= 0 || indexOf(segs, "pulls") >= 0:
		return parseGitHub(scheme, host, segs)
	case indexOf(segs, "merge_requests") >= 0:
		return parseGitLab(scheme, host, segs)
	default:
		return Ref{}, fmt.Errorf(
			"%w: %s has no /pull/<n> (GitHub) or /-/merge_requests/<n> (GitLab) segment",
			ErrUnsupportedURL, u.Path)
	}
}

// parseGitHub reads /{owner}/{repo}/pull/{n}[/…].
func parseGitHub(scheme, host string, segs []string) (Ref, error) {
	i := indexOf(segs, "pull")
	if i < 0 {
		i = indexOf(segs, "pulls")
	}
	// GitHub nests neither owners nor repos: the marker is always the third
	// segment. Requiring that (rather than "somewhere after two segments")
	// is what stops /a/b/c/pull/1 from being read as owner=a repo=b.
	if i != 2 {
		return Ref{}, fmt.Errorf(
			"%w: a GitHub pull-request URL is /<owner>/<repo>/pull/<number>", ErrUnsupportedURL)
	}
	if len(segs) < 4 {
		return Ref{}, fmt.Errorf("%w: no pull-request number after /pull/", ErrUnsupportedURL)
	}
	number, err := parseNumber(segs[3])
	if err != nil {
		return Ref{}, err
	}
	owner, repo := segs[0], segs[1]
	if err := validatePathSegments(owner, repo); err != nil {
		return Ref{}, err
	}
	return Ref{
		Provider: ProviderGitHub,
		Host:     host,
		Owner:    owner,
		Repo:     repo,
		Number:   number,
		Kind:     KindPullRequest,
		Scheme:   scheme,
		URL:      fmt.Sprintf("%s://%s/%s/%s/pull/%d", scheme, host, owner, repo, number),
	}, nil
}

// parseGitLab reads /{group}/…/{repo}[/-]/merge_requests/{n}[/…].
func parseGitLab(scheme, host string, segs []string) (Ref, error) {
	i := indexOf(segs, "merge_requests")
	if len(segs) < i+2 {
		return Ref{}, fmt.Errorf("%w: no merge-request number after /merge_requests/", ErrUnsupportedURL)
	}
	number, err := parseNumber(segs[i+1])
	if err != nil {
		return Ref{}, err
	}
	// The "-" separator has been in GitLab's project routes since 11.0 and
	// is optional in old links, so accept both and treat it as not part of
	// the project path either way.
	repoIdx := i - 1
	if repoIdx >= 0 && segs[repoIdx] == "-" {
		repoIdx--
	}
	// repoIdx is the repo; everything before it is the group path, and there
	// must be at least one group segment (GitLab projects always live under
	// a namespace).
	if repoIdx < 1 {
		return Ref{}, fmt.Errorf(
			"%w: a GitLab merge-request URL is /<group>/<project>/-/merge_requests/<number>", ErrUnsupportedURL)
	}
	ownerSegs := segs[:repoIdx]
	if len(ownerSegs) > maxOwnerSegments {
		return Ref{}, fmt.Errorf("%w: group path nests %d levels (max %d)",
			ErrUnsupportedURL, len(ownerSegs), maxOwnerSegments)
	}
	repo := segs[repoIdx]
	if err := validatePathSegments(append(append([]string{}, ownerSegs...), repo)...); err != nil {
		return Ref{}, err
	}
	owner := strings.Join(ownerSegs, "/")
	return Ref{
		Provider: ProviderGitLab,
		Host:     host,
		Owner:    owner,
		Repo:     repo,
		Number:   number,
		Kind:     KindPullRequest,
		Scheme:   scheme,
		URL:      fmt.Sprintf("%s://%s/%s/%s/-/merge_requests/%d", scheme, host, owner, repo, number),
	}, nil
}

// ProjectPath is "owner/repo" — the GitLab project path, and a convenient
// display form for GitHub too.
func (r Ref) ProjectPath() string { return r.Owner + "/" + r.Repo }

// APIEndpointFor is the provider REST URL that answers "what is the state of
// this pull request", addressed to `host`.
//
// # Why the host is an argument and not r.Host
//
// r.Host comes from a URL somebody pasted. `host` comes from the caller's own
// records: the `account_label` of the credential this workspace stores for that
// forge, or — for github.com / gitlab.com only — a canonical-host constant. The
// two are equal in every real call (the caller matched one against the other to
// find the credential in the first place, and Fetch refuses the pair if they
// ever disagree), so this changes no destination. What it changes is where the
// string that names the destination COMES FROM.
//
// That is the difference between "the request goes somewhere we vetted" being
// true because of a check several call frames away, and it being true because
// there is no other value available to build the URL out of. It is also the
// remedy `go/request-forgery` asks for in as many words — "maintain a list of
// authorized request targets and choose from that list based on the user input
// provided" — and the reason the alert on this file is closed rather than
// dismissed: with the host supplied by the caller, no user-controlled value
// reaches the authority component of the request URL at all.
//
// The remaining user-derived parts — owner, repo, number — are placed strictly
// AFTER a literal path separator, and each has already been checked against
// [validatePathSegments], so none of them can reshape the endpoint. The scheme
// is rebuilt from a constant rather than carried through, so a paste can only
// select between "https" and "http", never introduce a third scheme.
//
// # Provider layouts
//
// GitHub SaaS answers on api.github.com; GitHub Enterprise Server answers on
// the SAME host under /api/v3 (this is the documented GHES layout and the
// reason the API base cannot simply be derived by string-prefixing "api.").
// GitLab always answers on its own host under /api/v4, SaaS included, and
// addresses a project by its URL-encoded path.
//
// The concatenation is written with `+` and constant separators rather than
// fmt.Sprintf on purpose: it is the form in which the boundary between "host"
// and "everything a user chose" is visible to a reader in one line, without
// having to align a format string against its arguments.
func (r Ref) APIEndpointFor(host string) string {
	// Constant, not r.Scheme: an http:// paste selects the "http" literal
	// rather than flowing its own string into the URL.
	scheme := "https"
	if r.Scheme == "http" {
		scheme = "http"
	}
	switch r.Provider {
	case ProviderGitLab:
		return scheme + "://" + host +
			"/api/v4/projects/" + url.PathEscape(r.ProjectPath()) +
			"/merge_requests/" + strconv.Itoa(r.Number)
	default:
		base := scheme + "://" + host + "/api/v3"
		if host == "github.com" || host == "www.github.com" {
			base = "https://api.github.com"
		}
		return base +
			"/repos/" + url.PathEscape(r.Owner) +
			"/" + url.PathEscape(r.Repo) +
			"/pulls/" + strconv.Itoa(r.Number)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────

// normalizeHost drops a port that merely restates the scheme's default.
//
// Host is not cosmetic — three things key off it, and all three break on a
// gratuitous ":443". Attaching https://github.com:443/acme/thing/pull/7 to an
// issue that already carries https://github.com/acme/thing/pull/7 would miss
// the duplicate check (`WHERE … host = ?`) and store the same pull request
// twice; credential resolution would look for a credential labelled
// "github.com:443" and find none; and APIEndpointFor would send the request to
// https://github.com:443/api/v3/… — the GitHub Enterprise layout — instead of
// api.github.com. A non-default port is meaningful and is kept
// (gitlab.acme.internal:8443).
//
// Only the exact scheme-default pairing is stripped: :443 under https and :80
// under http. An https URL that really is served on :80 keeps its port.
func normalizeHost(host, scheme string) string {
	switch {
	case scheme == "https" && strings.HasSuffix(host, ":443"):
		return strings.TrimSuffix(host, ":443")
	case scheme == "http" && strings.HasSuffix(host, ":80"):
		return strings.TrimSuffix(host, ":80")
	default:
		return host
	}
}

func splitPath(p string) []string {
	out := make([]string, 0, 8)
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func indexOf(segs []string, want string) int {
	for i, s := range segs {
		if s == want {
			return i
		}
	}
	return -1
}

func parseNumber(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: %q is not a positive pull-request number", ErrUnsupportedURL, s)
	}
	return n, nil
}

// validatePathSegments rejects anything that would not survive being placed
// back into a provider API path. GitHub and GitLab both restrict namespace and
// project slugs to alphanumerics plus `.`, `_` and `-`, so this rejects no
// real repository — while `.`, `..`, a slash or a backslash (all reachable
// through percent-encoding in the pasted URL) are refused before they can
// reshape /repos/{owner}/{repo}/pulls/{n} into some other endpoint.
func validatePathSegments(segs ...string) error {
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return fmt.Errorf("%w: %q is not a valid repository path segment", ErrUnsupportedURL, s)
		}
		for _, r := range s {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			case r == '.', r == '_', r == '-':
			default:
				return fmt.Errorf("%w: %q contains a character not allowed in a repository path", ErrUnsupportedURL, s)
			}
		}
	}
	return nil
}
