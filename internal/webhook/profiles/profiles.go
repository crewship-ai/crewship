// Package profiles verifies inbound webhook signatures for the named ingress
// profiles of the Crewship 1.0 webhook contract
// (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md, §5).
//
// It is a pure package. Bytes and headers go in, a [Verdict] comes out. It
// opens no database, registers no route, starts no work and never reads the
// clock: the caller injects [VerifyRequest.Now], so the ±5 minute tolerance
// boundary is exercisable in a table test instead of by sleeping. Policy —
// which events are allowed, what a ping means, whether a delivery is a
// duplicate — belongs to the caller; this package only answers "is this
// request authentic, and what does its verified content identify".
//
// # Reasons never leak key material
//
// [Verdict.Reason] is one of the Reason* constants below and nothing else. The
// expected signature, the secret, and any derived key bytes never appear in a
// Reason or in any error this package produces, so a Reason is safe to put in
// an audit record or an HTTP body. §5 requires this ("Neprozrazovat očekávaný
// podpis").
//
// # How Key.Secret is interpreted
//
// [Key.Secret] is the raw HMAC key: the exact bytes handed to HMAC-SHA256.
// This package does not guess encodings, with one deliberate exception.
//
//   - profile "github": the bytes are always used raw. That is what GitHub
//     does — the secret you type into the repository webhook form is the HMAC
//     key, byte for byte.
//
//   - profile "standard-webhooks": the ecosystem convention serialises secrets
//     as base64 with a "whsec_" prefix (spec, "Secret serialization"). A Secret
//     whose bytes begin with the ASCII prefix "whsec_" is therefore decoded:
//     the prefix is stripped and the remainder is standard-base64 decoded, and
//     those decoded bytes are the HMAC key. Bytes that do not carry the prefix
//     are used raw. A "whsec_" value that fails to decode, or decodes to
//     nothing, makes that key ineligible rather than silently becoming a
//     literal key.
//
// The prefix is unambiguous, a bare base64 string is not: the reference
// implementation's NewWebhook base64-decodes even an unprefixed secret, while
// its NewWebhookRaw takes the bytes literally, and from a []byte we cannot tell
// the two apart. So if a Standard Webhooks provider gave you a bare base64
// secret with no prefix, run it through [DecodeSecret] before putting it in a
// Key — do not hand the ASCII across and hope. [DecodeSecret] implements the
// reference behaviour (optional prefix, then base64).
//
// # Key rotation
//
// At most [MaxKeys] keys may be supplied, old and new, per §5's "nejvýše dva
// aktivní ověřovací klíče". Every eligible key is tried and each comparison is
// constant time. A key whose NotAfter has passed relative to Now is not
// eligible — the overlap window has a real end. Rotation cannot buy a request
// past the timestamp check: for "standard-webhooks" the timestamp is validated
// before any key is consulted, so a second key changes nothing about freshness
// (§5, "Rotace nesmí obejít timestamp kontrolu").
package profiles

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

// Profile names an inbound signature scheme. §5 defines exactly two for 1.0;
// an endpoint's profile is configured explicitly and is never sniffed from the
// headers present on a request ("Legacy endpointy nezmění své profily
// heuristicky podle přítomnosti hlavičky").
type Profile string

const (
	// ProfileGitHub is GitHub's raw-body HMAC-SHA256 scheme.
	ProfileGitHub Profile = "github"
	// ProfileStandardWebhooks is the Standard Webhooks symmetric v1 scheme.
	ProfileStandardWebhooks Profile = "standard-webhooks"
)

// MaxKeys is the number of verification keys an endpoint may have active at
// once. Two exist only so a rotation has an overlap window.
const MaxKeys = 2

// Tolerance bounds how far webhook-timestamp may sit from Now in either
// direction for ProfileStandardWebhooks. The boundary is inclusive: exactly
// ±Tolerance verifies, one second beyond does not. That matches the reference
// implementation, which rejects only when the difference is strictly greater
// than the tolerance.
const Tolerance = 5 * time.Minute

// Key is one active verification key. A rotation carries at most two.
type Key struct {
	// ID identifies the key to the caller. It is echoed in Verdict.KeyID on a
	// match so an audit record can say which key a delivery arrived under. It
	// is an identifier, not a secret.
	ID string
	// Secret is the raw HMAC key. See the package doc for how each profile
	// interprets it, and for the "whsec_" exception.
	Secret []byte
	// NotAfter is when this key stops being accepted — the explicit end of a
	// rotation overlap that §5 requires. The zero value means no scheduled
	// end. The boundary is inclusive: a key is eligible while Now is not
	// after NotAfter.
	NotAfter time.Time
}

// eligible reports whether the key may be used at now.
func (k Key) eligible(now time.Time) bool {
	if len(k.Secret) == 0 {
		// Fail-safe, the same guard internal/webhook.ValidateHMAC applies: an
		// empty key still produces a perfectly valid HMAC, so a blank or
		// half-configured secret would otherwise accept anything a sender
		// signed with the empty key.
		return false
	}
	if k.NotAfter.IsZero() {
		return true
	}
	return !now.After(k.NotAfter)
}

// VerifyRequest is one inbound delivery presented for verification.
type VerifyRequest struct {
	// Profile selects the scheme. It comes from endpoint configuration.
	Profile Profile
	// Header is the request's header set.
	Header http.Header
	// RawBody is the exact bytes read off the wire. Signatures cover these
	// bytes; a re-serialised payload will not verify and must not be passed.
	RawBody []byte
	// Keys are the active verification keys, at most MaxKeys.
	Keys []Key
	// Now is the verification instant. Injected, never time.Now() inside.
	Now time.Time
}

// Verdict is the answer. On a rejection OK is false, Reason says why, and every
// other field is zero — nothing derived from an unverified body is reported.
type Verdict struct {
	// OK is true only when a supplied, eligible key authenticated the request.
	OK bool
	// KeyID is the ID of the key that matched.
	KeyID string
	// SourceEventID is the sender's delivery identifier: X-GitHub-Delivery for
	// "github", webhook-id for "standard-webhooks". For "github" it is NOT
	// covered by the signature — see ContentKey.
	SourceEventID string
	// EventType is X-GitHub-Event for "github". For "standard-webhooks" it is
	// empty: that profile carries no event-type header, and the endpoint's
	// static payload mapping supplies the type if one is needed.
	EventType string
	// Action is the verified payload's top-level "action" string for "github",
	// empty when the payload has none. It is read from the signed body and
	// never from a header, so a modified unsigned header cannot turn one body
	// into a different action (§5).
	Action string
	// ContentKey is the profile's replay key over verified content, scoped by
	// the caller to the endpoint before use:
	//
	//   github:            "sha256:<hex>" over the raw body. The GitHub
	//                      signature covers neither the delivery id nor a
	//                      timestamp, so §5 makes byte-identical verified
	//                      bodies on one endpoint the same delivery however
	//                      X-GitHub-Delivery changed. This is a deliberate
	//                      collapse of byte-identical payloads for this
	//                      profile, not a claim about GitHub event identity in
	//                      general.
	//
	//   standard-webhooks: the signed message id verbatim. Identity is
	//                      explicit in this profile: webhook-id is inside the
	//                      signed material, so it can be trusted once the
	//                      signature verifies.
	ContentKey string
	// Reason is a machine-readable rejection reason, one of the Reason*
	// constants. It never contains the expected signature, the secret or any
	// key material. It is empty when OK is true.
	Reason string
}

// Rejection reasons. These are the complete set; they are stable identifiers,
// safe to log, to audit and to map onto an HTTP status.
const (
	// ReasonUnknownProfile means VerifyRequest.Profile is not a profile this
	// package implements. It is a configuration error, not a sender error.
	ReasonUnknownProfile = "unknown_profile"
	// ReasonTooManyKeys means more than MaxKeys keys were supplied. Also a
	// configuration error: an overlap wider than old+new is not a rotation.
	ReasonTooManyKeys = "too_many_keys"
	// ReasonNoEligibleKey means no key was supplied, or every supplied key was
	// empty or past its NotAfter at Now.
	ReasonNoEligibleKey = "no_eligible_key"
	// ReasonMissingSignatureHeader means the profile's signature header is
	// absent or empty.
	ReasonMissingSignatureHeader = "missing_signature_header"
	// ReasonMissingRequiredHeader means a non-signature header the profile
	// requires is absent or empty (standard-webhooks: webhook-id,
	// webhook-timestamp).
	ReasonMissingRequiredHeader = "missing_required_header"
	// ReasonMalformedSignatureHeader means the signature header is present but
	// not in the profile's shape (github: no "sha256=" prefix, or the
	// remainder is not 32 bytes of hex).
	ReasonMalformedSignatureHeader = "malformed_signature_header"
	// ReasonNoSupportedSignatureVersion means the signature header parsed but
	// listed no version this package implements — e.g. only the asymmetric
	// "v1a". Unimplemented versions alongside a v1 are ignored, not fatal.
	ReasonNoSupportedSignatureVersion = "no_supported_signature_version"
	// ReasonSignatureMismatch means a well-formed signature did not match
	// under any eligible key.
	ReasonSignatureMismatch = "signature_mismatch"
	// ReasonMalformedTimestamp means webhook-timestamp is not a base-10 unix
	// seconds integer.
	ReasonMalformedTimestamp = "malformed_timestamp"
	// ReasonTimestampOutOfTolerance means webhook-timestamp is further than
	// Tolerance from Now in either direction.
	ReasonTimestampOutOfTolerance = "timestamp_out_of_tolerance"
	// ReasonBodyNotJSON means the signature verified but the body is not a
	// JSON object, for an event whose mapping needs one. The signature is
	// still valid; the payload is unusable.
	ReasonBodyNotJSON = "body_not_json"
)

// Verify authenticates one delivery under its configured profile.
//
// Verification is total: it returns a Verdict rather than an error, because
// every rejection here is a routine outcome the caller turns into an HTTP
// status, not an exceptional condition.
func Verify(req VerifyRequest) Verdict {
	if len(req.Keys) > MaxKeys {
		return reject(ReasonTooManyKeys)
	}
	switch req.Profile {
	case ProfileGitHub, ProfileStandardWebhooks:
	default:
		// Checked before the keys so a misconfigured profile is never
		// reported as a key problem.
		return reject(ReasonUnknownProfile)
	}

	eligible := eligibleKeys(req.Profile, req.Keys, req.Now)
	if len(eligible) == 0 {
		return reject(ReasonNoEligibleKey)
	}

	if req.Profile == ProfileGitHub {
		return verifyGitHub(req, eligible)
	}
	return verifyStandardWebhooks(req, eligible)
}

// resolvedKey is an eligible key with its secret already resolved to the bytes
// the HMAC will use, so the verification loops do no decoding of their own.
type resolvedKey struct {
	id  string
	key []byte
}

// eligibleKeys drops keys that are empty, expired, or whose secret does not
// resolve under the profile, preserving order. A key that cannot be used is
// dropped here rather than inside the comparison loop, so an endpoint whose
// only key is unusable is reported as ReasonNoEligibleKey — a configuration
// fault — instead of being blamed on the sender as a signature mismatch.
func eligibleKeys(p Profile, keys []Key, now time.Time) []resolvedKey {
	out := make([]resolvedKey, 0, len(keys))
	for _, k := range keys {
		if !k.eligible(now) {
			continue
		}
		secret, ok := hmacKey(p, k.Secret)
		if !ok {
			continue
		}
		out = append(out, resolvedKey{id: k.ID, key: secret})
	}
	return out
}

// reject builds a rejection verdict. Nothing but the reason is reported: a
// caller must not be able to read a delivery id or an event type off a request
// that failed to authenticate.
func reject(reason string) Verdict {
	return Verdict{Reason: reason}
}

// header reads a header value with surrounding whitespace removed. Go's
// http.Header already canonicalises the key, so "webhook-id" and "Webhook-Id"
// are the same lookup.
func header(h http.Header, name string) string {
	if h == nil {
		return ""
	}
	return strings.TrimSpace(h.Get(name))
}

// ContentKeyForBody returns the "github" profile's content replay key for a
// body: "sha256:" followed by the lowercase hex SHA-256 of the exact bytes.
// The caller scopes it to the endpoint (and workspace) before using it as a
// dedup key; on its own it says only "this is the same bytes".
func ContentKeyForBody(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// standardWebhooksSecretPrefix is the Standard Webhooks secret serialisation
// prefix ("Secret serialization" in the spec).
const standardWebhooksSecretPrefix = "whsec_"

// DecodeSecret converts a Standard Webhooks secret as a provider hands it to
// you — base64, with or without the "whsec_" prefix — into the raw key bytes
// for [Key.Secret]. It mirrors the reference implementation's NewWebhook.
//
// Use it when the configured secret is a bare base64 string: this package
// cannot tell a bare base64 secret from a raw key inside a []byte, so it
// auto-decodes only the unambiguous prefixed form.
func DecodeSecret(secret string) ([]byte, bool) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, standardWebhooksSecretPrefix))
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	return raw, true
}

// hmacKey resolves the bytes actually used as the HMAC key for a profile.
// ok is false when the key is unusable and must be skipped.
func hmacKey(p Profile, secret []byte) (key []byte, ok bool) {
	if p == ProfileStandardWebhooks && strings.HasPrefix(string(secret), standardWebhooksSecretPrefix) {
		// The prefix is only ever produced by the serialisation convention, so
		// its presence is a statement about encoding, not a literal key that
		// happens to start with "whsec_". Decode it or refuse it.
		return DecodeSecret(string(secret))
	}
	if len(secret) == 0 {
		return nil, false
	}
	return secret, true
}
