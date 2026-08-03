package backup

// What a bundle produced before #1713 does now.
//
// The answer has to be "everything it really contains restores, and the
// operator is told, once, that the memory it claims is not in there" —
// because the claim is baked into bundles that already exist and cannot
// be corrected in place. A reader that takes memory_included at face
// value on such a bundle repeats the original lie; one that refuses the
// bundle outright throws away the workspace files and DB rows that ARE
// real. Neither is acceptable, so the format version decides.

import "testing"

func TestCrewSummary_HasCrewMemory_IsFormatAware(t *testing.T) {
	// The shape every pre-fix bundle has: memory_included asserted, and
	// the section behind it holding /output rather than the memory tree.
	legacy := CrewSummary{Slug: "engineering", MemoryIncluded: true}

	for _, v := range []int{1, 2} {
		if legacy.HasCrewMemory(v) {
			t.Errorf("format v%d claims crew memory; no bundle at that version has ever contained any", v)
		}
	}
	if !legacy.HasCrewMemory(FormatVersionCrewMemory) {
		t.Errorf("at v%d the flag is observed, so it must be believed", FormatVersionCrewMemory)
	}

	// And the flag still means "no" when it says no, at every version.
	none := CrewSummary{Slug: "engineering"}
	for _, v := range []int{1, 2, FormatVersionCrewMemory} {
		if none.HasCrewMemory(v) {
			t.Errorf("format v%d invented crew memory from an unset flag", v)
		}
	}
}

// TestCrewSummary_HasFilesystemSections_LegacyBundleStillRestores is the
// backward-compatibility half. The restore docker phase gates on this
// predicate, so if a legacy bundle's memory_included stopped counting
// WITHOUT the /output section being counted in its place, a pre-fix
// bundle carrying only that section would silently restore nothing.
func TestCrewSummary_HasFilesystemSections_LegacyBundleStillRestores(t *testing.T) {
	// Pre-v3: the only flag set is memory_included, standing for /output.
	legacyOutputOnly := CrewSummary{Slug: "engineering", MemoryIncluded: true}
	if !legacyOutputOnly.HasFilesystemSections(2) {
		t.Error("a v2 bundle whose only section is the old memory/ (i.e. /output) must still get a docker phase; otherwise upgrading silently stops restoring it")
	}

	// v3+: the same flag means the crew memory tree, and still counts.
	current := CrewSummary{Slug: "engineering", MemoryIncluded: true}
	if !current.HasFilesystemSections(FormatVersionCrewMemory) {
		t.Error("a v3 bundle with crew memory must get a docker phase")
	}

	// v3+ with only /output.
	outputOnly := CrewSummary{Slug: "engineering", OutputIncluded: true}
	if !outputOnly.HasFilesystemSections(FormatVersionCrewMemory) {
		t.Error("a v3 bundle carrying only the /output section must still get a docker phase")
	}

	// Nothing at all: no docker phase, at any version. This is the case
	// that used to be impossible to express — the flags were set from
	// "the crew had a container ID", so a crew with no data on disk still
	// demanded a provisioned container and failed the restore for the
	// whole workspace when it did not find one.
	empty := CrewSummary{Slug: "engineering"}
	for _, v := range []int{1, 2, FormatVersionCrewMemory} {
		if empty.HasFilesystemSections(v) {
			t.Errorf("format v%d demanded a docker phase for a crew with no sections", v)
		}
	}
}

// TestFormatVersion_LegacyBundlesStayReadable pins the N-2 window across
// the v3 bump: the fix must not orphan bundles taken last month.
func TestFormatVersion_LegacyBundlesStayReadable(t *testing.T) {
	for _, v := range []int{1, 2, FormatVersion} {
		if !IsCompatible(v) {
			t.Errorf("format v%d is no longer readable; bundles taken before the fix must still restore", v)
		}
	}
	if IsCompatible(FormatVersion + 1) {
		t.Error("a future format version must not be silently accepted")
	}
}
