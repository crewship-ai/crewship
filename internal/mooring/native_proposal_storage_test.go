package mooring

import (
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// The per-Page proposal budget stays independent: exhausting it is reported
// as a proposal refusal, never as aggregate storage. The aggregate ceiling is
// a separate, shared limit that counts proposals' real bytes, and a refusal
// there keeps every confirmed version and proposal.
func TestNativeProposalBudgetStaysIndependentOfAggregateStorage(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	human := *acting
	if _, _, err := s.Initialize(t.Context(), scope, human, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":"` + strings.Repeat("x", 4096) + `"}`)
	*acting = nativeProposalAgent
	first, err := s.Propose(t.Context(), scope, 1, "one", "Review", raw)
	if err != nil {
		t.Fatal(err)
	}
	s.config.MaxProposalBytes = int64(len(raw)) + 2000
	_, err = s.Propose(t.Context(), scope, 1, "two", "Review", raw)
	if !errors.Is(err, artifacts.ErrVersionQuota) || errors.Is(err, artifacts.ErrStorageQuota) {
		t.Fatalf("proposal budget refusal misreported as aggregate storage: %v", err)
	}
	s.config.MaxProposalBytes = DefaultNativeProposalBytes
	var used int64
	if err = s.db.QueryRow(`SELECT used_bytes FROM artifact_storage_workspaces WHERE workspace_id=?`, scope.WorkspaceID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used < int64(len(raw)) {
		t.Fatalf("aggregate storage ignored retained proposal bytes: %d", used)
	}
	if _, err = s.db.Exec(`UPDATE artifact_storage_workspaces SET max_bytes=used_bytes WHERE workspace_id=?`, scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Propose(t.Context(), scope, 1, "three", "Review", raw); !errors.Is(err, artifacts.ErrStorageQuota) {
		t.Fatalf("full workspace admitted a proposal: %v", err)
	}
	list, err := s.Proposals(t.Context(), scope)
	if err != nil || len(list) != 1 || list[0].ID != first.ID {
		t.Fatalf("aggregate refusal changed confirmed proposals: %+v %v", list, err)
	}
	*acting = human
	state, err := s.Read(t.Context(), scope)
	if err != nil || state.LatestVersion != 1 || state.Quota.Level == artifacts.QuotaLevelFull || state.Quota.StorageLevel != artifacts.QuotaLevelFull {
		t.Fatalf("history quota must stay Page-scoped while storage reports full: %+v %v", state.Quota, err)
	}
}
