package seeddata

import (
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/statuses"
)

// validHop reports whether the transition from → to is allowed by the
// canonical server-side DAG.
func validHop(from, to string) bool {
	for _, next := range statuses.ValidIssueTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

func TestStatusPathFrom_SameStatus(t *testing.T) {
	t.Parallel()
	got := StatusPathFrom("BACKLOG", "BACKLOG")
	if got == nil {
		t.Fatal("current == target must return an empty NON-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestStatusPathFrom_UnknownStatuses(t *testing.T) {
	t.Parallel()
	if got := StatusPathFrom("NOT_A_STATUS", "DONE"); got != nil {
		t.Errorf("unknown current must return nil; got %v", got)
	}
	if got := StatusPathFrom("BACKLOG", "NOT_A_STATUS"); got != nil {
		t.Errorf("unknown target must return nil; got %v", got)
	}
}

func TestStatusPathFrom_DirectAndMultiHop(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		current string
		target  string
		wantLen int
	}{
		{"direct hop", "BACKLOG", "TODO", 1},
		{"two hops to DONE", "BACKLOG", "DONE", 2}, // BACKLOG→IN_PROGRESS→DONE
		{"DONE back to TODO", "DONE", "TODO", 2},   // DONE→BACKLOG→TODO
		{"REVIEW to DONE", "REVIEW", "DONE", 1},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := StatusPathFrom(tc.current, tc.target)
			if got == nil {
				t.Fatalf("StatusPathFrom(%s, %s) = nil, want a path", tc.current, tc.target)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("path %v has %d hops, want %d", got, len(got), tc.wantLen)
			}
			if got[len(got)-1] != tc.target {
				t.Errorf("path %v does not end at %s", got, tc.target)
			}
			// Every hop must be a legal transition in the canonical DAG.
			prev := tc.current
			for _, next := range got {
				if !validHop(prev, next) {
					t.Errorf("illegal hop %s → %s in path %v", prev, next, got)
				}
				prev = next
			}
		})
	}
}

func TestStatusPathFrom_UnreachableTarget(t *testing.T) {
	t.Parallel()
	// DUPLICATE is a sink in the DAG: it exists as a key (so the target
	// check passes) but nothing transitions INTO it.
	if got := StatusPathFrom("BACKLOG", "DUPLICATE"); got != nil {
		t.Errorf("unreachable target must return nil; got %v", got)
	}
	// And nothing leaves it either.
	if got := StatusPathFrom("DUPLICATE", "BACKLOG"); got != nil {
		t.Errorf("path out of DUPLICATE must be nil; got %v", got)
	}
}

func TestStatusPath_DelegatesToBacklogStart(t *testing.T) {
	t.Parallel()
	want := StatusPathFrom("BACKLOG", "DONE")
	got := StatusPath("DONE")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("StatusPath(DONE) = %v, want %v (same as StatusPathFrom(BACKLOG, DONE))", got, want)
	}
	if got := StatusPath("BACKLOG"); got == nil || len(got) != 0 {
		t.Errorf("StatusPath(BACKLOG) = %v, want empty non-nil", got)
	}
}

func TestBundleCataloguesLoaded(t *testing.T) {
	t.Parallel()
	if len(Labels) == 0 {
		t.Error("Labels catalogue is empty — issues.yaml schema drift?")
	}
	if len(Projects) == 0 {
		t.Error("Projects catalogue is empty")
	}
	if len(Issues) == 0 {
		t.Error("Issues catalogue is empty")
	}
	for i, l := range Labels {
		if l.Name == "" {
			t.Errorf("Labels[%d] has empty name", i)
		}
	}
	for i, is := range Issues {
		if is.Title == "" || is.CrewSlug == "" {
			t.Errorf("Issues[%d] missing title/crew_slug: %+v", i, is)
		}
		// Every non-empty target state must be reachable from BACKLOG —
		// otherwise the seeder would silently leave the issue stuck.
		if is.TargetState != "" && is.TargetState != "BACKLOG" {
			if path := StatusPath(is.TargetState); path == nil {
				t.Errorf("Issues[%d] target_state %q unreachable from BACKLOG", i, is.TargetState)
			}
		}
	}
}

func TestMustLoadIssuesBundle_Idempotent(t *testing.T) {
	t.Parallel()
	doc := mustLoadIssuesBundle()
	if len(doc.Labels) != len(Labels) || len(doc.Projects) != len(Projects) || len(doc.Issues) != len(Issues) {
		t.Errorf("re-parse drifted from cached bundle: %d/%d/%d vs %d/%d/%d",
			len(doc.Labels), len(doc.Projects), len(doc.Issues),
			len(Labels), len(Projects), len(Issues))
	}
}
