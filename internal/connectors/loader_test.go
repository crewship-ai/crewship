package connectors_test

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/crewship-ai/crewship/internal/connectors"
)

// -------------------------------------------------------------------
// LoadAll on the shipped FixturesFS — the four canonical manifests
// must all parse + validate.
// -------------------------------------------------------------------

func TestLoadAll_ShippedFixtures(t *testing.T) {
	cat, errs := connectors.LoadAll(connectors.FixturesFS)
	if len(errs) != 0 {
		t.Fatalf("expected no load errors, got %d: %v", len(errs), errs)
	}
	wantIDs := []string{"linear", "github", "slack", "postgres", "everything", "filesystem"}
	if cat.Len() != len(wantIDs) {
		t.Errorf("len = %d, want %d (%v)", cat.Len(), len(wantIDs), wantIDs)
	}
	for _, id := range wantIDs {
		t.Run(id, func(t *testing.T) {
			m, err := cat.LoadByID(id)
			if err != nil {
				t.Fatalf("LoadByID(%q): %v", id, err)
			}
			if m.ID != id {
				t.Errorf("got id %q", m.ID)
			}
		})
	}
}

func TestLoadByID_NotFound(t *testing.T) {
	cat, _ := connectors.LoadAll(connectors.FixturesFS)
	_, err := cat.LoadByID("does-not-exist")
	if !errors.Is(err, connectors.ErrConnectorNotFound) {
		t.Errorf("err = %v, want ErrConnectorNotFound", err)
	}
}

func TestList_StableOrder(t *testing.T) {
	cat, _ := connectors.LoadAll(connectors.FixturesFS)
	a := cat.List()
	b := cat.List()
	if len(a) != len(b) {
		t.Fatalf("list lengths differ")
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Errorf("non-stable order at %d: %q vs %q", i, a[i].ID, b[i].ID)
		}
	}
}

// -------------------------------------------------------------------
// LoadAll on a synthetic fstest.MapFS — partial load semantics.
// -------------------------------------------------------------------

func TestLoadAll_PartialFailureSkipsBadManifest(t *testing.T) {
	good := []byte(`
id: good
name: Good
auth_mode: pat
fields:
  - {key: token, label: Token, type: password, required: true}
mcp:
  transport: stdio
  command: foo
  env:
    TOK: "${field.token}"
`)
	bad := []byte(`
id: bad
name: Bad
# unknown auth_mode → Validate will fail
auth_mode: not_a_thing
mcp:
  transport: stdio
  command: foo
`)
	memFS := fstest.MapFS{
		"fixtures/good.yaml": &fstest.MapFile{Data: good},
		"fixtures/bad.yaml":  &fstest.MapFile{Data: bad},
	}
	cat, errs := connectors.LoadAll(memFS)
	if len(errs) != 1 {
		t.Errorf("expected 1 load error, got %d: %v", len(errs), errs)
	}
	if cat.Len() != 1 {
		t.Errorf("expected 1 valid manifest, got %d", cat.Len())
	}
	m, err := cat.LoadByID("good")
	if err != nil {
		t.Fatalf("good not loaded: %v", err)
	}
	if m.AuthMode != connectors.AuthModePAT {
		t.Errorf("good auth_mode = %q", m.AuthMode)
	}
	if _, err := cat.LoadByID("bad"); !errors.Is(err, connectors.ErrConnectorNotFound) {
		t.Errorf("bad must not load: err = %v", err)
	}
}

func TestLoadAll_RejectsDuplicateIDs(t *testing.T) {
	dupA := []byte(`id: dup
name: A
auth_mode: none
mcp: {transport: streamable-http, endpoint: https://x}
`)
	dupB := []byte(`id: dup
name: B
auth_mode: none
mcp: {transport: streamable-http, endpoint: https://y}
`)
	memFS := fstest.MapFS{
		"fixtures/a.yaml": &fstest.MapFile{Data: dupA},
		"fixtures/b.yaml": &fstest.MapFile{Data: dupB},
	}
	_, errs := connectors.LoadAll(memFS)
	if len(errs) == 0 {
		t.Fatal("expected duplicate-id error")
	}
	hasDupErr := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "duplicate") || strings.Contains(e.Error(), "dup") {
			hasDupErr = true
			break
		}
	}
	if !hasDupErr {
		t.Errorf("errors did not mention duplicate: %v", errs)
	}
}

func TestLoadAll_IgnoresNonYAMLFiles(t *testing.T) {
	good := []byte(`id: good
name: Good
auth_mode: none
brand: {logo: x, color: "#000000"}
mcp: {transport: streamable-http, endpoint: https://x}
`)
	memFS := fstest.MapFS{
		"fixtures/good.yaml": &fstest.MapFile{Data: good},
		"fixtures/README.md": &fstest.MapFile{Data: []byte("ignore me")},
		"fixtures/notes.txt": &fstest.MapFile{Data: []byte("ignore me")},
		"fixtures/.DS_Store": &fstest.MapFile{Data: []byte{0}},
	}
	cat, errs := connectors.LoadAll(memFS)
	if len(errs) != 0 {
		t.Errorf("expected no errors (non-yaml ignored), got %v", errs)
	}
	if cat.Len() != 1 {
		t.Errorf("len = %d, want 1", cat.Len())
	}
}

func TestLoadAll_IgnoresSubdirectories(t *testing.T) {
	// The embed glob is "fixtures/*.yaml" — flat. If a future PR drops
	// a fixture under fixtures/vendors/foo.yaml, it must NOT be loaded
	// silently (the embed wouldn't even include it). This test pins
	// the contract: nested yaml files are skipped, not errored.
	good := []byte(`id: top
name: Top
auth_mode: none
brand: {logo: x, color: "#000000"}
mcp: {transport: streamable-http, endpoint: https://x}
`)
	nested := []byte(`id: nested
name: Nested
auth_mode: none
brand: {logo: x, color: "#000000"}
mcp: {transport: streamable-http, endpoint: https://x}
`)
	memFS := fstest.MapFS{
		"fixtures/top.yaml":            &fstest.MapFile{Data: good},
		"fixtures/vendors/nested.yaml": &fstest.MapFile{Data: nested},
	}
	cat, _ := connectors.LoadAll(memFS)
	if cat.Len() != 1 {
		t.Errorf("len = %d, want 1 (nested yaml must be ignored)", cat.Len())
	}
	if _, err := cat.LoadByID("nested"); !errors.Is(err, connectors.ErrConnectorNotFound) {
		t.Errorf("nested fixture must not load: err = %v", err)
	}
}
