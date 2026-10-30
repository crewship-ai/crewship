package mooring

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

func TestNativeProposalRetainedBytesUseOwnQuota(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	human := *acting
	lease, _, err := s.Initialize(t.Context(), scope, human, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Read(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":"` + strings.Repeat("x", 4096) + `"}`)
	*acting = nativeProposalAgent
	p, err := s.Propose(t.Context(), scope, 1, "one", "Review", raw)
	if err != nil {
		t.Fatal(err)
	}
	used, err := nativeProposalBytes(t.Context(), s.db, scope)
	if err != nil || used < int64(len(raw)) {
		t.Fatal("proposal excluded from retained quota", used, err)
	}
	*acting = human
	if _, err := s.RejectProposal(t.Context(), scope, p.ID); err != nil {
		t.Fatal(err)
	}
	rejected, err := nativeProposalBytes(t.Context(), s.db, scope)
	if err != nil || rejected < used {
		t.Fatal("rejection replenished retained quota", rejected, err)
	}
	s.config.MaxProposalBytes = int64(len(raw)) + 2000
	*acting = nativeProposalAgent
	if _, err := s.Propose(t.Context(), scope, 1, "two", "Review", raw); !errors.Is(err, artifacts.ErrVersionQuota) {
		t.Fatal("retained proposal flood bypassed its own quota", err)
	}
	list, err := s.Proposals(t.Context(), scope)
	if err != nil || len(list) != 1 || list[0].ID != p.ID || list[0].State != "rejected" {
		t.Fatal("refusal changed confirmed proposals", list, err)
	}
	*acting = human
	state, err := s.Read(t.Context(), scope)
	if err != nil || state.LatestVersion != 1 || state.Quota.UsedBytes != before.Quota.UsedBytes {
		t.Fatal("proposal consumed human history allowance", state, err)
	}
	big := []byte(fmt.Sprintf(`{"v":"%s"}`, strings.Repeat("y", len(raw)+1000)))
	if _, err := s.Save(t.Context(), lease, 1, big); err != nil {
		t.Fatal("proposal quota blocked the human save", err)
	}
}

func TestNativeProposalConcurrentCandidatesShareOwnQuota(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	if _, _, err := s.Initialize(t.Context(), scope, *acting, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":"` + strings.Repeat("x", 4096) + `"}`)
	s.config.MaxProposalBytes = int64(len(raw)) + 2000
	*acting = nativeProposalAgent
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []string{"concurrent-one", "concurrent-two"} {
		go func(id string) {
			<-start
			_, err := s.Propose(t.Context(), scope, 1, id, "Review", raw)
			results <- err
		}(id)
	}
	close(start)
	accepted, refused := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, artifacts.ErrVersionQuota):
			refused++
		default:
			t.Fatal(err)
		}
	}
	if accepted != 1 || refused != 1 {
		t.Fatal("proposal budget was not atomic", accepted, refused)
	}
	list, err := s.Proposals(t.Context(), scope)
	if err != nil || len(list) != 1 {
		t.Fatal("quota rollback changed confirmed candidates", list, err)
	}
}

func TestNativeProposalQuotaCountsLegacyTextBytes(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	if _, _, err := s.Initialize(t.Context(), scope, *acting, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	before, err := s.Read(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":"` + strings.Repeat("🙂", 4096) + `"}`)
	*acting = nativeProposalAgent
	p, err := s.Propose(t.Context(), scope, 1, "unicode", "", raw)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the TEXT affinity of rows restored by the earlier backup code.
	_, err = s.db.ExecContext(t.Context(), `INSERT INTO craft_native_proposals(id,page_id,workspace_id,application_id,engine_pin,author_actor_id,request_id,base_version,summary,content_hash,content) SELECT ?,page_id,workspace_id,application_id,engine_pin,author_actor_id,'legacy-text',base_version,summary,content_hash,CAST(content AS TEXT) FROM craft_native_proposals WHERE id=?`, fmt.Sprintf("ncp_%024x", 12345), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.Read(t.Context(), scope)
	if err != nil || after.Quota.UsedBytes != before.Quota.UsedBytes {
		t.Fatalf("legacy proposals consumed human history allowance: %+v %v", after.Quota, err)
	}
	used, err := nativeProposalBytes(t.Context(), s.db, scope)
	if err != nil || used < 2*int64(len(raw)) {
		t.Fatalf("legacy TEXT charged as characters instead of bytes: %d %v", used, err)
	}
}

func TestNativeProposalFullQuotaRollsBackAcceptAndAllowsReject(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	human := *acting
	lease, _, err := s.Initialize(t.Context(), scope, human, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	*acting = nativeProposalAgent
	p, err := s.Propose(t.Context(), scope, 1, "full-accept", "Review", []byte(`{"v":"candidate"}`))
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.Read(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	s.config.MaxHistoryBytes = state.Quota.UsedBytes
	s.versions = artifacts.NewVersionStore(s.db, artifacts.VersionOptions{Enabled: true, ContentType: s.config.Application, MaxHistoryBytes: s.config.MaxHistoryBytes})
	*acting = human
	if _, _, err := s.AcceptProposal(t.Context(), lease, p.ID); !errors.Is(err, artifacts.ErrVersionQuota) {
		t.Fatalf("accept over full quota: %v", err)
	}
	list, err := s.Proposals(t.Context(), scope)
	if err != nil || len(list) != 1 || list[0].State != "open" {
		t.Fatalf("failed accept committed decision: %+v %v", list, err)
	}
	after, err := s.Read(t.Context(), scope)
	if err != nil || after.LatestVersion != 1 || after.Quota.UsedBytes != state.Quota.UsedBytes {
		t.Fatalf("failed accept changed confirmed data/quota: %+v %v", after, err)
	}
	if _, err := s.RejectProposal(t.Context(), scope, p.ID); err != nil {
		t.Fatalf("full quota prevented rejection: %v", err)
	}
	after, err = s.Read(t.Context(), scope)
	if err != nil || after.Quota.UsedBytes != state.Quota.UsedBytes {
		t.Fatalf("rejection replenished budget or needed extra space: %+v %v", after.Quota, err)
	}
}

func TestNativeProposalOpenRemainsDiscoverableAfterDecidedHistory(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	human := *acting
	if _, _, err := s.Initialize(t.Context(), scope, human, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	*acting = nativeProposalAgent
	open, err := s.Propose(t.Context(), scope, 1, "old-open", "Still awaiting a decision", []byte(`{"v":"open"}`))
	if err != nil {
		t.Fatal(err)
	}
	// Seed an older persisted pending row without changing immutable fields.
	oldID := "ncp_000000000000000000000001"
	if _, err = s.db.ExecContext(t.Context(), `INSERT INTO craft_native_proposals(id,page_id,workspace_id,application_id,engine_pin,author_actor_id,request_id,base_version,summary,content_hash,content,created_at) SELECT ?,page_id,workspace_id,application_id,engine_pin,author_actor_id,'older-persisted-open',base_version,summary,content_hash,content,'2000-01-01T00:00:00Z' FROM craft_native_proposals WHERE id=?`, oldID, open.ID); err != nil {
		t.Fatal(err)
	}
	*acting = human
	if _, err = s.RejectProposal(t.Context(), scope, open.ID); err != nil {
		t.Fatal(err)
	}
	open.ID = oldID
	for i := range 51 {
		*acting = nativeProposalAgent
		decided, err := s.Propose(t.Context(), scope, 1, fmt.Sprintf("newer-decided-%d", i), "Already reviewed", []byte(`{"v":"declined"}`))
		if err != nil {
			t.Fatal(err)
		}
		*acting = human
		if _, err = s.RejectProposal(t.Context(), scope, decided.ID); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.Proposals(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 50 {
		t.Fatalf("expected bounded review list, got %d", len(list))
	}
	found := false
	for _, p := range list {
		if p.ID == open.ID && p.State == "open" {
			found = true
		}
	}
	if !found {
		t.Fatal("undecided proposal disappeared behind newer decided history")
	}
}

func TestNativeProposalCannotConsumeHumanHistoryAllowance(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	human := *acting
	lease, _, err := s.Initialize(t.Context(), scope, human, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	s.config.MaxHistoryBytes = 10000
	s.versions = artifacts.NewVersionStore(s.db, artifacts.VersionOptions{Enabled: true, ContentType: s.config.Application, MaxHistoryBytes: 10000})
	*acting = nativeProposalAgent
	candidate := []byte(`{"v":"` + strings.Repeat("p", 6000) + `"}`)
	if _, err := s.Propose(t.Context(), scope, 1, "pending-human-save", "", candidate); err != nil {
		t.Fatal(err)
	}
	*acting = human
	content := []byte(`{"v":"` + strings.Repeat("h", 7000) + `"}`)
	if _, err := s.Save(t.Context(), lease, 1, content); err != nil {
		t.Fatal("retained agent proposal blocked the human's available history budget", err)
	}
	state, err := s.Read(t.Context(), scope)
	if err != nil || state.LatestVersion != 2 || string(state.Manifest.Files["document.json"].Content) != string(content) {
		t.Fatal("human save was not confirmed", state, err)
	}
}

func TestNativeProposalOwnRetainedBudgetSurvivesRejectAndReplay(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	human := *acting
	if _, _, err := s.Initialize(t.Context(), scope, human, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	s.config.MaxProposalBytes = 6000
	*acting = nativeProposalAgent
	raw := []byte(`{"v":"` + strings.Repeat("x", 4096) + `"}`)
	first, err := s.Propose(t.Context(), scope, 1, "bounded-one", "", raw)
	if err != nil {
		t.Fatal(err)
	}
	*acting = human
	if _, err := s.RejectProposal(t.Context(), scope, first.ID); err != nil {
		t.Fatal(err)
	}
	*acting = nativeProposalAgent
	replay, err := s.Propose(t.Context(), scope, 1, "bounded-one", "", raw)
	if err != nil || !replay.Replayed || replay.ID != first.ID {
		t.Fatal("exact retry lost confirmed proposal", replay, err)
	}
	if _, err := s.Propose(t.Context(), scope, 1, "bounded-two", "", raw); !errors.Is(err, artifacts.ErrVersionQuota) {
		t.Fatal("agent bypassed its own retained budget", err)
	}
	list, err := s.Proposals(t.Context(), scope)
	if err != nil || len(list) != 1 || list[0].State != "rejected" {
		t.Fatal("quota refusal changed confirmed proposals", list, err)
	}
}
