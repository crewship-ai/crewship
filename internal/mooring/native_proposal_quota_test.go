package mooring

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

func TestNativeProposalRetainedBytesUseHistoryQuota(t *testing.T) {
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
	after, err := s.Read(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if after.Quota.UsedBytes < before.Quota.UsedBytes+int64(len(raw)) {
		t.Errorf("proposal storage excluded: before=%+v after=%+v", before.Quota, after.Quota)
	}
	*acting = human
	if _, err := s.RejectProposal(t.Context(), scope, p.ID); err != nil {
		t.Fatal(err)
	}
	rejected, err := s.Read(t.Context(), scope)
	if err != nil || rejected.Quota.UsedBytes < after.Quota.UsedBytes {
		t.Fatalf("rejection hid retained bytes: %+v %v", rejected.Quota, err)
	}
	// Tighten the test policy around the stored data. Rejections must never
	// replenish the byte allowance while retaining the entire candidate.
	s.config.MaxHistoryBytes = before.Quota.UsedBytes + int64(len(raw)) + 2000
	s.versions = artifacts.NewVersionStore(s.db, artifacts.VersionOptions{Enabled: true, ContentType: s.config.Application, MaxHistoryBytes: s.config.MaxHistoryBytes})
	*acting = nativeProposalAgent
	if _, err := s.Propose(t.Context(), scope, 1, "two", "Review", raw); !errors.Is(err, artifacts.ErrVersionQuota) {
		t.Errorf("retained proposal flood bypassed history quota: %v", err)
	}
	list, err := s.Proposals(t.Context(), scope)
	if err != nil || len(list) != 1 || list[0].ID != p.ID || list[0].State != "rejected" {
		t.Fatalf("refusal changed confirmed proposals: %+v %v", list, err)
	}
	*acting = human
	state, err := s.Read(t.Context(), scope)
	if err != nil || state.LatestVersion != 1 || string(state.Manifest.Files["document.json"].Content) != `{"v":1}` {
		t.Fatalf("quota refusal changed confirmed document: %+v %v", state, err)
	}
	// Version publication shares the same allowance with retained proposals.
	big := []byte(fmt.Sprintf(`{"v":"%s"}`, strings.Repeat("y", len(raw)+1000)))
	if _, err := s.Save(t.Context(), lease, 1, big); !errors.Is(err, artifacts.ErrVersionQuota) {
		t.Errorf("save ignored retained proposal bytes: %v", err)
	}
}

func TestNativeProposalConcurrentCandidatesShareHistoryQuota(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	if _, _, err := s.Initialize(t.Context(), scope, *acting, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	before, err := s.Read(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":"` + strings.Repeat("x", 4096) + `"}`)
	s.config.MaxHistoryBytes = before.Quota.UsedBytes + int64(len(raw)) + 2000
	s.versions = artifacts.NewVersionStore(s.db, artifacts.VersionOptions{Enabled: true, ContentType: s.config.Application, MaxHistoryBytes: s.config.MaxHistoryBytes})
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
			t.Errorf("unexpected concurrent result: %v", err)
		}
	}
	if accepted != 1 || refused != 1 {
		t.Fatalf("shared quota admitted %d candidates, refused %d", accepted, refused)
	}
	list, err := s.Proposals(t.Context(), scope)
	if err != nil || len(list) != 1 {
		t.Fatalf("quota rollback lost or retained wrong candidates: %+v %v", list, err)
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
	if err != nil || after.Quota.UsedBytes < before.Quota.UsedBytes+2*int64(len(raw)) {
		t.Fatalf("legacy TEXT was charged as characters instead of bytes: %+v %v", after.Quota, err)
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
