package mooring

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

// Same bytes as the pure Rust consumer. SQL-history qualification is separately
// tested; post-engine host checks must reject a candidate bypass nonterminally,
// even where the qualified application would return a direct read failure.
func TestCausalTargetsSharedCandidateReadAndEffectVectors(t *testing.T) {
	raw, err := os.ReadFile("../../mooring/protocol/causal-read-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	type step struct {
		ID      string              `json:"op_id"`
		Effects []BatchTargetEffect `json:"effects"`
	}
	var fixture struct {
		Cases []struct {
			ID         string                   `json:"id"`
			Head       string                   `json:"document_revision"`
			Targets    []BatchTargetVersion     `json:"targets"`
			Operations []BatchOperation         `json:"operations"`
			Labels     []BatchOperationRevision `json:"operation_revisions"`
			Reads      []BatchQualifiedRead     `json:"qualified_reads"`
			Steps      []step                   `json:"steps"`
			Expected   struct {
				Rust string `json:"rust"`
				ID   string `json:"op_id"`
			} `json:"expected"`
			Tamper *struct {
				Index  string          `json:"qualification_index"`
				Append *string         `json:"append_qualification_index"`
				Remove *string         `json:"remove_qualification_index"`
				Field  string          `json:"field"`
				Value  json.RawMessage `json:"value"`
			} `json:"tamper"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil || len(fixture.Cases) != 29 {
		t.Fatal("fixture drift", err)
	}
	store := &BatchStore{config: BatchStoreConfig{Targets: &TargetStoreConfig{Limits: DefaultBatchTargetLimits()}}}
	validator, err := newPrivateControlValidator()
	if err != nil {
		t.Fatal(err)
	}
	consumed := 0
	for _, tc := range fixture.Cases {
		if tc.Expected.Rust == "not_called" {
			continue
		}
		consumed++
		t.Run(tc.ID, func(t *testing.T) {
			c := BatchChange{Operations: tc.Operations}
			ctx := BatchCausalPrepareContext{DocumentRevision: tc.Head, Targets: tc.Targets, OperationRevisions: tc.Labels, QualifiedReads: tc.Reads}
			if tc.Tamper != nil {
				data, _ := json.Marshal(ctx.QualifiedReads)
				var values []map[string]json.RawMessage
				if json.Unmarshal(data, &values) != nil {
					t.Fatal("tamper fixture")
				}
				indexText := tc.Tamper.Index
				if tc.Tamper.Append != nil {
					indexText = *tc.Tamper.Append
				}
				if tc.Tamper.Remove != nil {
					indexText = *tc.Tamper.Remove
				}
				index, err := strconv.Atoi(indexText)
				if err != nil || index < 0 || index >= len(values) {
					t.Fatal("tamper index")
				}
				switch {
				case tc.Tamper.Append != nil:
					values = append(values, values[index])
				case tc.Tamper.Remove != nil:
					values = append(values[:index], values[index+1:]...)
				default:
					values[index][tc.Tamper.Field] = tc.Tamper.Value
				}
				// Explicit null is a raw-wire failure: a *string cannot retain its
				// distinction from absence after decoding. Check the frozen raw schema
				// before producing the pure host-owned context, as the RPC boundary does.
				if tc.Tamper.Field == "observed_structure_revision" && string(tc.Tamper.Value) == "null" {
					data, _ := json.Marshal(values[index])
					var value any
					if json.Unmarshal(data, &value) != nil {
						t.Fatal("raw qualification fixture")
					}
					node, ok := validator.schema.resolve("#/$defs/QualifiedCausalRead")
					if !ok || validator.schema.matches(node, value, 0) {
						t.Fatal("optional null passed raw qualifier schema")
					}
					return
				}
				data, _ = json.Marshal(values)
				if json.Unmarshal(data, &ctx.QualifiedReads) != nil {
					t.Fatal("tamper decode")
				}
			}
			inventory := make([]BatchTargetIdentity, 0, len(ctx.Targets))
			state := map[string]bool{}
			for _, target := range ctx.Targets {
				inventory = append(inventory, BatchTargetIdentity{TargetID: target.TargetID, Deleted: target.Deleted})
				state[target.TargetID] = target.Deleted
			}
			failure, bindingErr := causalTargetReadFailure(c, ctx, inventory)
			if tc.Expected.Rust == "nonterminal" {
				if bindingErr == nil || failure != nil {
					t.Fatal("malformed bindings became terminal", failure, bindingErr)
				}
				return
			}
			if bindingErr != nil {
				t.Fatal(bindingErr)
			}
			if failure != nil {
				if failure.OperationID != tc.Expected.ID || failure.Reason.Code != tc.Expected.Rust {
					t.Fatal("direct original read failure", failure, tc.Expected)
				}
				return
			}
			effects := map[string][]BatchTargetEffect{}
			for _, step := range tc.Steps {
				effects[step.ID] = step.Effects
			}
			candidate := BatchTargetCandidate{Effects: []BatchOperationEffects{}, Inventory: []BatchTargetIdentity{}}
			for _, op := range c.Operations {
				targets := effects[op.ID]
				if targets == nil {
					targets = []BatchTargetEffect{}
				}
				candidate.Effects = append(candidate.Effects, BatchOperationEffects{OperationID: op.ID, Targets: targets})
				for _, effect := range targets {
					state[effect.TargetID] = effect.Deleted
				}
			}
			for id, deleted := range state {
				candidate.Inventory = append(candidate.Inventory, BatchTargetIdentity{TargetID: id, Deleted: deleted})
			}
			_, err := store.validateCausalTargetCandidate(c, ctx, candidate, candidate.Inventory)
			if (err == nil) != (tc.Expected.Rust == "eligible") {
				t.Fatal("host candidate bypass check disagrees", tc.Expected, err)
			}
		})
	}
	if consumed != 24 {
		t.Fatal("unconsumed shared vectors", consumed)
	}
}
