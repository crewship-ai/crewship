package pages

import (
	"encoding/json"
	"math"
	"testing"
)

func TestActionNumberInputsRejectNonFiniteValues(t *testing.T) {
	for _, value := range []any{"NaN", "+Inf", "-Inf", math.NaN(), math.Inf(1), math.Inf(-1), json.Number("NaN"), json.Number("Inf")} {
		action := callAction()
		action.Inputs = []PanelInput{{Name: "amount", Type: "number"}}
		if result, err := action.ResolveInputs(map[string]any{"amount": value}); err == nil {
			t.Fatalf("nonfinite action input accepted: value=%v resolved=%v", value, result)
		}
	}
}

func TestActionNumberDefaultsMustBeSerializable(t *testing.T) {
	for _, value := range []string{"NaN", "+Inf", "-Inf"} {
		action := callAction()
		action.Inputs = []PanelInput{{Name: "amount", Type: "number", Default: value}}
		if err := actionsDoc(action).Validate(); err == nil {
			t.Fatalf("nonfinite numeric default stored: %s", value)
		}
	}
}

func TestActionNumberInputsRemainJSONSerializable(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  float64
	}{{0, 0}, {-42, -42}, {1.25, 1.25}, {" 2e3 ", 2000}, {json.Number("-0.25"), -0.25}, {math.MaxFloat64, math.MaxFloat64}} {
		action := callAction()
		action.Inputs = []PanelInput{{Name: "amount", Type: "number"}}
		result, err := action.ResolveInputs(map[string]any{"amount": tc.value})
		if err != nil || result["amount"] != tc.want {
			t.Fatalf("finite number rejected or changed: %v %+v %v", tc.value, result, err)
		}
		if _, err := json.Marshal(result); err != nil {
			t.Fatalf("resolved action cannot enter dispatch fingerprint: %v", err)
		}
	}
	for _, value := range []any{true, []any{1}, map[string]any{"value": 1}, json.Number("invalid"), "1e10000", "not-number"} {
		action := callAction()
		action.Inputs = []PanelInput{{Name: "amount", Type: "number"}}
		if _, err := action.ResolveInputs(map[string]any{"amount": value}); err == nil {
			t.Fatalf("invalid number accepted: %#v", value)
		}
	}
}
