package kindguide

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/documents"
)

func TestEveryKindDeclaresModesAndCapabilities(t *testing.T) {
	name := regexp.MustCompile(`^[a-z][A-Za-z0-9]*(\.[a-z][A-Za-z0-9]*)*$`)
	for _, g := range List() {
		if len(g.Modes) == 0 || g.Modes[0] != "ask" {
			t.Errorf("%s: modes %v must start with ask", g.Kind, g.Modes)
		}
		last := -1
		for _, m := range g.Modes {
			i := slices.Index(AgentModes, m)
			if i <= last {
				t.Errorf("%s: mode %q unknown or out of order", g.Kind, m)
			}
			last = i
		}
		if len(g.Capabilities.Supported) == 0 || len(g.Capabilities.Unsupported) == 0 {
			t.Errorf("%s: declare both supported and unsupported capabilities", g.Kind)
		}
		seen := map[string]bool{}
		for _, c := range append(append([]string{}, g.Capabilities.Supported...), g.Capabilities.Unsupported...) {
			if !name.MatchString(c) || seen[c] {
				t.Errorf("%s: capability %q invalid or listed twice", g.Kind, c)
			}
			seen[c] = true
		}
		// A mode the kind has no path for is declared unsupported, never silently absent.
		if !slices.Contains(g.Modes, "propose") && !slices.Contains(g.Capabilities.Unsupported, "propose") {
			t.Errorf("%s: no propose path but propose is not declared unsupported", g.Kind)
		}
		// Comment never implies Propose: comment must be its own mode.
		if !slices.Contains(g.Modes, "comment") {
			t.Errorf("%s: comment mode missing", g.Kind)
		}
	}
}

// The App declaration is the interactive runtime's own method list, so the
// agent contract cannot claim a method the broker does not serve.
func TestAppCapabilitiesMatchTheInteractiveRuntime(t *testing.T) {
	src, err := os.ReadFile("../../../lib/pages/interactive-artifact.ts")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`export const INTERACTIVE_METHODS = \[([^\]]*)\] as const`).FindSubmatch(src)
	if m == nil {
		t.Fatal("INTERACTIVE_METHODS not found")
	}
	var runtime []string
	for _, part := range strings.Split(string(m[1]), ",") {
		runtime = append(runtime, strings.Trim(strings.TrimSpace(part), `"`))
	}
	app, _ := Lookup("app")
	got := append([]string{}, app.Capabilities.Supported...)
	sort.Strings(got)
	sort.Strings(runtime)
	if !slices.Equal(got, runtime) {
		t.Fatalf("app supported %v, interactive runtime serves %v", got, runtime)
	}
	for _, c := range app.Capabilities.Supported {
		if !strings.Contains(app.Content, "`"+c+"`") {
			t.Errorf("app guide does not name supported method %q", c)
		}
	}
}

// Every Docs block the guide declares is accepted by the creation validator.
func TestDocsCapabilitiesMatchTheAuthoringValidator(t *testing.T) {
	g, _ := Lookup("docs")
	text, level := "Text", 1
	for _, c := range g.Capabilities.Supported {
		kind, ok := strings.CutPrefix(c, "blocks.")
		if !ok {
			continue
		}
		block := documents.AuthoringBlock{Type: kind, Text: &text}
		switch kind {
		case "heading":
			block.Level = &level
		case "bulletList", "orderedList":
			block.Text, block.Items = nil, []string{"Item"}
		}
		if err := documents.ValidateAuthoring([]documents.AuthoringBlock{block}); err != nil {
			t.Errorf("declared block %q refused by the validator: %v", kind, err)
		}
	}
	for _, refused := range []string{"table", "image", "embed"} {
		if err := documents.ValidateAuthoring([]documents.AuthoringBlock{{Type: refused, Text: &text}}); err == nil {
			t.Errorf("validator accepts %q that the guide does not declare", refused)
		}
	}
}

// Owner's decision (Q2): a Dashboard data write counts as Edit, a code change
// as Propose. The guide must say so, and the kind must declare both modes.
func TestDashboardDataWritesAreEdit(t *testing.T) {
	g, err := Lookup("dashboard")
	if err != nil {
		t.Fatal(err)
	}
	modes := strings.Join(g.Modes, ",")
	if !strings.Contains(modes, "propose") || !strings.Contains(modes, "edit") {
		t.Fatalf("dashboard modes %v", g.Modes)
	}
	flat := strings.Join(strings.Fields(g.Content), " ")
	for _, want := range []string{"Code changes are Propose", "Data writes change what readers see at once: they are Edit"} {
		if !strings.Contains(flat, want) {
			t.Errorf("dashboard guide lacks %q", want)
		}
	}
}

// Docs Propose exists on the proposal schema only (#5142). The declaration
// names that schema from the real constant, the limits come from the store,
// and the tool the guide names is the one the sidecar serves.
func TestDocsProposeMatchesTheProposalCode(t *testing.T) {
	g, _ := Lookup("docs")
	if !slices.Contains(g.Modes, "propose") || slices.Contains(g.Capabilities.Unsupported, "propose") {
		t.Fatalf("docs modes %v unsupported %v: Propose exists on schema %d", g.Modes, g.Capabilities.Unsupported, documents.ProposalSchema)
	}
	schema := fmt.Sprintf("propose.schema%d", documents.ProposalSchema)
	if !slices.Contains(g.Capabilities.Supported, schema) {
		t.Errorf("docs supported %v lacks %q", g.Capabilities.Supported, schema)
	}
	for v := 1; v < documents.ProposalSchema; v++ {
		if !slices.Contains(g.Capabilities.Unsupported, fmt.Sprintf("propose.schema%d", v)) {
			t.Errorf("docs does not declare propose unsupported on schema %d", v)
		}
	}
	flat := strings.Join(strings.Fields(g.Content), " ")
	for _, want := range []string{
		"doc_draft_propose",
		fmt.Sprintf("schema %d", documents.ProposalSchema),
		fmt.Sprintf("%d open proposals", documents.ProposalOpenLimit),
		fmt.Sprintf("%d stored", documents.ProposalLimit),
		"a person accepts or rejects",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("docs guide lacks %q", want)
		}
	}
	src, err := os.ReadFile("../../sidecar/documents_draft_mcp.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `{"doc_draft_propose",`) {
		t.Error("sidecar no longer serves doc_draft_propose")
	}
}
