//go:build unix

package runoutput

import "testing"

func TestInspectRetainsActualResultWithoutOutput(t *testing.T) {
	dir, store := testStore(t, DefaultLimit)
	live, err := Inspect(t.Context(), dir)
	if err != nil || live.Complete || live.Result != nil || live.Sequence != 1 {
		t.Fatalf("live=%+v err=%v", live, err)
	}
	if _, err = store.Write("stdout", []byte("private command output")); err != nil {
		t.Fatal(err)
	}
	if err = store.Finish(7, "exited"); err != nil {
		t.Fatal(err)
	}
	done, err := Inspect(t.Context(), dir)
	if err != nil || !done.Complete || done.Result == nil || done.Result.ExitCode != 7 || done.Sequence != 3 {
		t.Fatalf("done=%+v err=%v", done, err)
	}
}

func TestSnapshotRejectsAmbiguousResult(t *testing.T) {
	for _, value := range []Snapshot{
		{Version: 2, Sequence: 1},
		{Version: 1, Sequence: 2, Complete: true},
		{Version: 1, Sequence: 2, Result: &Result{ExitCode: 0, Reason: "exited"}},
		{Version: 1, Sequence: 2, Complete: true, Result: &Result{ExitCode: -1, Reason: "exited"}},
		{Version: 1, Sequence: 2, Complete: true, Result: &Result{ExitCode: 0, Reason: "unknown"}},
	} {
		if err := value.Validate(); err == nil {
			t.Fatalf("accepted %+v", value)
		}
	}
}
