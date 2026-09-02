package journalgen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func parseSrc(t *testing.T, src string) (*token.FileSet, []EntryConst) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "src.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := scanFile(fset, file)
	if err != nil {
		t.Fatalf("scanFile: %v", err)
	}
	return fset, out
}

func values(consts []EntryConst) []string {
	out := make([]string, len(consts))
	for i, c := range consts {
		out[i] = c.Value
	}
	sort.Strings(out)
	return out
}

func TestScanFile_UnqualifiedTypesGoShape(t *testing.T) {
	src := `package journal

const (
	EntryFoo EntryType = "foo.happened"
	EntryBar EntryType = "bar.happened"
)
`
	_, out := parseSrc(t, src)
	if got := values(out); len(got) != 2 || got[0] != "bar.happened" || got[1] != "foo.happened" {
		t.Fatalf("got %v, want [bar.happened foo.happened]", got)
	}
	for _, c := range out {
		if !c.InJournalPkg {
			t.Errorf("%q: InJournalPkg = false, want true (declared inside package journal)", c.Value)
		}
		if c.Name == "" {
			t.Errorf("%q: Name is empty, want a Go identifier", c.Value)
		}
	}
}

func TestScanFile_QualifiedTypedConst(t *testing.T) {
	// pages_transfer_owner.go / onboarding_proposal.go's shape.
	src := `package api

import "github.com/crewship-ai/crewship/internal/journal"

const entryPageOwnerTransferred journal.EntryType = "page.owner_transferred"
`
	_, out := parseSrc(t, src)
	if got := values(out); len(got) != 1 || got[0] != "page.owner_transferred" {
		t.Fatalf("got %v, want [page.owner_transferred]", got)
	}
	if out[0].InJournalPkg {
		t.Error("InJournalPkg = true for a const declared outside package journal")
	}
	if out[0].Name != "entryPageOwnerTransferred" {
		t.Errorf("Name = %q, want entryPageOwnerTransferred", out[0].Name)
	}
}

func TestScanFile_ConversionCallInConstBlock(t *testing.T) {
	// pages_webhooks.go / pages_public_tokens.go's shape.
	src := `package api

import "github.com/crewship-ai/crewship/internal/journal"

const (
	journalPageWebhookIssued  = journal.EntryType("page.webhook_issued")
	journalPageWebhookRevoked = journal.EntryType("page.webhook_revoked")
)
`
	_, out := parseSrc(t, src)
	// scanFile itself does not dedupe (ScanTree does, across files) — the
	// generic conversion-call walk and the named-spec pass both match this
	// shape, so each value legitimately appears twice here: once named, once
	// not.
	names := map[string]string{}
	for _, c := range out {
		if c.Name != "" {
			names[c.Value] = c.Name
		} else if _, ok := names[c.Value]; !ok {
			names[c.Value] = ""
		}
	}
	if len(names) != 2 {
		t.Fatalf("got %d distinct values %v, want 2", len(names), names)
	}
	if names["page.webhook_issued"] != "journalPageWebhookIssued" {
		t.Errorf("name for page.webhook_issued = %q, want journalPageWebhookIssued", names["page.webhook_issued"])
	}
	if names["page.webhook_revoked"] != "journalPageWebhookRevoked" {
		t.Errorf("name for page.webhook_revoked = %q, want journalPageWebhookRevoked", names["page.webhook_revoked"])
	}
}

func TestScanFile_InlineConversionAtEmitSite(t *testing.T) {
	// harbormaster/reward.go / assignments_stuck_sweeper.go's shape: a
	// conversion call with no name at all, nested inside a composite
	// literal deep in a function body.
	src := `package harbormaster

import "github.com/crewship-ai/crewship/internal/journal"

func emit(j journal.Emitter) {
	j.Emit(nil, journal.Entry{
		Type: journal.EntryType("keeper.rule_auto_tuned"),
	})
}
`
	_, out := parseSrc(t, src)
	if got := values(out); len(got) != 1 || got[0] != "keeper.rule_auto_tuned" {
		t.Fatalf("got %v, want [keeper.rule_auto_tuned]", got)
	}
	if out[0].Name != "" {
		t.Errorf("Name = %q, want empty (no binding for an inline conversion)", out[0].Name)
	}
}

func TestScanFile_BareLiteralIsInvisible(t *testing.T) {
	// The one shape the scanner cannot see, by design (see the package doc
	// comment): a bare string literal assigned to a field typed
	// journal.EntryType, with no EntryType token anywhere in the expression.
	// This test pins that it stays invisible rather than being silently
	// "fixed" by a change that would reintroduce the false-positive rate
	// documented on ScanTree — any fix for this shape belongs at the call
	// site (a typed const), not in the scanner.
	src := `package api

import "github.com/crewship-ai/crewship/internal/journal"

func emit(j journal.Emitter) {
	j.Emit(nil, journal.Entry{
		Type: "policy.changed",
	})
}
`
	_, out := parseSrc(t, src)
	if len(out) != 0 {
		t.Fatalf("got %v, want no matches for a bare string literal", out)
	}
}

func TestScanFile_ImplicitRepeatIsAnError(t *testing.T) {
	src := `package journal

const (
	EntryFoo EntryType = "foo.happened"
	EntryBar
)
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "src.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, err = scanFile(fset, file)
	if err == nil {
		t.Fatal("scanFile returned no error for an implicit-repeat EntryType spec, want an error")
	}
	if !strings.Contains(err.Error(), "EntryBar") {
		t.Errorf("error %q does not name the offending constant", err.Error())
	}
}

func TestScanFile_LengthMismatchIsAnError(t *testing.T) {
	// A grouped multi-name spec where the number of names and values differ
	// is not a shape types.go uses, but if it appeared it must fail loud,
	// not vanish.
	src := `package journal

const A, B EntryType = "a.happened", "b.happened", "c.happened"
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "src.go", src, parser.ParseComments)
	if err == nil {
		// The snippet above is not even valid Go (mismatched arity is a
		// parse-time concern for some shapes); if it parsed, scanning it
		// must still error.
		if _, serr := scanFile(fset, file); serr == nil {
			t.Fatal("scanFile returned no error for a name/value length mismatch, want an error")
		}
	}
}

func TestScanFile_NonLiteralValueIsAnError(t *testing.T) {
	src := `package journal

var seed = "not.a.literal"

const EntryFoo EntryType = seed
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "src.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, err = scanFile(fset, file)
	if err == nil {
		t.Fatal("scanFile returned no error for an EntryType constant initialised from a non-literal, want an error")
	}
}

func TestScanTree_SkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	prod := `package pkg

import "github.com/crewship-ai/crewship/internal/journal"

const entryReal journal.EntryType = "pkg.real"
`
	test := `package pkg

import "github.com/crewship-ai/crewship/internal/journal"

const entryFromTest journal.EntryType = "pkg.from_test_file_should_be_ignored"

var _ = journal.EntryType("pkg.inline_from_test_file_should_be_ignored")
`
	if err := os.WriteFile(filepath.Join(dir, "internal", "pkg", "real.go"), []byte(prod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "pkg", "real_test.go"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := ScanTree(dir, "internal")
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	got := values(out)
	if len(got) != 1 || got[0] != "pkg.real" {
		t.Fatalf("got %v, want only [pkg.real] — _test.go files must be skipped", got)
	}
}

func TestScanTree_DedupePrefersJournalPackageName(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "journal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	journalSrc := `package journal

const EntryShared EntryType = "shared.value"
`
	otherSrc := `package other

import "github.com/crewship-ai/crewship/internal/journal"

const entrySharedAlias journal.EntryType = "shared.value"
`
	if err := os.WriteFile(filepath.Join(dir, "internal", "journal", "types.go"), []byte(journalSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "other", "other.go"), []byte(otherSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := ScanTree(dir, "internal")
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d entries, want 1 (deduped by value): %v", len(out), out)
	}
	if out[0].Name != "EntryShared" || !out[0].InJournalPkg {
		t.Errorf("got Name=%q InJournalPkg=%v, want the package-journal declaration to win the dedupe",
			out[0].Name, out[0].InJournalPkg)
	}
}
