package profiles

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// GitHub's headers. HeaderGitHubSignature carries HMAC-SHA256 over the raw
// body; the other two are unsigned, which is the whole reason ContentKey
// exists.
const (
	HeaderGitHubSignature = "X-Hub-Signature-256"
	HeaderGitHubEvent     = "X-GitHub-Event"
	HeaderGitHubDelivery  = "X-GitHub-Delivery"
)

// githubSignaturePrefix is the only algorithm prefix accepted. The legacy
// SHA-1 "X-Hub-Signature" header is not read at all: reading it would let a
// sender pick the weaker scheme.
const githubSignaturePrefix = "sha256="

// EventPing is GitHub's connectivity probe. The profile reports it so the
// caller can answer 200/ignored without starting an agent (§5). Deciding that
// is policy and lives outside this package.
const EventPing = "ping"

// eventOmitsAction lists the events whose payload legitimately carries no
// "action" and need not be a JSON object for the mapping to be satisfied.
//
// §5's rejection of a non-JSON body is qualified with "when an action is
// expected", and ping is the event where none is: it is a connectivity probe
// that starts no work. Any other event's payload must be a JSON object, since
// the endpoint mapping reads its fields.
var eventOmitsAction = map[string]bool{EventPing: true}

// verifyGitHub implements the "github" profile: HMAC-SHA256 over the raw body,
// hex, in X-Hub-Signature-256.
func verifyGitHub(req VerifyRequest, keys []resolvedKey) Verdict {
	provided := header(req.Header, HeaderGitHubSignature)
	if provided == "" {
		return reject(ReasonMissingSignatureHeader)
	}
	// The prefix is compared case-sensitively, as GitHub emits it. A "SHA256="
	// is malformed rather than a second spelling to support.
	if !strings.HasPrefix(provided, githubSignaturePrefix) {
		return reject(ReasonMalformedSignatureHeader)
	}
	providedMAC, err := hex.DecodeString(provided[len(githubSignaturePrefix):])
	if err != nil || len(providedMAC) != sha256.Size {
		return reject(ReasonMalformedSignatureHeader)
	}

	matched := false
	keyID := ""
	for _, k := range keys {
		mac := hmac.New(sha256.New, k.key)
		mac.Write(req.RawBody)
		// Every eligible key is tried; the loop is not cut short on a match, so
		// how long verification takes does not say which key matched.
		if hmac.Equal(providedMAC, mac.Sum(nil)) && !matched {
			matched = true
			keyID = k.id
		}
	}
	if !matched {
		return reject(ReasonSignatureMismatch)
	}

	event := header(req.Header, HeaderGitHubEvent)
	action, isObject := githubAction(req.RawBody)
	if !isObject && !eventOmitsAction[event] {
		return reject(ReasonBodyNotJSON)
	}

	return Verdict{
		OK: true,
		// KeyID and the header-derived fields are reported only past this
		// point, so nothing off an unauthenticated request reaches the caller.
		KeyID:         keyID,
		SourceEventID: header(req.Header, HeaderGitHubDelivery),
		EventType:     event,
		Action:        action,
		// Over the body alone: X-GitHub-Event and X-GitHub-Delivery are
		// unsigned, so letting either into the key would hand a replayer a
		// free way to mint a "new" delivery from bytes we already have.
		ContentKey: ContentKeyForBody(req.RawBody),
	}
}

// githubAction reads the top-level "action" from a verified payload.
//
// isObject reports whether the body is a JSON object at all. A payload that is
// an object but has no "action" (ping, push) yields "" with isObject true —
// absent is not malformed. An "action" that is present but is not a JSON
// string also yields "": the endpoint's allow-list then simply does not match
// it, which fails closed, and calling that a malformed body would be a lie
// about which part is wrong.
func githubAction(body []byte) (action string, isObject bool) {
	trimmed := strings.TrimLeft(string(body), " \t\r\n")
	if !strings.HasPrefix(trimmed, "{") {
		// Guards the JSON literal "null", which unmarshals into a map without
		// error and would otherwise pass as an object.
		return "", false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return "", false
	}
	raw, present := obj["action"]
	if !present {
		return "", true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", true
	}
	return s, true
}
