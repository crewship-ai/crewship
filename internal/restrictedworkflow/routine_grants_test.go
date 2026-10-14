//go:build linux

package restrictedworkflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestRoutineGrantsSelectThreeOfTenAndFenceRevocation(t *testing.T) {
	s, _ := fixture(t)
	if _, err := s.db.Exec(`UPDATE workspace_members SET role='VIEWER',capabilities='[]' WHERE user_id IN ('h1','h2')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("selected-%d", i)
		if _, err := s.db.Exec(`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,author_crew_id,status) VALUES(?,'w',?,?,?,?,'crew','active')`, id, id, id, routine, hash(routine)); err != nil {
			t.Fatal(err)
		}
	}
	store := access.Store{DB: s.db}
	replace := func(user string, rights []access.Right) {
		t.Helper()
		m, err := store.Membership(t.Context(), user, "w")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Replace(t.Context(), "owner", user, "w", "restricted", m, rights); err != nil {
			t.Fatal(err)
		}
	}
	rights := []access.Right{{Kind: "agent", ID: "agent", Operation: "run"}}
	for i := 0; i < 3; i++ {
		rights = append(rights, access.Right{Kind: "routine", ID: fmt.Sprintf("selected-%d", i), Operation: "run"})
	}
	replace("h1", rights)
	replace("h2", []access.Right{{Kind: "agent", ID: "agent", Operation: "run"}, {Kind: "routine", ID: "selected-9", Operation: "run"}})
	for _, user := range []string{"h1", "h2"} {
		list, err := s.Catalog(t.Context(), user, "w")
		if err != nil {
			t.Fatal(err)
		}
		want := 3
		if user == "h2" {
			want = 1
		}
		if len(list) != want {
			t.Fatalf("%s catalog = %+v", user, list)
		}
		raw, _ := json.Marshal(list)
		for i := 0; i < 10; i++ {
			visible := user == "h1" && i < 3 || user == "h2" && i == 9
			if strings.Contains(string(raw), fmt.Sprintf("selected-%d", i)) != visible {
				t.Fatalf("foreign routine in %s response: %s", user, raw)
			}
		}
	}
	if _, err := s.AdmitManual(t.Context(), "h1", "w", "selected-9", map[string]any{"task": "denied"}, "", 0); err == nil {
		t.Fatal("agent grant admitted an unshared routine")
	}
	receipt, err := s.AdmitManual(t.Context(), "h1", "w", "selected-0", map[string]any{"task": "allowed"}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Remove only the selected routine, retaining the agent and other routines.
	replace("h1", append(rights[:1:1], rights[2:]...))
	if _, err = s.ReceiptForActor(t.Context(), "h1", "w", receipt.ID); err == nil {
		t.Fatal("revoked routine receipt remained accessible")
	}
	if _, err = s.AdmitManual(t.Context(), "h1", "w", "selected-0", map[string]any{"task": "denied"}, "", 0); err == nil {
		t.Fatal("revoked routine admitted")
	}
	// Routine alone cannot borrow an ungranted agent.
	replace("h1", []access.Right{{Kind: "routine", ID: "selected-0", Operation: "run"}})
	list, err := s.Catalog(t.Context(), "h1", "w")
	if err != nil || len(list) != 0 {
		t.Fatalf("routine bypassed agent ceiling: %+v %v", list, err)
	}
}
