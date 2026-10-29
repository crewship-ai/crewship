package mooring

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestPrivateCausalControlSharedShapesDirectionsAndProfileIsolation(t *testing.T) {
	raw, err := os.ReadFile("../../mooring/protocol/causal-control-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			ID      string          `json:"id"`
			Message json.RawMessage `json:"message"`
			Valid   bool            `json:"valid"`
			Sender  string          `json:"sender"`
		} `json:"cases"`
	}
	if json.Unmarshal(raw, &fixture) != nil || len(fixture.Cases) != 25 {
		t.Fatal("fixture drift")
	}
	v, err := newPrivateControlValidator()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			peer := privateCausalPeer(0)
			if tc.Sender == "host" {
				peer = privateCausalHost
			} else if tc.Sender == "runtime" {
				peer = privateCausalRuntime
			}
			_, err := v.validateCausal(tc.Message, peer)
			if (err == nil) != tc.Valid {
				t.Fatal("causal result", tc.Valid, err)
			}
			if _, err := v.validate(tc.Message, tc.Sender == "runtime"); err == nil {
				t.Fatal("causal shape entered strict profile")
			}
			if tc.ID == "atomic_prepared-causal" {
				var value map[string]any
				if json.Unmarshal(tc.Message, &value) != nil {
					t.Fatal("candidate fixture")
				}
				value["native_delta"] = map[string]any{"writer_origin": "1", "allocation_origins": []any{}, "update": "", "targets": []any{"p"}}
				if !v.schema.matches(v.schema.root, value, 0) {
					t.Fatal("session-native DATA is not schema-valid")
				}
				packet, _ := json.Marshal(value)
				if _, err := v.validateCausal(packet, peer); err == nil {
					t.Fatal("standalone causal acquired optional session capability")
				}
				value["native_delta"] = nil
				packet, _ = json.Marshal(value)
				if _, err := v.validateCausal(packet, peer); err == nil {
					t.Fatal("null optional field widened standalone profile")
				}
			}
		})
	}
	strict := privateValidateFixture(t)
	if _, err := v.validateCausal(strict, privateCausalHost); err == nil {
		t.Fatal("strict message entered causal profile")
	}
	causal := bytes.ReplaceAll(strict, []byte("validate_checkpoint"), []byte("validate_checkpoint_causal"))
	causal = bytes.ReplaceAll(causal, []byte(PrivatePrepareProfile), []byte(PrivateCausalPrepareProfile))
	if _, err := v.validateCausal(causal, privateCausalHost); err != nil {
		t.Fatal(err)
	}
	if _, err := v.validateCausal(append([]byte(`{"request_id":"foreign",`), causal[1:]...), privateCausalHost); err == nil {
		t.Fatal("duplicate identity accepted")
	}
}
