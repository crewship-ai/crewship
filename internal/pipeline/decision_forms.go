package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// DecisionForm is frozen with the waitpoint, not read from an editable recipe.
// Approved actions continue the DAG; denied actions use the existing OnFail path.
type DecisionForm struct {
	Fields  []InputSpec      `json:"fields" yaml:"fields"`
	Actions []DecisionAction `json:"actions" yaml:"actions"`
}
type DecisionAction struct {
	ID       string `json:"id" yaml:"id"`
	Label    string `json:"label" yaml:"label"`
	Approved bool   `json:"approved" yaml:"approved"`
}
type DecisionAnswer struct {
	ActionID string         `json:"action_id" yaml:"action_id"`
	Data     map[string]any `json:"data" yaml:"data"`
	Comment  string         `json:"comment,omitempty" yaml:"comment,omitempty"`
}

var ErrDecisionInput = errors.New("invalid decision input")

func decisionFields(form *DecisionForm) ([]InputSpec, error) {
	fields := append([]InputSpec(nil), form.Fields...)
	for i := range fields {
		f := &fields[i]
		widget := map[string]string{"string": "text", "integer": "number", "number": "number", "boolean": "boolean", "array": "textarea", "object": "textarea"}[f.Type]
		if widget == "" {
			return nil, fmt.Errorf("field %q: unsupported type %q", f.Name, f.Type)
		}
		if f.Widget == "" {
			f.Widget = widget
		}
	}
	if err := validateInputForms(&DSL{Inputs: fields}); err != nil {
		return nil, err
	}
	return fields, nil
}

func ValidateDecisionForm(form *DecisionForm) error {
	if form == nil {
		return nil
	}
	if form.Fields == nil {
		return fmt.Errorf("decision fields must be an array (use [] for no fields)")
	}
	if len(form.Fields) > 50 || len(form.Actions) < 2 || len(form.Actions) > 20 {
		return fmt.Errorf("decision form needs 2–20 actions and at most 50 fields")
	}
	if _, err := decisionFields(form); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, a := range form.Actions {
		if !inputFormNameRE.MatchString(a.ID) || seen[a.ID] || strings.TrimSpace(a.Label) == "" {
			return fmt.Errorf("decision actions need unique variable IDs and nonempty labels")
		}
		seen[a.ID] = true
	}
	return nil
}

// NormalizeDecisionAnswer validates against the stored schema and preserves typed
// defaults, including false and zero. Rejecting may omit required fields; any
// supplied value must still be well typed. Text remains data, never code.
func NormalizeDecisionAnswer(form *DecisionForm, approved bool, payload string) (string, error) {
	if err := ValidateDecisionForm(form); err != nil {
		return "", fmt.Errorf("%w: %v", ErrDecisionInput, err)
	}
	if form == nil {
		return payload, nil
	}
	var answer DecisionAnswer
	if err := json.Unmarshal([]byte(payload), &answer); err != nil {
		return "", fmt.Errorf("%w: action_id and data required", ErrDecisionInput)
	}
	found := false
	for _, a := range form.Actions {
		if a.ID == answer.ActionID && a.Approved == approved {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("%w: choose a declared action matching approved", ErrDecisionInput)
	}
	fields, _ := decisionFields(form)
	known := map[string]bool{}
	for i := range fields {
		known[fields[i].Name] = true
		if !approved {
			fields[i].Required = false
		}
	}
	for key := range answer.Data {
		if !known[key] {
			return "", fmt.Errorf("%w: unknown field %q", ErrDecisionInput, key)
		}
	}
	dsl := &DSL{Inputs: fields}
	if err := ValidateFormInputs(dsl, answer.Data); err != nil {
		return "", fmt.Errorf("%w: %v", ErrDecisionInput, err)
	}
	answer.Data = mergeInputs(answer.Data, dsl)
	encoded, err := json.Marshal(answer)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDecisionInput, err)
	}
	return string(encoded), nil
}

// ApprovalOutput reads the durable answer after restart. Legacy gates keep their
// exact historical output so existing conditions are unaffected.
func (s *SQLWaitpointStore) ApprovalOutput(ctx context.Context, workspaceID, token string) (string, error) {
	var form, payload string
	err := s.db.QueryRowContext(ctx, `SELECT decision_form_json, COALESCE(decision_payload,'') FROM pipeline_waitpoints WHERE workspace_id=? AND token=? AND status='approved'`, workspaceID, token).Scan(&form, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrAlreadyDecided
	}
	if err != nil {
		return "", err
	}
	if form == "" {
		return "waited:approval:approved", nil
	}
	return payload, nil
}
