package main

import (
	"testing"
)

func TestEvalCmdStructure(t *testing.T) {
	t.Parallel()

	if evalCmd.Use != "eval" {
		t.Errorf("eval Use: got %q want %q", evalCmd.Use, "eval")
	}
	have := map[string]bool{}
	for _, sub := range evalCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"replay", "regression", "runs"} {
		if !have[want] {
			t.Errorf("eval missing subcommand %q; have %v", want, have)
		}
	}
}

func TestEvalReplayArgs(t *testing.T) {
	t.Parallel()

	if err := evalReplayCmd.Args(evalReplayCmd, []string{}); err == nil {
		t.Error("replay with no args should error")
	}
	if err := evalReplayCmd.Args(evalReplayCmd, []string{"mis-1"}); err != nil {
		t.Errorf("replay with one arg should pass; got %v", err)
	}
	if err := evalRegressionCmd.Args(evalRegressionCmd, []string{"one"}); err == nil {
		t.Error("regression with one arg should error (needs 2)")
	}
	if err := evalRegressionCmd.Args(evalRegressionCmd, []string{"a", "b"}); err != nil {
		t.Errorf("regression with 2 args should pass; got %v", err)
	}
}

func TestEvalFlags(t *testing.T) {
	t.Parallel()

	if evalReplayCmd.Flags().Lookup("seed") == nil {
		t.Error("eval replay missing --seed flag")
	}
	if evalRunsCmd.Flags().Lookup("limit") == nil {
		t.Error("eval runs missing --limit flag")
	}
}
