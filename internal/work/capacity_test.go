package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/diskusage"
)

// checkIngress runs the admission check in its own transaction, the way the
// acceptance path runs it inside the transaction that would do the insert.
func checkIngress(t *testing.T, s *Store, db *sql.DB, lim IngressLimits, req IngressRequest) error {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	return s.CheckIngressTx(ctx, tx, lim, req)
}

// fill accepts n deliveries on one endpoint and leaves their work queued, i.e.
// non-terminal and therefore counting against every ingress limit.
func fill(t *testing.T, s *Store, db *sql.DB, endpoint string, n int, body []byte) []Receipt {
	t.Helper()
	out := make([]Receipt, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, acceptDelivery(t, s, db,
			retDelivery(fmt.Sprintf("%s-%04d", endpoint, i), endpoint, body), retWork()))
	}
	return out
}

// §6: over the limit the answer is 429 with Retry-After: 5 and nothing new is
// taken. Each limit refuses AT the boundary and admits one below it — an
// off-by-one here is a cap that is silently 1 short or 1 over for the life of
// the deployment.
func TestCheckIngress_EachLimitRefusesAtItsBoundary(t *testing.T) {
	t.Run("endpoint", func(t *testing.T) {
		s, db, _ := newTestStore(t)
		lim := IngressLimits{EndpointNonTerminal: 3, WorkspaceNonTerminal: 1000, WorkspaceRawBytes: 1 << 20}

		fill(t, s, db, "ep-a", 2, []byte(`{"x":1}`))
		if err := checkIngress(t, s, db, lim, IngressRequest{
			WorkspaceID: "ws1", EndpointID: "ep-a", SourceDeliveryID: "next", BodyBytes: 7,
		}); err != nil {
			t.Fatalf("at 2 of 3 = %v, want admitted", err)
		}

		fill(t, s, db, "ep-a", 3, []byte(`{"x":1}`)) // now 3 (the first two are re-accepted as duplicates)
		err := checkIngress(t, s, db, lim, IngressRequest{
			WorkspaceID: "ws1", EndpointID: "ep-a", SourceDeliveryID: "next", BodyBytes: 7,
		})
		if !errors.Is(err, ErrEndpointFull) {
			t.Errorf("at 3 of 3 = %v, want ErrEndpointFull", err)
		}
		if !IsIngressFull(err) {
			t.Error("IsIngressFull is false for ErrEndpointFull; the handler would not answer 429")
		}

		// Another endpoint in the same workspace is unaffected: the endpoint cap
		// is per endpoint, and one noisy sender must not lock out the rest.
		if err := checkIngress(t, s, db, lim, IngressRequest{
			WorkspaceID: "ws1", EndpointID: "ep-b", SourceDeliveryID: "other", BodyBytes: 7,
		}); err != nil {
			t.Errorf("a different endpoint = %v, want admitted", err)
		}
	})

	t.Run("workspace count", func(t *testing.T) {
		s, db, _ := newTestStore(t)
		lim := IngressLimits{EndpointNonTerminal: 1000, WorkspaceNonTerminal: 4, WorkspaceRawBytes: 1 << 20}

		fill(t, s, db, "ep-a", 2, []byte(`{"x":1}`))
		fill(t, s, db, "ep-b", 1, []byte(`{"x":1}`))
		if err := checkIngress(t, s, db, lim, IngressRequest{
			WorkspaceID: "ws1", EndpointID: "ep-c", SourceDeliveryID: "next", BodyBytes: 7,
		}); err != nil {
			t.Fatalf("at 3 of 4 = %v, want admitted", err)
		}

		fill(t, s, db, "ep-b", 2, []byte(`{"x":1}`))
		err := checkIngress(t, s, db, lim, IngressRequest{
			WorkspaceID: "ws1", EndpointID: "ep-c", SourceDeliveryID: "next", BodyBytes: 7,
		})
		if !errors.Is(err, ErrWorkspaceFull) {
			t.Errorf("at 4 of 4 = %v, want ErrWorkspaceFull", err)
		}

		// Terminal work stops counting: finishing one makes room, because the
		// limit bounds what we are holding, not what we have ever held.
		var oneID string
		if err := db.QueryRow(
			`SELECT id FROM work_items WHERE state = 'queued' LIMIT 1`).Scan(&oneID); err != nil {
			t.Fatalf("pick one: %v", err)
		}
		finish(t, s, oneID, StateSucceeded)
		if err := checkIngress(t, s, db, lim, IngressRequest{
			WorkspaceID: "ws1", EndpointID: "ep-c", SourceDeliveryID: "next", BodyBytes: 7,
		}); err != nil {
			t.Errorf("after one finished = %v, want admitted", err)
		}
	})

	t.Run("workspace bytes", func(t *testing.T) {
		s, db, _ := newTestStore(t)
		body := []byte(`0123456789`) // 10 bytes
		lim := IngressLimits{EndpointNonTerminal: 1000, WorkspaceNonTerminal: 1000, WorkspaceRawBytes: 30}

		fill(t, s, db, "ep-a", 2, body) // 20 bytes held
		if err := checkIngress(t, s, db, lim, IngressRequest{
			WorkspaceID: "ws1", EndpointID: "ep-a", SourceDeliveryID: "next", BodyBytes: 10,
		}); err != nil {
			t.Fatalf("20 held + 10 arriving = %v, want admitted at exactly the limit", err)
		}
		if err := checkIngress(t, s, db, lim, IngressRequest{
			WorkspaceID: "ws1", EndpointID: "ep-a", SourceDeliveryID: "next", BodyBytes: 11,
		}); !errors.Is(err, ErrWorkspaceBytesFull) {
			t.Errorf("20 held + 11 arriving = %v, want ErrWorkspaceBytesFull", err)
		}

		fill(t, s, db, "ep-a", 3, body) // 30 bytes held, exactly full
		if err := checkIngress(t, s, db, lim, IngressRequest{
			WorkspaceID: "ws1", EndpointID: "ep-a", SourceDeliveryID: "next", BodyBytes: 1,
		}); !errors.Is(err, ErrWorkspaceBytesFull) {
			t.Errorf("30 held + 1 arriving = %v, want ErrWorkspaceBytesFull", err)
		}
	})
}

// The one that is easy to get backwards, and worse than having no limit at all
// when it is.
//
// A sender that did not hear our 202 retries the SAME delivery. Refusing that
// retry because we are full tells the sender to give up on work we are already
// holding: it stops resending, the work runs, and nobody upstream ever learns
// it did. §6 says a duplicate of already-accepted work is NOT refused for
// fullness, so this admits with EVERY limit set to zero headroom at once.
func TestCheckIngress_DuplicateIsAdmittedWhenEverythingIsFull(t *testing.T) {
	s, db, _ := newTestStore(t)
	body := []byte(`{"delivery":"already-here"}`)

	original := acceptDelivery(t, s, db, retDelivery("dlv-known", "ep-a", body), retWork())

	// Fill past every limit simultaneously.
	fill(t, s, db, "ep-a", 5, body)
	full := IngressLimits{EndpointNonTerminal: 1, WorkspaceNonTerminal: 1, WorkspaceRawBytes: 1}

	// The control: a NEW delivery on the same endpoint is refused, so the limits
	// really are exhausted and the duplicate case below is not passing because
	// nothing is full.
	if err := checkIngress(t, s, db, full, IngressRequest{
		WorkspaceID: "ws1", EndpointID: "ep-a", SourceDeliveryID: "brand-new", BodyBytes: int64(len(body)),
	}); !IsIngressFull(err) {
		t.Fatalf("a new arrival at a full endpoint = %v, want a capacity refusal", err)
	}

	// The duplicate, by source delivery id.
	if err := checkIngress(t, s, db, full, IngressRequest{
		WorkspaceID:      "ws1",
		EndpointID:       "ep-a",
		SourceDeliveryID: "dlv-known",
		BodyBytes:        int64(len(body)),
	}); err != nil {
		t.Errorf("re-delivery of accepted work = %v, want admitted however full we are", err)
	}

	// And by content key, which is how a GitHub replay is recognised: the same
	// verified bytes arriving under a fresh delivery id.
	keyed := retDelivery("dlv-github-1", "ep-b", body)
	keyed.Profile = "github"
	keyed.ContentKey = "sha256:identical-verified-bytes"
	acceptDelivery(t, s, db, keyed, retWork())

	if err := checkIngress(t, s, db, full, IngressRequest{
		WorkspaceID:      "ws1",
		EndpointID:       "ep-b",
		SourceDeliveryID: "dlv-github-2",
		ContentKey:       "sha256:identical-verified-bytes",
		BodyBytes:        int64(len(body)),
	}); err != nil {
		t.Errorf("content-key replay = %v, want admitted however full we are", err)
	}

	// The duplicate check must not have created anything: the receipt the
	// acceptance path returns is still the original one.
	again := acceptDelivery(t, s, db, retDelivery("dlv-known", "ep-a", body), retWork())
	if !again.Duplicate || again.WorkID != original.WorkID {
		t.Errorf("re-acceptance = %+v, want the original work %q", again, original.WorkID)
	}

	// Accepted work is never evicted under pressure: everything we admitted is
	// still here.
	if !deliveryExists(t, db, original.DeliveryID) {
		t.Error("an accepted delivery was evicted to make room")
	}
}

// A refusal must be distinguishable per limit, because "we are full" without a
// reason is a support ticket rather than a fix — and it must carry §6's
// Retry-After.
func TestIngressErrors_AreDistinguishableAndCarryRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"endpoint", ErrEndpointFull},
		{"workspace", ErrWorkspaceFull},
		{"bytes", ErrWorkspaceBytesFull},
		{"disk", ErrDiskPressure},
	} {
		if !IsIngressFull(tc.err) {
			t.Errorf("IsIngressFull(%s) = false, want true", tc.name)
		}
		for _, other := range []error{ErrEndpointFull, ErrWorkspaceFull, ErrWorkspaceBytesFull, ErrDiskPressure} {
			if other != tc.err && errors.Is(tc.err, other) {
				t.Errorf("%s matches %v; a handler could not name the limit", tc.name, other)
			}
		}
	}
	if !IsIngressFull(fmt.Errorf("wrapped: %w", ErrWorkspaceFull)) {
		t.Error("IsIngressFull does not see through a wrap")
	}
	if IsIngressFull(ErrDuplicateDelivery) {
		t.Error("a duplicate is reported as a capacity refusal; that is the 429 that must never be sent")
	}
	if IngressRetryAfter.Seconds() != 5 {
		t.Errorf("IngressRetryAfter = %s, want §6's 5s", IngressRetryAfter)
	}
}

// §6: when disk is short, refuse NEW acceptance — and never delete a
// non-terminal payload to make room. The probe is injected because a test that
// really fills a disk is a test nobody runs twice.
func TestDiskGuard_RefusesNewAcceptanceUnderPressure(t *testing.T) {
	stub := func(free uint64) func(string) (diskusage.Stats, error) {
		return func(p string) (diskusage.Stats, error) {
			return diskusage.Stats{Path: p, TotalBytes: 100 << 30, FreeBytes: free}, nil
		}
	}

	g := &DiskGuard{Path: "/data", MinFree: 1 << 30, probe: stub(2 << 30)}
	if err := g.Check(); err != nil {
		t.Errorf("2 GiB free against a 1 GiB floor = %v, want admitted", err)
	}

	g.probe = stub(1<<30 - 1)
	if err := g.Check(); !errors.Is(err, ErrDiskPressure) {
		t.Errorf("just under the floor = %v, want ErrDiskPressure", err)
	}
	if err := g.Check(); !IsIngressFull(err) {
		t.Error("disk pressure is not an ingress refusal; the sender would get the wrong status")
	}

	// A probe that fails is not pressure. Refusing every arrival because statfs
	// is unavailable would take ingress down for a reason unrelated to disk.
	g.probe = func(string) (diskusage.Stats, error) { return diskusage.Stats{}, errors.New("statfs unsupported") }
	if err := g.Check(); err != nil {
		t.Errorf("an unavailable probe = %v, want admitted", err)
	}

	// An unconfigured guard admits, rather than failing closed on a deployment
	// that never set a path.
	if err := (&DiskGuard{}).Check(); err != nil {
		t.Errorf("unconfigured guard = %v, want admitted", err)
	}
	var nilGuard *DiskGuard
	if err := nilGuard.Check(); err != nil {
		t.Errorf("nil guard = %v, want admitted", err)
	}
}

// The capacity report: what a workspace holds against each limit, and what its
// finished work still costs on disk. §6 excludes terminal payloads from the
// ingress byte limit but requires them counted in the report — the two numbers
// have to be reported separately, and this pins that they are.
func TestWorkspaceUsage_SeparatesNonTerminalFromTerminalPayloadBytes(t *testing.T) {
	s, db, clock := newTestStore(t)
	ctx := context.Background()
	body := []byte(`0123456789`) // 10 bytes

	live := fill(t, s, db, "ep-a", 3, body)
	done := fill(t, s, db, "ep-b", 2, body)
	for _, r := range done {
		finish(t, s, r.WorkID, StateSucceeded)
	}
	_ = live

	lim := DefaultIngressLimits()
	u, err := s.WorkspaceUsage(ctx, "ws1", lim)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if u.NonTerminalItems != 3 {
		t.Errorf("NonTerminalItems = %d, want 3", u.NonTerminalItems)
	}
	if u.NonTerminalBytes != 30 {
		t.Errorf("NonTerminalBytes = %d, want 30", u.NonTerminalBytes)
	}
	if u.TerminalPayloadBytes != 20 {
		t.Errorf("TerminalPayloadBytes = %d, want 20 — finished work still costs disk", u.TerminalPayloadBytes)
	}
	if u.ExpiredPayloads != 0 {
		t.Errorf("ExpiredPayloads = %d, want 0 before any sweep", u.ExpiredPayloads)
	}
	items, bytes := u.Remaining()
	if items != lim.WorkspaceNonTerminal-3 || bytes != lim.WorkspaceRawBytes-30 {
		t.Errorf("Remaining() = (%d, %d), want (%d, %d)",
			items, bytes, lim.WorkspaceNonTerminal-3, lim.WorkspaceRawBytes-30)
	}

	// After retention takes the finished payloads, the disk cost goes to zero
	// and the report says how many replays are now unavailable.
	clock.Advance(DefaultRawBodyRetention + time.Hour)
	if res := sweep(t, s, DefaultRetentionPolicy()); res.RawBodiesExpired != 2 {
		t.Fatalf("sweep expired %d payloads, want 2", res.RawBodiesExpired)
	}
	u, err = s.WorkspaceUsage(ctx, "ws1", lim)
	if err != nil {
		t.Fatalf("usage after sweep: %v", err)
	}
	if u.TerminalPayloadBytes != 0 {
		t.Errorf("TerminalPayloadBytes after sweep = %d, want 0", u.TerminalPayloadBytes)
	}
	if u.ExpiredPayloads != 2 {
		t.Errorf("ExpiredPayloads = %d, want 2", u.ExpiredPayloads)
	}
	if u.NonTerminalBytes != 30 {
		t.Errorf("NonTerminalBytes after sweep = %d, want 30 — the sweep must not touch live payloads",
			u.NonTerminalBytes)
	}
}

// The report covers every workspace holding anything, so an operator can ask
// where the disk is going without knowing which workspace to ask about.
func TestUsageReport_CoversEveryWorkspace(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()

	fill(t, s, db, "ep-a", 2, []byte(`0123456789`))

	other := retDelivery("dlv-other", "ep-z", []byte(`01234`))
	other.WorkspaceID = "ws2"
	w := retWork()
	w.WorkspaceID = "ws2"
	acceptDelivery(t, s, db, other, w)

	report, err := s.UsageReport(ctx, DefaultIngressLimits())
	if err != nil {
		t.Fatalf("usage report: %v", err)
	}
	if len(report) != 2 {
		t.Fatalf("report covers %d workspaces, want 2: %+v", len(report), report)
	}
	byID := map[string]WorkspaceUsage{}
	for _, u := range report {
		byID[u.WorkspaceID] = u
	}
	if got := byID["ws1"].NonTerminalBytes; got != 20 {
		t.Errorf("ws1 NonTerminalBytes = %d, want 20", got)
	}
	if got := byID["ws2"].NonTerminalBytes; got != 5 {
		t.Errorf("ws2 NonTerminalBytes = %d, want 5", got)
	}
}

// §6 does the disk arithmetic for retained terminal payloads as
// 36 000 × 64 KiB ≈ 2.2 GiB before indexes and WAL. That number belongs in the
// code where it can be asked for, not in a spreadsheet nobody kept.
func TestProjectedTerminalPayloadBytes_ReproducesTheContractsFigure(t *testing.T) {
	got := ReferenceTerminalPayloadBytes()
	if want := int64(36000) * (64 << 10); got != want {
		t.Errorf("ReferenceTerminalPayloadBytes() = %d, want %d", got, want)
	}
	// 2 359 296 000 bytes = 2.197 GiB. §6's "≈ 2,2 GiB".
	gib := float64(got) / float64(1<<30)
	if gib < 2.15 || gib > 2.25 {
		t.Errorf("reference projection = %.3f GiB, want §6's ≈2.2 GiB", gib)
	}
	if ProjectedTerminalPayloadBytes(0, 1) != 0 || ProjectedTerminalPayloadBytes(1, 0) != 0 {
		t.Error("a projection over nothing is not zero")
	}
}

// The non-terminal set the ingress counts use and the terminal set the sweeper
// deletes on must be exact complements. They are derived from State.Terminal()
// for exactly this reason: work.go records that an earlier version of this
// package hand-wrote a state list in four queries and one of them disagreed
// with the other three.
func TestIngressStateSets_AreExactComplements(t *testing.T) {
	for _, st := range allStates {
		inTerminal := containsQuoted(sqlTerminal, string(st))
		inNonTerminal := containsQuoted(sqlNonTerminal, string(st))
		if inTerminal == inNonTerminal {
			t.Errorf("%s appears in both sets or neither: terminal=%v non-terminal=%v",
				st, inTerminal, inNonTerminal)
		}
		if inTerminal != st.Terminal() {
			t.Errorf("sqlTerminal disagrees with State.Terminal() for %s", st)
		}
	}
	// needs_reconciliation is the one that matters most: it is NOT terminal, so
	// its payload is never swept and it counts against ingress. A runtime may
	// still be alive under its locator.
	if !containsQuoted(sqlNonTerminal, string(StateNeedsReconciliation)) {
		t.Error("needs_reconciliation is not in the non-terminal set; its payload would be swept")
	}
}

func containsQuoted(list, state string) bool {
	q := "'" + state + "'"
	for i := 0; i+len(q) <= len(list); i++ {
		if list[i:i+len(q)] == q {
			return true
		}
	}
	return false
}
