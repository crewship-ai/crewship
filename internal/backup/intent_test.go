package backup

import (
	"sort"
	"testing"
)

// TestBackupTableIntent_AllIncludedAreDumped guards the intent→dump wiring:
// a table declared IntentInclude but absent from BackupTables is never
// actually exported/restored (the dumper iterates BackupTables only), so it
// is silent data loss. This is exactly the class of regression that shipped
// pipeline_routine_state / pipeline_run_step_outputs as "backed up" while the
// dumper skipped them. Any new IntentInclude table must also be added to
// BackupTables (with a workspaceFilterSQL scope clause if it has no
// workspace_id column).
func TestBackupTableIntent_AllIncludedAreDumped(t *testing.T) {
	dumped := map[string]bool{}
	for _, n := range BackupTables {
		dumped[n] = true
	}
	for _, n := range IncludedTables() {
		if !dumped[n] {
			t.Errorf("table %q is IntentInclude but missing from BackupTables — it will never be backed up or restored (silent data loss). Add it to BackupTables in FK-safe order.", n)
		}
	}
}

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

// TestKeeperAuxSettings_ExcludedFromBundles pins that the instance-global
// evaluator wiring is classified, and classified as an EXCLUDE.
//
// keeper_aux_settings holds one row per Keeper evaluator slot: which provider
// and model it dials, and — since #1554 — which vault credential it BILLS. That
// last column is a FOREIGN KEY into `credentials`, which is workspace-scoped, so
// the reverse-FK walk in DiscoverScopedTables now reaches this table from
// `workspaces` and demands a BackupTableIntent entry it did not previously need.
//
// The entry has to be an exclude, and the reason is not bookkeeping. The table
// is instance-global — one row per slot for the whole server, no workspace_id —
// so carrying it across a restore would let a bundle from one instance repoint
// the TARGET's evaluators at the source's models and, worse, at a credential id
// that means nothing in the target's vault. Those five slots are the paid half
// of the Keeper stack, so the failure would be a silent spend against the wrong
// subscription (or a degrade to the env key) on an instance nobody touched.
//
// Flipping this to IntentInclude is therefore a product decision, not a
// refactor, and this test is what makes that flip argue for itself.
func TestKeeperAuxSettings_ExcludedFromBundles(t *testing.T) {
	got, ok := BackupTableIntent["keeper_aux_settings"]
	if !ok {
		t.Fatalf("keeper_aux_settings has no BackupTableIntent entry: its credential_id FK " +
			"makes it reachable from workspaces, so CategoriseScopedTables now returns " +
			"ErrDiscoveryDrift for it")
	}
	if got == IntentInclude {
		t.Errorf("keeper_aux_settings is IntentInclude; it is instance-global evaluator "+
			"wiring (incl. which paid key each slot spends) and must not ride a workspace "+
			"bundle into another instance, got %v", got)
	}
}
