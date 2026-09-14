package server

import "testing"

// E0 artifact attribution. A run's working directory is
// /output/<agent>/runs/<runID>, so a watched write can name the run that made
// it — which is the artifact half of "two run streams must never merge on
// agent slug alone" (IMPLEMENTATION §9).
//
// The cases that matter are the NEGATIVE ones. The reserved "runs" segment is
// the only thing separating a run directory from any other directory an agent
// happens to create, and orchestrator.ValidRunID — a safety check for shell
// interpolation, not a recogniser — says yes to "attachments", "src", "docs"
// and every other plausible folder name.
func TestRunIDFromEventPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"run artifact", "eva/runs/c0k3xj9q0001/report.md", "c0k3xj9q0001"},
		{"nested run artifact", "eva/runs/run-a/sub/dir/report.md", "run-a"},
		{"windows separators", `eva\runs\run-a\report.md`, "run-a"},
		{"leading dot-slash", "./eva/runs/run-a/report.md", "run-a"},

		// A chat attachment. The server writes these into the agent's SHARED
		// tree for the agent to read; without the reserved segment every one
		// of them would have been attributed to a run named "attachments".
		{"chat attachment is not a run", "eva/attachments/chat-1/att-1/photo.png", ""},
		// A file the agent wrote straight into its shared tree — still legal,
		// still attributed to the agent, but to no run.
		{"file in the shared tree", "eva/notes.txt", ""},
		{"directory in the shared tree", "eva/src/main.go", ""},
		// The run directory itself, with nothing under it yet.
		{"run dir with no file", "eva/runs/run-a", ""},
		{"bare filename at the crew root", "notes.txt", ""},
		{"empty", "", ""},
		// The segment is in the right place but is not a usable id.
		{"unsafe run id refused", "eva/runs/../escape/x", ""},
		{"empty run id refused", "eva/runs//x", ""},
		// "runs" in the wrong position must not match.
		{"runs as the agent segment", "runs/eva/run-a/x", ""},
		{"runs deeper down", "eva/src/runs/run-a/x", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := runIDFromEventPath(c.path); got != c.want {
				t.Errorf("runIDFromEventPath(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

// Two runs of ONE agent writing files must be distinguishable. Before E0 the
// watcher's only attribution was the agent slug, so the two streams were one.
func TestRunIDFromEventPath_TwoRunsOfOneAgentAreDistinguishable(t *testing.T) {
	a := runIDFromEventPath("eva/runs/run-a/out.txt")
	b := runIDFromEventPath("eva/runs/run-b/out.txt")
	if a == "" || b == "" {
		t.Fatalf("both writes must be attributed; got %q and %q", a, b)
	}
	if a == b {
		t.Errorf("two runs of one agent attributed to the same run: %q", a)
	}
}
