package api

// Single source of truth for the credential `type` enum used by both
// Create and Update. Backend previously accepted any string in that
// column — a long-standing gap that let the wizard ship five values
// while the schema documented three. This file closes it: every type
// in validCredentialTypes is wizard-accessible and has explicit
// validation rules in validateCredentialPayload.
//
// The four "vault" types (USERPASS, SSH_KEY, CERTIFICATE, GENERIC_SECRET)
// were added in PR feat/credentials-vault-types alongside DB migration
// v93. Together with the original AI_CLI_TOKEN/API_KEY/CLI_TOKEN/SECRET/
// OAUTH2 set they form the full surface a Crewship workspace can
// inject into an agent container.
//
// Adding a new type: add it here, add its validation case in
// validateCredentialPayload, then update the wizard's PROVIDER_TILES
// map in components/features/credentials/add-credential-wizard/types.ts
// so users can actually pick it.

import "strings"

// CredentialType is a string alias used to document intent at call
// sites — the DB column stays plain TEXT for migration simplicity.
type CredentialType = string

const (
	CredTypeAICLIToken    CredentialType = "AI_CLI_TOKEN"
	CredTypeAPIKey        CredentialType = "API_KEY"
	CredTypeCLIToken      CredentialType = "CLI_TOKEN"
	CredTypeSecret        CredentialType = "SECRET"
	CredTypeOAuth2        CredentialType = "OAUTH2"
	CredTypeUserPass      CredentialType = "USERPASS"
	CredTypeSSHKey        CredentialType = "SSH_KEY"
	CredTypeCertificate   CredentialType = "CERTIFICATE"
	CredTypeGenericSecret CredentialType = "GENERIC_SECRET"
)

// validCredentialTypes is the closed set the Create path accepts. The
// map shape (vs slice) lets validateCredentialType run as O(1) on the
// hot path without allocating.
var validCredentialTypes = map[CredentialType]struct{}{
	CredTypeAICLIToken:    {},
	CredTypeAPIKey:        {},
	CredTypeCLIToken:      {},
	CredTypeSecret:        {},
	CredTypeOAuth2:        {},
	CredTypeUserPass:      {},
	CredTypeSSHKey:        {},
	CredTypeCertificate:   {},
	CredTypeGenericSecret: {},
}

// validateCredentialType checks only the closed type enum — extracted
// so callers that don't have a payload to shape-check (manifest-pending
// slot creation) can still gate on the type without running the full
// per-type field validation.
func validateCredentialType(t string) string {
	if _, ok := validCredentialTypes[t]; !ok {
		return "type must be one of: AI_CLI_TOKEN, API_KEY, CLI_TOKEN, SECRET, OAUTH2, USERPASS, SSH_KEY, CERTIFICATE, GENERIC_SECRET"
	}
	return ""
}

// validateCredentialPayload enforces per-type field requirements. It
// runs after the generic "value required unless OAUTH2" gate in the
// Create handler so this function can assume Value is populated for
// every type that needs it (it still checks Username for USERPASS
// because that's a USERPASS-specific requirement, not a generic one).
//
// Returns an empty string when the payload is valid, otherwise an
// end-user-readable error message suitable for a 400 response body.
func validateCredentialPayload(req *createCredentialRequest) string {
	if msg := validateCredentialType(req.Type); msg != "" {
		return msg
	}

	switch req.Type {
	case CredTypeUserPass:
		// Username is the cleartext identifier half of the credential
		// (e.g. "user@gmail.com"). The injected env var pair is
		// <NAME>_USERNAME + <NAME>_PASSWORD, both of which the agent
		// looks up by the credential's binding name — so missing
		// username here would silently inject an empty username at
		// runtime, breaking auth without a recoverable error.
		if req.Username == nil || strings.TrimSpace(*req.Username) == "" {
			return "username is required for USERPASS credentials"
		}

	case CredTypeSSHKey:
		// PEM gate keeps obviously-wrong pastes out of the vault —
		// the most common mistake is pasting an OpenSSH public key
		// (ssh-rsa AAAA...) into the private-key field. We don't
		// fully parse the key here; ssh.ParsePrivateKey lives in
		// the sidecar's mount path where a bad key surfaces as a
		// container-start error the operator can correlate.
		if !looksLikePEM(req.Value, "PRIVATE KEY") {
			return "ssh key must be a PEM-encoded private key (begins with -----BEGIN ... PRIVATE KEY-----)"
		}

	case CredTypeCertificate:
		if !looksLikePEM(req.Value, "CERTIFICATE") {
			return "certificate must be PEM-encoded (begins with -----BEGIN CERTIFICATE-----)"
		}

	case CredTypeGenericSecret:
		// Intentionally no shape check — the whole point of GENERIC_SECRET
		// is opaque values (webhook secrets, signing keys, custom tokens).
		// The generic "value required" gate in the Create handler is
		// enough.
	}
	return ""
}

// looksLikePEM is a deliberately cheap structural check, not a parser.
// Real PEM validation happens at mount time in the sidecar — this is
// just there to catch finger-slip mistakes (wrong field, wrong key
// type) at submit time instead of at agent-run time.
//
// `marker` is the PEM type label, e.g. "PRIVATE KEY" or "CERTIFICATE".
// For private keys we accept both "PRIVATE KEY" and "RSA PRIVATE KEY"
// / "OPENSSH PRIVATE KEY" / "EC PRIVATE KEY" by matching the suffix.
//
// BOTH the BEGIN and END labels must match the marker — a payload
// shaped like `-----BEGIN PUBLIC KEY-----\n…\n-----END CERTIFICATE-----`
// would otherwise sail through the SSH_KEY check because the BEGIN
// label looked unrelated to "PRIVATE KEY" but `strings.Contains(v,
// "-----END ")` was trivially true. Real PEMs always have matching
// labels; mismatched pairs are either copy-paste accidents or
// hostile shapes.
//
// TrimSpace before stripping the dashes catches CRLF line endings —
// a key exported from a Windows openssl build, or pasted from
// Notepad, lands here as `-----BEGIN ... PRIVATE KEY-----\r\n…` and
// the naked IndexByte('\n') leaves a stray `\r` glued to the closing
// `-----`. Without the early trim, TrimSuffix("-----") silently
// no-ops on the `…KEY-----\r` form and we'd reject a valid key.
func looksLikePEM(value, marker string) bool {
	// Empty marker is a programmer error — HasSuffix(any, "") is
	// trivially true, so without this guard a future caller that
	// forgot to pass a label would silently accept any PEM-shaped
	// blob. Fail closed so the foot-gun never reaches production.
	if marker == "" {
		return false
	}
	v := strings.TrimSpace(value)
	if !strings.HasPrefix(v, "-----BEGIN ") {
		return false
	}
	if !strings.Contains(v, "-----END ") {
		return false
	}

	beginLabel := pemLabel(v, "-----BEGIN ")
	if !labelMatchesMarker(beginLabel, marker) {
		return false
	}
	// END label sits on the last non-empty line. Extract it via the
	// last LF — if there's no LF in the value, the whole thing is one
	// line (which can't have both a BEGIN and END block anyway, but
	// we already gated on Contains above so the single-line case
	// would mean the payload is malformed).
	endLine := v
	if nl := strings.LastIndexByte(v, '\n'); nl >= 0 {
		endLine = v[nl+1:]
	}
	endLine = strings.TrimSpace(endLine)
	if !strings.HasPrefix(endLine, "-----END ") {
		return false
	}
	endLabel := pemLabel(endLine, "-----END ")
	return labelMatchesMarker(endLabel, marker)
}

// labelMatchesMarker returns true when the PEM label is exactly the
// marker (e.g. "PRIVATE KEY" matches "PRIVATE KEY") or a
// space-separated variant (e.g. "OPENSSH PRIVATE KEY" matches
// "PRIVATE KEY"). The space gate keeps `XPRIVATE KEY` from sneaking
// past — a naked HasSuffix lets any string ending in the marker pass,
// which is the entire foot-gun this function exists to close.
func labelMatchesMarker(label, marker string) bool {
	if label == marker {
		return true
	}
	return strings.HasSuffix(label, " "+marker)
}

// pemLabel pulls the label out of a PEM armour line — given
// "-----BEGIN OPENSSH PRIVATE KEY-----" and "-----BEGIN ", returns
// "OPENSSH PRIVATE KEY". Works for END lines too by passing
// "-----END ". Callers pass an already-trimmed first/last line.
func pemLabel(line, prefix string) string {
	firstLine := line
	if nl := strings.IndexByte(firstLine, '\n'); nl >= 0 {
		firstLine = firstLine[:nl]
	}
	firstLine = strings.TrimSpace(firstLine)
	stripped := strings.TrimSuffix(strings.TrimPrefix(firstLine, prefix), "-----")
	return strings.TrimSpace(stripped)
}
