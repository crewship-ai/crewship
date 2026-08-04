package gitlink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/httpsafe"
)

// State is the normalised lifecycle of a pull request / merge request. The
// two providers spell four states six different ways; this is the one enum
// stored, rendered and (in phase 2) transitioned on.
type State string

const (
	StateOpen   State = "OPEN"
	StateDraft  State = "DRAFT"
	StateMerged State = "MERGED"
	StateClosed State = "CLOSED"
)

// Failure modes, each distinguishable at the call site so the HTTP layer can
// tell the user what to actually do about it. "The fetch failed" is not an
// actionable error; "your token was revoked" and "the repo is private to this
// token" and "GitHub is rate-limiting us until 14:05" are.
var (
	// ErrBlockedHost — the host is one this instance refuses to dial:
	// loopback/RFC1918 without the operator opt-in, or cloud metadata and
	// friends, ever. Also covers a plain-http target in strict mode.
	ErrBlockedHost = errors.New("gitlink: refusing to fetch from this host")
	// ErrUnauthorized — the provider rejected the credential (401).
	ErrUnauthorized = errors.New("gitlink: the stored credential was rejected by the provider")
	// ErrForbidden — authenticated but not allowed (403, not a rate limit).
	ErrForbidden = errors.New("gitlink: the stored credential may not read this repository")
	// ErrNotFound — 404. On both providers a private repository the token
	// cannot see is indistinguishable from one that does not exist; the
	// message says so rather than guessing.
	ErrNotFound = errors.New("gitlink: no such pull request, or the credential cannot see it")
	// ErrRateLimited — 429, or GitHub's 403-with-X-RateLimit-Remaining-0.
	ErrRateLimited = errors.New("gitlink: the provider is rate-limiting this credential")
	// ErrProviderUnavailable — 5xx, or the request never completed.
	ErrProviderUnavailable = errors.New("gitlink: the provider is unavailable")
	// ErrUnexpectedStatus — anything else, including a 200 whose body is
	// not the JSON we asked for (a captive portal or an SSO login page).
	ErrUnexpectedStatus = errors.New("gitlink: unexpected response from the provider")
)

// maxResponseBytes caps what we read from a provider. A GitHub pull-request
// object with a long body runs to a few tens of KiB; 1 MiB is generous and
// still bounded.
const maxResponseBytes = 1 << 20

// fetchTimeout bounds the whole round trip. A wedged self-hosted forge must
// not hold an API request open.
const fetchTimeout = 20 * time.Second

// FetchError carries the sentinel plus the evidence a user needs: which
// provider, what status, and how long until a retry is worth trying. It never
// carries the credential — the message is surfaced verbatim in an API
// response and stored in mission_code_links.last_sync_error.
type FetchError struct {
	Err        error
	Provider   Provider
	Status     int
	RetryAfter time.Duration
	Detail     string
}

func (e *FetchError) Error() string {
	var b strings.Builder
	b.WriteString(e.Err.Error())
	if e.Status != 0 {
		fmt.Fprintf(&b, " (%s HTTP %d)", e.Provider, e.Status)
	} else if e.Provider != "" {
		fmt.Fprintf(&b, " (%s)", e.Provider)
	}
	if e.RetryAfter > 0 {
		fmt.Fprintf(&b, "; retry in %s", e.RetryAfter.Round(time.Second))
	}
	if e.Detail != "" {
		b.WriteString(": ")
		b.WriteString(e.Detail)
	}
	return b.String()
}

func (e *FetchError) Unwrap() error { return e.Err }

// Details is the state of a pull request as the provider reports it.
//
// Title and Author are UNTRUSTED external content — anyone who can open a PR
// against a watched repository chooses them. They are stored raw (the UI needs
// the real string) and MUST be run through internal/untrusted before they are
// concatenated into anything an agent reads. See internal/api/issues_internal.go
// for the fenced read path.
type Details struct {
	Title        string
	State        State
	Author       string
	SourceBranch string
	TargetBranch string
	// Provider timestamps, verbatim (RFC 3339). Empty when absent.
	CreatedAt string
	UpdatedAt string
	MergedAt  string
	ClosedAt  string
	// WebURL is the provider's own canonical link, when it supplies one.
	WebURL string
}

// Client fetches pull-request state. Construct with NewClient; the zero value
// is not usable.
type Client struct {
	http         *http.Client
	allowPrivate bool
	// schemes is the scheme allowlist handed to httpsafe on every request
	// AND on every redirect hop.
	schemes []string
}

// NewClient builds a Client with SSRF defences wired in.
//
// allowPrivate is the operator opt-in for reaching a forge on a private
// address. It is FALSE in production unless an admin turns it on, and it maps
// onto httpsafe's existing two tiers rather than inventing a third policy:
//
//	false — the strict default. Loopback, RFC1918, CGNAT, ULA, link-local,
//	        cloud metadata and friends are all refused, and only https is
//	        accepted, so a pasted link can never be turned into a probe of
//	        the host Crewship runs on. This is the posture that matters,
//	        because the URL is attacker-supplied by construction: an agent
//	        or any MEMBER can paste one, and the server attaches a
//	        credential to whatever it dials.
//	true  — the soft tier (loopback/RFC1918/ULA/CGNAT) becomes reachable and
//	        http is accepted, because a self-hosted GitLab on an intranet
//	        address is a real deployment and a feature that cannot reach it
//	        is broken for exactly the users this design was chosen for. The
//	        HARD tier does not move: 169.254.169.254 and every other
//	        metadata/multicast/reserved range stays refused.
//
// Both layers of httpsafe are used on purpose. ValidateURL rejects the obvious
// cases without a network round trip; the transport's dialer re-resolves the
// host at connect time and refuses the resolved IP, which is what closes DNS
// rebinding and split-horizon aliases (a public name whose A record answers
// 127.0.0.1). Redirects are re-validated per hop so a permitted host cannot
// bounce us into a refused one.
func NewClient(allowPrivate bool) *Client {
	schemes := []string{"https"}
	if allowPrivate {
		schemes = []string{"http", "https"}
	}
	c := &Client{allowPrivate: allowPrivate, schemes: schemes}
	c.http = &http.Client{
		Timeout:   fetchTimeout,
		Transport: httpsafe.SafeTransportForEndpoint(allowPrivate),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("gitlink: too many redirects")
			}
			_, err := httpsafe.ValidateURLForEndpoint(req.URL.String(), allowPrivate, schemes...)
			return err
		},
	}
	return c
}

// Fetch reads the current state of ref using token.
//
// The token is placed in a request HEADER and nowhere else: not in the URL
// (it would be logged by every proxy on the path and by us), not in a query
// parameter, and never on a command line — Crewship never shells out to `gh`
// or `glab` for this.
func (c *Client) Fetch(ctx context.Context, ref Ref, token string) (Details, error) {
	endpoint := ref.APIEndpoint()

	// Layer 1: cheap, no-network reject. Catches a literal private IP, a
	// wrong scheme, and embedded userinfo before any DNS or TCP happens.
	//
	// The two postures are separate branches rather than one call with a bool,
	// and the request is built from what the check RETURNS rather than from
	// `endpoint`. Both are for the reader first and the analyser second:
	//
	//   - The strict default goes through httpsafe.ValidateURL — the same
	//     entry point every other outbound-fetch site in this repo uses, and
	//     the one CodeQL's go/request-forgery model recognises as a barrier.
	//     ValidateURLForEndpoint(…, false, …) is documented as byte-for-byte
	//     identical to it, so this branch changes no behaviour; what it changes
	//     is that the SHIPPED posture is provably sanitised rather than
	//     sanitised-if-a-flag-happens-to-be-false.
	//   - The opt-in branch is the one an operator turned on to reach an
	//     intranet forge. It is deliberately the narrower-looking path: it is
	//     the only place private addresses become reachable at all, and
	//     anything added here weakens a boundary rather than widening a feature.
	//
	// Passing the returned URL into http.NewRequest (rather than re-passing
	// `endpoint`) means deleting either check would not merely be a missing
	// guard — the request would have nothing to be built from.
	var (
		validated *url.URL
		err       error
	)
	if c.allowPrivate {
		validated, err = httpsafe.ValidateURLForEndpoint(endpoint, true, c.schemes...)
	} else {
		validated, err = httpsafe.ValidateURL(endpoint, c.schemes...)
	}
	if err != nil {
		return Details{}, &FetchError{
			Err:      ErrBlockedHost,
			Provider: ref.Provider,
			Detail:   blockedDetail(err, c.allowPrivate),
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, validated.String(), nil)
	if err != nil {
		return Details{}, &FetchError{Err: ErrUnexpectedStatus, Provider: ref.Provider, Detail: err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Crewship/1.0 (+link-first git integration)")
	switch ref.Provider {
	case ProviderGitLab:
		req.Header.Set("PRIVATE-TOKEN", token)
	default:
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Layer 2 rejects at dial time (rebind / split-horizon DNS). That
		// is a policy refusal, not an outage, and must not be reported as
		// "the provider is down".
		if errors.Is(err, httpsafe.ErrBlocked) || errors.Is(err, httpsafe.ErrInvalidURL) {
			return Details{}, &FetchError{
				Err:      ErrBlockedHost,
				Provider: ref.Provider,
				Detail:   blockedDetail(err, c.allowPrivate),
			}
		}
		return Details{}, &FetchError{
			Err:      ErrProviderUnavailable,
			Provider: ref.Provider,
			Detail:   scrubToken(err.Error(), token),
		}
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))

	if resp.StatusCode != http.StatusOK {
		return Details{}, statusError(ref.Provider, resp)
	}
	if readErr != nil {
		return Details{}, &FetchError{
			Err:      ErrProviderUnavailable,
			Provider: ref.Provider,
			Status:   resp.StatusCode,
			Detail:   scrubToken(readErr.Error(), token),
		}
	}

	details, err := decode(ref.Provider, body)
	if err != nil {
		return Details{}, &FetchError{
			Err:      ErrUnexpectedStatus,
			Provider: ref.Provider,
			Status:   resp.StatusCode,
			Detail:   "the response was not a " + string(ref.Provider) + " pull-request object",
		}
	}
	return details, nil
}

// statusError maps a non-200 onto the sentinel that tells the user what to do.
func statusError(p Provider, resp *http.Response) *FetchError {
	fe := &FetchError{Provider: p, Status: resp.StatusCode}
	fe.RetryAfter = retryAfter(resp)
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		fe.Err = ErrUnauthorized
		fe.Detail = "rotate or re-add the credential"
	case resp.StatusCode == http.StatusTooManyRequests:
		fe.Err = ErrRateLimited
		fe.Detail = "the provider's rate limit for this credential is exhausted"
	case resp.StatusCode == http.StatusForbidden:
		// GitHub answers an exhausted rate limit with 403, not 429, and
		// only the X-RateLimit-Remaining header tells the two apart. Read
		// as "no access", it sends the user to rotate a perfectly good
		// token; read as a rate limit, a genuine permission problem looks
		// transient. Hence the header check rather than a status check.
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			fe.Err = ErrRateLimited
			fe.Detail = "the provider's rate limit for this credential is exhausted (reported as 403, not 429)"
			if fe.RetryAfter == 0 {
				fe.RetryAfter = untilRateLimitReset(resp.Header.Get("X-RateLimit-Reset"))
			}
		} else {
			fe.Err = ErrForbidden
			fe.Detail = "the token needs read access to the repository (GitHub: `repo` scope; GitLab: `read_api`)"
		}
	case resp.StatusCode == http.StatusNotFound:
		fe.Err = ErrNotFound
		fe.Detail = "if the repository is private, the credential needs access to it"
	case resp.StatusCode >= 500:
		fe.Err = ErrProviderUnavailable
	default:
		fe.Err = ErrUnexpectedStatus
	}
	return fe
}

func retryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(v); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}

// untilRateLimitReset reads GitHub's X-RateLimit-Reset (unix seconds).
func untilRateLimitReset(v string) time.Duration {
	sec, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0
	}
	if d := time.Until(time.Unix(sec, 0)); d > 0 {
		return d
	}
	return 0
}

// blockedDetail turns an httpsafe refusal into advice. Which advice depends on
// whether the operator has already opted in: telling someone to flip a setting
// that is already on is worse than saying nothing.
func blockedDetail(err error, allowPrivate bool) string {
	if allowPrivate {
		return "the address is in a range that is never reachable (cloud metadata, link-local, multicast)"
	}
	return "private and loopback addresses are refused by default; an admin can allow a self-hosted forge " +
		"on an internal address with the `git_links.allow_private_hosts` instance setting"
}

// scrubToken keeps a credential out of an error string on the vanishing chance
// a transport error quoted the request. Cheap insurance on a string that is
// persisted and shown.
func scrubToken(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "[redacted]")
}

// ── provider payloads ───────────────────────────────────────────────────

type githubPull struct {
	Title  string `json:"title"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	Number int    `json:"number"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	MergedAt  string `json:"merged_at"`
	ClosedAt  string `json:"closed_at"`
	HTMLURL   string `json:"html_url"`
}

type gitlabMR struct {
	Title  string `json:"title"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	WIP    bool   `json:"work_in_progress"`
	IID    int    `json:"iid"`
	Author struct {
		Username string `json:"username"`
	} `json:"author"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	MergedAt     string `json:"merged_at"`
	ClosedAt     string `json:"closed_at"`
	WebURL       string `json:"web_url"`
}

// errNotAPullRequest marks a 200 whose body is not the object we asked for.
var errNotAPullRequest = errors.New("gitlink: response is not a pull-request object")

func decode(p Provider, body []byte) (Details, error) {
	switch p {
	case ProviderGitLab:
		var mr gitlabMR
		if err := json.Unmarshal(body, &mr); err != nil {
			return Details{}, err
		}
		if mr.IID == 0 && mr.Title == "" {
			return Details{}, errNotAPullRequest
		}
		return Details{
			Title:        mr.Title,
			State:        gitlabState(mr),
			Author:       mr.Author.Username,
			SourceBranch: mr.SourceBranch,
			TargetBranch: mr.TargetBranch,
			CreatedAt:    mr.CreatedAt,
			UpdatedAt:    mr.UpdatedAt,
			MergedAt:     mr.MergedAt,
			ClosedAt:     mr.ClosedAt,
			WebURL:       mr.WebURL,
		}, nil
	default:
		var pr githubPull
		if err := json.Unmarshal(body, &pr); err != nil {
			return Details{}, err
		}
		if pr.Number == 0 && pr.Title == "" {
			return Details{}, errNotAPullRequest
		}
		return Details{
			Title:        pr.Title,
			State:        githubState(pr),
			Author:       pr.User.Login,
			SourceBranch: pr.Head.Ref,
			TargetBranch: pr.Base.Ref,
			CreatedAt:    pr.CreatedAt,
			UpdatedAt:    pr.UpdatedAt,
			MergedAt:     pr.MergedAt,
			ClosedAt:     pr.ClosedAt,
			WebURL:       pr.HTMLURL,
		}, nil
	}
}

// githubState collapses GitHub's (state, draft, merged_at) triple. merged_at
// is checked FIRST because a merged PR reports state "closed" — reading state
// alone would file every merge as a rejection, which is exactly the fact
// phase 2 is going to transition issues on.
func githubState(pr githubPull) State {
	if pr.MergedAt != "" {
		return StateMerged
	}
	if strings.EqualFold(pr.State, "closed") {
		return StateClosed
	}
	if pr.Draft {
		return StateDraft
	}
	return StateOpen
}

// gitlabState collapses GitLab's state enum. `locked` is a transient state a
// merge passes through, so it reads as open rather than as a fifth value
// nothing downstream understands. `work_in_progress` is the pre-14.0 name for
// `draft` and old self-managed instances still send it.
func gitlabState(mr gitlabMR) State {
	switch strings.ToLower(mr.State) {
	case "merged":
		return StateMerged
	case "closed":
		return StateClosed
	}
	if mr.Draft || mr.WIP {
		return StateDraft
	}
	return StateOpen
}
