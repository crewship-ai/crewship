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

import (
	"archive/tar"
	"bytes"
	"testing"
	"time"
)

// TestRepackResult_SkeletonDirsAreNotMemory is #3: a crew that has never
// written a memory note still has `crews/<id>/shared` and
// `crews/<id>/agents` on disk, because the docker provider's
// prepareCrewDirs creates them at container-creation time.
//
// Counting tar ENTRIES counts those directories, so `memory_included`
// came back true for a bundle containing no memory — the exact claim
// this whole change exists to end, arriving through an observed flag
// instead of a predicted one. Observing the wrong thing is not better
// than predicting; it is the same claim with a more convincing
// provenance.
func TestRepackResult_SkeletonDirsAreNotMemory(t *testing.T) {
	// Exactly what CopyFrom("/crew") returns for a provisioned crew that
	// has never been used: the wrapper dir and the two skeleton dirs.
	src := tarOf(t,
		dirEntry("crew/"),
		dirEntry("crew/shared/"),
		dirEntry("crew/agents/"),
	)

	var buf bytes.Buffer
	w, err := NewTarZstWriter(&buf)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	res, err := RepackTarWithExcludes(bytes.NewReader(src), w, "crew/eng", nil)
	if err != nil {
		t.Fatalf("repack: %v", err)
	}
	_ = w.Close()

	if res.Entries == 0 {
		t.Fatal("fixture produced no entries; the test would prove nothing")
	}
	if res.Files != 0 {
		t.Errorf("Files = %d for a directory-only section; empty directories are not content", res.Files)
	}
	if res.MemoryFiles != 0 {
		t.Errorf("MemoryFiles = %d for a crew that has never written a memory note", res.MemoryFiles)
	}

	summary := CrewSummary{Slug: "eng", MemoryIncluded: res.MemoryFiles > 0}
	if summary.HasCrewMemory(FormatVersion) {
		t.Error("memory_included is true for a bundle whose /crew section is the provider's skeleton and nothing else")
	}
}

// TestRepackResult_CountsMemoryFilesOnlyInsideMemoryDirs pins the other
// half: /crew legitimately carries content that is not memory, and that
// content must not be counted as memory either.
func TestRepackResult_CountsMemoryFilesOnlyInsideMemoryDirs(t *testing.T) {
	src := tarOf(t,
		dirEntry("crew/"),
		dirEntry("crew/shared/"),
		dirEntry("crew/shared/.memory/"),
		fileEntry("crew/shared/.memory/CREW.md", "charter\n"),
		fileEntry("crew/shared/.memory/topics/quarterly review/pins.md", "pinned\n"),
		dirEntry("crew/agents/"),
		dirEntry("crew/agents/alex/.memory/"),
		fileEntry("crew/agents/alex/.memory/AGENT.md", "identity\n"),
		// Crew-level content that is NOT memory.
		fileEntry("crew/init.sh", "#!/bin/sh\n"),
		// Deliberate near-misses: a component that merely contains the
		// string, and a file whose NAME looks like it.
		fileEntry("crew/shared/.memoryX/stray.md", "not memory\n"),
		fileEntry("crew/shared/notes.memory.md", "not memory\n"),
	)

	var buf bytes.Buffer
	w, err := NewTarZstWriter(&buf)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	res, err := RepackTarWithExcludes(bytes.NewReader(src), w, "crew/eng", nil)
	if err != nil {
		t.Fatalf("repack: %v", err)
	}
	_ = w.Close()

	if got, want := res.MemoryFiles, 3; got != want {
		t.Errorf("MemoryFiles = %d, want %d (CREW.md, the spaced topic's pins.md, AGENT.md — and nothing else)", got, want)
	}
	if got, want := res.Files, 6; got != want {
		t.Errorf("Files = %d, want %d (every non-directory entry)", got, want)
	}
	// The section has real content but its memory is what the flag is
	// about; both facts have to be representable at once.
	summary := CrewSummary{
		Slug:              "eng",
		MemoryIncluded:    res.MemoryFiles > 0,
		CrewFilesIncluded: res.Files > 0,
	}
	if !summary.HasCrewMemory(FormatVersion) {
		t.Error("a section with real memory files must report memory_included")
	}
	if !summary.HasFilesystemSections(FormatVersion) {
		t.Error("a section with real files must get a docker phase")
	}
}

// TestCrewSummary_SkeletonOnlyCrewNeedsNoContainer is the second-order
// effect the entry count caused: HasFilesystemSections went true for a
// skeleton-only crew, so a --files-only resume hard-failed with "crew
// has filesystem data in the bundle but container is not provisioned"
// for a crew that has none.
func TestCrewSummary_SkeletonOnlyCrewNeedsNoContainer(t *testing.T) {
	skeleton := CrewSummary{Slug: "eng"}
	if skeleton.HasFilesystemSections(FormatVersion) {
		t.Error("a crew whose bundle carries only provider-created directories must not demand a provisioned container")
	}
}

// tarOf builds an uncompressed tar from the given entries, in order.
func tarOf(t *testing.T, entries ...func(*tar.Writer)) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		e(tw)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close fixture tar: %v", err)
	}
	return buf.Bytes()
}

func dirEntry(name string) func(*tar.Writer) {
	return func(tw *tar.Writer) {
		_ = tw.WriteHeader(&tar.Header{
			Name: name, Typeflag: tar.TypeDir, Mode: 0o2775,
			ModTime: time.Unix(1785746753, 0), Uid: 1001, Gid: 1002,
		})
	}
}

func fileEntry(name, body string) func(*tar.Writer) {
	return func(tw *tar.Writer) {
		_ = tw.WriteHeader(&tar.Header{
			Name: name, Typeflag: tar.TypeReg, Mode: 0o2664,
			Size: int64(len(body)), ModTime: time.Unix(1785746753, 0),
			Uid: 1001, Gid: 1002,
		})
		_, _ = tw.Write([]byte(body))
	}
}

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
