package health

import "testing"

func TestMonitorResetDropsHistoryAndStartsFresh(t *testing.T) {
	m := NewMonitor(0)
	m.Record(Verdict{WorkspaceID: "before", Decision: "ALLOW"})
	before, ok := m.Snapshot("before")
	if !ok || before.Samples != 1 || before.Newest.IsZero() {
		t.Fatalf("initial sample = %+v, %v", before, ok)
	}
	m.Reset()
	if _, ok := m.Snapshot("before"); ok || m.TrackedWorkspaces() != 0 {
		t.Fatal("reset retained prior health history")
	}
	m.Record(Verdict{WorkspaceID: "after", Decision: "DENY"})
	after, ok := m.Snapshot("after")
	if !ok || after.Samples != 1 || after.Allow != 0 || after.Deny != 1 {
		t.Fatalf("new sample = %+v, %v", after, ok)
	}
	var absent *Monitor
	if _, ok := absent.Snapshot("before"); ok {
		t.Fatal("nil monitor invented history")
	}
	if absent.TrackedWorkspaces() != 0 {
		t.Fatal("nil monitor has workspaces")
	}
}
