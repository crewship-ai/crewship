# Design note — Backup MANIFEST.json integrity (iter#3 H1-new)

**Status:** design only — net-new feature with key-storage and
migration decisions that need a dedicated session, not a loop tick.
**Source:** iter#3 H1-new (LIVE dry-run probe: tamper MANIFEST →
restore returns 200 OK).
**Author:** audit-loop, 2026-05-22.

## What the audit caught

The backup bundle has a `MANIFEST.json` entry describing the
payload (table list, devcontainer manifest, source workspace,
counts). The restorer validates the bundle shape syntactically
(JSON parses, expected keys present) but does NOT verify that the
manifest hasn't been modified between the time it was emitted and
the time it's restored.

LIVE-verified: hand-edit `MANIFEST.json` (e.g. add a fake table
hint, flip a count field) inside the bundle tar, feed it to the
restorer, and the restore returns 200 OK against the tampered
manifest. The same attack path applies to any filesystem write or
MITM intercepting the bundle between backup-emit and restore-read.

## Why this isn't a loop-sized fix

Three decisions need to land before the code can:

### 1. Where does the signing key live

Three plausible options:

| Option | Pro | Con |
|---|---|---|
| Reuse `ENCRYPTION_KEY` | Already required + provisioned | Couples backup integrity to credential encryption — key rotation now risks breaking *both* surfaces |
| New env var `CREWSHIP_BACKUP_HMAC_KEY` | Decoupled, can rotate independently | Operators need to provision one more secret; not in the current `.env.example` |
| Per-cluster derived from `NEXTAUTH_SECRET` | Single source of truth | NextAuth-secret rotation breaks legacy backups irrecoverably |

PRD §10 hasn't decided this yet. The audit team's preference
should be reconciled with whoever owns the env-var surface before
picking.

### 2. Detached signature vs. in-manifest HMAC

- **Detached `MANIFEST.json.sig`**: matches how cosign signs
  release archives. Verifier reads sig, manifest, key, computes
  HMAC, compares constant-time. Clean separation. **But** sig file
  itself is unsigned — an attacker who writes the bundle can swap
  both manifest + sig.
- **In-manifest `_integrity` field**: HMAC computed over the
  manifest minus that field, written into a reserved key. Simpler:
  one file. **But** parsers must strip the field before re-computing,
  and any forward-compatible parser must agree on the canonicalised
  shape (key ordering, whitespace).

Either works; the audit didn't specify which.

### 3. Migration / fail-mode

Three sub-decisions:

- **Existing bundles** lack the signature. `restorer.go` has to
  decide: `warn`, `require` (fail without sig), or `lax` (require
  iff the key is present in env).
- **Skew across versions**: a 0.1 → 0.2 SDK bump that adds a new
  manifest field changes the canonicalisation; the HMAC has to
  cover only fields the producer + verifier agree on.
- **Operator UX**: if `CREWSHIP_BACKUP_HMAC_KEY` is set wrong,
  every restore fails. Need a clear error message with the
  specific mismatched-field hint, not a flat "signature mismatch."

## Sketch of where the code would land

Three call sites:

| File | What |
|---|---|
| `internal/backup/manifest.go` | Add `Sign(manifest, key)` + `Verify(manifest, sig, key)` helpers. |
| `internal/backup/runner.go` (write) | After writing `MANIFEST.json` to the tar, emit `MANIFEST.json.sig` (option 2.a) or include the integrity field in the JSON itself (option 2.b). |
| `internal/backup/runner_restore.go` (read) | After parsing the manifest, verify the signature before any restore action. Surface the failure with operator-readable context. |

LoC estimate: ~200–300 across code + tests, plus operator-facing
docs for the env var and the migration toggle.

## Recommended sequencing

1. Open a discussion with the maintainer on (1) + (3.a). Five
   minutes; unblocks everything below.
2. Land a `crewship backup verify <bundle>` standalone command
   first — read-only, exercises the verifier on existing bundles
   without changing the write path. Validates the canonicalisation
   choices before the producer side touches anything.
3. Once verify is stable, land write-side signing behind a
   `CREWSHIP_BACKUP_SIGN=1` opt-in flag.
4. After a stable beta with sign + verify both ON, flip the
   restore-side default to require a signature.

## Until then

Operators relying on backup integrity should:

- Store backup bundles in a filesystem the restore host can read
  but the network can't write to (S3 bucket with versioning + write
  ACL, signed cosign envelope, sneakernet).
- Run `sha256sum` on emitted bundles and pin the digest in a
  separate channel (commit message, deployment runbook, secret
  manager). Compare on restore.

These are operational mitigations, not a code fix — the audit
finding stays open until the signing surface lands.
