package memory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/memory/memdiff"
	"github.com/crewship-ai/crewship/internal/scrubber"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Mutate is the one supported way to change a memory file.
//
// Contract: docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §8, with
// §3's invariants I4 (a mutation records the run and generation it belongs to)
// and I6 (a write the memory API confirmed is never lost to concurrency or to
// recovery).
//
// # Why this exists
//
// Before it there were eleven write paths and no shared mutation function.
// There was a shared durability primitive (writeFileDurable) and a shared lock
// (FileLock), and each path re-assembled its own sequence around them — or
// skipped both. Two of them (the portability placer, the seed-data writer) took
// no lock at all; one (WriteFile) took the lock *after* the cap check; one
// (Restore) deliberately took none. None of them had a revision, an operation
// id, a declared-removal check or a durable intent, so "two writers, one file"
// resolved as last-writer-wins with no evidence that anything was lost.
//
// # The order under the lock, which §8 fixes exactly
//
//	authorization
//	  -> recovery of any unconfirmed intent for this key
//	  -> idempotency (request hash vs the stored operation)
//	  -> revision and on-disk drift
//	  -> the real diff vs the declared removals
//	  -> protected records and cap
//	  -> durable intent
//	  -> fsync / atomic rename / fsync parent dir
//	  -> confirm the revision and the mutation in the DB
//
// §8 lists authorization first and does not say where recovery sits. It goes
// second here: the drift check compares the on-disk hash against the anchor,
// and an unconfirmed intent from a crashed writer is exactly the case where
// those two legitimately disagree. Running recovery after the drift check would
// turn every crash into a permanent conflict.
//
// Two rules that are easy to get wrong, and that §8 is explicit about:
//
//   - No open SQLite write transaction spans the filesystem I/O. The intent is
//     written and committed, then the file work happens, then a second
//     transaction confirms. SQLite has one writer; holding it across an fsync
//     would serialise every other writer in the process behind a disk flush.
//   - A unique pending intent per key is what blocks a second writer while
//     recovery is still possible. That is the partial unique index
//     idx_memory_mutations_pending, not a mutex.
//
// # Ledgerless mode
//
// db may be nil. That is NOT the full contract and Mutate says so: the result
// carries LedgerRecorded=false, revisions are unavailable, and an operation id
// buys no idempotency. What survives is everything the filesystem can enforce
// on its own — the lock, UTF-8 and CRLF normalisation, the declared-removal
// check, the cap, the scrubber, the durable write, and a CAS against the
// on-disk hash via ExpectedSHA256.
//
// It exists because the two agent-facing write paths cannot reach the database.
// The MCP dispatcher (tools.go) and the sidecar HTTP handler both run inside
// the agent container; neither holds a *sql.DB and neither can, without an IPC
// round trip through an API endpoint that does not exist. Reporting that
// honestly is better than pretending a revision was checked.

// MutateOp is the kind of change. §8 has exactly two.
type MutateOp string

const (
	// OpAppend adds Content to the end of the current file. ExpectedRevision
	// is optional (0 = unconditional), because appending cannot lose a
	// concurrent writer's lines — it can only order them.
	OpAppend MutateOp = "append"
	// OpReplace substitutes the whole body. §8 requires ExpectedRevision and
	// the declared Removals; an append expressed as a replace still needs
	// both (with empty Removals), and a caller that does not want CAS must
	// use OpAppend instead.
	OpReplace MutateOp = "replace"
)

func (o MutateOp) valid() bool { return o == OpAppend || o == OpReplace }

// The §8 error vocabulary. Every rejection a client can act on maps onto one of
// these with errors.Is; a rejection the *policy* produced (cap, scrubber,
// verifier, screen) is not an error at all — see MutateRejection.
var (
	// ErrMemoryConflict is a stale expected_revision, or on-disk content that
	// does not match the revision the ledger recorded (drift). §8: the agent
	// re-reads and proposes again with a NEW operation id, at most twice,
	// then surfaces the conflict. There is no automatic merge and no
	// automatic resurrection of deleted text.
	ErrMemoryConflict = errors.New("memory_conflict")

	// ErrUndeclaredRemoval is the real diff removing a base line the request
	// did not declare — or declaring one the diff keeps. §8 folds both into
	// one code; the wrapped *memdiff.RemovalError tells them apart.
	ErrUndeclaredRemoval = errors.New("undeclared_removal")

	// ErrOperationConflict is one operation id reused with a different
	// request. An identical retry returns the original result; a different
	// request under the same id is a client bug, and answering it would make
	// the id meaningless.
	ErrOperationConflict = errors.New("operation_conflict")

	// ErrProtectedRemoval is an attempt to remove a protected record without
	// an explicitly authorized operation. §8: "Chráněný záznam lze odstranit
	// jen explicitně autorizovanou operací, samotná removals deklarace
	// oprávnění nedává." Declaring the removal correctly is a correctness
	// statement, never a permission.
	ErrProtectedRemoval = errors.New("protected_removal")

	// ErrContentTooLarge is a replace whose base or target exceeds the
	// diffable budget. memdiff's Myers is quadratic in the edit distance and
	// its Diff has no error to return, so the bound has to live here, under
	// the lock, before the call. §8 sets a 1 MiB webhook body limit and says
	// nothing about memory content size; this is our number, not §8's.
	ErrContentTooLarge = errors.New("memory_content_too_large")

	// ErrLedgerRequired is a request that needs the durable ledger on a call
	// that passed db == nil — an ExpectedRevision, or Idempotent set with an
	// OperationID the caller expects to be honoured.
	ErrLedgerRequired = errors.New("memory_ledger_required")

	// ErrProfileUnmet is a write that asked for the guaranteed profile without
	// carrying what the guaranteed profile requires. It is deliberately not a
	// downgrade: a caller that cannot meet the contract is refused rather than
	// quietly served under the legacy one, because a write a UI then describes
	// as revision-checked is worse than a write that did not happen.
	ErrProfileUnmet = errors.New("memory_profile_unmet")

	// ErrNotCanonical is on-disk content that is not in the canonical form
	// (invalid UTF-8, or CRLF) on an operation that has to diff it. §8:
	// existing files go through an explicit import with a new revision, not
	// a silent normalisation. Set MutateRequest.Import to perform that
	// import as part of this mutation.
	ErrNotCanonical = errors.New("memory_not_canonical")
)

// Diff budget. memdiff's own doc records the cost on the reference dev box:
// two files of n lines with nothing in common cost 17 ms / 16 MiB at n=1000,
// 61 ms / 65 MiB at n=2000 and 239 ms / 265 MiB at n=4000. That is paid while
// holding the per-file lock, so the ceiling is a latency budget, not a memory
// one. 5000 lines is roughly 300 ms of absolute worst case and about 40x the
// largest capped tier (daily, 30 KB).
const (
	// DefaultMaxDiffLines bounds each side of the diff in lines.
	DefaultMaxDiffLines = 5000
	// DefaultMaxDiffBytes bounds each side of the diff in bytes, for the
	// pathological one-enormous-line shape that no line count catches.
	DefaultMaxDiffBytes = 1 << 20
)

// Profile is which guarantee a call is operating under, and it must be stated.
//
// There is no default on purpose. The historic behaviour is still available and
// still correct for the paths that cannot yet do better — but it has to be
// ASKED for by name, so that "this write had no revision check" is a decision
// somebody made rather than a field nobody filled in. An empty Profile is an
// error.
type Profile string

const (
	// ProfileLegacy keeps whatever the filesystem alone can enforce: the lock,
	// normalisation, the cap, the scrubber, the durable write, and a CAS against
	// the on-disk hash when the caller supplies one. It does NOT give revisions,
	// operation-id idempotency, crash recovery, or run/generation verification,
	// and a result from it says so.
	ProfileLegacy Profile = "legacy"

	// ProfileGuaranteed is what the parallel release profile requires, and it
	// refuses anything less. A write under it must carry a caller-supplied
	// operation id, must reach the durable ledger, must declare
	// ExpectedRevision and non-nil Removals when it replaces, and must wire
	// Authorize so the run and generation are verified (I4).
	ProfileGuaranteed Profile = "guaranteed"
)

// requirements returns why p refuses req, or nil.
//
// Each clause is one of the things the guaranteed profile exists FOR. They are
// listed together rather than checked wherever the code happens to reach them,
// so the contract can be read in one place and so a caller learns everything it
// is missing at once instead of one round trip at a time.
func (p Profile) requirements(req MutateRequest, hasLedger bool) error {
	switch p {
	case ProfileLegacy:
		return nil
	case ProfileGuaranteed:
	case "":
		return fmt.Errorf("%w: no profile declared; pass ProfileLegacy or ProfileGuaranteed explicitly", ErrProfileUnmet)
	default:
		return fmt.Errorf("%w: unknown profile %q", ErrProfileUnmet, p)
	}

	var missing []string
	if !hasLedger {
		missing = append(missing, "a durable ledger handle (db was nil)")
	}
	if strings.TrimSpace(req.OperationID) == "" {
		missing = append(missing, "a caller-supplied operation id (one synthesised here is not idempotent across a retry)")
	}
	if req.Authorize == nil {
		missing = append(missing, "an Authorize func to verify the run and its generation")
	}
	if req.Op == OpReplace {
		if req.ExpectedRevision == 0 {
			missing = append(missing, "ExpectedRevision (a replace without CAS can silently lose a concurrent write)")
		}
		if req.Removals == nil {
			missing = append(missing, "a non-nil Removals declaration (nil means undeclared, not empty)")
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: the guaranteed profile requires %s", ErrProfileUnmet, strings.Join(missing, "; "))
}

// MutateRequest is one proposed change. The zero value is not usable:
// Profile, OperationID, AuditPath, Path and Op are always required.
type MutateRequest struct {
	// Profile is which guarantee this write is made under. Required — there is
	// no default, so a legacy write has to name itself one.
	Profile Profile

	// OperationID is stable across retries of the SAME logical write. It is
	// the idempotency key: an identical retry returns the original result, a
	// different request under the same id is ErrOperationConflict. Required
	// even in ledgerless mode, where it is recorded in the result but buys
	// no deduplication.
	OperationID string

	WorkspaceID string

	// ActorType is the §3 provenance discriminator: agent | user | system |
	// consolidator. ActorID names the specific one.
	ActorType string
	ActorID   string

	// RunID and Generation are I4's fencing pair, in internal/work's
	// vocabulary: run_id is one attempt, generation is bumped on every claim
	// and refuses a transition carrying a stale term. Mutate RECORDS them.
	// Verifying that they are still current is Authorize's job, because the
	// work ledger lives in a package internal/memory must not import.
	RunID      string
	Generation int64

	// Source is free-form provenance for the audit trail: "memory.write",
	// "sidecar", "restore", "consolidator". Never a secret.
	Source string

	// Tier is the memory_versions tier this file belongs to. Scope is the
	// ownership discriminator ("agent:<slug>", "crew:<id>") — the same
	// vocabulary audit_watcher.go already uses in memory_versions.path.
	Tier  Tier
	Scope string
	Key   string

	// Path is the resolved canonical absolute path on disk. AuditPath is the
	// workspace-unique logical key ("agent:alice/AGENT.md"), which is what
	// the revision anchor and the ledger are keyed by. Two agents in one
	// workspace both own an "AGENT.md"; only the scoped form separates them.
	Path      string
	AuditPath string

	Op MutateOp
	// Content is the delta for OpAppend and the whole new body for
	// OpReplace. It is normalised (CRLF -> LF, trailing newline preserved,
	// invalid UTF-8 rejected) before anything else looks at it.
	Content string

	// ExpectedRevision is required for OpReplace: the revision the client
	// diffed against. 0 means "no revision recorded yet" and is correct for
	// the first contract write to a key. For OpAppend, 0 is unconditional.
	ExpectedRevision int64
	// ExpectedSHA256 is the optional on-disk CAS. When set, the raw bytes on
	// disk must hash to it. This is the only CAS available in ledgerless
	// mode, and it is a useful belt-and-braces check with the ledger too.
	ExpectedSHA256 string

	// Removals are the spans the client says its replace deletes, in the
	// base revision's normalised line numbering. §8 requires them on every
	// replace; an empty slice is a positive statement that nothing is
	// removed, not an absence of one.
	Removals []memdiff.Removal

	// Import performs the §8 explicit import as part of this mutation, when
	// the on-disk base is not already canonical. Without it, a non-canonical
	// base is ErrNotCanonical rather than a silent normalisation.
	Import bool

	// OverrideDrift skips the drift check — the on-disk content is replaced
	// whatever it is. This is for an operation whose whole purpose is to
	// overwrite the current file, and Restore is the only one: refusing a
	// restore because the file was hand-edited would refuse it in exactly
	// the case an operator reaches for it.
	//
	// It does NOT weaken §8's "never automatically overwrite", which governs
	// RECOVERY — an unattended process deciding on its own that a third
	// party's bytes are expendable. This is a human asking for it, and it is
	// recorded as such (memory_mutations.override_drift).
	OverrideDrift bool

	// Cfg is today's write policy, verbatim: cap, scrubber, verifier.
	Cfg WriteConfig

	// BlobRoot is the content-addressed store ({memoryRoot}/versions) the
	// durable intent parks the target content in, so a crash between the
	// intent and the rename can still be completed. Required when db != nil.
	BlobRoot string

	// StorageRoot confines canonical reads, locks, writes and inline recovery
	// beneath the host storage directory. HTTP callers must set this; empty is
	// reserved for trusted in-process/legacy callers.
	StorageRoot string

	// RecordVersion additionally inserts a memory_versions row for this
	// write, the way Restore and the consolidator already do. Off by default
	// because the MCP dispatcher never did.
	RecordVersion bool

	// MaxDiffLines / MaxDiffBytes override the diff budget. 0 uses the
	// package defaults; negative disables the bound, which is only sane for
	// a trusted caller with its own ceiling.
	MaxDiffLines int
	MaxDiffBytes int

	// Authorize runs first, under the lock. It is where a caller enforces
	// what internal/memory cannot see: the permission scope, and I4's check
	// that RunID/Generation are still the live attempt. nil = no check, which
	// is what every caller does today and is the honest description of the
	// pre-existing behaviour, not an improvement on it.
	Authorize func(ctx context.Context) error

	// Screen runs at the "protected records and cap" step, on the exact
	// bytes that would land on disk, before the cap. It is how the MCP
	// dispatcher keeps its prompt-injection scan and quarantine on the write
	// path without internal/memory growing a dependency on the agent's
	// memory dir. Returning a non-nil rejection refuses the write without it
	// being a Go error.
	Screen func(ctx context.Context, final []byte) (*MutateRejection, error)

	// Protected reports whether a base line may not be removed without an
	// explicitly authorized operation. nil = nothing is protected, which is
	// today's behaviour. AuthorizedForProtectedRemoval is that explicit
	// authorization; §8 is emphatic that a correct removals declaration is
	// not one.
	Protected                     func(line []byte) bool
	AuthorizedForProtectedRemoval bool

	// testHook stops a mutation at a named step so the crash tests can
	// simulate a process death at exactly that point. Unexported: only
	// package memory can set it, so it cannot be reached from production
	// code by accident. Steps: "after_intent", "after_rename",
	// "before_confirm".
	testHook func(step string) error
}

// MutateRejection is a policy refusal. It is deliberately not an error: the
// same distinction WriteResult already draws — a cap or scrubber refusal is a
// structured outcome the caller renders as a 422 or a memory.write_rejected
// journal entry, while a filesystem failure is an error.
type MutateRejection struct {
	Kind             string // "cap" | "scrubber" | "verifier" | caller-defined
	Message          string
	Detail           map[string]any
	Hits             []scrubber.Hit
	VerifierFindings []VerifierFinding
}

// MutateResult is the outcome of one Mutate call. Rejection != nil means the
// file was NOT changed and err is nil.
type MutateResult struct {
	// Revision is the new monotonic revision for this key, or 0 in
	// ledgerless mode. ContentSHA256 hashes the exact bytes now on disk.
	Revision      int64
	ContentSHA256 string
	BytesWritten  int
	// Written is the exact byte slice that landed on disk (post-redaction
	// when the scrubber is in ModeRedact). Callers that need to show the new
	// body back to the model — the dispatcher's soft-cap guidance does —
	// must use this rather than re-reading the file, which another writer
	// may already have changed.
	Written []byte

	// BaseRevision / BaseSHA256 describe what this mutation was applied to.
	// On a conflict they are the values the caller should re-read against.
	BaseRevision int64
	BaseSHA256   string

	// Base is the raw on-disk content this mutation read under the lock. The
	// dispatcher's cap-overflow guidance hands it back to the model as
	// current_entries so it can consolidate in the same turn.
	Base []byte

	MutationID string
	Op         MutateOp

	// Idempotent is true when this call returned a prior operation's stored
	// result without touching the file.
	Idempotent bool
	// LedgerRecorded is false in ledgerless mode. A caller that reports
	// success to a user must not describe such a write as revision-checked.
	LedgerRecorded bool
	// Recovered counts unconfirmed intents this call completed before
	// serving. Non-zero means a previous writer crashed.
	Recovered int
	// AdoptedBase is true when no revision anchor existed and this mutation
	// adopted the on-disk content as revision 0 — the §8 explicit import,
	// performed by the first contract write to a pre-existing file.
	AdoptedBase bool
	// NormalizedBase is true when Import rewrote a non-canonical base.
	NormalizedBase bool
	// RemovalsVerified is true when the request declared removals (even an
	// empty set) and the real diff was checked against them. False means the
	// caller declared nothing and the write went through unverified — see
	// buildTarget on why that is accepted rather than refused.
	RemovalsVerified bool
	// MergedFinalLine is true when an append ran against a base that did not
	// end in LF, so the first appended bytes joined the previous last line.
	// A line diff reads that as a delete plus an insert; §8 does not require
	// an append to declare it, so it is surfaced instead of refused.
	MergedFinalLine bool

	Rejection *MutateRejection

	// Version is the memory_versions row, when RecordVersion was set.
	Version *RecordResult
}

// mutationStateIntent and friends are the §8 intent state machine. renamed is
// written by the recovery path, not by the happy path: it is the state a
// crashed writer is *found* in, and recording it on every normal write would
// cost a third fsync to describe something the file's own hash already says.
const (
	mutationStateIntent     = "intent"
	mutationStateRenamed    = "renamed"
	mutationStateConfirmed  = "confirmed"
	mutationStateConflicted = "conflicted"
)

// Mutate applies one change to one memory file. See the package-level contract
// above for the ordering and the ledgerless caveat.
func Mutate(ctx context.Context, db *sql.DB, req MutateRequest) (MutateResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return MutateResult{}, fmt.Errorf("preflight context check: %w", err)
	}
	// The profile gate runs FIRST, ahead of field validation, because it
	// explains more. A guaranteed write missing its operation id should be told
	// which guarantee it failed and everything else it is missing, not just
	// that one field is empty — a caller that fixes one field at a time makes
	// one round trip per requirement.
	if err := req.Profile.requirements(req, db != nil); err != nil {
		return MutateResult{}, err
	}
	if err := req.validate(db); err != nil {
		return MutateResult{}, err
	}

	// The input is normalised before anything else reads it, so every later
	// step — the request hash, the diff, the cap, the bytes on disk — sees
	// the same canonical form. §8: CRLF to LF, trailing newline preserved,
	// invalid UTF-8 rejected.
	normContent, err := memdiff.Normalize([]byte(req.Content))
	if err != nil {
		return MutateResult{}, fmt.Errorf("%w: content: %w", ErrNotCanonical, err)
	}

	file, err := openMutationFile(req.StorageRoot, req.Path, true)
	if err != nil {
		return MutateResult{}, fmt.Errorf("open canonical parent: %w", err)
	}
	defer file.close()

	// One lock for the whole supported write contract, not for one handler.
	lk := file.lock()
	if err := lk.Lock(); err != nil {
		return MutateResult{}, fmt.Errorf("acquire mutation lock: %w", err)
	}
	defer func() { _ = lk.Unlock() }()

	if err := ctx.Err(); err != nil {
		return MutateResult{}, fmt.Errorf("post-lock context check: %w", err)
	}

	// 1. Authorization. Also where I4's run/generation freshness check
	// belongs; internal/memory cannot see the work ledger.
	if req.Authorize != nil {
		if err := req.Authorize(ctx); err != nil {
			return MutateResult{}, err
		}
	}

	var res MutateResult
	res.Op = req.Op
	res.LedgerRecorded = db != nil

	// 2. Recovery, before serving any new write for this key.
	if db != nil {
		recovered, err := recoverFileLocked(ctx, db, req.WorkspaceID, req.AuditPath, file, req.BlobRoot)
		if err != nil {
			return res, err
		}
		res.Recovered = recovered
	}

	// 3. Idempotency: the request hash against the stored operation.
	requestSHA := requestHash(req, normContent)
	if db != nil {
		prior, err := loadOperation(ctx, db, req.WorkspaceID, req.AuditPath, req.OperationID)
		if err != nil {
			return res, err
		}
		if prior != nil {
			if prior.requestSHA != requestSHA {
				return res, fmt.Errorf("%w: operation %q on %s was recorded with a different request",
					ErrOperationConflict, req.OperationID, req.AuditPath)
			}
			switch prior.state {
			case mutationStateConfirmed:
				out := prior.result
				out.Idempotent = true
				out.LedgerRecorded = true
				out.Recovered = res.Recovered
				out.Op = req.Op
				return out, nil
			default:
				// Recovery ran above and did not settle it, so the file is
				// at neither the base nor the target hash. Surfacing that as
				// a conflict is the only safe answer; overwriting would
				// destroy whatever third party wrote it.
				return res, fmt.Errorf("%w: operation %q on %s is unconfirmed and its file no longer matches either the base or the target",
					ErrMemoryConflict, req.OperationID, req.AuditPath)
			}
		}
	}

	// 4. Revision and on-disk drift.
	rawBase, err := file.read()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return res, fmt.Errorf("read canonical: %w", err)
	}
	baseSHA := sha256Hex(rawBase)
	res.Base = rawBase
	res.BaseSHA256 = baseSHA

	var anchor *revisionAnchor
	if db != nil {
		anchor, err = loadAnchor(ctx, db, req.WorkspaceID, req.AuditPath)
		if err != nil {
			return res, err
		}
	}
	if anchor != nil {
		res.BaseRevision = anchor.revision
		if anchor.contentSHA != baseSHA && !req.OverrideDrift {
			return res, fmt.Errorf("%w: %s is at revision %d with content %s but the file on disk hashes to %s — it was edited outside the memory contract",
				ErrMemoryConflict, req.AuditPath, anchor.revision, short(anchor.contentSHA), short(baseSHA))
		}
	} else if db != nil && len(rawBase) > 0 {
		// No anchor and a non-empty file: this is the first contract write to
		// a pre-existing file. §8 wants that to be an explicit import with a
		// new revision rather than a silent adoption, and this IS that
		// import — recorded on the mutation row and surfaced in the result.
		res.AdoptedBase = true
	}
	if req.ExpectedSHA256 != "" && !strings.EqualFold(req.ExpectedSHA256, baseSHA) {
		return res, fmt.Errorf("%w: %s expected on-disk content %s but found %s",
			ErrMemoryConflict, req.AuditPath, short(req.ExpectedSHA256), short(baseSHA))
	}
	if db != nil && !req.OverrideDrift && (req.Op == OpReplace || req.ExpectedRevision != 0) {
		if req.ExpectedRevision != res.BaseRevision {
			return res, fmt.Errorf("%w: %s is at revision %d, request expected %d",
				ErrMemoryConflict, req.AuditPath, res.BaseRevision, req.ExpectedRevision)
		}
	}

	// 5. The real diff against the declared removals.
	final, normalizedBase, removalsVerified, err := req.buildTarget(rawBase, normContent)
	if err != nil {
		return res, err
	}
	res.NormalizedBase = normalizedBase
	res.RemovalsVerified = removalsVerified
	res.MergedFinalLine = req.Op == OpAppend && len(rawBase) > 0 &&
		rawBase[len(rawBase)-1] != '\n' && len(normContent) > 0

	// 6. Protected records and cap.
	if screenRej, err := req.screen(ctx, final); err != nil {
		return res, err
	} else if screenRej != nil {
		res.Rejection = screenRej
		return res, nil
	}
	effective, policyRej, err := applyWritePolicy(ctx, final, len(rawBase), req.Cfg)
	if err != nil {
		return res, err
	}
	if policyRej != nil {
		res.Rejection = policyRej
		return res, nil
	}

	targetSHA := sha256Hex(effective)
	newRevision := res.BaseRevision + 1

	// A no-op write still consumes an operation id and still gets a row, but
	// it must not bump the revision: §8's revision is a content identity that
	// clients compare, and two different numbers for identical bytes would
	// make every cached expected_revision spuriously stale.
	if targetSHA == baseSHA && anchor != nil {
		newRevision = res.BaseRevision
	}

	// 7. Durable intent, committed before any filesystem work.
	var mutationID string
	if db != nil {
		mutationID, err = writeIntent(ctx, db, req, intentRow{
			requestSHA:   requestSHA,
			baseRevision: res.BaseRevision,
			baseSHA:      baseSHA,
			newRevision:  newRevision,
			targetSHA:    targetSHA,
			target:       effective,
			adopted:      res.AdoptedBase,
			normalized:   normalizedBase,
			verified:     removalsVerified,
			override:     req.OverrideDrift,
		})
		if err != nil {
			return res, err
		}
		res.MutationID = mutationID
		if req.testHook != nil {
			if herr := req.testHook("after_intent"); herr != nil {
				return res, herr
			}
		}
	}

	// 8. fsync tempfile, atomic rename, fsync parent dir.
	if err := file.write(effective); err != nil {
		return res, fmt.Errorf("durable write: %w", err)
	}
	if req.testHook != nil {
		if herr := req.testHook("after_rename"); herr != nil {
			return res, herr
		}
	}

	res.ContentSHA256 = targetSHA
	res.BytesWritten = len(effective)
	res.Written = effective
	if db != nil {
		// Revision stays 0 in ledgerless mode. Reporting a number that no
		// anchor backs would be worse than reporting none: the client would
		// pass it back as an expected_revision that nothing can check.
		res.Revision = newRevision
	}

	// 9. Confirm the revision and the mutation, in one second transaction.
	if db != nil {
		if req.testHook != nil {
			if herr := req.testHook("before_confirm"); herr != nil {
				return res, herr
			}
		}
		if err := confirmMutation(ctx, db, req, mutationID, newRevision, targetSHA, len(effective), res); err != nil {
			return res, err
		}
		if req.RecordVersion {
			parent, perr := LatestVersionSha(ctx, db, req.WorkspaceID, req.AuditPath)
			if perr != nil {
				return res, fmt.Errorf("lookup parent version sha: %w", perr)
			}
			rec, verr := RecordVersion(ctx, db, VersionRecord{
				WorkspaceID: req.WorkspaceID,
				Path:        req.AuditPath,
				Tier:        req.Tier,
				Content:     effective,
				WrittenBy:   req.ActorID,
				ParentSha:   parent,
				BlobRoot:    req.BlobRoot,
			})
			if verr != nil {
				return res, fmt.Errorf("record version: %w", verr)
			}
			res.Version = rec
		}
	}
	return res, nil
}

func (r MutateRequest) validate(db *sql.DB) error {
	if strings.TrimSpace(r.OperationID) == "" {
		return fmt.Errorf("mutate: operation_id required")
	}
	if strings.TrimSpace(r.Path) == "" {
		return fmt.Errorf("mutate: path required")
	}
	if strings.TrimSpace(r.AuditPath) == "" {
		return fmt.Errorf("mutate: audit_path required")
	}
	if !r.Op.valid() {
		return fmt.Errorf("mutate: op must be %q or %q, got %q", OpAppend, OpReplace, r.Op)
	}
	if db == nil {
		if r.ExpectedRevision != 0 {
			return fmt.Errorf("%w: expected_revision %d cannot be checked without a mutation ledger",
				ErrLedgerRequired, r.ExpectedRevision)
		}
		return nil
	}
	if strings.TrimSpace(r.WorkspaceID) == "" {
		return fmt.Errorf("mutate: workspace_id required")
	}
	if strings.TrimSpace(r.BlobRoot) == "" {
		return fmt.Errorf("mutate: blob_root required — the durable intent has nowhere to park the target content")
	}
	if r.ExpectedRevision < 0 {
		return fmt.Errorf("mutate: expected_revision must not be negative")
	}
	if r.RecordVersion && !ValidTier(r.Tier) {
		return fmt.Errorf("%w: %q", ErrInvalidTier, r.Tier)
	}
	return nil
}

// buildTarget assembles the bytes that would land on disk and runs the
// declared-removal check against the REAL diff.
//
// # nil removals versus empty removals
//
// This is the one place the distinction between a nil slice and an empty one
// carries meaning, and it is load-bearing.
//
// §8 says an append expressed as a replace "musí nést expected_revision a
// prázdné removals" — must carry expected_revision AND EMPTY removals. So an
// EMPTY (non-nil) Removals is a positive assertion: this replace deletes
// nothing. It is verified, and a replace that actually drops a line is refused
// with undeclared_removal.
//
// A NIL Removals is the absence of that assertion: the caller did not declare.
// §8 read strictly would refuse it, and refusing it would break every
// memory.write the model has issued since the tool shipped, none of which
// carries removals. So a nil declaration is accepted UNVERIFIED and recorded as
// such — memory_mutations.removals_declared is 0 and MutateResult.RemovalsVerified
// is false. An unverified replace is visible in the ledger rather than
// indistinguishable from a verified one, which is the honest version of the
// compatibility break §8 does not budget for.
//
// Append is never diffed on its own account. Appending cannot delete a base
// line — with one exception worth naming: when the base does not end in LF, the
// delta merges into the final line, which a line diff reads as a delete plus an
// insert. Making that an undeclared removal would fail every append to a file
// without a trailing newline, which is most of them, so it is REPORTED
// (MutateResult.MergedFinalLine) rather than refused.
func (r MutateRequest) buildTarget(rawBase, normContent []byte) (final []byte, normalizedBase, verified bool, err error) {
	// The Protected scan needs the real diff too, so a caller with a
	// protected-record predicate pays for the diff on a replace whether or
	// not it declared removals.
	needsDiff := r.Removals != nil || (r.Op == OpReplace && r.Protected != nil)

	if !needsDiff {
		// Nothing to verify. The base is copied through byte-for-byte, so a
		// non-canonical file is not silently rewritten by an append.
		if r.Op == OpReplace {
			return normContent, false, false, nil
		}
		out := make([]byte, 0, len(rawBase)+len(normContent))
		out = append(out, rawBase...)
		out = append(out, normContent...)
		return out, false, false, nil
	}

	base := rawBase
	if len(base) > 0 {
		canonical, nerr := memdiff.Normalize(base)
		if nerr != nil {
			return nil, false, false, fmt.Errorf("%w: %s is not valid UTF-8 on disk; it needs an explicit import before it can be diffed: %w",
				ErrNotCanonical, r.AuditPath, nerr)
		}
		if string(canonical) != string(base) {
			if !r.Import {
				return nil, false, false, fmt.Errorf("%w: %s contains CRLF on disk; §8 requires an explicit import with a new revision rather than a silent normalisation (set Import)",
					ErrNotCanonical, r.AuditPath)
			}
			base = canonical
			normalizedBase = true
		}
	}

	if r.Op == OpAppend {
		final = make([]byte, 0, len(base)+len(normContent))
		final = append(final, base...)
		final = append(final, normContent...)
	} else {
		final = normContent
	}

	if err := r.checkDiffBudget(base, final); err != nil {
		return nil, normalizedBase, false, err
	}

	baseLines := memdiff.SplitLines(base)
	targetLines := memdiff.SplitLines(final)

	if r.Removals != nil {
		if err := memdiff.VerifyRemovals(baseLines, targetLines, r.Removals); err != nil {
			return nil, normalizedBase, false, fmt.Errorf("%w: %s: %w", ErrUndeclaredRemoval, r.AuditPath, err)
		}
		verified = true
	}

	// Protected records. Checked against the REAL removals, not the declared
	// ones: a client that declares nothing and removes a protected line must
	// still be refused, and it is refused here rather than by the declaration
	// check alone.
	if r.Protected != nil && !r.AuthorizedForProtectedRemoval {
		for _, span := range memdiff.Removals(baseLines, targetLines) {
			for ln := span.StartLine; ln <= span.EndLine(); ln++ {
				if r.Protected(baseLines[ln-1]) {
					return nil, normalizedBase, verified, fmt.Errorf(
						"%w: base line %d of %s is a protected record; removing it needs an explicitly authorized operation, and a correct removals declaration is not one",
						ErrProtectedRemoval, ln, r.AuditPath)
				}
			}
		}
	}
	return final, normalizedBase, verified, nil
}

// checkDiffBudget bounds both sides before memdiff.Diff is called. memdiff's
// own documentation is explicit that it has no such guard and that the caller
// wiring it into a write path owns this.
func (r MutateRequest) checkDiffBudget(base, target []byte) error {
	maxLines := r.MaxDiffLines
	if maxLines == 0 {
		maxLines = DefaultMaxDiffLines
	}
	maxBytes := r.MaxDiffBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxDiffBytes
	}
	for _, side := range []struct {
		name string
		b    []byte
	}{{"base", base}, {"target", target}} {
		if maxBytes > 0 && len(side.b) > maxBytes {
			return fmt.Errorf("%w: %s %s is %d bytes, over the %d-byte diff budget",
				ErrContentTooLarge, r.AuditPath, side.name, len(side.b), maxBytes)
		}
		if maxLines > 0 {
			n := countLines(side.b)
			if n > maxLines {
				return fmt.Errorf("%w: %s %s is %d lines, over the %d-line diff budget",
					ErrContentTooLarge, r.AuditPath, side.name, n, maxLines)
			}
		}
	}
	return nil
}

func countLines(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}

func (r MutateRequest) screen(ctx context.Context, final []byte) (*MutateRejection, error) {
	if r.Screen == nil {
		return nil, nil
	}
	return r.Screen(ctx, final)
}

// applyWritePolicy is the cap + scrubber + verifier pass, factored out of
// WriteFile so Mutate runs the identical policy in the identical order. The
// only difference is where it runs: WriteFile checks before taking the lock,
// Mutate checks under it, which is what §8 requires and what makes the cap
// race safe.
func applyWritePolicy(ctx context.Context, content []byte, currentSize int, cfg WriteConfig) ([]byte, *MutateRejection, error) {
	if cfg.MaxBytes > 0 && len(content) > cfg.MaxBytes {
		detail := map[string]any{
			"bytes_attempted": len(content),
			"bytes_limit":     cfg.MaxBytes,
		}
		detail["current_size"] = currentSize
		return nil, &MutateRejection{
			Kind:    "cap",
			Message: fmt.Sprintf("content is %d bytes; the cap is %d", len(content), cfg.MaxBytes),
			Detail:  detail,
		}, nil
	}

	effective := content
	var hits []scrubber.Hit
	if cfg.Scrubber != nil {
		vres := cfg.Scrubber.ValidateWithAllowlist(string(content), cfg.ScrubberMode, cfg.AllowlistRegex)
		hits = vres.Hits
		switch cfg.ScrubberMode {
		case scrubber.ModeBlock:
			if vres.Decision == scrubber.DecisionReject {
				return nil, &MutateRejection{
					Kind:    "scrubber",
					Message: "scrubber policy rejected this write",
					Detail:  map[string]any{"hits": len(hits)},
					Hits:    hits,
				}, nil
			}
		case scrubber.ModeRedact:
			effective = []byte(vres.Cleaned)
		case scrubber.ModeWarn:
		}
	}

	if cfg.Verifier.Mode != VerifierOff {
		vres, vErr := VerifyWrite(ctx, effective, cfg.Verifier)
		if vErr != nil {
			return nil, nil, fmt.Errorf("verifier: %w", vErr)
		}
		if vres.Decision == VerifierReject {
			return nil, &MutateRejection{
				Kind:    "verifier",
				Message: "verifier policy rejected this write",
				Detail: map[string]any{
					"verifier_kind":     vres.Kind,
					"verifier_findings": vres.Findings,
				},
				VerifierFindings: vres.Findings,
			}, nil
		}
	}
	return effective, nil, nil
}

// requestHash is the idempotency identity: two calls with the same operation id
// are the same request iff this hash matches. It covers everything that changes
// what lands on disk and nothing that does not — the actor and the run are
// provenance, and a retry from a different attempt of the same work is still
// the same write.
func requestHash(r MutateRequest, normContent []byte) string {
	h := sha256.New()
	writeField := func(parts ...string) {
		for _, p := range parts {
			fmt.Fprintf(h, "%d:", len(p))
			h.Write([]byte(p))
		}
	}
	writeField("v1", string(r.Op), r.WorkspaceID, r.AuditPath, r.Scope, string(r.Tier))
	fmt.Fprintf(h, "rev=%d;", r.ExpectedRevision)
	writeField(strings.ToLower(r.ExpectedSHA256))
	h.Write([]byte("content:"))
	fmt.Fprintf(h, "%d:", len(normContent))
	h.Write(normContent)
	rem, _ := json.Marshal(canonicalRemovals(r.Removals))
	h.Write([]byte("removals:"))
	h.Write(rem)
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalRemovals normalises declaration ORDER and hash case so that two
// requests that declare the same spans differently are still one request.
func canonicalRemovals(in []memdiff.Removal) []memdiff.Removal {
	out := make([]memdiff.Removal, len(in))
	copy(out, in)
	for i := range out {
		out[i].OldSHA256 = strings.ToLower(out[i].OldSHA256)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].StartLine < out[j-1].StartLine; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func short(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

// utf8Valid is used by ReadCanonical to report whether the canonical file is in
// a form the contract can diff, without normalising it on the read path.
func utf8Valid(b []byte) bool { return utf8.Valid(b) }

func newMutationID() (string, error) {
	var rnd [10]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", fmt.Errorf("rand for mutation id: %w", err)
	}
	return "mm_" + hex.EncodeToString(rnd[:]), nil
}

func nowStamp() string { return tsformat.Format(time.Now()) }
