package backup

// Forked restores and capability tokens (#2274).
//
// Five BackupTables carry a capability token — a secret whose knowledge
// IS the authorization: workspace_invitations.token redeems an invite,
// port_exposures powers /exposed/{token}/…, pipeline_webhooks fires
// /api/v1/webhooks/{token}, page_public_tokens and page_webhooks key
// their public paths on a digest. Each is UNIQUE across the whole
// instance, so a fork (--as-workspace / --as-crew) landing beside its
// source on the same instance collided, and RestoreDump's INSERT OR
// IGNORE dropped every row without a word — the #2260 shape, pinned for
// years in knownForkDrops.
//
// # The product decision
//
// A FORK DOES NOT INHERIT LIVE CAPABILITIES. Minting the pair fresh —
// token and digest together, through the same primitive the auth layer
// verifies with (pipeline.HashCapabilityToken) — means no secret that
// worked against the source works against the fork: anyone who ever
// learned a source URL holds nothing on the copy. The alternative,
// letting the fork share the source's tokens, would make every
// published /exposed/ URL and every configured webhook sender hit two
// workspaces at once — a leaked source URL doubling its blast radius
// the moment an admin forks for testing.
//
// The rows themselves still land. What they carry besides the secret is
// data the fork should keep: an invitation's email/role/expiry, a
// webhook's pipeline binding and rate limit, a public link's provenance
// settings. What the re-mint costs the operator is re-learning the new
// secret, which each surface already supports:
//
//   - workspace_invitations stores its token in the clear and the API
//     returns it on every list, so the fork's invitation is fully
//     functional — the admin reads the new link and sends it.
//   - pipeline_webhooks / port_exposures / page_* store only a digest
//     (#1888 — the cleartext is gone by design, on the source and
//     therefore in any bundle of it), so their re-minted digests match
//     no token anybody holds: the capability is carried across REVOKED,
//     and the restore result says so, by table and count. Re-issuing is
//     the normal rotate/re-create flow on the fork.
//
// Either way nothing points at access that silently differs from what
// the row claims: the fork's digests are real digests of tokens that
// were never published, and the operator is told that is the case.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// rekeyForkedCapabilityTokens re-mints the constrained secret columns of
// every capability-token row a fork is about to insert, so none collides
// with the source row still holding the old value. It runs AFTER
// RemapIDs — the redaction markers for port_exposures.token and
// pipeline_webhooks.token embed the row's NEW id, matching what the
// #1888 write path would have left there.
//
// Returns the per-table count of re-keyed rows for the restore result.
// Rows whose secret column is empty are left alone and not counted:
// an empty token is not a capability, and minting one would invent
// access the source never granted.
func rekeyForkedCapabilityTokens(dump *DBDump) (map[string]int, error) {
	counts := map[string]int{}

	// workspace_invitations: the one table whose cleartext token is
	// stored and re-displayed. Mint a fresh token of the exact shape
	// internal/api's invitation creator mints (32 bytes, hex), so the
	// fork's pending invitation is redeemable through the link the
	// fork's admin re-reads from the invitation list.
	for _, row := range dump.Tables["workspace_invitations"] {
		if !hasNonEmptyString(row, "token") {
			continue
		}
		tok, err := mintHexToken(32)
		if err != nil {
			return nil, fmt.Errorf("backup: re-mint invitation token: %w", err)
		}
		row["token"] = tok
		counts["workspace_invitations"]++
	}

	// port_exposures: cleartext column gets the dead redaction marker
	// (its NOT NULL UNIQUE demands a value; the real secret lives in the
	// digest), digest gets a fresh mint nobody can present.
	for _, row := range dump.Tables["port_exposures"] {
		if !hasNonEmptyString(row, "token") {
			continue
		}
		id, _ := row["id"].(string)
		digest, err := mintCapabilityDigest()
		if err != nil {
			return nil, fmt.Errorf("backup: re-mint port exposure token: %w", err)
		}
		row["token"] = pipeline.RedactedCapabilityToken(id)
		row["token_hash"] = digest
		counts["port_exposures"]++
	}

	// pipeline_webhooks: same arrangement as port_exposures.
	for _, row := range dump.Tables["pipeline_webhooks"] {
		if !hasNonEmptyString(row, "token") {
			continue
		}
		id, _ := row["id"].(string)
		digest, err := mintCapabilityDigest()
		if err != nil {
			return nil, fmt.Errorf("backup: re-mint pipeline webhook token: %w", err)
		}
		row["token"] = pipeline.RedactedCapabilityToken(id)
		row["token_hash"] = digest
		counts["pipeline_webhooks"]++
	}

	// page_public_tokens / page_webhooks: hash-only tables. The digest is
	// the lookup key; a fresh mint of an unpublished token leaves the
	// row structurally intact and its capability carried-across-revoked.
	for _, row := range dump.Tables["page_public_tokens"] {
		if !hasNonEmptyString(row, "token_hash") {
			continue
		}
		digest, err := mintCapabilityDigest()
		if err != nil {
			return nil, fmt.Errorf("backup: re-mint page public token: %w", err)
		}
		row["token_hash"] = digest
		counts["page_public_tokens"]++
	}
	for _, row := range dump.Tables["page_webhooks"] {
		if !hasNonEmptyString(row, "token_hash") {
			continue
		}
		digest, err := mintCapabilityDigest()
		if err != nil {
			return nil, fmt.Errorf("backup: re-mint page webhook token: %w", err)
		}
		row["token_hash"] = digest
		counts["page_webhooks"]++
	}

	return counts, nil
}

// hasNonEmptyString reports whether row carries column as a non-empty
// string — the gate for "this row holds a capability worth re-keying".
func hasNonEmptyString(row map[string]any, column string) bool {
	v, ok := row[column].(string)
	return ok && v != ""
}

// mintHexToken produces nBytes of crypto/rand as hex — the invitation
// token shape (internal/api.generateToken).
func mintHexToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// mintCapabilityDigest mints a 32-byte token (base64url, the exposure
// token shape) and returns only its digest. The token is discarded on
// purpose: for these tables the fork's capability is deliberately
// carried across with no holder — see the file comment — so the
// preimage must not survive anywhere, including this process.
func mintCapabilityDigest() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return pipeline.HashCapabilityToken(base64.RawURLEncoding.EncodeToString(b)), nil
}

// warnCapabilityTokensReminted is the operator-facing note, shared by
// the dry-run and committed paths so the two cannot describe the same
// fork differently. It is the difference between "carried across
// revoked, reported" and the silent capability change #2274 forbids.
func warnCapabilityTokensReminted(logger func(string), counts map[string]int, dryRun bool) {
	if len(counts) == 0 || logger == nil {
		return
	}
	verb := "re-keyed"
	if dryRun {
		verb = "would be re-keyed"
	}
	tables := make([]string, 0, len(counts))
	n := 0
	for t, c := range counts {
		tables = append(tables, fmt.Sprintf("%s (%d)", t, c))
		n += c
	}
	sortStrings(tables)
	logger(fmt.Sprintf(
		"NOTE: %d capability token(s) %s across: %s. A fork does not inherit live capabilities — "+
			"the source's invitation links, /exposed/ URLs, webhook and public-page tokens keep working "+
			"ONLY against the source workspace. Re-send the fork's invitations from its member list; "+
			"re-create or rotate its webhooks, exposures and public links from the fork itself.",
		n, verb, strings.Join(tables, ", ")))
}
