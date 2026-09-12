package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory/memdiff"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Every test here runs against a real migrated SQLite database and a real
// temporary filesystem. A fake would prove the code calls the functions it
// calls; the invariants under test (§8, and §3's I6 — "zápisy potvrzené memory
// API se neztratí při konkurenci ani po recovery") are properties of the
// database's constraints and the filesystem's rename, so they have to be
// exercised against both.

type mutateFixture struct {
	db        *sql.DB
	dir       string
	path      string
	auditPath string
	blobRoot  string
}

func newMutateFixture(t *testing.T) *mutateFixture {
	t.Helper()
	dir := t.TempDir()
	f := &mutateFixture{
		db:        testutil.MigratedSQLDB(t),
		dir:       dir,
		path:      filepath.Join(dir, "AGENT.md"),
		auditPath: "agent:alice/AGENT.md",
		blobRoot:  filepath.Join(dir, "versions"),
	}
	t.Cleanup(func() { _ = os.Remove(f.path + ".lock") })
	return f
}

// req builds a request with the fixture's identity filled in. Callers override
// the fields their case is about, which keeps each test's diff to the thing it
// is testing.
func (f *mutateFixture) req(opID string, op MutateOp, content string) MutateRequest {
	return MutateRequest{
		// Legacy by default in the fixture: most of these tests exercise what
		// the filesystem enforces, and the guaranteed profile has its own tests
		// that build their requests deliberately.
		Profile:     ProfileLegacy,
		OperationID: opID,
		WorkspaceID: "ws_test",
		ActorType:   "agent",
		ActorID:     "alice",
		RunID:       "run_1",
		Generation:  1,
		Source:      "test",
		Tier:        TierAgent,
		Scope:       "agent:alice",
		Path:        f.path,
		AuditPath:   f.auditPath,
		Op:          op,
		Content:     content,
		BlobRoot:    f.blobRoot,
	}
}

func (f *mutateFixture) mutate(t *testing.T, req MutateRequest) (MutateResult, error) {
	t.Helper()
	return Mutate(context.Background(), f.db, req)
}

func (f *mutateFixture) mustMutate(t *testing.T, req MutateRequest) MutateResult {
	t.Helper()
	res, err := f.mutate(t, req)
	if err != nil {
		t.Fatalf("Mutate(%s %q): %v", req.Op, req.OperationID, err)
	}
	if res.Rejection != nil {
		t.Fatalf("Mutate(%s %q) rejected by %s: %s", req.Op, req.OperationID,
			res.Rejection.Kind, res.Rejection.Message)
	}
	return res
}

func (f *mutateFixture) onDisk(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read canonical: %v", err)
	}
	return string(b)
}

func (f *mutateFixture) anchor(t *testing.T) (int64, string) {
	t.Helper()
	a, err := loadAnchor(context.Background(), f.db, "ws_test", f.auditPath)
	if err != nil {
		t.Fatalf("load anchor: %v", err)
	}
	if a == nil {
		return 0, ""
	}
	return a.revision, a.contentSHA
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// spanHash is the old_sha256 a client computes for a declared removal: SHA-256
// over the exact removed bytes including their LFs.
func spanHash(t *testing.T, base string, startLine, lineCount int) string {
	t.Helper()
	h, err := memdiff.HashSpan(memdiff.SplitLines([]byte(base)), startLine, lineCount)
	if err != nil {
		t.Fatalf("HashSpan(%d,%d): %v", startLine, lineCount, err)
	}
	return h
}

// ---------------------------------------------------------------------------
// 1. CAS: two replaces from the same revision. §10 asks for 100 races.
// ---------------------------------------------------------------------------

// TestMutate_ConcurrentReplace_ExactlyOneWins is §10's memory target: "100
// závodů dvou replace stejné revize: vždy jeden úspěch a jeden konflikt".
//
// The barrier is a closed channel, not a sleep: both goroutines are parked on
// the same receive and released by one close, so they enter Mutate as close to
// simultaneously as the scheduler allows. A sleep would make the test slower
// AND weaker — it would pass on a build where the lock serialised them by
// accident of timing.
func TestMutate_ConcurrentReplace_ExactlyOneWins(t *testing.T) {
	f := newMutateFixture(t)

	// Seed revision 1 so both racers have a real revision to CAS against.
	f.mustMutate(t, f.req("op_seed", OpReplace, "seed\n"))

	for round := 0; round < 100; round++ {
		rev, _ := f.anchor(t)

		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, 2)
		results := make([]MutateResult, 2)

		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				req := f.req(fmt.Sprintf("op_r%d_w%d", round, i), OpReplace,
					fmt.Sprintf("round %d writer %d\n", round, i))
				req.ExpectedRevision = rev
				<-start
				results[i], errs[i] = Mutate(context.Background(), f.db, req)
			}(i)
		}
		close(start)
		wg.Wait()

		wins, conflicts := 0, 0
		for i := 0; i < 2; i++ {
			switch {
			case errs[i] == nil && results[i].Rejection == nil:
				wins++
			case errors.Is(errs[i], ErrMemoryConflict):
				conflicts++
			default:
				t.Fatalf("round %d writer %d: unexpected outcome err=%v rejection=%+v",
					round, i, errs[i], results[i].Rejection)
			}
		}
		if wins != 1 || conflicts != 1 {
			t.Fatalf("round %d: wins=%d conflicts=%d, want exactly 1 and 1", round, wins, conflicts)
		}

		// The winner's bytes are what is on disk, and the anchor agrees with
		// them. A lost update would show up here as an anchor describing
		// content the file does not have.
		newRev, newSHA := f.anchor(t)
		if newRev != rev+1 {
			t.Fatalf("round %d: revision %d -> %d, want +1", round, rev, newRev)
		}
		if got := hashOf(f.onDisk(t)); got != newSHA {
			t.Fatalf("round %d: anchor sha %s does not describe the file (%s)", round, newSHA, got)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. Declared removals.
// ---------------------------------------------------------------------------

func TestMutate_Removals(t *testing.T) {
	const base = "alpha\nbravo\ncharlie\ndelta\n"

	t.Run("undeclared removal is rejected", func(t *testing.T) {
		f := newMutateFixture(t)
		f.mustMutate(t, f.req("op_base", OpReplace, base))
		rev, _ := f.anchor(t)

		// Drops "bravo" while declaring an empty removal set — which §8 makes
		// a positive assertion that nothing is removed.
		req := f.req("op_drop", OpReplace, "alpha\ncharlie\ndelta\n")
		req.ExpectedRevision = rev
		req.Removals = []memdiff.Removal{}

		_, err := f.mutate(t, req)
		if !errors.Is(err, ErrUndeclaredRemoval) {
			t.Fatalf("want undeclared_removal, got %v", err)
		}
		if got := f.onDisk(t); got != base {
			t.Fatalf("a rejected write must not touch the file; got %q", got)
		}
		if r, _ := f.anchor(t); r != rev {
			t.Fatalf("a rejected write must not move the revision; %d -> %d", rev, r)
		}
	})

	t.Run("correctly declared removal is accepted", func(t *testing.T) {
		f := newMutateFixture(t)
		f.mustMutate(t, f.req("op_base", OpReplace, base))
		rev, _ := f.anchor(t)

		req := f.req("op_drop", OpReplace, "alpha\ncharlie\ndelta\n")
		req.ExpectedRevision = rev
		req.Removals = []memdiff.Removal{{
			StartLine: 2, LineCount: 1, OldSHA256: spanHash(t, base, 2, 1),
		}}

		res := f.mustMutate(t, req)
		if !res.RemovalsVerified {
			t.Error("a declared removal must be recorded as verified")
		}
		if got := f.onDisk(t); got != "alpha\ncharlie\ndelta\n" {
			t.Fatalf("content = %q", got)
		}
	})

	t.Run("wrong span hash is rejected", func(t *testing.T) {
		f := newMutateFixture(t)
		f.mustMutate(t, f.req("op_base", OpReplace, base))
		rev, _ := f.anchor(t)

		// The client diffed against some other revision: it declares line 2
		// but hands the hash of line 3.
		req := f.req("op_drop", OpReplace, "alpha\ncharlie\ndelta\n")
		req.ExpectedRevision = rev
		req.Removals = []memdiff.Removal{{
			StartLine: 2, LineCount: 1, OldSHA256: spanHash(t, base, 3, 1),
		}}

		_, err := f.mutate(t, req)
		if !errors.Is(err, ErrUndeclaredRemoval) {
			t.Fatalf("want undeclared_removal for a mismatched span hash, got %v", err)
		}
		if !errors.Is(err, memdiff.ErrHashMismatch) {
			t.Errorf("the memdiff sentinel should survive the wrap so logs can tell the two apart; got %v", err)
		}
	})

	t.Run("nil removals is unverified, not refused", func(t *testing.T) {
		// The compatibility case: every memory.write the model has issued
		// since the tool shipped carries no removals. Those keep working, and
		// the ledger records that nothing was verified.
		f := newMutateFixture(t)
		f.mustMutate(t, f.req("op_base", OpReplace, base))
		rev, _ := f.anchor(t)

		req := f.req("op_drop", OpReplace, "alpha\n")
		req.ExpectedRevision = rev
		req.Removals = nil

		res := f.mustMutate(t, req)
		if res.RemovalsVerified {
			t.Error("an undeclared replace must not be recorded as verified")
		}
		var declared int
		if err := f.db.QueryRow(
			`SELECT removals_declared FROM memory_mutations WHERE id = ?`, res.MutationID,
		).Scan(&declared); err != nil {
			t.Fatal(err)
		}
		if declared != 0 {
			t.Errorf("removals_declared = %d, want 0 — an unverified replace must be auditable as unverified", declared)
		}
	})

	t.Run("protected record needs explicit authorization", func(t *testing.T) {
		f := newMutateFixture(t)
		const protectedBase = "alpha\n[pinned] never drop me\ncharlie\n"
		f.mustMutate(t, f.req("op_base", OpReplace, protectedBase))
		rev, _ := f.anchor(t)

		protect := func(line []byte) bool { return strings.HasPrefix(string(line), "[pinned]") }

		// Declaring the removal CORRECTLY still does not grant permission.
		req := f.req("op_drop", OpReplace, "alpha\ncharlie\n")
		req.ExpectedRevision = rev
		req.Protected = protect
		req.Removals = []memdiff.Removal{{
			StartLine: 2, LineCount: 1, OldSHA256: spanHash(t, protectedBase, 2, 1),
		}}

		if _, err := f.mutate(t, req); !errors.Is(err, ErrProtectedRemoval) {
			t.Fatalf("want protected_removal, got %v", err)
		}

		req.OperationID = "op_drop_authorized"
		req.AuthorizedForProtectedRemoval = true
		f.mustMutate(t, req)
		if got := f.onDisk(t); got != "alpha\ncharlie\n" {
			t.Fatalf("an authorized removal must go through; got %q", got)
		}
	})
}

// ---------------------------------------------------------------------------
// 3 + 4. Idempotency.
// ---------------------------------------------------------------------------

// TestMutate_IdenticalAppendRetried is §10's other memory target: "100 retry
// identického append: jeden přírůstek".
func TestMutate_IdenticalAppendRetried(t *testing.T) {
	f := newMutateFixture(t)
	f.mustMutate(t, f.req("op_seed", OpAppend, "seed\n"))

	first := f.mustMutate(t, f.req("op_retry", OpAppend, "one line\n"))
	if first.Idempotent {
		t.Fatal("the first call is not a replay")
	}

	for i := 0; i < 99; i++ {
		res := f.mustMutate(t, f.req("op_retry", OpAppend, "one line\n"))
		if !res.Idempotent {
			t.Fatalf("retry %d was executed again instead of replayed", i)
		}
		if res.Revision != first.Revision || res.ContentSHA256 != first.ContentSHA256 {
			t.Fatalf("retry %d returned a different result: rev %d/%s vs %d/%s",
				i, res.Revision, res.ContentSHA256, first.Revision, first.ContentSHA256)
		}
	}

	if got := f.onDisk(t); got != "seed\none line\n" {
		t.Fatalf("100 retries produced %q — an append ran more than once", got)
	}
	rev, sha := f.anchor(t)
	if rev != first.Revision {
		t.Fatalf("revision moved on replay: %d, want %d", rev, first.Revision)
	}
	if sha != hashOf("seed\none line\n") {
		t.Fatalf("anchor sha does not describe the file")
	}

	var rows int
	if err := f.db.QueryRow(
		`SELECT COUNT(*) FROM memory_mutations WHERE workspace_id = ? AND path = ? AND operation_id = ?`,
		"ws_test", f.auditPath, "op_retry").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("operation rows = %d, want 1", rows)
	}
}

// TestMutate_ConcurrentIdenticalAppend is the same invariant with the retries
// arriving at once rather than in sequence — a client that fired the retry
// before the first response came back.
func TestMutate_ConcurrentIdenticalAppend(t *testing.T) {
	f := newMutateFixture(t)
	f.mustMutate(t, f.req("op_seed", OpAppend, "seed\n"))

	const racers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = Mutate(context.Background(), f.db, f.req("op_retry", OpAppend, "one line\n"))
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer %d: %v", i, err)
		}
	}
	if got := f.onDisk(t); got != "seed\none line\n" {
		t.Fatalf("concurrent identical retries produced %q", got)
	}
}

func TestMutate_SameOperationDifferentContent_OperationConflict(t *testing.T) {
	f := newMutateFixture(t)
	f.mustMutate(t, f.req("op_x", OpAppend, "first\n"))

	_, err := f.mutate(t, f.req("op_x", OpAppend, "SOMETHING ELSE\n"))
	if !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("want operation_conflict, got %v", err)
	}
	if got := f.onDisk(t); got != "first\n" {
		t.Fatalf("a conflicting operation must not write; got %q", got)
	}
}

// TestMutate_OperationIDIsRequestScoped proves the request hash is not so loose
// that a different TARGET counts as the same request, nor so tight that
// provenance changes make a genuine retry look new.
func TestMutate_OperationIDIsRequestScoped(t *testing.T) {
	f := newMutateFixture(t)
	f.mustMutate(t, f.req("op_x", OpAppend, "line\n"))

	// Same write, retried from a different attempt of the same work: still
	// the same request, still one increment.
	retry := f.req("op_x", OpAppend, "line\n")
	retry.RunID = "run_2"
	retry.Generation = 7
	res, err := f.mutate(t, retry)
	if err != nil {
		t.Fatalf("a retry from a new attempt must replay, not conflict: %v", err)
	}
	if !res.Idempotent {
		t.Error("retry from a new attempt should have replayed")
	}

	// A different expected revision is a different request.
	other := f.req("op_x", OpAppend, "line\n")
	other.ExpectedRevision = 99
	if _, err := f.mutate(t, other); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("want operation_conflict for a changed expected_revision, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 5. Crash recovery.
// ---------------------------------------------------------------------------

var errSimulatedCrash = errors.New("simulated crash")

// crashAt returns a hook that aborts the mutation at the named step, standing
// in for the process dying there. It is the only way to observe the states
// between the intent and the confirmation: a real kill -9 would take the test
// process with it, and a subprocess harness would prove the same thing about
// far more code (T14 is that harness, and it is not this test).
func crashAt(step string) func(string) error {
	return func(got string) error {
		if got == step {
			return errSimulatedCrash
		}
		return nil
	}
}

func TestMutate_CrashRecovery(t *testing.T) {
	t.Run("crash after intent, before rename", func(t *testing.T) {
		f := newMutateFixture(t)
		f.mustMutate(t, f.req("op_seed", OpReplace, "seed\n"))
		rev, _ := f.anchor(t)

		req := f.req("op_crash", OpReplace, "recovered\n")
		req.ExpectedRevision = rev
		req.testHook = crashAt("after_intent")
		if _, err := f.mutate(t, req); !errors.Is(err, errSimulatedCrash) {
			t.Fatalf("want the simulated crash, got %v", err)
		}

		// The file is still the base; the intent is durable and unconfirmed.
		if got := f.onDisk(t); got != "seed\n" {
			t.Fatalf("file changed before the rename: %q", got)
		}
		assertPending(t, f, 1)

		recovered, conflicted, err := RecoverPending(context.Background(), f.db, f.blobRoot)
		if err != nil {
			t.Fatalf("RecoverPending: %v", err)
		}
		if recovered != 1 || conflicted != 0 {
			t.Fatalf("recovered=%d conflicted=%d, want 1 and 0", recovered, conflicted)
		}
		// §8: file at the BASE hash -> complete the intent. The write the
		// crashed process promised is finished from the parked blob.
		if got := f.onDisk(t); got != "recovered\n" {
			t.Fatalf("recovery did not complete the intent; file = %q", got)
		}
		newRev, sha := f.anchor(t)
		if newRev != rev+1 || sha != hashOf("recovered\n") {
			t.Fatalf("anchor after recovery = (%d, %s)", newRev, sha)
		}
		assertPending(t, f, 0)
	})

	t.Run("crash after the tempfile fsync, before the rename", func(t *testing.T) {
		// §8 and T11 name "po fsync" as its own crash point. It is NOT
		// separately hookable here, and that is a property of the design
		// rather than a gap in the test: the fsync and the rename both live
		// inside writeFileDurable, and from any observer's point of view the
		// two states are identical — the canonical file is still the base,
		// and the fsync'd tempfile is unreferenced garbage.
		//
		// What IS worth asserting is that the garbage cannot be mistaken for
		// the write. The tempfile carries a random suffix and is opened
		// O_EXCL, so a leftover from a crashed writer is never adopted;
		// recovery goes to the content-addressed blob instead. This plants
		// one and proves recovery ignores it.
		f := newMutateFixture(t)
		f.mustMutate(t, f.req("op_seed", OpReplace, "seed\n"))

		req := f.req("op_crash", OpReplace, "the real target\n")
		req.ExpectedRevision = 1
		req.testHook = crashAt("after_intent")
		if _, err := f.mutate(t, req); !errors.Is(err, errSimulatedCrash) {
			t.Fatalf("want the simulated crash, got %v", err)
		}

		leftover := f.path + ".tmp.0123456789abcdef"
		if err := os.WriteFile(leftover, []byte("a crashed writer's tempfile\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		recovered, conflicted, err := RecoverPending(context.Background(), f.db, f.blobRoot)
		if err != nil {
			t.Fatalf("RecoverPending: %v", err)
		}
		if recovered != 1 || conflicted != 0 {
			t.Fatalf("recovered=%d conflicted=%d, want 1 and 0", recovered, conflicted)
		}
		if got := f.onDisk(t); got != "the real target\n" {
			t.Fatalf("recovery adopted a stray tempfile: %q", got)
		}
	})

	t.Run("crash after rename, before confirm", func(t *testing.T) {
		f := newMutateFixture(t)
		f.mustMutate(t, f.req("op_seed", OpReplace, "seed\n"))
		rev, _ := f.anchor(t)

		req := f.req("op_crash", OpReplace, "renamed\n")
		req.ExpectedRevision = rev
		req.testHook = crashAt("after_rename")
		if _, err := f.mutate(t, req); !errors.Is(err, errSimulatedCrash) {
			t.Fatalf("want the simulated crash, got %v", err)
		}

		// The rename landed; only the confirmation is missing. The anchor
		// still describes the OLD content, which is exactly the state a plain
		// drift check would misread as tampering.
		if got := f.onDisk(t); got != "renamed\n" {
			t.Fatalf("file = %q, want the renamed content", got)
		}
		if r, sha := f.anchor(t); r != rev || sha != hashOf("seed\n") {
			t.Fatalf("anchor moved before the confirmation: (%d, %s)", r, sha)
		}

		recovered, conflicted, err := RecoverPending(context.Background(), f.db, f.blobRoot)
		if err != nil {
			t.Fatalf("RecoverPending: %v", err)
		}
		if recovered != 1 || conflicted != 0 {
			t.Fatalf("recovered=%d conflicted=%d, want 1 and 0", recovered, conflicted)
		}
		if got := f.onDisk(t); got != "renamed\n" {
			t.Fatalf("recovery rewrote a file that was already correct: %q", got)
		}
		if r, sha := f.anchor(t); r != rev+1 || sha != hashOf("renamed\n") {
			t.Fatalf("anchor after recovery = (%d, %s)", r, sha)
		}
	})

	t.Run("crash at the confirmation boundary", func(t *testing.T) {
		// before_confirm fires after the rename and before the second
		// transaction — the same durable state as after_rename, reached by a
		// different path. It is tested separately because the two hooks
		// bracket the one window where the file and the ledger legitimately
		// disagree.
		f := newMutateFixture(t)
		f.mustMutate(t, f.req("op_seed", OpReplace, "seed\n"))
		rev, _ := f.anchor(t)

		req := f.req("op_crash", OpReplace, "boundary\n")
		req.ExpectedRevision = rev
		req.testHook = crashAt("before_confirm")
		if _, err := f.mutate(t, req); !errors.Is(err, errSimulatedCrash) {
			t.Fatalf("want the simulated crash, got %v", err)
		}
		if _, _, err := RecoverPending(context.Background(), f.db, f.blobRoot); err != nil {
			t.Fatalf("RecoverPending: %v", err)
		}
		if r, sha := f.anchor(t); r != rev+1 || sha != hashOf("boundary\n") {
			t.Fatalf("anchor after recovery = (%d, %s)", r, sha)
		}
	})

	t.Run("crash after confirm leaves nothing to recover", func(t *testing.T) {
		f := newMutateFixture(t)
		f.mustMutate(t, f.req("op_seed", OpReplace, "seed\n"))
		res := f.mustMutate(t, func() MutateRequest {
			r := f.req("op_done", OpReplace, "done\n")
			r.ExpectedRevision = 1
			return r
		}())

		assertPending(t, f, 0)
		recovered, conflicted, err := RecoverPending(context.Background(), f.db, f.blobRoot)
		if err != nil {
			t.Fatalf("RecoverPending: %v", err)
		}
		if recovered != 0 || conflicted != 0 {
			t.Fatalf("recovered=%d conflicted=%d, want 0 and 0", recovered, conflicted)
		}
		if r, sha := f.anchor(t); r != res.Revision || sha != res.ContentSHA256 {
			t.Fatalf("recovery moved a confirmed key: (%d, %s)", r, sha)
		}
	})

	t.Run("a third party's content is never overwritten", func(t *testing.T) {
		f := newMutateFixture(t)
		f.mustMutate(t, f.req("op_seed", OpReplace, "seed\n"))
		rev, _ := f.anchor(t)

		req := f.req("op_crash", OpReplace, "would have been mine\n")
		req.ExpectedRevision = rev
		req.testHook = crashAt("after_intent")
		if _, err := f.mutate(t, req); !errors.Is(err, errSimulatedCrash) {
			t.Fatalf("want the simulated crash, got %v", err)
		}

		// Somebody else — the agent's own Write tool on the bind mount, the
		// E0 boundary — puts different bytes there while the intent is
		// pending. Neither the base hash nor the target hash matches.
		const theirs = "somebody else was here\n"
		if err := os.WriteFile(f.path, []byte(theirs), 0o644); err != nil {
			t.Fatal(err)
		}

		recovered, conflicted, err := RecoverPending(context.Background(), f.db, f.blobRoot)
		if err != nil {
			t.Fatalf("RecoverPending must report drift as a count, not an error: %v", err)
		}
		if recovered != 0 || conflicted != 1 {
			t.Fatalf("recovered=%d conflicted=%d, want 0 and 1", recovered, conflicted)
		}
		if got := f.onDisk(t); got != theirs {
			t.Fatalf("recovery overwrote a third party's content: %q", got)
		}
		// The anchor stays stale ON PURPOSE, so the next write of this key
		// also conflicts instead of the file quietly rejoining the contract
		// at whatever was left behind.
		if r, sha := f.anchor(t); r != rev || sha != hashOf("seed\n") {
			t.Fatalf("anchor moved on a drift outcome: (%d, %s)", r, sha)
		}
		next := f.req("op_next", OpReplace, "after the mess\n")
		next.ExpectedRevision = rev
		if _, err := f.mutate(t, next); !errors.Is(err, ErrMemoryConflict) {
			t.Fatalf("the next write must still conflict; got %v", err)
		}
		if got := f.onDisk(t); got != theirs {
			t.Fatalf("the next write overwrote the drifted file: %q", got)
		}
	})

	t.Run("a pending intent is recovered before the next write is served", func(t *testing.T) {
		// Recovery is not a boot-only sweep: §8 puts it before EVERY write of
		// a key, because the crashed writer may have been another process
		// that this one never sees restart.
		f := newMutateFixture(t)
		f.mustMutate(t, f.req("op_seed", OpReplace, "seed\n"))

		req := f.req("op_crash", OpReplace, "crashed write\n")
		req.ExpectedRevision = 1
		req.testHook = crashAt("after_rename")
		if _, err := f.mutate(t, req); !errors.Is(err, errSimulatedCrash) {
			t.Fatalf("want the simulated crash, got %v", err)
		}

		// No RecoverPending call. The next Mutate does it.
		next := f.req("op_next", OpAppend, "and then mine\n")
		res := f.mustMutate(t, next)
		if res.Recovered != 1 {
			t.Fatalf("Recovered = %d, want 1", res.Recovered)
		}
		if got := f.onDisk(t); got != "crashed write\nand then mine\n" {
			t.Fatalf("content = %q — the crashed write was lost (I6)", got)
		}
	})
}

func assertPending(t *testing.T, f *mutateFixture, want int) {
	t.Helper()
	var n int
	if err := f.db.QueryRow(
		`SELECT COUNT(*) FROM memory_mutations WHERE workspace_id = ? AND path = ? AND state IN ('intent','renamed')`,
		"ws_test", f.auditPath).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Fatalf("pending intents = %d, want %d", n, want)
	}
}

// TestMutate_PendingIntentBlocksASecondWriter proves the partial unique index
// is doing the blocking, not a mutex. A mutex dies with the process; this has
// to survive it, which is the whole reason §8 asks for it.
func TestMutate_PendingIntentBlocksASecondWriter(t *testing.T) {
	f := newMutateFixture(t)
	f.mustMutate(t, f.req("op_seed", OpReplace, "seed\n"))

	stuck := f.req("op_stuck", OpReplace, "stuck\n")
	stuck.ExpectedRevision = 1
	stuck.testHook = crashAt("after_intent")
	if _, err := f.mutate(t, stuck); !errors.Is(err, errSimulatedCrash) {
		t.Fatalf("want the simulated crash, got %v", err)
	}

	// Corrupt the parked blob so recovery cannot complete the intent, leaving
	// the pending row in place — the state the index exists to guard.
	var blobRef string
	if err := f.db.QueryRow(
		`SELECT target_blob_ref FROM memory_mutations WHERE operation_id = ?`, "op_stuck").Scan(&blobRef); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blobRef, []byte("not the target content"), 0o644); err != nil {
		t.Fatal(err)
	}

	next := f.req("op_next", OpReplace, "next\n")
	next.ExpectedRevision = 1
	_, err := f.mutate(t, next)
	if err == nil {
		t.Fatal("a second writer must not proceed past an unrecoverable pending intent")
	}
	if got := f.onDisk(t); got != "seed\n" {
		t.Fatalf("the second writer wrote anyway: %q", got)
	}
}

// ---------------------------------------------------------------------------
// 6. Manual drift.
// ---------------------------------------------------------------------------

func TestMutate_ManualDriftIsAConflict(t *testing.T) {
	f := newMutateFixture(t)
	f.mustMutate(t, f.req("op_seed", OpReplace, "seed\n"))
	rev, _ := f.anchor(t)

	// The E0 boundary: ~75% of real write events are the agent's own Write /
	// Edit tools on the bind-mounted tree (audit_watcher.go). Those bypass
	// this contract entirely, and the contract's job is to NOTICE, not to
	// pretend it merged them.
	const edited = "seed\nhand-edited by the agent's Write tool\n"
	if err := os.WriteFile(f.path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		req  func() MutateRequest
	}{
		{"replace", func() MutateRequest {
			r := f.req("op_replace", OpReplace, "mine\n")
			r.ExpectedRevision = rev
			return r
		}},
		{"append", func() MutateRequest {
			return f.req("op_append", OpAppend, "mine\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := f.mutate(t, tc.req())
			if !errors.Is(err, ErrMemoryConflict) {
				t.Fatalf("want memory_conflict, got %v", err)
			}
			if res.BaseSHA256 != hashOf(edited) {
				t.Errorf("the conflict should hand back the CURRENT hash so the client can re-read; got %s", res.BaseSHA256)
			}
			if got := f.onDisk(t); got != edited {
				t.Fatalf("drifted content was merged over: %q", got)
			}
		})
	}

	// And it is surfaced on the read too, rather than the read quietly
	// reporting a revision that describes different bytes.
	rr, err := ReadCanonical(context.Background(), f.db, ReadRequest{
		WorkspaceID: "ws_test", Path: f.path, AuditPath: f.auditPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rr.Drift {
		t.Error("ReadCanonical must report drift")
	}
	if string(rr.Content) != edited {
		t.Error("ReadCanonical must return the FILE's bytes, not the anchor's idea of them")
	}
}

// TestMutate_ExpectedSHA256IsTheLedgerlessCAS covers the mode the two
// agent-facing callers actually run in: no database, so no revision, but the
// on-disk hash still refuses a lost update.
func TestMutate_ExpectedSHA256IsTheLedgerlessCAS(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")
	t.Cleanup(func() { _ = os.Remove(path + ".lock") })

	base := MutateRequest{
		Profile:     ProfileLegacy,
		OperationID: "op_1",
		Path:        path,
		AuditPath:   "agent:alice/AGENT.md",
		Op:          OpReplace,
		Content:     "first\n",
	}
	res, err := Mutate(context.Background(), nil, base)
	if err != nil {
		t.Fatal(err)
	}
	if res.LedgerRecorded {
		t.Error("a nil db must report LedgerRecorded=false — a caller that reports success must not call this revision-checked")
	}
	if res.Revision != 0 {
		t.Errorf("Revision = %d without a ledger; want 0", res.Revision)
	}
	if res.ContentSHA256 != hashOf("first\n") {
		t.Errorf("ContentSHA256 = %s", res.ContentSHA256)
	}

	// Somebody else writes. The CAS refuses.
	if err := os.WriteFile(path, []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := base
	stale.OperationID = "op_2"
	stale.Content = "mine\n"
	stale.ExpectedSHA256 = res.ContentSHA256
	if _, err := Mutate(context.Background(), nil, stale); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("want memory_conflict, got %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "theirs\n" {
		t.Fatalf("the stale write landed anyway: %q", b)
	}

	// And an expected_revision without a ledger is refused rather than
	// silently ignored — the one thing worse than no CAS is a CAS that
	// reports success without checking.
	bogus := base
	bogus.OperationID = "op_3"
	bogus.ExpectedRevision = 1
	if _, err := Mutate(context.Background(), nil, bogus); !errors.Is(err, ErrLedgerRequired) {
		t.Fatalf("want memory_ledger_required, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 7. Normalisation: CRLF, trailing newlines, repeated lines, UTF-8.
// ---------------------------------------------------------------------------

func TestMutate_Normalization(t *testing.T) {
	t.Run("CRLF in the content becomes LF on disk", func(t *testing.T) {
		f := newMutateFixture(t)
		res := f.mustMutate(t, f.req("op_1", OpReplace, "alpha\r\nbravo\r\n"))
		if got := f.onDisk(t); got != "alpha\nbravo\n" {
			t.Fatalf("content = %q, want CRLF normalised to LF", got)
		}
		if res.ContentSHA256 != hashOf("alpha\nbravo\n") {
			t.Error("content_sha256 must hash the NORMALISED bytes that landed, not the request")
		}
	})

	t.Run("a lone CR is content, not a terminator", func(t *testing.T) {
		f := newMutateFixture(t)
		f.mustMutate(t, f.req("op_1", OpReplace, "alpha\rbravo\n"))
		if got := f.onDisk(t); got != "alpha\rbravo\n" {
			t.Fatalf("content = %q — a bare CR must survive", got)
		}
	})

	t.Run("the trailing newline is preserved either way", func(t *testing.T) {
		for _, tc := range []struct{ name, content string }{
			{"present", "alpha\nbravo\n"},
			{"absent", "alpha\nbravo"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newMutateFixture(t)
				f.mustMutate(t, f.req("op_1", OpReplace, tc.content))
				if got := f.onDisk(t); got != tc.content {
					t.Fatalf("content = %q, want %q — the trailing newline decides the hash of the last line's removal", got, tc.content)
				}
			})
		}
	})

	t.Run("invalid UTF-8 is rejected", func(t *testing.T) {
		f := newMutateFixture(t)
		_, err := f.mutate(t, f.req("op_1", OpReplace, "alpha\n\xff\xfe\n"))
		if !errors.Is(err, ErrNotCanonical) {
			t.Fatalf("want memory_not_canonical, got %v", err)
		}
		if _, statErr := os.Stat(f.path); !os.IsNotExist(statErr) {
			t.Error("a rejected write must not create the file")
		}
	})

	t.Run("multi-byte characters round-trip", func(t *testing.T) {
		f := newMutateFixture(t)
		const body = "čeština\n日本語\n🛳️ crewship\n"
		f.mustMutate(t, f.req("op_1", OpReplace, body))
		if got := f.onDisk(t); got != body {
			t.Fatalf("content = %q", got)
		}
		rr, err := ReadCanonical(context.Background(), f.db, ReadRequest{
			WorkspaceID: "ws_test", Path: f.path, AuditPath: f.auditPath,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !rr.Canonical {
			t.Error("valid UTF-8 with LF endings is canonical")
		}
		if rr.ContentSHA256 != hashOf(body) {
			t.Error("content_sha256 must hash the exact bytes")
		}
	})

	t.Run("repeated lines are resolved by position", func(t *testing.T) {
		// §8: "Opakované řádky určuje jejich pozice v původní revizi."
		//
		// Three identical lines, one goes. Which one is not a matter of
		// taste: memdiff's tie-break (delete before insert, adjacent
		// deletions merged) makes it the THIRD, and both sides have to agree
		// byte-for-byte or the removal hashes stop meaning anything. So
		// declaring the first is refused even though the resulting file is
		// identical.
		f := newMutateFixture(t)
		const base = "dup\ndup\ndup\nkeep\n"
		const target = "dup\ndup\nkeep\n"
		f.mustMutate(t, f.req("op_base", OpReplace, base))

		wrong := f.req("op_wrong", OpReplace, target)
		wrong.ExpectedRevision = 1
		wrong.Removals = []memdiff.Removal{{
			StartLine: 1, LineCount: 1, OldSHA256: spanHash(t, base, 1, 1),
		}}
		if _, err := f.mutate(t, wrong); !errors.Is(err, ErrUndeclaredRemoval) {
			t.Fatalf("declaring the wrong one of three identical lines must be refused; got %v", err)
		}

		right := f.req("op_right", OpReplace, target)
		right.ExpectedRevision = 1
		right.Removals = []memdiff.Removal{{
			StartLine: 3, LineCount: 1, OldSHA256: spanHash(t, base, 3, 1),
		}}
		f.mustMutate(t, right)
		if got := f.onDisk(t); got != target {
			t.Fatalf("content = %q", got)
		}
	})

	t.Run("a CRLF file on disk needs an explicit import", func(t *testing.T) {
		// §8: "Existující soubory projdou explicitním importem s novou revizí,
		// nikoli tichou normalizací při čtení."
		f := newMutateFixture(t)
		if err := os.WriteFile(f.path, []byte("alpha\r\nbravo\r\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		// A replace that has to diff against it is refused without Import.
		req := f.req("op_1", OpReplace, "alpha\n")
		req.Removals = []memdiff.Removal{}
		if _, err := f.mutate(t, req); !errors.Is(err, ErrNotCanonical) {
			t.Fatalf("want memory_not_canonical, got %v", err)
		}

		// An append leaves the file's own bytes alone, so it is not blocked —
		// and it does not silently rewrite them either.
		f.mustMutate(t, f.req("op_2", OpAppend, "charlie\n"))
		if got := f.onDisk(t); got != "alpha\r\nbravo\r\ncharlie\n" {
			t.Fatalf("an append silently normalised the existing bytes: %q", got)
		}
	})

	t.Run("appending onto a file with no trailing newline is reported", func(t *testing.T) {
		f := newMutateFixture(t)
		f.mustMutate(t, f.req("op_1", OpReplace, "alpha"))
		res := f.mustMutate(t, f.req("op_2", OpAppend, "bravo\n"))
		if !res.MergedFinalLine {
			t.Error("MergedFinalLine must be set — silence here is how an agent ends up believing it added a bullet when it extended one")
		}
		if got := f.onDisk(t); got != "alphabravo\n" {
			t.Fatalf("content = %q", got)
		}
	})
}

// ---------------------------------------------------------------------------
// 9. Read.
// ---------------------------------------------------------------------------

func TestReadCanonical_RevisionAndProvenance(t *testing.T) {
	f := newMutateFixture(t)

	missing, err := ReadCanonical(context.Background(), f.db, ReadRequest{
		WorkspaceID: "ws_test", Path: f.path, AuditPath: f.auditPath,
	})
	if err != nil {
		t.Fatalf("a missing file is not an error: %v", err)
	}
	if missing.Exists || missing.Revision != 0 {
		t.Errorf("missing file: exists=%v revision=%d", missing.Exists, missing.Revision)
	}
	if missing.ContentSHA256 != hashOf("") {
		t.Error("a missing file hashes as no bytes, so a client can CAS against 'not there yet'")
	}

	written := f.mustMutate(t, f.req("op_1", OpReplace, "hello\n"))

	got, err := ReadCanonical(context.Background(), f.db, ReadRequest{
		WorkspaceID: "ws_test", Path: f.path, AuditPath: f.auditPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Content) != "hello\n" {
		t.Fatalf("content = %q", got.Content)
	}
	if got.Revision != written.Revision || got.ContentSHA256 != written.ContentSHA256 {
		t.Fatalf("read (%d, %s) does not match the write (%d, %s)",
			got.Revision, got.ContentSHA256, written.Revision, written.ContentSHA256)
	}
	if got.Drift {
		t.Error("a file nobody touched must not report drift")
	}
	if got.Scope != "agent:alice" || got.Tier != TierAgent {
		t.Errorf("scope/tier = %q/%q", got.Scope, got.Tier)
	}
	p := got.Provenance
	if p.ActorType != "agent" || p.ActorID != "alice" || p.RunID != "run_1" || p.Generation != 1 {
		t.Errorf("provenance = %+v — I4's actor and run must survive to the read", p)
	}
	if p.OperationID != "op_1" || p.Op != string(OpReplace) || p.RecordedAt == "" || p.ConfirmedAt == "" {
		t.Errorf("provenance = %+v", p)
	}
	if p.ValidFrom != "" || p.ValidTo != "" {
		t.Error("valid_from/valid_to must stay empty — §8 forbids inventing validity")
	}
}

// TestReadCanonical_ReadsThroughAStaleIndex is §8's "read-after-write čte
// potvrzený kanonický obsah i při zpoždění indexu".
//
// The index is deliberately NOT refreshed after the write, and the assertion is
// two-sided: the index still answers with the old content (so the staleness is
// real, not a test that forgot to make it stale), and the read returns the new.
func TestReadCanonical_ReadsThroughAStaleIndex(t *testing.T) {
	f := newMutateFixture(t)
	memRoot := filepath.Join(f.dir, "memroot")
	if err := os.MkdirAll(memRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(memRoot, "AGENT.md")
	t.Cleanup(func() { _ = os.Remove(path + ".lock") })

	const before = "the index knows about pelicans\n"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := New(memRoot, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if err := eng.ReindexContext(context.Background()); err != nil {
		t.Fatal(err)
	}

	req := f.req("op_1", OpReplace, "the index has never heard of albatrosses\n")
	req.Path = path
	req.Import = true
	res := f.mustMutate(t, req)

	// The index is now stale by construction: nothing reindexed.
	hits, err := eng.Search(context.Background(), "pelicans", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("the index should still be serving the OLD content — without that this test proves nothing")
	}

	got, err := ReadCanonical(context.Background(), f.db, ReadRequest{
		WorkspaceID: "ws_test", Path: path, AuditPath: f.auditPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Content) != "the index has never heard of albatrosses\n" {
		t.Fatalf("read-after-write returned %q — the read went through the index", got.Content)
	}
	if got.Revision != res.Revision {
		t.Fatalf("revision %d, want %d", got.Revision, res.Revision)
	}
}

// ---------------------------------------------------------------------------
// Policy: the cap and the scrubber still apply, now under the lock.
// ---------------------------------------------------------------------------

func TestMutate_CapIsCheckedUnderTheLock(t *testing.T) {
	f := newMutateFixture(t)
	req := f.req("op_1", OpAppend, strings.Repeat("x", 100))
	req.Cfg = WriteConfig{MaxBytes: 50}

	res, err := f.mutate(t, req)
	if err != nil {
		t.Fatalf("a cap refusal is an outcome, not an error: %v", err)
	}
	if res.Rejection == nil || res.Rejection.Kind != "cap" {
		t.Fatalf("rejection = %+v, want kind=cap", res.Rejection)
	}
	if res.Rejection.Detail["bytes_attempted"] != 100 || res.Rejection.Detail["bytes_limit"] != 50 {
		t.Errorf("detail = %+v", res.Rejection.Detail)
	}
	if _, statErr := os.Stat(f.path); !os.IsNotExist(statErr) {
		t.Error("a capped write must not create the file")
	}
	// A refusal consumes no revision and leaves no pending intent behind.
	if r, _ := f.anchor(t); r != 0 {
		t.Errorf("revision = %d after a refusal, want 0", r)
	}
	assertPending(t, f, 0)
}

// TestMutate_DiffBudgetIsBoundedBeforeMemdiff pins the guard memdiff's own doc
// says the caller owns: "Nothing here caps that. […] The caller wiring this
// into the write path is responsible for bounding the input."
func TestMutate_DiffBudgetIsBoundedBeforeMemdiff(t *testing.T) {
	f := newMutateFixture(t)
	big := strings.Repeat("a line of text\n", 200)
	f.mustMutate(t, f.req("op_base", OpReplace, big))

	req := f.req("op_diff", OpReplace, strings.Repeat("a different line\n", 200))
	req.ExpectedRevision = 1
	req.Removals = []memdiff.Removal{}
	req.MaxDiffLines = 50

	if _, err := f.mutate(t, req); !errors.Is(err, ErrContentTooLarge) {
		t.Fatalf("want memory_content_too_large, got %v", err)
	}
	if got := f.onDisk(t); got != big {
		t.Error("an over-budget replace must not write")
	}
}

// The guaranteed profile is what the parallel release profile requires, and its
// whole job is to REFUSE rather than downgrade. A write that cannot be checked
// must fail; a write that succeeded unchecked and is then described as
// revision-checked is worse than one that did not happen.
func TestMutate_GuaranteedProfileRefusesWhatItCannotCheck(t *testing.T) {
	f := newMutateFixture(t)

	// Establish revision 1 so the guaranteed replace below has a real base to
	// CAS against; a replace expecting a revision the file has never reached is
	// a conflict, not a profile failure, and would test the wrong thing.
	seed := f.req("op_seed", OpAppend, "one\n")
	if _, err := Mutate(context.Background(), f.db, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	full := func() MutateRequest {
		r := f.req("op_guaranteed", OpReplace, "one\ntwo\n")
		r.Profile = ProfileGuaranteed
		r.ExpectedRevision = 1
		r.Removals = []memdiff.Removal{}
		r.Authorize = func(context.Context) error { return nil }
		return r
	}

	tests := []struct {
		name    string
		mutate  func(*MutateRequest)
		ledger  bool
		wantErr error
	}{
		{
			name:    "no ledger",
			mutate:  func(*MutateRequest) {},
			ledger:  false,
			wantErr: ErrProfileUnmet,
		},
		{
			name:    "no operation id",
			mutate:  func(r *MutateRequest) { r.OperationID = "" },
			ledger:  true,
			wantErr: ErrProfileUnmet,
		},
		{
			name:    "no Authorize, so the run and generation are never verified",
			mutate:  func(r *MutateRequest) { r.Authorize = nil },
			ledger:  true,
			wantErr: ErrProfileUnmet,
		},
		{
			name:    "replace without ExpectedRevision",
			mutate:  func(r *MutateRequest) { r.ExpectedRevision = 0 },
			ledger:  true,
			wantErr: ErrProfileUnmet,
		},
		{
			name:    "replace with nil Removals, which means undeclared rather than empty",
			mutate:  func(r *MutateRequest) { r.Removals = nil },
			ledger:  true,
			wantErr: ErrProfileUnmet,
		},
		{
			name:    "no profile declared at all",
			mutate:  func(r *MutateRequest) { r.Profile = "" },
			ledger:  true,
			wantErr: ErrProfileUnmet,
		},
		{
			name:    "unknown profile",
			mutate:  func(r *MutateRequest) { r.Profile = "best-effort" },
			ledger:  true,
			wantErr: ErrProfileUnmet,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := full()
			tc.mutate(&req)
			var db *sql.DB
			if tc.ledger {
				db = f.db
			}
			_, err := Mutate(context.Background(), db, req)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			// And it must be a refusal, not a downgrade: nothing may have been
			// written, and the caller must not be able to mistake the result
			// for a successful legacy write.
			if err != nil && errors.Is(err, ErrProfileUnmet) && !strings.Contains(err.Error(), "guaranteed") &&
				!strings.Contains(err.Error(), "profile") {
				t.Errorf("the refusal does not say what was missing: %v", err)
			}
		})
	}

	// The complete request is accepted, so the gate refuses for the stated
	// reasons rather than refusing everything.
	if _, err := Mutate(context.Background(), f.db, full()); err != nil {
		t.Fatalf("a complete guaranteed request was refused: %v", err)
	}
}

// Legacy must stay reachable and must stay honest about what it is.
func TestMutate_LegacyProfileIsExplicitAndSaysWhatItIsNot(t *testing.T) {
	f := newMutateFixture(t)

	res, err := Mutate(context.Background(), nil, f.req("op_legacy", OpAppend, "line\n"))
	if err != nil {
		t.Fatalf("legacy append: %v", err)
	}
	if res.LedgerRecorded {
		t.Error("a ledgerless write reported LedgerRecorded; a caller would describe it as revision-checked")
	}
}
