package mooring

import (
	"encoding/json"
	"strings"
	"testing"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

func TestSessionDeliveryBudgetIncludesOriginalOrdinaryWrapper(t *testing.T) {
	// DATA-only budget unit: schema/SQL/candidate authority are independently
	// checked by validateFullSessionOutputSelected and the original writer.
	var result protocol.SessionCommitResult
	if err := json.Unmarshal([]byte(`{"control_profile":"mooring.session-causal.v1","scope":{"workspace_id":"w","document_id":"d"},"session_id":"s","runtime_ref":"r","fence":{"generation":"1","token":"t"},"receipt":{"request_id":"private"},"public_receipt":{"request_id":"public"},"native_deltas":[{"revision":"1","native_delta":{"update":"AAA=","targets":["p"]}}]}`), &result); err != nil {
		t.Fatal(err)
	}
	if err := validateSessionParticipantBudgets(result); err != nil {
		t.Fatal("small delivery", err)
	}
	(*result.NativeDeltas)[0].NativeDelta.Update = strings.Repeat("A", 260000)
	// The application data itself fits the public bound. Its whole recipient
	// wrapper does not; discovering that after COMMIT would lose Saved output.
	inner, err := json.Marshal((*result.NativeDeltas)[0].NativeDelta)
	if err != nil || len(inner) >= MaxE3WireBytes {
		t.Fatal("fixture inner not bounded", err, len(inner))
	}
	if err := validateSessionParticipantBudgets(result); err == nil {
		t.Fatal("accepted oversized participant wrapper")
	}
	(*result.NativeDeltas)[0].NativeDelta.Update = "AAA="
	result.PublicReceipt = nil
	if err := validateSessionParticipantBudgets(result); err == nil {
		t.Fatal("missing original public receipt")
	}
}
