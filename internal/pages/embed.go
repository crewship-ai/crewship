package pages

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/crewship-ai/crewship/internal/httpsafe"
	"github.com/crewship-ai/crewship/schemas"
)

// embed.v1 — the one escape hatch (PRD §3.1), and the only panel whose content
// this product does not draw.
//
// ## Why the payload carries a NAME and not a URL
//
// This is the whole design and it deserves the argument rather than the
// conclusion. An iframe `src` is fetched by the READER's browser, from the
// reader's network, at the moment the page is opened. So a URL that a producer
// could set on every push is not "a link to a dashboard" — it is an outbound
// channel with the reader's IP on it, and the producer chooses both the
// destination and the bytes. Encode a panel's numbers into the path, push,
// wait for someone to open the page, and the data has left the workspace.
//
// That is the exact shape of the two incidents §8 is written from: CamoLeak
// exfiltrated through an <img> a trusted first-party proxy was happy to fetch,
// and Slack AI's private-channel leak was a rendered link. §8 rule 2 removes
// the image field and rule 3 removes the URL field, in both cases from the
// SCHEMA rather than from a sanitiser. An iframe is strictly worse than an
// <img>: it fetches AND it executes.
//
// The sandbox does not answer this. `sandbox` constrains what the framed
// document may DO; it has never had anything to say about whether the request
// is made. Nor does CSP: the only `frame-src` that closes the channel is one
// that names the exact origins a human already approved — which is the same
// statement as "a human authored the URL".
//
// So a human authors it, and §8 rule 4 already gives the shape for "a human
// declares the set, a machine selects from it": the operator writes an
// allow-list of vetted URLs, and the payload names one entry. The producer's
// entire influence over what the browser fetches is log2(n) bits — which of n
// approved pages is showing — instead of an arbitrary string. An unknown name
// is refused and never fetched to find out what it was.
//
// ## Why the allow-list is the OPERATOR's and not the page author's
//
// Because a page spec is the smaller change and this is the shipping order,
// not the destination. The natural home for the allow-list is the panel's own
// spec — `embed: {sources: [...]}` in the YAML the page author writes — which
// scopes each vetted URL to the one panel that may show it. That field does
// not exist yet, and adding it is the next change. Until it does, the
// instance-wide list is authored by an operator editing configuration, which
// is still a human, still not a producer, and still a closed set; what it
// costs is that any panel may select any vetted source. Both readings satisfy
// the rule that matters — nothing a producer sends can widen the set.
//
// ## Fail closed
//
// The zero value of the process policy has no sources. In that state
// `embed.v1` is Known but not Producible (so a page spec cannot even declare
// one), ValidateEmbed refuses every payload, and FrameSrc emits
// `frame-src 'none'`. There is no configuration mistake that silently
// downgrades this panel into a same-origin iframe — the parser refuses a
// source on our own origin outright.

// EmbedSandbox is the sandbox attribute the renderer puts on the frame, and
// the reason there is exactly one string for it in this repo.
//
// `allow-scripts` alone. It is the minimum that renders anything a person
// would embed, and it is deliberately not accompanied by `allow-same-origin`:
// the two together are the documented way for a framed document to reach its
// own frame element and DELETE the sandbox attribute, which turns this panel
// into script execution on our origin. Without `allow-same-origin` the framed
// document is given an opaque origin, so it has no cookies, no storage and no
// same-origin handle on anything of ours even if the URL it was pointed at is
// ours by accident.
//
// The absent tokens are each a specific refusal: no `allow-top-navigation`
// (an embed must never be able to steer the operator's tab somewhere else),
// no `allow-forms` and no `allow-popups` (a credential prompt drawn inside our
// chrome is the phishing surface §8 rule 5 keeps host-drawn), no
// `allow-modals`, `allow-downloads`, `allow-pointer-lock` or
// `allow-presentation` (privileged APIs, blocked per §3.1).
const EmbedSandbox = "allow-scripts"

// embedSourceNameRE is the `source` pattern from panel.embed.v1.json, restated
// so the allow-list an operator writes and the name a producer pushes cannot
// disagree about what a source is called.
var embedSourceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// maxEmbedURLBytes bounds one allow-list entry. 2048 is the length every
// browser and proxy handles without truncating; a URL longer than that is a
// payload wearing a URL's clothes.
const maxEmbedURLBytes = 2048

// EmbedSource is one vetted destination: the name a producer may select and
// the URL a human approved for it.
type EmbedSource struct {
	// Name is the producer-facing vocabulary — a slug, matching the `source`
	// pattern in panel.embed.v1.json.
	Name string
	// URL is the absolute https URL the reader's browser will be pointed at.
	// It never appears in a payload and is never assembled from one.
	URL string
	// Origin is URL's scheme://host[:port] with a default port removed. It is
	// what a CSP `frame-src` list is matched on.
	Origin string
}

// EmbedPolicy is the set of vetted sources for this instance, plus the origin
// this instance itself is served from so "cross-origin" can be checked rather
// than assumed.
type EmbedPolicy struct {
	// SelfOrigin is the app's own origin (CREWSHIP_PUBLIC_URL). Empty means
	// "unknown", in which case the same-origin check cannot run — the parser
	// still refuses everything else, but an operator who has not set a public
	// URL gets no protection from this particular mistake.
	SelfOrigin string
	sources    []EmbedSource
}

// Enabled reports whether this instance has any vetted source at all. An
// instance with none does not render, accept or advertise embed panels.
func (p EmbedPolicy) Enabled() bool { return len(p.sources) > 0 }

// Len is how many sources a human vetted. Reported at startup so an operator
// who mistyped the separator sees "1" where they expected four.
func (p EmbedPolicy) Len() int { return len(p.sources) }

// Sources returns the vetted set, in declaration order.
func (p EmbedPolicy) Sources() []EmbedSource {
	out := make([]EmbedSource, len(p.sources))
	copy(out, p.sources)
	return out
}

// Lookup resolves a source name. The second result is false for a name that
// was never declared — the only correct response to which is a refusal.
func (p EmbedPolicy) Lookup(name string) (EmbedSource, bool) {
	for _, s := range p.sources {
		if s.Name == name {
			return s, true
		}
	}
	return EmbedSource{}, false
}

// Names returns the declared names, sorted, for an error message a producer
// script can act on.
func (p EmbedPolicy) Names() []string {
	out := make([]string, 0, len(p.sources))
	for _, s := range p.sources {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

// FrameSrc renders the CSP `frame-src` directive this policy implies.
//
// §3.1 pins the default as `frame-src 'none'`, and an empty policy therefore
// emits exactly that rather than emitting nothing: a missing directive falls
// back to `default-src`, so "forgot to mention frames" and "forbade frames"
// would be the same header for an instance that has embeds turned off and a
// `default-src` that happens to be permissive. Saying it is cheaper than
// reasoning about it.
//
// This is the value the HTTP layer needs in order for the panel to render at
// all; see the note in docs/guides/pages.mdx about what remains.
func (p EmbedPolicy) FrameSrc() string {
	if len(p.sources) == 0 {
		return "frame-src 'none'"
	}
	seen := make(map[string]bool, len(p.sources))
	origins := make([]string, 0, len(p.sources))
	for _, s := range p.sources {
		if seen[s.Origin] {
			continue
		}
		seen[s.Origin] = true
		origins = append(origins, s.Origin)
	}
	sort.Strings(origins)
	return "frame-src " + strings.Join(origins, " ")
}

// ParseEmbedPolicy parses the operator's allow-list.
//
// The format is `name=url` pairs separated by commas, which is the shape every
// other list-valued Crewship environment variable already has
// (CREWSHIP_ALLOWED_ORIGINS, CREWSHIP_TRUSTED_PROXY_CIDRS). selfOrigin is the
// instance's own base URL; a source that resolves to it is refused.
//
// Every failure here refuses the WHOLE policy rather than skipping the bad
// entry. A typo that silently dropped one source would leave an operator
// looking at a panel that refuses to render with no explanation, and a typo
// that silently dropped the same-origin CHECK would be worse than that.
func ParseEmbedPolicy(spec, selfOrigin string) (EmbedPolicy, error) {
	self, err := normaliseOrigin(selfOrigin)
	if err != nil {
		return EmbedPolicy{}, fmt.Errorf("embed policy: self origin %q: %w", selfOrigin, err)
	}
	policy := EmbedPolicy{SelfOrigin: self}

	seen := map[string]bool{}
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, raw, ok := strings.Cut(entry, "=")
		if !ok {
			return EmbedPolicy{}, fmt.Errorf("embed source %q: expected name=url", entry)
		}
		name = strings.TrimSpace(name)
		raw = strings.TrimSpace(raw)
		if !embedSourceNameRE.MatchString(name) {
			return EmbedPolicy{}, fmt.Errorf(
				"embed source %q: %q is not a valid embed source name; it is the word a producer pushes, so it is a slug (a-z, 0-9, -)", entry, name)
		}
		if seen[name] {
			return EmbedPolicy{}, fmt.Errorf("embed source %q is declared twice; one of the two could never be selected", name)
		}
		seen[name] = true

		src, err := parseEmbedSource(name, raw, self)
		if err != nil {
			return EmbedPolicy{}, err
		}
		policy.sources = append(policy.sources, src)
	}
	return policy, nil
}

// parseEmbedSource validates one vetted URL. These checks run when an OPERATOR
// writes the list, not when a producer pushes — a producer cannot reach them,
// which is the point. They exist because an allow-list entry is still a
// human's typo away from being a hole.
func parseEmbedSource(name, raw, selfOrigin string) (EmbedSource, error) {
	if len(raw) > maxEmbedURLBytes {
		return EmbedSource{}, fmt.Errorf("embed source %q: url is %d bytes, the cap is %d", name, len(raw), maxEmbedURLBytes)
	}
	// https only, no userinfo, and no literal address in a blocked range —
	// httpsafe is the repo's one answer to "is this URL safe to point at",
	// and an embed pointed at https://192.168.1.1/ would draw the reader's own
	// router admin page inside a Crewship panel.
	u, err := httpsafe.ValidateURL(raw, "https")
	if err != nil {
		return EmbedSource{}, fmt.Errorf("embed source %q: %w", name, err)
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		// A fragment never reaches the server that serves the frame, so it can
		// only mean something to script inside it. It is a channel with no
		// legitimate use here and it is not worth reasoning about twice.
		return EmbedSource{}, fmt.Errorf("embed source %q: url may not carry a fragment", name)
	}
	origin, err := normaliseOrigin(u.Scheme + "://" + u.Host)
	if err != nil {
		return EmbedSource{}, fmt.Errorf("embed source %q: %w", name, err)
	}
	if selfOrigin != "" && origin == selfOrigin {
		// The one refusal this whole file exists for. A same-origin iframe is
		// not a sandbox — it shares cookies, storage and (with any sandbox
		// escape) the document that framed it. §3.1 says cross-origin, and an
		// operator who points an embed at their own Crewship gets told so
		// rather than getting a frame that looks like it worked.
		return EmbedSource{}, fmt.Errorf(
			"embed source %q: %s is this instance's own origin; an embed must be cross-origin, and a same-origin frame is not a sandbox", name, origin)
	}
	return EmbedSource{Name: name, URL: u.String(), Origin: origin}, nil
}

// normaliseOrigin reduces a base URL to scheme://host[:port] with a default
// port dropped, so https://x.example and https://x.example:443 compare equal.
// An empty input is an empty origin rather than an error: not every install
// sets CREWSHIP_PUBLIC_URL.
func normaliseOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("not a url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("not an absolute origin")
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = host + ":" + port
	}
	return scheme + "://" + host, nil
}

// EmbedPolicyFromEnv builds the policy from the environment. It is the one
// line a startup path needs; nothing in this package reads the environment on
// its own, because a validator whose answer depends on an ambient variable is
// a validator nobody can test at the boundary.
func EmbedPolicyFromEnv() (EmbedPolicy, error) {
	return ParseEmbedPolicy(
		os.Getenv("CREWSHIP_PAGES_EMBED_SOURCES"),
		os.Getenv("CREWSHIP_PUBLIC_URL"),
	)
}

var (
	embedPolicyMu sync.RWMutex
	embedPolicy   EmbedPolicy
)

// SetEmbedPolicy installs the process-wide policy and returns a function that
// restores the previous one. The server calls it once at startup with
// EmbedPolicyFromEnv; tests call it with a fixture and defer the restore.
func SetEmbedPolicy(p EmbedPolicy) (restore func()) {
	embedPolicyMu.Lock()
	prev := embedPolicy
	embedPolicy = p
	embedPolicyMu.Unlock()
	return func() {
		embedPolicyMu.Lock()
		embedPolicy = prev
		embedPolicyMu.Unlock()
	}
}

// CurrentEmbedPolicy returns the installed policy.
func CurrentEmbedPolicy() EmbedPolicy {
	embedPolicyMu.RLock()
	defer embedPolicyMu.RUnlock()
	return embedPolicy
}

// FrameSrcDirective is the CSP directive for the installed policy, ready to
// join into the UI's Content-Security-Policy header.
//
// It lives here rather than being assembled in the middleware because the
// allow-list and the directive that admits it must never be two decisions. A
// middleware that built its own string could admit an origin the validator
// refuses, or refuse one it accepts, and either way the disagreement shows up
// as a blank iframe with nothing in any log.
//
// With no policy installed — every instance that has not opted in — this is
// "frame-src 'none'", which is stricter than the default-src 'self' fallback
// it replaces.
func FrameSrcDirective() string { return CurrentEmbedPolicy().FrameSrc() }

// EmbedEnabled reports whether this instance has an embed allow-list at all.
func EmbedEnabled() bool { return CurrentEmbedPolicy().Enabled() }

// ResolveEmbedSource looks a validated payload's source name up in the process
// policy. This is where the URL enters the system, on the READ path, from the
// operator's list — never from the bytes a producer pushed.
func ResolveEmbedSource(name string) (EmbedSource, bool) {
	return CurrentEmbedPolicy().Lookup(name)
}

// EmbedPayload is embed.v1: which vetted source to show, and one line saying
// why. There is no URL field, and adding one would be the bug.
type EmbedPayload struct {
	// Source names an entry in the instance's allow-list.
	Source string `json:"source"`
	// Caption is plain text drawn by host chrome above the frame.
	Caption string `json:"caption,omitempty"`
}

// Schema implements Payload.
func (p *EmbedPayload) Schema() PanelSchema { return SchemaEmbed }

// ValidateEmbed validates and decodes an embed.v1 payload.
//
// Four gates, and the first one is not about the bytes: an instance with no
// vetted sources has no sandbox to render into, so it accepts no embed
// payloads at all. Storing them would fill the ring with pushes that can only
// ever render as a refusal.
func ValidateEmbed(raw []byte) (*EmbedPayload, error) {
	policy := CurrentEmbedPolicy()
	if !policy.Enabled() {
		return nil, newError(CodeUnknownSchema, SchemaEmbed,
			"embed.v1 is not enabled on this instance: no embed sources are configured, so there is nothing a panel could be pointed at. An operator declares the vetted set in CREWSHIP_PAGES_EMBED_SOURCES")
	}
	if err := checkSize(SchemaEmbed, raw); err != nil {
		return nil, err
	}
	if err := validateAgainst(SchemaEmbed, schemas.PanelEmbedV1, raw); err != nil {
		return nil, err
	}
	var p EmbedPayload
	if err := decodeStrict(raw, &p); err != nil {
		return nil, newError(CodeSchemaViolation, SchemaEmbed, "%v", err)
	}
	if _, ok := policy.Lookup(p.Source); !ok {
		// Named, not fetched. The refusal lists the vocabulary because the
		// reader of this string is a producer script's author, and "unknown
		// source" without the list is a bug report instead of a fixed script.
		return nil, newError(CodeInconsistentPayload, SchemaEmbed,
			"%q is not a declared embed source on this instance; the vetted set is: %s",
			p.Source, strings.Join(policy.Names(), ", "))
	}
	return &p, nil
}
