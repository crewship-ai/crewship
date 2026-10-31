package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// nativeProposalFixture authorizes whichever principal the test currently
// acts as, so one store sees the human holder and the agent in turn.
func nativeProposalFixture(t *testing.T) (*NativeStore, *EditLeaseStore, *ownerTestClock, EditLeaseScope, *EditLeasePrincipal, *error) {
	t.Helper()
	_, db, clock, key := ownerFixture(t)
	scope := EditLeaseScope{key.WorkspaceID, key.ArtifactID}
	acting := &EditLeasePrincipal{"user:u", "native-tab-a"}
	var denied error
	leases := NewEditLeaseStore(db.DB, EditLeaseConfig{Enabled: true, Clock: clock, TTL: 5 * time.Second, Authority: func(context.Context, *sql.Tx, EditLeaseAccess) error { return nil }})
	s := NewNativeStore(db.DB, NativeStoreConfig{
		Enabled: true, Application: "wordcraft", EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Leases: leases,
		Authorize: func(context.Context, *sql.Tx, EditLeaseScope, string) (EditLeasePrincipal, error) {
			return *acting, denied
		},
		Validate: func(_ context.Context, b []byte) error {
			if !json.Valid(b) {
				return ErrInvalid
			}
			return nil
		},
		MaxHistoryBytes: 64 << 20,
	})
	return s, leases, clock, scope, acting, &denied
}

var nativeProposalAgent = EditLeasePrincipal{"agent:a1", "native-agent:run1"}

func TestNativeProposalWhileHumanHoldsLeaseLeavesDocumentUntilAccepted(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	human := *acting
	l, _, err := s.Initialize(t.Context(), scope, human, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	*acting = nativeProposalAgent
	p, err := s.Propose(t.Context(), scope, 1, "req-1", "Add a heading", []byte(`{"v":"agent"}`))
	if err != nil || p.State != "open" || p.BaseVersion != 1 || p.AuthorActorID != "agent:a1" || p.Summary != "Add a heading" {
		t.Fatalf("proposal not recorded: %+v %v", p, err)
	}
	// The exact retry after a lost acknowledgement returns the same proposal.
	again, err := s.Propose(t.Context(), scope, 1, "req-1", "Add a heading", []byte(`{"v":"agent"}`))
	if err != nil || again.ID != p.ID {
		t.Fatalf("retry recorded a second proposal: %+v %v", again, err)
	}
	if _, err = s.Propose(t.Context(), scope, 1, "req-1", "Add a heading", []byte(`{"v":"other"}`)); !errors.Is(err, artifacts.ErrVersionConflict) {
		t.Fatal("reused request id with other content admitted", err)
	}
	state, err := s.Read(t.Context(), scope)
	if err != nil || state.LatestVersion != 1 || string(state.Manifest.Files["document.json"].Content) != `{"v":1}` {
		t.Fatalf("proposal changed the document: %+v %v", state, err)
	}
	*acting = human
	list, err := s.Proposals(t.Context(), scope)
	if err != nil || len(list) != 1 || list[0].ID != p.ID {
		t.Fatalf("human cannot review the proposal: %+v %v", list, err)
	}
	content, err := s.ProposalContent(t.Context(), scope, p.ID)
	if err != nil || string(content.Content) != `{"v":"agent"}` {
		t.Fatalf("candidate unavailable for comparison: %+v %v", content, err)
	}
	accepted, receipt, err := s.AcceptProposal(t.Context(), l, p.ID)
	if err != nil || accepted.State != "accepted" || accepted.ResultVersion != 2 || receipt.Version != "2" || receipt.BaseVersion != "1" || receipt.EditorRevision != "native-proposal:"+p.ID {
		t.Fatalf("accept did not save a fenced version: %+v %+v %v", accepted, receipt, err)
	}
	state, _ = s.Read(t.Context(), scope)
	if state.LatestVersion != 2 || string(state.Manifest.Files["document.json"].Content) != `{"v":"agent"}` {
		t.Fatalf("accepted content missing: %+v", state)
	}
	if _, _, err = s.AcceptProposal(t.Context(), l, p.ID); !errors.Is(err, ErrNativeProposalDecided) {
		t.Fatal("proposal accepted twice", err)
	}
	if _, err = s.RejectProposal(t.Context(), scope, p.ID); !errors.Is(err, ErrNativeProposalDecided) {
		t.Fatal("final decision changed", err)
	}
}

func TestNativeProposalAcceptRechecksVersionLeaseAndRights(t *testing.T) {
	s, leases, clock, scope, acting, denied := nativeProposalFixture(t)
	human := *acting
	l, _, err := s.Initialize(t.Context(), scope, human, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	propose := func(id string) NativeProposal {
		t.Helper()
		*acting = nativeProposalAgent
		defer func() { *acting = human }()
		state, err := s.Read(t.Context(), scope)
		if err != nil {
			t.Fatal(err)
		}
		p, err := s.Propose(t.Context(), scope, state.LatestVersion, id, "", []byte(`{"v":"`+id+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	// The human kept typing after the agent's base: the proposal goes stale and
	// the human's newer version stays.
	stale := propose("stale")
	if _, err = s.SaveSnapshot(t.Context(), l, 1, "r1", nativeReceiptHash([]byte(`{"v":"human"}`)), []byte(`{"v":"human"}`)); err != nil {
		t.Fatal(err)
	}
	p, _, err := s.AcceptProposal(t.Context(), l, stale.ID)
	if !errors.Is(err, artifacts.ErrVersionConflict) || p.State != "stale" {
		t.Fatalf("stale base accepted: %+v %v", p, err)
	}
	list, _ := s.Proposals(t.Context(), scope)
	if len(list) != 1 || list[0].State != "stale" {
		t.Fatalf("stale state not recorded: %+v", list)
	}
	state, _ := s.Read(t.Context(), scope)
	if state.LatestVersion != 2 || string(state.Manifest.Files["document.json"].Content) != `{"v":"human"}` {
		t.Fatalf("human work replaced: %+v", state)
	}

	// Revoked write rights refuse the decision and keep the proposal open.
	open := propose("rights")
	*denied = ErrEditLeaseDenied
	if _, _, err = s.AcceptProposal(t.Context(), l, open.ID); !errors.Is(err, ErrEditLeaseDenied) {
		t.Fatal("accept without current rights", err)
	}
	*denied = nil

	// A lease that moved to another tab cannot accept with the old generation.
	clock.advance(6 * time.Second)
	if _, err = leases.Acquire(t.Context(), scope, EditLeasePrincipal{ActorID: human.ActorID, SessionID: "native-tab-b"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AcceptProposal(t.Context(), l, open.ID); err == nil {
		t.Fatal("old generation accepted a proposal")
	}
	list, _ = s.Proposals(t.Context(), scope)
	for _, item := range list {
		if item.ID == open.ID && item.State != "open" {
			t.Fatalf("refused accept decided the proposal: %+v", item)
		}
	}

	// The agent cannot decide its own proposal.
	*acting = nativeProposalAgent
	if _, err = s.RejectProposal(t.Context(), scope, open.ID); !errors.Is(err, ErrEditLeaseDenied) {
		t.Fatal("agent rejected a proposal", err)
	}
	*acting = human
	rejected, err := s.RejectProposal(t.Context(), scope, open.ID)
	if err != nil || rejected.State != "rejected" {
		t.Fatalf("reject failed: %+v %v", rejected, err)
	}
	state, _ = s.Read(t.Context(), scope)
	if state.LatestVersion != 2 {
		t.Fatalf("reject changed the document: %+v", state)
	}
}

func TestNativeProposalRefusesOldBaseHumanAuthorAndFlood(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	human := *acting
	l, _, err := s.Initialize(t.Context(), scope, human, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveSnapshot(t.Context(), l, 1, "r1", nativeReceiptHash([]byte(`{"v":2}`)), []byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Propose(t.Context(), scope, 2, "h", "", []byte(`{}`)); !errors.Is(err, ErrEditLeaseDenied) {
		t.Fatal("human recorded an agent proposal", err)
	}
	*acting = nativeProposalAgent
	if _, err = s.Propose(t.Context(), scope, 1, "old", "", []byte(`{}`)); !errors.Is(err, artifacts.ErrVersionConflict) {
		t.Fatal("proposal over an unknown version admitted", err)
	}
	if _, err = s.Propose(t.Context(), scope, 2, "bad", "", []byte(`not json`)); err == nil {
		t.Fatal("invalid native candidate admitted")
	}
	for i := 0; i < MaxOpenNativeProposals; i++ {
		if _, err = s.Propose(t.Context(), scope, 2, "r"+string(rune('a'+i)), "", []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.Propose(t.Context(), scope, 2, "overflow", "", []byte(`{}`)); !errors.Is(err, artifacts.ErrVersionQuota) {
		t.Fatal("unbounded open proposals", err)
	}
}

// #4500: open proposals over a version the document has moved past can never
// be accepted, so they must not keep holding places under the cap. They stay
// on record as stale.
func TestNativeProposalSupersededOpenProposalsStopCountingTowardCap(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	human := *acting
	l, _, err := s.Initialize(t.Context(), scope, human, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	*acting = nativeProposalAgent
	for i := 0; i < MaxOpenNativeProposals; i++ {
		if _, err = s.Propose(t.Context(), scope, 1, "old"+string(rune('a'+i)), "", []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	*acting = human
	if _, err = s.SaveSnapshot(t.Context(), l, 1, "r1", nativeReceiptHash([]byte(`{"v":2}`)), []byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	*acting = nativeProposalAgent
	fresh, err := s.Propose(t.Context(), scope, 2, "new", "", []byte(`{"v":"new"}`))
	if err != nil || fresh.State != "open" {
		t.Fatalf("superseded proposals still fill the cap: %+v %v", fresh, err)
	}
	*acting = human
	list, err := s.Proposals(t.Context(), scope)
	if err != nil || len(list) != MaxOpenNativeProposals+1 {
		t.Fatalf("proposal rows lost: %d %v", len(list), err)
	}
	for _, p := range list {
		want := "stale"
		if p.ID == fresh.ID {
			want = "open"
		}
		if p.State != want {
			t.Fatalf("proposal %s over v%d is %q, want %q", p.ID, p.BaseVersion, p.State, want)
		}
	}
	// A stale proposal is final: it can be neither accepted nor rejected.
	if _, err = s.RejectProposal(t.Context(), scope, list[len(list)-1].ID); !errors.Is(err, ErrNativeProposalDecided) {
		t.Fatal("stale proposal decided again", err)
	}
}

// The editor saves its holder's state before accepting. When that save only
// repeats the proposal's base bytes, the document did not change and the
// proposal still applies on top of the new head (dev3 D1, #4491).
func TestNativeProposalAcceptAfterUnchangedSaveApplies(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	human := *acting
	l, _, err := s.Initialize(t.Context(), scope, human, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	*acting = nativeProposalAgent
	p, err := s.Propose(t.Context(), scope, 1, "req-same", "", []byte(`{"v":"agent"}`))
	if err != nil {
		t.Fatal(err)
	}
	*acting = human
	if _, err = s.SaveSnapshot(t.Context(), l, 1, "flush", nativeReceiptHash([]byte(`{"v":1}`)), []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	accepted, receipt, err := s.AcceptProposal(t.Context(), l, p.ID)
	if err != nil || accepted.State != "accepted" || accepted.ResultVersion != 3 || receipt.Version != "3" || receipt.BaseVersion != "2" {
		t.Fatalf("unchanged save made the proposal stale: %+v %+v %v", accepted, receipt, err)
	}
	state, _ := s.Read(t.Context(), scope)
	if state.LatestVersion != 3 || string(state.Manifest.Files["document.json"].Content) != `{"v":"agent"}` {
		t.Fatalf("accepted content missing: %+v", state)
	}
}

// A save that only repeats the base bytes does not move the document, so a
// later proposal must not retire an earlier one that accept would still apply
// (review of #4511 against the identical-bytes rule).
func TestNativeProposalSupersedingKeepsProposalsOverUnchangedContent(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	human := *acting
	l, _, err := s.Initialize(t.Context(), scope, human, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	*acting = nativeProposalAgent
	first, err := s.Propose(t.Context(), scope, 1, "p1", "", []byte(`{"v":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	*acting = human
	if _, err = s.SaveSnapshot(t.Context(), l, 1, "flush", nativeReceiptHash([]byte(`{"v":1}`)), []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	*acting = nativeProposalAgent
	if _, err = s.Propose(t.Context(), scope, 2, "p2", "", []byte(`{"v":"p2"}`)); err != nil {
		t.Fatal(err)
	}
	*acting = human
	accepted, _, err := s.AcceptProposal(t.Context(), l, first.ID)
	if err != nil || accepted.State != "accepted" {
		t.Fatalf("proposal over unchanged content was retired: %+v %v", accepted, err)
	}
}

// A save that lands between choosing the acceptance base and publishing, and
// repeats the base bytes, does not change the document: the proposal must
// still apply on the new head instead of going permanently stale (G4 B26).
func TestNativeProposalAcceptSurvivesConcurrentUnchangedSave(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	human := *acting
	l, _, err := s.Initialize(t.Context(), scope, human, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	*acting = nativeProposalAgent
	p, err := s.Propose(t.Context(), scope, 1, "req-race", "", []byte(`{"v":"agent"}`))
	if err != nil {
		t.Fatal(err)
	}
	*acting = human
	raced := false
	s.afterProposalBase = func() {
		if raced {
			return
		}
		raced = true
		if _, err := s.SaveSnapshot(t.Context(), l, 1, "flush", nativeReceiptHash([]byte(`{"v":1}`)), []byte(`{"v":1}`)); err != nil {
			t.Error(err)
		}
	}
	accepted, receipt, err := s.AcceptProposal(t.Context(), l, p.ID)
	if err != nil || accepted.State != "accepted" || accepted.ResultVersion != 3 || receipt.Version != "3" {
		t.Fatalf("identical concurrent save made the proposal stale: %+v %+v %v", accepted, receipt, err)
	}
}

// A concurrent save that changes the content still retires the proposal.
func TestNativeProposalAcceptStaleAfterConcurrentChangedSave(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	human := *acting
	l, _, err := s.Initialize(t.Context(), scope, human, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	*acting = nativeProposalAgent
	p, err := s.Propose(t.Context(), scope, 1, "req-race2", "", []byte(`{"v":"agent"}`))
	if err != nil {
		t.Fatal(err)
	}
	*acting = human
	raced := false
	s.afterProposalBase = func() {
		if raced {
			return
		}
		raced = true
		if _, err := s.SaveSnapshot(t.Context(), l, 1, "edit", nativeReceiptHash([]byte(`{"v":2}`)), []byte(`{"v":2}`)); err != nil {
			t.Error(err)
		}
	}
	got, _, err := s.AcceptProposal(t.Context(), l, p.ID)
	if !errors.Is(err, artifacts.ErrVersionConflict) || got.State != "stale" {
		t.Fatalf("changed save must make the proposal stale: %+v %v", got, err)
	}
}
