package mooring

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

// This is the pure context/read/effect intersection. A defensive Go rejection
// of a bypassing candidate is nonterminal, not a runtime prepared rejection or
// a Saved receipt. SQL and actual application qualification have separate tests.
func TestBatchTargetsSharedPrepareContextVectors(t *testing.T) {
	var fixture struct {
		Cases []struct {
			ID               string                `json:"id"`
			DocumentRevision string                `json:"document_revision"`
			Inventory        []BatchTargetIdentity `json:"inventory"`
			Targets          []BatchTargetVersion  `json:"targets"`
			Operations       []struct {
				ID    string                  `json:"op_id"`
				Base  string                  `json:"base_revision"`
				Reads []BatchReadPrecondition `json:"read_preconditions"`
			} `json:"operations"`
			Labels []BatchOperationRevision `json:"operation_revisions"`
			Steps  []struct {
				ID      string              `json:"op_id"`
				Effects []BatchTargetEffect `json:"effects"`
			} `json:"steps"`
			Expected struct {
				Context bool `json:"context_valid"`
				Entry   *struct {
					ID   string `json:"op_id"`
					Code string `json:"code"`
				} `json:"entry_failure"`
				Working *struct {
					ID   string `json:"op_id"`
					Code string `json:"code"`
				} `json:"working_failure"`
				Effects *bool                `json:"effects_valid"`
				Final   []BatchTargetVersion `json:"final_targets"`
			} `json:"expected"`
		} `json:"cases"`
	}
	raw, err := os.ReadFile("../../mooring/protocol/prepare-context-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	s := &BatchStore{config: BatchStoreConfig{Targets: &TargetStoreConfig{Enabled: true, Limits: DefaultBatchTargetLimits()}}}
	for _, c := range fixture.Cases {
		t.Run(c.ID, func(t *testing.T) {
			change := BatchChange{ID: "golden"}
			for _, op := range c.Operations {
				change.Operations = append(change.Operations, BatchOperation{ID: op.ID, BaseRevision: op.Base, Reads: op.Reads})
			}
			ctx := BatchPrepareContext{ReadPolicy: "agent_confirmed_current", DocumentRevision: c.DocumentRevision, Targets: c.Targets, OperationRevisions: c.Labels}
			before, _ := json.Marshal(ctx)
			err := validateConfirmedTargetContext(change, ctx, c.Inventory)
			if (err == nil) != c.Expected.Context {
				t.Fatal("context", err, c.Expected.Context)
			}
			if err != nil {
				return
			}
			failure := confirmedTargetReadFailure(change, ctx)
			if c.Expected.Entry != nil {
				if failure == nil || failure.OperationID != c.Expected.Entry.ID || failure.Reason.Code != c.Expected.Entry.Code {
					t.Fatal("entry", failure, c.Expected.Entry)
				}
				return
			}
			if failure != nil {
				t.Fatal("unexpected entry", failure)
			}
			candidate := BatchTargetCandidate{Inventory: []BatchTargetIdentity{}, Effects: []BatchOperationEffects{}}
			targets := c.Expected.Final
			if targets == nil {
				targets = c.Targets
			}
			for _, target := range targets {
				candidate.Inventory = append(candidate.Inventory, BatchTargetIdentity{TargetID: target.TargetID, Deleted: target.Deleted})
			}
			for _, step := range c.Steps {
				candidate.Effects = append(candidate.Effects, BatchOperationEffects{OperationID: step.ID, Targets: step.Effects})
			}
			rows, err := s.validateTargetCandidate(change, ctx, candidate, candidate.Inventory)
			reject := c.Expected.Working != nil || (c.Expected.Effects != nil && !*c.Expected.Effects)
			if (err != nil) != reject {
				t.Fatal("defensive effects result", err, reject)
			}
			after, _ := json.Marshal(ctx)
			if string(before) != string(after) {
				t.Fatal("bad candidate mutated trusted context")
			}
			if err != nil {
				return
			}
			state := map[string]BatchTargetVersion{}
			for _, v := range ctx.Targets {
				state[v.TargetID] = v
			}
			for _, r := range rows {
				state[r.After.TargetID] = r.After
			}
			actual := make([]BatchTargetVersion, 0, len(state))
			for _, v := range state {
				actual = append(actual, v)
			}
			sort.Slice(actual, func(i, j int) bool { return actual[i].TargetID < actual[j].TargetID })
			want := append([]BatchTargetVersion(nil), c.Expected.Final...)
			sort.Slice(want, func(i, j int) bool { return want[i].TargetID < want[j].TargetID })
			if !reflect.DeepEqual(actual, want) {
				t.Fatal("final targets", actual, want)
			}
		})
	}
}
