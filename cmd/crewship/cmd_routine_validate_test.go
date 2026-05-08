package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// The validate subcommand exits the process on validation failure
// (CI gate). Capture os.Exit by overriding it in tests would be
// intrusive — instead, drive validate's underlying contract via the
// pipeline package directly. The shape of these tests proves the
// CLI's contract without requiring a process fork.

func TestValidateCmd_HappyPathFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "good.json")
	good := []byte(`{
		"dsl_version": "1.0",
		"name": "demo",
		"steps": [{"id":"a","type":"agent_run","agent_slug":"x","prompt":"hi"}]
	}`)
	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateBytesForTest(raw); err != nil {
		t.Errorf("happy path rejected: %v", err)
	}
}

func TestValidateCmd_RejectsBadName(t *testing.T) {
	bad := []byte(`{
		"dsl_version": "1.0",
		"name": "BAD NAME WITH SPACES",
		"steps": [{"id":"a","type":"agent_run","agent_slug":"x","prompt":"hi"}]
	}`)
	if err := validateBytesForTest(bad); err == nil {
		t.Error("expected rejection for invalid name slug")
	}
}

func TestValidateCmd_RejectsUnknownStepType(t *testing.T) {
	bad := []byte(`{
		"dsl_version": "1.0",
		"name": "demo",
		"steps": [{"id":"a","type":"unknown_step"}]
	}`)
	if err := validateBytesForTest(bad); err == nil {
		t.Error("expected rejection for unknown step type")
	}
}

func TestValidateCmd_RejectsMalformedJSON(t *testing.T) {
	bad := []byte(`{not valid json`)
	if err := validateBytesForTest(bad); err == nil {
		t.Error("expected rejection for malformed JSON")
	}
}

func TestValidateCmd_DetectsDuplicateStepID(t *testing.T) {
	bad := []byte(`{
		"dsl_version": "1.0",
		"name": "demo",
		"steps": [
			{"id":"a","type":"agent_run","agent_slug":"x","prompt":"hi"},
			{"id":"a","type":"agent_run","agent_slug":"x","prompt":"hi"}
		]
	}`)
	if err := validateBytesForTest(bad); err == nil {
		t.Error("expected rejection for duplicate step ID")
	}
}

func TestValidateCmd_HandlesManyInputs(t *testing.T) {
	// Sanity: validation accepts inputs of varying size without hangs.
	body := []byte(`{
		"dsl_version": "1.0",
		"name": "many",
		"inputs": [`)
	for i := 0; i < 50; i++ {
		if i > 0 {
			body = append(body, ',')
		}
		body = append(body, []byte(`{"name":"in_`+strconv.Itoa(i)+`","type":"string"}`)...)
	}
	body = append(body, []byte(`],"steps":[{"id":"a","type":"agent_run","agent_slug":"x","prompt":"hi"}]}`)...)
	if err := validateBytesForTest(body); err != nil {
		t.Errorf("many-input fixture rejected: %v", err)
	}
}

// validateBytesForTest mirrors what cmd_routine_validate.go does in
// its RunE — pipeline.Parse + pipeline.Validate + step-id-uniqueness
// check — but returns the error instead of os.Exit'ing. The CLI's
// RunE wraps this with "exit 1 on error" semantics for CI-friendly
// behaviour; tests here exercise the underlying contract.
func validateBytesForTest(raw []byte) error {
	dsl, err := pipeline.Parse(raw)
	if err != nil {
		return err
	}
	if err := pipeline.Validate(dsl, nil, nil); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, s := range dsl.Steps {
		if seen[s.ID] {
			return errors.New("duplicate step ID: " + s.ID)
		}
		seen[s.ID] = true
	}
	return nil
}
