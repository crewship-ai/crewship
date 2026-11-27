package api

import (
	"encoding/json"
	"testing"
)

// The operations saved with an agent version are the agent's commands with every address
// resolved (the sheet folded in), in order, named as the editor's execute takes them, so an editor
// replays exactly what was applied. An address without its sheet would land on the person's
// active sheet: such a version is not replayable.
func TestAgentOperationsAreTheResolvedCommands(t *testing.T) {
	ops, ok := craftNativeAgentResolvedOps(json.RawMessage(`[
	 {"command":"range.clear","params":{"range":"B8:B577","sheet":"L"}},
	 {"command":"cell.set","params":{"cell":"C1","input":"done","sheet":"L"}},
	 {"command":"range.setValues","params":{"range":"'L'!D1:E1","values":[[1,"x"]]}}]`))
	want := `[{"command":"edit.clearContents","params":{"range":"'L'!B8:B577"}},{"command":"cell.set","params":{"cell":"'L'!C1","input":"done"}},{"command":"range.setValues","params":{"range":"'L'!D1:E1","values":[[1,"x"]]}}]`
	if !ok || string(ops) != want {
		t.Fatalf("ops %v %s\nwant %s", ok, ops, want)
	}
	// Anything not a write (or not resolvable) leaves the version without operations.
	for _, bad := range []string{`[{"command":"range.columnByHeader","params":{"header":"x"}}]`, `[{"command":"cell.set","params":{"cell":"C1","input":"a","sheet":"it's"}}]`, `{}`, `[]`, `[{"command":"cell.set","params":{"cell":"C1","input":"a"}}]`} {
		if _, ok := craftNativeAgentResolvedOps(json.RawMessage(bad)); ok {
			t.Errorf("replayable %s", bad)
		}
	}
}
