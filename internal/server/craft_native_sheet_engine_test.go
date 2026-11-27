package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/craftsession"
)

// Without a pinned engine there is no session to open: a Sheet without a CLI
// pin never gets a manager, so nothing else can be started by accident.
func TestSheetSessionsNeedAPinnedEngine(t *testing.T) {
	s := &sheetSessions{}
	if s.manager(goapi.CraftNativeDocumentProfile{Application: "gridcraft"}) != nil {
		t.Fatal("a manager was created without a pinned engine")
	}
}

// One manager per pinned engine, reused; Close empties the set, ends every
// session and may be called again.
func TestSheetSessionsOneManagerPerPinAndCloseIsBounded(t *testing.T) {
	s := &sheetSessions{}
	p := goapi.CraftNativeDocumentProfile{Application: "gridcraft", CLIPath: "/nonexistent/engine", CLISHA256: "00"}
	first := s.manager(p)
	if first == nil || s.manager(p) != first {
		t.Fatal("the same pin did not reuse its manager")
	}
	other := s.manager(goapi.CraftNativeDocumentProfile{Application: "gridcraft", CLIPath: "/nonexistent/other", CLISHA256: "11"})
	if other == nil || other == first {
		t.Fatal("a different pin shared a manager")
	}
	if !s.closeWithin(5 * time.Second) {
		t.Fatal("close did not finish in time")
	}
	if len(s.managers) != 0 || !s.closeWithin(time.Second) {
		t.Fatal("close was not idempotent")
	}
	var _ *craftsession.Manager = first
}

// Every agent change of a Sheet, a chat run's or a native Craft task's, goes
// through a persistent session (one open, one recalculation per batch); a Word
// document and a Sheet without an engine keep the one-shot adapter, whose own
// refusal answers here (no engine).
func TestAgentEngineUsesSessionsForEverySheetAgent(t *testing.T) {
	commands := json.RawMessage(`[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]`)
	for name, ctx := range map[string]context.Context{"task": context.Background()} {
		s := &sheetSessions{}
		apply, inspect := nativeCraftAgentEngine(s)
		if _, err := apply(ctx, goapi.CraftNativeDocumentProfile{Application: "gridcraft"}, []byte(`{}`), commands); err == nil {
			t.Fatalf("%s: applied without an engine", name)
		}
		if _, err := inspect(ctx, goapi.CraftNativeDocumentProfile{Application: "wordcraft", CLIPath: "/nonexistent", CLISHA256: "00"}, []byte(`{}`), json.RawMessage(`{"command":"text","params":{}}`)); err == nil {
			t.Fatalf("%s: inspected Word without an engine", name)
		}
		if len(s.managers) != 0 {
			t.Fatalf("%s: a session manager without a Sheet engine", name)
		}
		pinned := goapi.CraftNativeDocumentProfile{Application: "gridcraft", CLIPath: "/nonexistent/engine", CLISHA256: "00"}
		_, _ = apply(ctx, pinned, []byte(`{}`), commands)
		if len(s.managers) != 1 || len(s.adapters) != 1 {
			t.Fatalf("%s: the Sheet change did not go through a session (managers %d, adapters %d)", name, len(s.managers), len(s.adapters))
		}
		s.Close()
	}
}

// An installed package carries its engine as a verified module, not a pinned
// executable path: a chat run's Sheet still gets a persistent session (and so
// one recalculation per batch), from the module written once to a private file.
func TestSheetSessionsServeAnInstalledPackage(t *testing.T) {
	module := []byte("\x00asm synthetic module")
	sum := sha256.Sum256(module)
	loads := 0
	p := goapi.CraftNativeDocumentProfile{Application: "gridcraft", CLISHA256: hex.EncodeToString(sum[:]),
		EngineModule: &goapi.EngineModuleSource{Load: func() ([]byte, error) { loads++; return module, nil }}}
	s := &sheetSessions{}
	t.Cleanup(s.Close)
	m := s.manager(p)
	if m == nil {
		t.Fatal("an installed package has no Sheet session manager")
	}
	if s.manager(p) != m || loads != 1 {
		t.Fatalf("the module is not written once (loads %d)", loads)
	}
	if s.manager(goapi.CraftNativeDocumentProfile{Application: "gridcraft", CLISHA256: p.CLISHA256}) != m {
		t.Fatal("the same engine digest got another manager")
	}
	if s.manager(goapi.CraftNativeDocumentProfile{Application: "gridcraft"}) != nil {
		t.Fatal("a profile without an engine got a manager")
	}
}

// An import tells the Sheet sessions it was computed; a profile without an
// engine and a Word profile are ignored (no manager is started for them).
func TestSheetSessionsRecordAComputedDocument(t *testing.T) {
	s := &sheetSessions{}
	t.Cleanup(s.Close)
	s.computed(goapi.CraftNativeDocumentProfile{Application: "gridcraft"}, []byte("{}"))
	s.computed(goapi.CraftNativeDocumentProfile{Application: "wordcraft", CLIPath: "/nonexistent", CLISHA256: "00"}, []byte("{}"))
	if len(s.managers) != 0 {
		t.Fatalf("managers started: %d", len(s.managers))
	}
	p := goapi.CraftNativeDocumentProfile{Application: "gridcraft", CLIPath: "/nonexistent/engine", CLISHA256: "00"}
	s.computed(p, []byte("{}"))
	if s.manager(p) == nil {
		t.Fatal("no manager for the computed document's engine")
	}
	var nilSessions *sheetSessions
	nilSessions.computed(p, nil)
}

// Only bytes a manager recorded as computed by its own engine are vouched for:
// a browser's bytes, a Word document and no sessions at all are not.
func TestSheetSessionsTrustOnlyTheirComputedDocuments(t *testing.T) {
	s := &sheetSessions{}
	t.Cleanup(s.Close)
	p := goapi.CraftNativeDocumentProfile{Application: "gridcraft", CLIPath: "/nonexistent/engine", CLISHA256: "00"}
	s.computed(p, []byte(`{"a":1}`))
	if s.trusted(p, []byte(`{"a":2}`)) {
		t.Fatal("bytes the server never computed are trusted")
	}
	// The engine "00" keeps no saved values, so even its computed bytes are
	// recalculated.
	if s.trusted(p, []byte(`{"a":1}`)) {
		t.Fatal("an engine without saved values is trusted")
	}
	if s.trusted(goapi.CraftNativeDocumentProfile{Application: "wordcraft", CLIPath: "/x", CLISHA256: "00"}, []byte(`{"a":1}`)) {
		t.Fatal("a Word document is trusted")
	}
	var none *sheetSessions
	if none.trusted(p, []byte(`{"a":1}`)) {
		t.Fatal("no sessions trust a document")
	}
}

// With the measured engine the server vouches for exactly the bytes its own
// session or import recorded, and for nothing else.
func TestSheetSessionsTrustTheirComputedDocumentsWithAMeasuredEngine(t *testing.T) {
	s := &sheetSessions{}
	t.Cleanup(s.Close)
	p := goapi.CraftNativeDocumentProfile{Application: "gridcraft", CLIPath: "/nonexistent/engine", CLISHA256: "52acf14ac2a4c7386a7333f9d18e548ac24b089c02afd47c71ace7e1540d0bc3"}
	s.computed(p, []byte(`{"a":1}`))
	if !s.trusted(p, []byte(`{"a":1}`)) {
		t.Fatal("the server's own computed document is not trusted")
	}
	if s.trusted(p, []byte(`{"a":1} `)) || s.trusted(p, []byte(`{"a":2}`)) {
		t.Fatal("bytes the server never computed are trusted")
	}
	other := p
	other.CLISHA256 = "00"
	if s.trusted(other, []byte(`{"a":1}`)) {
		t.Fatal("another engine's manager trusts this one's document")
	}
}
