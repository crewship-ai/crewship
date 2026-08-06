package main

import (
	"bytes"
	"strings"
	"testing"
)

// The CLI breadth smoke found this real defect: agent avatar set declared
// --file/-f while the root already owns --format/-f. Cobra panics while merging
// persistent flags, even for --help. Keep the direct regression close to the
// command as well as in the binary breadth gate.
func TestAgentAvatarSet_HelpDoesNotPanic(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevErr := agentAvatarSetCmd.OutOrStdout(), agentAvatarSetCmd.ErrOrStderr()
	agentAvatarSetCmd.SetOut(&buf)
	agentAvatarSetCmd.SetErr(&buf)
	t.Cleanup(func() {
		agentAvatarSetCmd.SetOut(prevOut)
		agentAvatarSetCmd.SetErr(prevErr)
	})

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("agentAvatarSetCmd.Help panicked: %v", recovered)
		}
	}()
	if err := agentAvatarSetCmd.Help(); err != nil {
		t.Fatalf("Help: %v", err)
	}
	if !strings.Contains(buf.String(), "--file") {
		t.Fatal("help output missing --file")
	}
	if strings.Contains(buf.String(), "-f, --file") {
		t.Fatal("--file must not claim -f; root --format already owns that shorthand")
	}
}
