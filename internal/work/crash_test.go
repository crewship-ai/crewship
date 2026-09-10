package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// This file is the crash harness. Everything the repo had before was
// hand-crafted post-crash state: a test would INSERT the rows a crash "would
// have" left and assert recovery handled them, which proves the recovery code
// reads those rows and nothing about whether a crash produces them.
//
// Here a real child process really commits and is really killed by SIGKILL
// between the commit and any reply, and the parent inspects what survived.
//
// What this does NOT prove, and must not be described as proving: durability
// against an OS crash or a power cut. SIGKILL ends a process; the kernel still
// holds and eventually writes its page cache. That is T14, it needs a storage
// fault harness, and the difference is exactly why the contract lists them
// separately.

const crashEnvVar = "CREWSHIP_WORK_CRASH_CHILD"

// TestMain lets this test binary re-exec itself as the crashing child. The child
// takes the same code path as the parent — same store, same acceptance
// transaction — so the crash happens inside the real function, not a copy of it.
func TestMain(m *testing.M) {
	if spec := os.Getenv(crashEnvVar); spec != "" {
		runCrashChild(spec)
		return // unreachable: runCrashChild always exits
	}
	os.Exit(m.Run())
}

// runCrashChild accepts one delivery against the database at dbPath and then
// dies at the requested point. It never prints a receipt, because the whole
// scenario is "the sender never learned the id".
func runCrashChild(spec string) {
	parts := strings.SplitN(spec, "|", 3)
	if len(parts) != 3 {
		fmt.Fprintf(os.Stderr, "crash child: malformed spec %q\n", spec)
		os.Exit(2)
	}
	when, dbPath, sourceID := parts[0], parts[1], parts[2]

	db, err := database.Open("file:" + dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "crash child: open: %v\n", err)
		os.Exit(2)
	}
	store := NewStore(db.DB)
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "crash child: begin: %v\n", err)
		os.Exit(2)
	}
	if _, err := store.AcceptDeliveryTx(ctx, tx, crashDelivery(sourceID), crashWork()); err != nil {
		fmt.Fprintf(os.Stderr, "crash child: accept: %v\n", err)
		os.Exit(2)
	}

	if when == "before_commit" {
		// Die holding an uncommitted transaction. Nothing may survive.
		die()
	}

	if err := tx.Commit(); err != nil {
		fmt.Fprintf(os.Stderr, "crash child: commit: %v\n", err)
		os.Exit(2)
	}

	// Committed. The sender is still waiting for a response it will never get.
	die()
}

// die SIGKILLs this process. Not os.Exit: an orderly exit still unwinds defers,
// flushes buffers and closes the database handle, which is precisely the
// cooperation a crash does not offer. SIGKILL cannot be caught or deferred, so
// whatever survives is what the storage layer actually committed.
func die() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	// Unreachable in practice; if the signal were somehow blocked, exiting with
	// a distinctive code makes that visible instead of looking like success.
	os.Exit(97)
}

func crashDelivery(sourceID string) Delivery {
	return Delivery{
		WorkspaceID: "ws1", EndpointID: "ep1", EndpointKind: "routine",
		Profile: "standard-webhooks", SourceDeliveryID: sourceID,
		BodySHA256: "sha-of-the-body", BodyBytes: 12,
		RawBody: []byte(`{"n":1}`), FilterDecision: FilterAccepted,
	}
}

func crashWork() AcceptRequest {
	return AcceptRequest{
		WorkspaceID: "ws1", Source: SourceWebhook, Class: ClassBackground,
		AgentID: "agent-jamie",
	}
}

// runChild re-execs this test binary as the crashing child and returns once it
// is dead. A child that exits any other way is a broken harness, not a result.
func runChild(t *testing.T, when, dbPath, sourceID string) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%s|%s|%s", crashEnvVar, when, dbPath, sourceID))
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("crash child did not fail (err=%v), output:\n%s", err, out)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("cannot read the child's wait status on this platform; output:\n%s", out)
	}
	if !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("crash child ended with %v (exit code %d) rather than SIGKILL — it failed before reaching the crash point. Output:\n%s",
			status, exit.ExitCode(), out)
	}
}

// I1 and the resend contract, together: a process that dies after the commit and
// before its response leaves the work durably queued, and the provider's resend
// finds the SAME work rather than creating a second one.
//
// This is the case the audit found the routine path getting wrong in the other
// direction: it reserved an idempotency key, then created the run in a goroutine
// afterwards, so a crash in between left a reservation pointing at a run that
// never existed — and the resend was deduplicated onto that phantom.
func TestCrash_AfterCommitBeforeResponse_WorkSurvivesAndResendFindsIt(t *testing.T) {
	db := testutil.MigratedDB(t)
	path := db.Path()
	// The child writes through its own handle; close ours so the two are not
	// sharing a connection pool across a fork.
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	const sourceID = "dlv-crash-1"
	runChild(t, "after_commit", path, sourceID)

	// Reopen exactly as a restarting server would.
	reopened, err := database.Open("file:" + path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	store := NewStore(reopened.DB)
	ctx := context.Background()

	// The work is there, queued, with its delivery.
	var deliveries, items int
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM webhook_deliveries`).Scan(&deliveries); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM work_items`).Scan(&items); err != nil {
		t.Fatalf("count work: %v", err)
	}
	if deliveries != 1 || items != 1 {
		t.Fatalf("after a crash past the commit: %d deliveries, %d work items; want 1 and 1", deliveries, items)
	}

	original, err := store.LookupDelivery(ctx, "ws1", "ep1", sourceID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if original.WorkID == "" {
		t.Fatal("the surviving delivery has no work id; a receipt that names no work is not a receipt")
	}
	if original.State != StateQueued {
		t.Errorf("state = %q, want queued — a restart must be able to run work it already accepted", original.State)
	}

	// The provider, having heard nothing, sends it again.
	tx, err := reopened.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin resend: %v", err)
	}
	resend, err := store.AcceptDeliveryTx(ctx, tx, crashDelivery(sourceID), crashWork())
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("resend: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit resend: %v", err)
	}

	if !resend.Duplicate {
		t.Error("the resend was not reported as a duplicate")
	}
	if resend.WorkID != original.WorkID {
		t.Errorf("resend work id = %q, want the original %q — a lost response must not mint a second work item",
			resend.WorkID, original.WorkID)
	}
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM work_items`).Scan(&items); err != nil {
		t.Fatalf("count: %v", err)
	}
	if items != 1 {
		t.Errorf("work items after the resend = %d, want 1", items)
	}

	// And the recovered work must actually be claimable — "it survived" is not
	// the same as "it can run".
	claimed, err := store.Claim(ctx, ClaimOptions{LeaseOwner: "after-restart"})
	if err != nil {
		t.Fatalf("claim recovered work: %v", err)
	}
	if claimed.Item.ID != original.WorkID {
		t.Errorf("claimed %s, want the recovered work %s", claimed.Item.ID, original.WorkID)
	}
}

// The other side of I1: a crash BEFORE the commit must leave nothing at all. A
// half-written acceptance is worse than a refusal, because the sender will
// retry against a state we cannot explain.
func TestCrash_BeforeCommit_LeavesNothing(t *testing.T) {
	db := testutil.MigratedDB(t)
	path := db.Path()
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	const sourceID = "dlv-crash-2"
	runChild(t, "before_commit", path, sourceID)

	reopened, err := database.Open("file:" + path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	for _, table := range []string{"webhook_deliveries", "work_items", "work_events"} {
		var n int
		if err := reopened.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s has %d rows after a crash before the commit, want 0", table, n)
		}
	}

	// And the resend is a fresh acceptance, not a duplicate of a ghost.
	store := NewStore(reopened.DB)
	ctx := context.Background()
	tx, err := reopened.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	r, err := store.AcceptDeliveryTx(ctx, tx, crashDelivery(sourceID), crashWork())
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("accept after crash: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if r.Duplicate {
		t.Error("the retry was deduplicated against a delivery that was never committed")
	}
	if r.WorkID == "" {
		t.Error("the retry produced no work")
	}
}

// §5's delivery table, in one test: a duplicate returns the original receipt, the
// same id with a different body is a conflict, and an ignored delivery is
// recorded without producing work.
func TestAcceptDelivery_DuplicateConflictAndIgnored(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()

	accept := func(d Delivery, w AcceptRequest) (Receipt, error) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return Receipt{}, err
		}
		r, err := s.AcceptDeliveryTx(ctx, tx, d, w)
		if err != nil {
			_ = tx.Rollback()
			return Receipt{}, err
		}
		return r, tx.Commit()
	}

	first, err := accept(crashDelivery("dlv-1"), crashWork())
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.Duplicate || first.WorkID == "" {
		t.Fatalf("first = %+v, want a fresh acceptance with a work id", first)
	}

	again, err := accept(crashDelivery("dlv-1"), crashWork())
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if !again.Duplicate || again.WorkID != first.WorkID || again.DeliveryID != first.DeliveryID {
		t.Errorf("duplicate = %+v, want the original ids with Duplicate set", again)
	}

	changed := crashDelivery("dlv-1")
	changed.BodySHA256 = "a-different-body"
	if _, err := accept(changed, crashWork()); !errors.Is(err, ErrDeliveryConflict) {
		t.Errorf("same id, different body = %v, want ErrDeliveryConflict", err)
	}

	ignored := crashDelivery("dlv-ping")
	ignored.FilterDecision = FilterIgnored
	ignored.FilterReason = "ping"
	ignored.EventType = "ping"
	r, err := accept(ignored, crashWork())
	if err != nil {
		t.Fatalf("ignored: %v", err)
	}
	if r.WorkID != "" {
		t.Errorf("an ignored delivery produced work %q; a ping must not wake an agent", r.WorkID)
	}
	if r.DeliveryID == "" {
		t.Error("an ignored delivery was not recorded; the filter decision has to stay auditable")
	}
	var items int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_items`).Scan(&items); err != nil {
		t.Fatalf("count: %v", err)
	}
	if items != 1 {
		t.Errorf("work items = %d, want 1 (only the accepted delivery produced any)", items)
	}
}

// GitHub does not sign its delivery id, so the same verified bytes arriving under
// a fresh id are the same delivery. §5 accepts that this merges two genuinely
// distinct deliveries whose payloads are byte-identical; it is a deliberate
// trade, and this pins that it happens on purpose.
func TestAcceptDelivery_ContentKeySuppressesAReplayUnderAFreshID(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()

	accept := func(d Delivery) (Receipt, error) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return Receipt{}, err
		}
		r, err := s.AcceptDeliveryTx(ctx, tx, d, crashWork())
		if err != nil {
			_ = tx.Rollback()
			return Receipt{}, err
		}
		return r, tx.Commit()
	}

	gh := func(deliveryID string) Delivery {
		d := crashDelivery(deliveryID)
		d.Profile = "github"
		d.ContentKey = "sha256:identical-verified-bytes"
		return d
	}

	first, err := accept(gh("gh-delivery-1"))
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	replay, err := accept(gh("gh-delivery-2"))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Duplicate || replay.WorkID != first.WorkID {
		t.Errorf("replay under a fresh delivery id = %+v, want the original work %q",
			replay, first.WorkID)
	}

	// Different bytes are a different delivery, even on the same endpoint.
	other := gh("gh-delivery-3")
	other.ContentKey = "sha256:different-verified-bytes"
	other.BodySHA256 = "another-body"
	fresh, err := accept(other)
	if err != nil {
		t.Fatalf("distinct payload: %v", err)
	}
	if fresh.Duplicate || fresh.WorkID == first.WorkID {
		t.Errorf("a distinct payload was merged into %q", first.WorkID)
	}
}

// Concurrent duplicates: the provider retried while the first was still in
// flight. Exactly one work item, one stable receipt, no errors.
func TestAcceptDelivery_ConcurrentDuplicatesCollapseToOne(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()

	const n = 24
	type result struct {
		receipt Receipt
		err     error
	}
	results := make([]result, n)
	start := make(chan struct{})
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			<-start
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				results[i] = result{err: err}
				done <- i
				return
			}
			r, err := s.AcceptDeliveryTx(ctx, tx, crashDelivery("dlv-concurrent"), crashWork())
			if err != nil {
				_ = tx.Rollback()
				results[i] = result{err: err}
				done <- i
				return
			}
			results[i] = result{receipt: r, err: tx.Commit()}
			done <- i
		}(i)
	}
	close(start)
	for i := 0; i < n; i++ {
		<-done
	}

	seen := map[string]int{}
	var fresh, errs int
	for _, r := range results {
		if r.err != nil {
			errs++
			t.Logf("accept error: %v", r.err)
			continue
		}
		seen[r.receipt.WorkID]++
		if !r.receipt.Duplicate {
			fresh++
		}
	}
	if errs != 0 {
		t.Errorf("%d of %d concurrent duplicates errored", errs, n)
	}
	if len(seen) != 1 {
		t.Errorf("distinct receipts = %d, want 1", len(seen))
	}
	if fresh != 1 {
		t.Errorf("%d acceptances reported themselves as fresh, want exactly 1", fresh)
	}
	var items, deliveries int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_items`).Scan(&items); err != nil {
		t.Fatalf("count work: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM webhook_deliveries`).Scan(&deliveries); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if items != 1 || deliveries != 1 {
		t.Errorf("%d work items and %d deliveries, want 1 and 1", items, deliveries)
	}
}
