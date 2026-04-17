package main

import (
	"strings"
	"testing"
)

func TestEvalCmdStructure(t *testing.T) {
	t.Parallel()

	if evalCmd.Use != "eval" {
		t.Errorf("eval Use: got %q want %q", evalCmd.Use, "eval")
	}
	if !strings.Contains(strings.ToLower(evalCmd.Long), "not yet") &&
		!strings.Contains(strings.ToLower(evalCmd.Long), "stub") {
		t.Errorf("eval Long should document stub status; got %q", evalCmd.Long)
	}
	have := map[string]bool{}
	for _, sub := range evalCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"replay", "regression"} {
		if !have[want] {
			t.Errorf("eval missing subcommand %q; have %v", want, have)
		}
	}
}

func TestEvalReplayRunE_Stub(t *testing.T) {
	t.Parallel()

	err := evalReplayCmd.RunE(evalReplayCmd, []string{"mis-1"})
	if err == nil || !strings.Contains(err.Error(), "not yet available") {
		t.Errorf("expected stub 'not yet available' error; got %v", err)
	}
}

func TestEvalRegressionRunE_Stub(t *testing.T) {
	t.Parallel()

	err := evalRegressionCmd.RunE(evalRegressionCmd, []string{"base", "cand"})
	if err == nil || !strings.Contains(err.Error(), "not yet available") {
		t.Errorf("expected stub 'not yet available' error; got %v", err)
	}
}

func TestEvalReplayArgs(t *testing.T) {
	t.Parallel()

	if err := evalReplayCmd.Args(evalReplayCmd, []string{}); err == nil {
		t.Error("replay with no args should error")
	}
	if err := evalRegressionCmd.Args(evalRegressionCmd, []string{"one"}); err == nil {
		t.Error("regression with one arg should error (needs 2)")
	}
}

func TestEvalReplayFlags(t *testing.T) {
	t.Parallel()

	if evalReplayCmd.Flags().Lookup("seed") == nil {
		t.Error("eval replay missing --seed flag")
	}
}
