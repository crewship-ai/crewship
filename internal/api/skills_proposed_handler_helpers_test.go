package api

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/skills"
)

// ---------------------------------------------------------------------------
// skills_proposed_handler.go — SetImporter + mapDirError + safeStagingFileName
// edge cases.
//
// The List / Approve / Reject HTTP handlers are partially covered;
// these fill the 2 zero-coverage support helpers (`SetImporter` and
// `mapDirError`) that handlers route through.
// ---------------------------------------------------------------------------

func newProposedHandlerForHelperTest(t *testing.T) *SkillProposedHandler {
	t.Helper()
	db := setupTestDB(t)
	return NewSkillProposedHandler(db, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1})))
}

// ---- SetImporter ----

func TestSetImporter_StoresNonNilImporter(t *testing.T) {
	h := newProposedHandlerForHelperTest(t)
	// The constructor wires a default importer; capture it so we can
	// verify SetImporter actually replaces it.
	defaultImp := h.importer
	if defaultImp == nil {
		t.Fatal("constructor did not wire a default importer")
	}

	custom := skills.NewImporter(h.db, slog.Default())
	h.SetImporter(custom)
	if h.importer != custom {
		t.Errorf("importer = %p, want %p (SetImporter must replace the default)", h.importer, custom)
	}
}

func TestSetImporter_NilArg_PreservesPriorImporter(t *testing.T) {
	// Source guard: `if imp != nil`. nil arg must NOT clobber the
	// existing importer — the production caller routes through here
	// during router setup and a nil leak would silently disable
	// the staging-skill import path.
	h := newProposedHandlerForHelperTest(t)
	original := h.importer
	if original == nil {
		t.Fatal("constructor did not wire a default importer")
	}
	h.SetImporter(nil)
	if h.importer != original {
		t.Errorf("nil SetImporter clobbered importer; want preservation of %p, got %p", original, h.importer)
	}
}

// ---- mapDirError ----

func TestMapDirError_ErrNotExist_404(t *testing.T) {
	// proposedDirForCrew returns os.ErrNotExist when the crew row is
	// missing (or its slug doesn't materialize on disk). mapDirError
	// must surface that as a 404 — the inbox UI relies on the status
	// code to render a "no proposals" empty state vs. an error toast.
	h := newProposedHandlerForHelperTest(t)
	rr := httptest.NewRecorder()
	h.mapDirError(rr, os.ErrNotExist)
	if rr.Code != 404 {
		t.Errorf("status = %d, want 404", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "crew not found") {
		t.Errorf("body = %q, want \"crew not found\"", rr.Body.String())
	}
}

func TestMapDirError_WrappedErrNotExist_404(t *testing.T) {
	// errors.Is unwraps — a wrapped os.ErrNotExist must also surface
	// as 404 (production wraps via fmt.Errorf("...: %w", os.ErrNotExist)).
	h := newProposedHandlerForHelperTest(t)
	rr := httptest.NewRecorder()
	wrapped := &os.PathError{Op: "stat", Path: "/missing", Err: fs.ErrNotExist}
	h.mapDirError(rr, wrapped)
	if rr.Code != 404 {
		t.Errorf("status = %d, want 404 (wrapped ErrNotExist)", rr.Code)
	}
}

func TestMapDirError_NotConfigured_503(t *testing.T) {
	// The "crew memory root not configured" string literal is the
	// signal proposedDirForCrew uses when SetCrewMemoryRoot was never
	// called. Pin the 503 mapping AND the exact body so an operator
	// triaging "why is the proposals tab empty" sees the right hint.
	h := newProposedHandlerForHelperTest(t)
	rr := httptest.NewRecorder()
	h.mapDirError(rr, errors.New("crew memory root not configured"))
	if rr.Code != 503 {
		t.Errorf("status = %d, want 503 (server-side setup gap)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "not configured") {
		t.Errorf("body = %q, want \"not configured\" hint", rr.Body.String())
	}
}

func TestMapDirError_GenericError_500WithGenericBody(t *testing.T) {
	// Any other error → 500 with a GENERIC body. Source comment is
	// explicit: "Raw DB / filesystem errors leak internals (table
	// names, paths, OS-level errno strings) that an attacker can use
	// to probe the deployment." Pin that the raw error message does
	// NOT appear in the response.
	h := newProposedHandlerForHelperTest(t)
	rr := httptest.NewRecorder()
	rawErr := errors.New("table 'crews' is missing column 'topics_root' at /var/lib/crewship/db.sqlite")
	h.mapDirError(rr, rawErr)
	if rr.Code != 500 {
		t.Errorf("status = %d, want 500", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "table 'crews'") || strings.Contains(rr.Body.String(), "/var/lib") {
		t.Errorf("response body leaked raw internals: %q", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "internal server error") {
		t.Errorf("body = %q, want generic \"internal server error\"", rr.Body.String())
	}
}

// ---- safeStagingFileName edge cases (already 80% covered; lock in
//      the path-escape rejection branch that's the security-critical one) ----

func TestSafeStagingFileName_RejectionTable(t *testing.T) {
	// The handler walks under filepath.Join({crewDir}, name); a
	// successful path escape here = arbitrary filesystem read. Pin
	// every rejection branch.
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"slash-traversal", "../skill-x.md", false},
		{"backslash-traversal", "..\\skill-x.md", false},
		{"slash-inside", "subdir/skill-x.md", false},
		{"dotdot-anywhere", "skill-x..md", false},
		{"missing-prefix", "other-file.md", false},
		{"missing-suffix", "skill-x.txt", false},
		{"valid-simple", "skill-foo.md", true},
		{"valid-with-dash", "skill-my-cool-name.md", true},
		{"valid-with-suffix-num", "skill-foo-2.md", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeStagingFileName(tc.in); got != tc.want {
				t.Errorf("safeStagingFileName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
