package backup

import (
	"testing"
)

// ---------------------------------------------------------------------------
// runner_restore.go — rewriteCrewSlug.
//
// Mutates the in-memory DBDump so a --as-crew <slug> restore lands
// under a new identity. ID (PK) stays stable; only slug + display
// name change. The function looks trivial but is the only thing
// keeping crew-scope renames safe — a regression that touched the
// wrong row, or all rows, or left the original slug, would either:
//   - silently overwrite an existing crew (collision on slug UNIQUE)
//   - rename every crew in the dump to the new slug
//   - leave both copies present after restore
// ---------------------------------------------------------------------------

func TestRewriteCrewSlug_HappyPath_UpdatesSlugAndNameOnMatchingRow(t *testing.T) {
	dump := &DBDump{
		Tables: map[string][]map[string]any{
			"crews": {
				{"id": "crew-1", "slug": "old-slug", "name": "Old Name"},
			},
		},
	}
	rewriteCrewSlug(dump, "crew-1", "fresh-start")

	got := dump.Tables["crews"][0]
	if got["slug"] != "fresh-start" {
		t.Errorf("slug = %v, want \"fresh-start\"", got["slug"])
	}
	if got["name"] != "fresh-start" {
		t.Errorf("name = %v, want \"fresh-start\" (display name also rewritten)", got["name"])
	}
	// PK stays stable — restoring under a new slug must preserve the
	// row identity so FK references still resolve.
	if got["id"] != "crew-1" {
		t.Errorf("id changed: %v, want \"crew-1\" — PK MUST stay stable across rename", got["id"])
	}
}

func TestRewriteCrewSlug_LeavesOtherCrewsUntouched(t *testing.T) {
	// A crew-scope dump can contain multiple crew rows (e.g. when the
	// exporter pulled the full crews table for FK resolution). Only
	// the row matching crewID must be rewritten — touching any other
	// would corrupt unrelated state on the target workspace.
	dump := &DBDump{
		Tables: map[string][]map[string]any{
			"crews": {
				{"id": "crew-1", "slug": "alpha", "name": "Alpha"},
				{"id": "crew-2", "slug": "beta", "name": "Beta"},
				{"id": "crew-3", "slug": "gamma", "name": "Gamma"},
			},
		},
	}
	rewriteCrewSlug(dump, "crew-2", "renamed-beta")

	if dump.Tables["crews"][0]["slug"] != "alpha" {
		t.Errorf("non-target crew-1 slug = %v, want \"alpha\" — must not change", dump.Tables["crews"][0]["slug"])
	}
	if dump.Tables["crews"][0]["name"] != "Alpha" {
		t.Errorf("non-target crew-1 name = %v, want \"Alpha\" — must not change", dump.Tables["crews"][0]["name"])
	}
	if dump.Tables["crews"][1]["slug"] != "renamed-beta" {
		t.Errorf("target crew-2 slug = %v, want \"renamed-beta\"", dump.Tables["crews"][1]["slug"])
	}
	if dump.Tables["crews"][2]["slug"] != "gamma" {
		t.Errorf("non-target crew-3 slug = %v, want \"gamma\" — must not change", dump.Tables["crews"][2]["slug"])
	}
}

func TestRewriteCrewSlug_NoMatch_NoOp(t *testing.T) {
	// If the supplied crewID doesn't exist in the dump, the function
	// returns without modification. Pin so a regression that wrote a
	// brand-new row (or silently appended) would surface.
	dump := &DBDump{
		Tables: map[string][]map[string]any{
			"crews": {
				{"id": "crew-1", "slug": "alpha", "name": "Alpha"},
			},
		},
	}
	rewriteCrewSlug(dump, "crew-DOES-NOT-EXIST", "ghost")

	if len(dump.Tables["crews"]) != 1 {
		t.Errorf("crews len = %d, want 1 (no-match must not append)", len(dump.Tables["crews"]))
	}
	if dump.Tables["crews"][0]["slug"] != "alpha" {
		t.Errorf("slug = %v, want \"alpha\" — non-match must not mutate any row", dump.Tables["crews"][0]["slug"])
	}
}

func TestRewriteCrewSlug_NoCrewsTable_NoOp(t *testing.T) {
	// A dump exported from a target where the crews table didn't exist
	// (theoretical, but the source defends against it) must not panic.
	dump := &DBDump{
		Tables: map[string][]map[string]any{
			"workspaces": {{"id": "ws-1", "slug": "ws"}},
		},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("rewriteCrewSlug panicked on missing crews table: %v", r)
		}
	}()
	rewriteCrewSlug(dump, "crew-1", "anything")
	if _, present := dump.Tables["crews"]; present {
		t.Error("crews table was created by side-effect; expected no-op")
	}
}

func TestRewriteCrewSlug_EmptyCrewsTable_NoOp(t *testing.T) {
	// An empty crews slice is distinct from a missing table — pin both.
	dump := &DBDump{
		Tables: map[string][]map[string]any{
			"crews": {},
		},
	}
	rewriteCrewSlug(dump, "crew-1", "anything")
	if len(dump.Tables["crews"]) != 0 {
		t.Errorf("crews len = %d, want 0 (empty slice must stay empty)", len(dump.Tables["crews"]))
	}
}

func TestRewriteCrewSlug_StopsAtFirstMatch(t *testing.T) {
	// Source: returns inside the loop after rewriting the matching row.
	// SHOULD never happen in practice (id is PK + UNIQUE) but the early-
	// return contract is observable: if two rows somehow shared an id,
	// only the first would be rewritten.
	dump := &DBDump{
		Tables: map[string][]map[string]any{
			"crews": {
				{"id": "crew-1", "slug": "first", "name": "First"},
				{"id": "crew-1", "slug": "second", "name": "Second"},
			},
		},
	}
	rewriteCrewSlug(dump, "crew-1", "new")
	if dump.Tables["crews"][0]["slug"] != "new" {
		t.Errorf("first match: slug = %v, want \"new\"", dump.Tables["crews"][0]["slug"])
	}
	// Pin the early-return — second row stays untouched.
	if dump.Tables["crews"][1]["slug"] != "second" {
		t.Errorf("second-with-same-id: slug = %v, want \"second\" (function returns after first match)", dump.Tables["crews"][1]["slug"])
	}
}

func TestRewriteCrewSlug_EmptyNewSlug_StillApplies(t *testing.T) {
	// Defensive: the function does NOT guard against an empty newSlug.
	// Pin the current behaviour (write through) so a future "reject
	// empty" guard has to flip this test and consider where the empty
	// check belongs (at the CLI flag parser? at the runner?).
	dump := &DBDump{
		Tables: map[string][]map[string]any{
			"crews": {
				{"id": "crew-1", "slug": "old", "name": "Old"},
			},
		},
	}
	rewriteCrewSlug(dump, "crew-1", "")
	if dump.Tables["crews"][0]["slug"] != "" {
		t.Errorf("slug = %v, want \"\" — empty newSlug passes through (validation lives at caller)", dump.Tables["crews"][0]["slug"])
	}
}

func TestRewriteCrewSlug_IDIsString_StringComparison(t *testing.T) {
	// dump rows are map[string]any — id is compared via Go's == which
	// requires comparable types. A row where id is interface{}(int)
	// would NOT match a string crewID. Pin string-id comparison.
	dump := &DBDump{
		Tables: map[string][]map[string]any{
			"crews": {
				// Pathological: id was scanned as int not string.
				// Real rows always use string IDs, but pin the safety:
				// no panic, no false match.
				{"id": 12345, "slug": "weird", "name": "Weird"},
				{"id": "crew-1", "slug": "normal", "name": "Normal"},
			},
		},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked on int-typed id: %v", r)
		}
	}()
	rewriteCrewSlug(dump, "crew-1", "renamed")
	if dump.Tables["crews"][0]["slug"] != "weird" {
		t.Errorf("int-id row got rewritten — string crewID must not match non-string id field")
	}
	if dump.Tables["crews"][1]["slug"] != "renamed" {
		t.Errorf("string-id row not rewritten: %v", dump.Tables["crews"][1]["slug"])
	}
}
