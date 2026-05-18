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

// validateCredentialPayload enforces per-type field requirements. It
// runs after the generic "value required unless OAUTH2" gate in the
// Create handler so this function can assume Value is populated for
// every type that needs it (it still checks Username for USERPASS
// because that's a USERPASS-specific requirement, not a generic one).
//
// Returns an empty string when the payload is valid, otherwise an
// end-user-readable error message suitable for a 400 response body.
func validateCredentialPayload(req *createCredentialRequest) string {
	if _, ok := validCredentialTypes[req.Type]; !ok {
		return "type must be one of: AI_CLI_TOKEN, API_KEY, CLI_TOKEN, SECRET, OAUTH2, USERPASS, SSH_KEY, CERTIFICATE, GENERIC_SECRET"
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
func looksLikePEM(value, marker string) bool {
	v := strings.TrimSpace(value)
	if !strings.HasPrefix(v, "-----BEGIN ") {
		return false
	}
	if !strings.Contains(v, "-----END ") {
		return false
	}
	// First line: "-----BEGIN <label>-----". Check the label ends
	// with the requested marker so "PRIVATE KEY" matches all four
	// flavours (PKCS#1, PKCS#8, OpenSSH, EC).
	firstLine := v
	if nl := strings.IndexByte(v, '\n'); nl >= 0 {
		firstLine = v[:nl]
	}
	firstLine = strings.TrimSuffix(strings.TrimPrefix(firstLine, "-----BEGIN "), "-----")
	return strings.HasSuffix(strings.TrimSpace(firstLine), marker)
}
