package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// L4 — the chained audit write is a PRECONDITION of the reveal, not a side
// effect of it. Tests T-R14 (fail closed), T-R15 (no value, no hash in the
// payload) and T-R16 (the chain still verifies).
//
// The distinction that makes this layer worth having: credential_audit is a
// flat append table. A DB-write attacker can delete a row from it and nothing
// notices. The journal is an HMAC hash-chain with signed compaction
// checkpoints, so a deleted reveal breaks verification. Writing to only the
// flat table would mean the most sensitive event in the product is the one
// that is easiest to erase.

// T-R14 — when the chained write fails, the reveal fails CLOSED: 500, and the
// value is nowhere in the response. The alternative ("return the value, retry
// the audit later") is what an async emit would give us, and it is precisely
// the shape that loses the record of the one event you will be asked about.
func TestReveal_ChainedAuditFailureFailsClosed(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-audit", true)
	r.seedMember(t, "ws-audit", "u-owner", "OWNER", []string{CapabilityChat, CapabilityCredentialReveal})
	r.seedCredential(t, "ws-audit", "u-owner", "cred-1", "GH_TOKEN", "ghp_failclosedvalue", SensitivityStandard)

	r.j.failWith = errors.New("journal: database is locked")

	rec := r.doReveal(r.revealReq("cred-1", "ws-audit", "u-owner", "OWNER", validRevealReason))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500 when the chained audit write fails", rec.Code, rec.Body.String())
	}
	if got := revealValue(t, rec); got != "" {
		t.Fatalf("value field = %q, want empty — an unaudited reveal must return nothing", got)
	}
	if bodyMentions(rec, "ghp_failclosedvalue") {
		t.Fatal("fail-closed reveal still leaked the credential value into the body")
	}
}

// A handler with no journal wired at all is the same failure, not a free
// pass. noopEmitter silently accepts most entry types, so "journal not
// configured" would otherwise degrade into "reveal with no audit" — the
// worst possible default for this endpoint.
func TestReveal_JournalNotWiredFailsClosed(t *testing.T) {
	r := newRevealRig(t)
	r.h.SetJournal(nil)
	r.seedWorkspace(t, "ws-nojournal", true)
	r.seedMember(t, "ws-nojournal", "u-owner", "OWNER", []string{CapabilityChat, CapabilityCredentialReveal})
	r.seedCredential(t, "ws-nojournal", "u-owner", "cred-1", "GH_TOKEN", "ghp_nojournalvalue", SensitivityStandard)

	rec := r.doReveal(r.revealReq("cred-1", "ws-nojournal", "u-owner", "OWNER", validRevealReason))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500 with no journal wired", rec.Code, rec.Body.String())
	}
	if bodyMentions(rec, "ghp_nojournalvalue") {
		t.Fatal("reveal with no audit sink leaked the credential value")
	}
}

// T-R15 — the payload carries who/what/why/where and NEITHER the value NOR
// any digest of it.
//
// Why a hash is banned and not merely discouraged: journal entries are
// readable by anyone with journal access and are exported in backups. A
// stored digest of a short secret (a 6-digit PIN, a 20-char API key from a
// known alphabet) is offline-crackable in seconds. Recording one would turn
// the tamper-evident audit log — the thing we built to protect the vault —
// into a second copy of the vault.
func TestReveal_JournalPayloadCarriesNoValueOrHash(t *testing.T) {
	r := newRevealRig(t)
	const secret = "ghp_payloadinspectionvalue"
	r.seedWorkspace(t, "ws-payload", true)
	r.seedMember(t, "ws-payload", "u-owner", "OWNER", []string{CapabilityChat, CapabilityCredentialReveal})
	r.seedCredential(t, "ws-payload", "u-owner", "cred-1", "GH_TOKEN", secret, SensitivityRestricted)

	rec := r.doReveal(r.revealReq("cred-1", "ws-payload", "u-owner", "OWNER", validRevealReason))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}

	entries := r.j.ofType(journal.EntryCredentialRevealed)
	if len(entries) != 1 {
		t.Fatalf("got %d credential.revealed entries, want exactly 1", len(entries))
	}
	e := entries[0]

	// Everything an incident responder needs, present.
	if e.WorkspaceID != "ws-payload" {
		t.Errorf("workspace_id = %q, want ws-payload", e.WorkspaceID)
	}
	if e.ActorType != journal.ActorUser || e.ActorID != "u-owner" {
		t.Errorf("actor = %s/%s, want user/u-owner", e.ActorType, e.ActorID)
	}
	for _, key := range []string{"credential_id", "credential_name", "classification", "reason", "actor_role", "ip"} {
		if _, ok := e.Payload[key]; !ok {
			t.Errorf("payload missing %q — the audit record has to answer who/what/why/where", key)
		}
	}
	if got := e.Payload["classification"]; got != SensitivityRestricted {
		t.Errorf("classification = %v, want %s", got, SensitivityRestricted)
	}
	if got := e.Payload["reason"]; got != validRevealReason {
		t.Errorf("reason = %v, want the caller's reason verbatim", got)
	}
	if got := e.Payload["ip"]; got != "203.0.113.7" {
		t.Errorf("ip = %v, want the request's client IP", got)
	}

	// Nothing that could reconstruct the secret, absent. Serialize the whole
	// entry — payload AND refs AND summary — and search it, so a value
	// smuggled into an unexpected field still fails the test.
	blob := revealMustJSON(t, map[string]any{
		"summary": e.Summary,
		"payload": e.Payload,
		"refs":    e.Refs,
	})
	if strings.Contains(blob, secret) {
		t.Fatalf("journal entry contains the plaintext value: %s", blob)
	}
	for name, digest := range digestsOf(secret) {
		if strings.Contains(blob, digest) {
			t.Fatalf("journal entry contains a %s digest of the value — a digest of a short secret is offline-crackable: %s", name, blob)
		}
	}
}

// digestsOf returns the encodings a well-meaning implementer would reach for
// if they decided a "fingerprint" of the value was harmless. Each one is
// banned for the same reason.
func digestsOf(secret string) map[string]string {
	sum := sha256.Sum256([]byte(secret))
	return map[string]string{
		"sha256-hex":        hex.EncodeToString(sum[:]),
		"sha256-hex-prefix": hex.EncodeToString(sum[:])[:12],
		"sha256-base64":     base64.StdEncoding.EncodeToString(sum[:]),
		"base64-plaintext":  base64.StdEncoding.EncodeToString([]byte(secret)),
	}
}

func revealMustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// T-R16 — end-to-end against the REAL journal writer and the REAL chain.
// The recording emitter above proves the handler's contract; this proves the
// row it produces is a well-formed link, i.e. that a reveal cannot be the
// entry that breaks verification for everyone else.
//
// It also exercises EmitSync's durability claim: VerifyChain reads committed
// rows, so if EmitSync had merely queued the entry the walk would find
// nothing to verify and the count assertion would fail.
func TestReveal_ChainStillVerifiesAfterReveal(t *testing.T) {
	r := newRevealRig(t)
	writer := journal.NewWriter(r.db, revealTestLogger(), journal.WriterOptions{})
	t.Cleanup(func() { _ = writer.Close() })
	r.h.SetJournal(writer)

	r.seedWorkspace(t, "ws-chain", true)
	r.seedMember(t, "ws-chain", "u-owner", "OWNER", []string{CapabilityChat, CapabilityCredentialReveal})
	r.seedCredential(t, "ws-chain", "u-owner", "cred-1", "GH_TOKEN", "ghp_chainvalue", SensitivityStandard)

	// A prior entry so the reveal is not the genesis link — a chain of one
	// verifies trivially and would prove nothing about prev_hash.
	if _, err := writer.EmitSync(context.Background(), journal.Entry{
		WorkspaceID: "ws-chain",
		Type:        journal.EntryCrewAction,
		ActorType:   journal.ActorUser,
		ActorID:     "u-owner",
		Summary:     "seed entry ahead of the reveal",
	}); err != nil {
		t.Fatalf("seed journal entry: %v", err)
	}

	for i := 0; i < 3; i++ {
		rec := r.doReveal(r.revealReq("cred-1", "ws-chain", "u-owner", "OWNER",
			fmt.Sprintf("Handing the token to the migration runbook, pass %d", i)))
		if rec.Code != http.StatusOK {
			t.Fatalf("reveal %d: status = %d body=%s, want 200", i, rec.Code, rec.Body.String())
		}
	}

	res, err := journal.VerifyChain(context.Background(), r.db, "ws-chain")
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !res.OK {
		t.Fatalf("chain broken after reveals: %+v", res)
	}

	// The reveals are actually ON the chain — a verifier that walked an
	// empty workspace would also report OK.
	var n int
	if err := r.db.QueryRow(
		`SELECT COUNT(*) FROM journal_entries WHERE workspace_id = ? AND entry_type = ?`,
		"ws-chain", string(journal.EntryCredentialRevealed)).Scan(&n); err != nil {
		t.Fatalf("count reveal entries: %v", err)
	}
	if n != 3 {
		t.Fatalf("committed %d credential.revealed rows, want 3 — EmitSync must be durable before the handler returns", n)
	}
}

// The flat credential_audit timeline gets the reveal too. §2.6 L4 says "NOT
// ONLY the flat table" — both, not either: the chain is the tamper-evident
// record, and the flat table is what the credential detail Sheet renders.
// This one is best-effort by design (the reveal already happened and the
// chained record already committed, so failing the request here would report
// a completed disclosure as an error).
func TestReveal_AlsoStampsFlatCredentialAudit(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-flat", true)
	r.seedMember(t, "ws-flat", "u-owner", "OWNER", []string{CapabilityChat, CapabilityCredentialReveal})
	r.seedCredential(t, "ws-flat", "u-owner", "cred-1", "GH_TOKEN", "ghp_flatvalue", SensitivityStandard)

	rec := r.doReveal(r.revealReq("cred-1", "ws-flat", "u-owner", "OWNER", validRevealReason))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}

	var metadata string
	if err := r.db.QueryRow(
		`SELECT COALESCE(metadata_json, '') FROM credential_audit WHERE credential_id = ? AND event_type = ?`,
		"cred-1", string(AuditEventReveal)).Scan(&metadata); err != nil {
		t.Fatalf("no REVEAL row in credential_audit: %v", err)
	}
	if strings.Contains(metadata, "ghp_flatvalue") {
		t.Fatalf("credential_audit metadata contains the value: %s", metadata)
	}
}
