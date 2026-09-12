package work

import (
	"context"
	"testing"
)

// Runtime ownership is a pair, not two independent allowlists. Higher-priority
// foreign work must retain its queue position, fence and complete retry budget.
func TestClaimKinds_OnlyDeclaredPairsCanConsumeWork(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kinds   []Kind
		foreign []Kind
	}{
		{"pairs", []Kind{{Source: SourceWebhook, DomainKind: DomainAgentRun}, {Source: SourceManual, DomainKind: DomainPipelineRun}},
			[]Kind{{Source: SourceWebhook, DomainKind: DomainPipelineRun}, {Source: SourceManual, DomainKind: DomainAgentRun}}},
		{"empty domain is a value", []Kind{{Source: SourceWebhook}},
			[]Kind{{Source: SourceWebhook, DomainKind: DomainAgentRun}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, db, _ := newTestStore(t)
			var foreignIDs []string
			for _, kind := range tc.foreign {
				r := accept(t, store, db, AcceptRequest{WorkspaceID: "ws1", Source: kind.Source, DomainKind: kind.DomainKind, Class: ClassBackground, Priority: 100})
				foreignIDs = append(foreignIDs, r.WorkID)
			}
			allowedIDs := make(map[string]bool)
			for _, kind := range tc.kinds {
				r := accept(t, store, db, AcceptRequest{WorkspaceID: "ws1", Source: kind.Source, DomainKind: kind.DomainKind, AgentID: "allowed-" + string(kind.Source) + "-" + kind.DomainKind, Class: ClassBackground})
				allowedIDs[r.WorkID] = true
			}
			for range tc.kinds {
				claimed, err := store.Claim(context.Background(), ClaimOptions{LeaseOwner: "owner", Kinds: tc.kinds})
				if err != nil {
					t.Fatal(err)
				}
				if !allowedIDs[claimed.Item.ID] {
					t.Fatalf("claimed foreign or repeated work %s", claimed.Item.ID)
				}
				delete(allowedIDs, claimed.Item.ID)
			}
			for _, id := range foreignIDs {
				item, err := store.Get(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				if item.State != StateQueued || item.Generation != 0 || item.Attempts != 0 {
					t.Fatalf("foreign work was consumed: %+v", item)
				}
				var attempts int
				if err := db.QueryRow(`SELECT COUNT(*) FROM work_attempts WHERE work_id = ?`, id).Scan(&attempts); err != nil {
					t.Fatal(err)
				}
				if attempts != 0 {
					t.Fatalf("foreign work has %d attempts", attempts)
				}
			}
		})
	}
}
