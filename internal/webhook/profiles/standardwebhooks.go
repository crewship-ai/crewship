package profiles

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// Standard Webhooks headers, all three required.
const (
	HeaderStandardWebhooksID        = "webhook-id"
	HeaderStandardWebhooksTimestamp = "webhook-timestamp"
	HeaderStandardWebhooksSignature = "webhook-signature"
)

// standardWebhooksVersion is the symmetric HMAC-SHA256 scheme. The asymmetric
// "v1a" (ed25519) is defined by the spec but not implemented here; it is
// skipped where it appears rather than treated as an error, so a sender that
// emits both schemes still verifies.
const standardWebhooksVersion = "v1"

// verifyStandardWebhooks implements the "standard-webhooks" profile: HMAC-SHA256
// over "<id>.<timestamp>.<raw body>", base64, in a space-separated list of
// versioned signatures.
func verifyStandardWebhooks(req VerifyRequest, keys []resolvedKey) Verdict {
	msgID := header(req.Header, HeaderStandardWebhooksID)
	timestamp := header(req.Header, HeaderStandardWebhooksTimestamp)
	signatures := header(req.Header, HeaderStandardWebhooksSignature)

	if signatures == "" {
		return reject(ReasonMissingSignatureHeader)
	}
	if msgID == "" || timestamp == "" {
		return reject(ReasonMissingRequiredHeader)
	}

	secs, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return reject(ReasonMalformedTimestamp)
	}
	// The freshness check runs before any key is consulted. That is what makes
	// "rotation must not bypass the timestamp check" (§5) structural rather
	// than a property of the loop below.
	if !withinTolerance(secs, req.Now.Unix()) {
		return reject(ReasonTimestampOutOfTolerance)
	}

	// The parsed integer, not the header text, is re-rendered into the signed
	// material — the same as the reference implementation, which formats
	// timestamp.Unix(). So "+1614265330" signs as "1614265330".
	signed := msgID + "." + strconv.FormatInt(secs, 10) + "."

	provided, sawSupportedVersion := parseSignatureList(signatures)
	if !sawSupportedVersion {
		return reject(ReasonNoSupportedSignatureVersion)
	}

	matched := false
	keyID := ""
	for _, k := range keys {
		mac := hmac.New(sha256.New, k.key)
		mac.Write([]byte(signed))
		mac.Write(req.RawBody)
		expected := []byte(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		for _, candidate := range provided {
			// Compared in their base64 form, as the reference does: a
			// non-canonical encoding of the right bytes is not the signature
			// that was sent. Constant time per comparison, and no candidate or
			// key is skipped once a match is found, so the timing does not
			// reveal which one matched.
			if hmac.Equal([]byte(candidate), expected) && !matched {
				matched = true
				keyID = k.id
			}
		}
	}
	if !matched {
		return reject(ReasonSignatureMismatch)
	}

	return Verdict{
		OK:            true,
		KeyID:         keyID,
		SourceEventID: msgID,
		// This profile carries no event-type header. The endpoint's static
		// payload mapping supplies one if the work input needs it; the
		// standard does not impose an envelope of ours (§5).
		EventType: "",
		Action:    "",
		// Unlike github, identity is explicit here: webhook-id is inside the
		// signed material, so once the signature verifies the sender's own id
		// is trustworthy and no content hash is needed to stand in for it.
		ContentKey: msgID,
	}
}

// withinTolerance reports whether a unix-seconds timestamp is within Tolerance
// of now, inclusive at both edges.
//
// The comparison is done in seconds against bounds instead of via time.Sub, for
// the reason internal/webhook.Handler.timestampFresh gives: an absurd
// far-future or far-past value overflows the int64-nanosecond Duration and can
// wrap back inside the window. now±tolerance cannot overflow, and secs is only
// compared, never used in arithmetic.
func withinTolerance(secs, nowSecs int64) bool {
	tol := int64(Tolerance / time.Second)
	return secs >= nowSecs-tol && secs <= nowSecs+tol
}

// parseSignatureList splits the space-separated versioned signature list.
//
// It returns the base64 payloads of every "v1," entry, and whether any entry
// named a supported version at all. Entries in versions we do not implement
// (the asymmetric "v1a") and entries with no comma are skipped: the spec's own
// example header lists v1 and v1a side by side, so failing on an unknown
// version would reject a compliant sender. A list that names no supported
// version is a different outcome from a signature that did not match, and gets
// its own reason.
//
// A bare "v1," yields an empty candidate: the version is supported, so the
// verdict is a mismatch rather than "no supported version", matching the
// reference implementation's handling of a partial signature.
func parseSignatureList(headerValue string) (candidates []string, sawSupportedVersion bool) {
	for _, entry := range strings.Fields(headerValue) {
		version, signature, found := strings.Cut(entry, ",")
		if !found || version != standardWebhooksVersion {
			continue
		}
		sawSupportedVersion = true
		candidates = append(candidates, signature)
	}
	return candidates, sawSupportedVersion
}
