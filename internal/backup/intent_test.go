package backup

import (
	"sort"
	"testing"
)

func TestIncludedTables_ReturnsOnlyInclude(t *testing.T) {
	got := IncludedTables()
	if len(got) == 0 {
		t.Fatal("IncludedTables returned empty; BackupTableIntent has Include entries")
	}
	gotSet := map[string]bool{}
	for _, n := range got {
		gotSet[n] = true
	}
	// Spot-check known Include entries.
	for _, must := range []string{"crews", "agents", "credentials", "journal_entries"} {
		if !gotSet[must] {
			t.Errorf("IncludedTables missing expected entry %q", must)
		}
	}
	// Spot-check known Exclude entries are absent.
	for _, mustNot := range []string{"audit_logs", "backup_locks", "user_sessions", "agent_status"} {
		if gotSet[mustNot] {
			t.Errorf("IncludedTables contains excluded entry %q", mustNot)
		}
	}
}

func TestBackupTableIntent_NoDuplicatesAndAllValid(t *testing.T) {
	seen := map[string]bool{}
	for name, intent := range BackupTableIntent {
		if seen[name] {
			t.Errorf("duplicate entry %q in BackupTableIntent", name)
		}
		seen[name] = true
		switch intent {
		case IntentInclude, IntentExcludeOperational, IntentExcludeRuntime:
			// valid
		default:
			t.Errorf("entry %q has unknown intent %d", name, intent)
		}
	}
}

// TestBackupTableIntent_SortedIncludedTables pins the contract that
// IncludedTables() returns its result already sorted alphabetically.
// Re-sorting `got` here would mask a regression where the function
// stops sorting; we check sort-order directly instead.
func TestBackupTableIntent_SortedIncludedTables(t *testing.T) {
	got := IncludedTables()
	if !sort.StringsAreSorted(got) {
		t.Fatalf("IncludedTables must return sorted output, got %v", got)
	}
	want := []string{}
	for n, i := range BackupTableIntent {
		if i == IntentInclude {
			want = append(want, n)
		}
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Errorf("count drift: IncludedTables=%d, direct filter=%d", len(got), len(want))
	}
	for i := range got {
		if i < len(want) && got[i] != want[i] {
			t.Errorf("entry %d drift: %q vs %q", i, got[i], want[i])
		}
	}
}
