package pages

import (
	"strings"
	"testing"
)

// Tabs — the authoring half (tabs.go).
//
// Three properties are pinned here, and each of them is a bug that would be
// invisible on the page rather than loud:
//
//   - The bar's ORDER and MEMBERSHIP, because they are derived rather than
//     declared. A panel that lands on the wrong tab is not an error anywhere;
//     it is a panel a reader stops finding.
//   - The refusals, because a tab HIDES panels. A blank tab name draws nothing
//     on the bar and still hides everything under it, and two names differing
//     only by case are two tabs a reader sees as one.
//   - That a page with no `tab` anywhere is untouched, which is the promise the
//     format was chosen for: adding a tab is one word, and adding none is
//     nothing at all.

// tabbedSpec is validSpec with a tab on each panel, plus a third panel.
const tabbedSpec = `
apiVersion: crewship/v1
kind: Page
metadata:
  name: Síť
  slug: sit
spec:
  panels:
    - id: dosah
      schema: status.v1
      owner: crew/lookout
      producer: script/ping-go
      sla: 30s
      tab: Síť
    - id: latence
      schema: metric.v1
      owner: crew/lookout
      producer: script/ping-go
      sla: 30s
      tab: Odezva
    - id: disk
      schema: metric.v1
      owner: crew/lookout
      producer: script/ping-go
      sla: 5m
      tab: Disk
`

// specWithTab is one panel carrying an arbitrary `tab:` scalar, for the
// refusal table. The value is inserted verbatim so a test can pass quoted YAML.
func specWithTab(tab string) string {
	return `
apiVersion: crewship/v1
kind: Page
metadata:
  name: Flotila .201
  slug: fleet-201
spec:
  panels:
    - id: sluzby
      schema: status.v1
      owner: crew/lookout
      producer: script/watch-services.sh
      sla: 30s
      tab: ` + tab + `
`
}

func TestParseDocument_TabRoundTrips(t *testing.T) {
	t.Parallel()

	doc, err := ParseDocument([]byte(tabbedSpec))
	if err != nil {
		t.Fatalf("a tabbed page was refused: %v", err)
	}
	want := []string{"Síť", "Odezva", "Disk"}
	for i, p := range doc.Spec.Panels {
		if p.Tab != want[i] {
			t.Errorf("panel %q tab = %q, want %q", p.ID, p.Tab, want[i])
		}
	}
}

func TestTabs_OrderIsFirstAppearance(t *testing.T) {
	t.Parallel()

	doc, err := ParseDocument([]byte(tabbedSpec))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	tabs := Tabs(doc.Spec.Panels)
	if len(tabs) != 3 {
		t.Fatalf("got %d tabs, want 3: %+v", len(tabs), tabs)
	}
	// The order the owner asked for: "bylo by třeba síť, pak by byla další
	// karta odezva, další karta disk" — which is the order the spec reads in,
	// because the spec is read top to bottom and the page is drawn the way it
	// reads (§9).
	for i, want := range []string{"Síť", "Odezva", "Disk"} {
		if tabs[i].Name != want {
			t.Errorf("tab %d = %q, want %q", i, tabs[i].Name, want)
		}
	}
}

func TestTabs_RepeatedTabKeepsItsFirstPosition(t *testing.T) {
	t.Parallel()

	panels := []PanelSpec{
		{ID: "a", Tab: "Síť"},
		{ID: "b", Tab: "Odezva"},
		{ID: "c", Tab: "Síť"},
	}
	tabs := Tabs(panels)
	if len(tabs) != 2 {
		t.Fatalf("got %d tabs, want 2 — a name declared twice is one tab: %+v", len(tabs), tabs)
	}
	if tabs[0].Name != "Síť" || tabs[1].Name != "Odezva" {
		t.Fatalf("bar order = %q, %q; want Síť, Odezva — first appearance, not last",
			tabs[0].Name, tabs[1].Name)
	}
	if got := strings.Join(tabs[0].PanelIDs, ","); got != "a,c" {
		t.Errorf("Síť holds %q, want a,c in spec order", got)
	}
}

func TestTabs_AnUntabbedPanelLandsOnTheFirstTab(t *testing.T) {
	t.Parallel()

	// The property that makes "adding a tab is one word" true: declaring a tab
	// on ONE panel of an existing page must produce a working page, not an
	// error and not a panel that vanished.
	panels := []PanelSpec{
		{ID: "sluzby"},
		{ID: "latence", Tab: "Odezva"},
		{ID: "disk", Tab: "Disk"},
		{ID: "pamet"},
	}
	tabs := Tabs(panels)
	if len(tabs) != 2 {
		t.Fatalf("got %d tabs, want 2 — an untabbed panel is not a tab of its own: %+v", len(tabs), tabs)
	}
	if got := strings.Join(tabs[0].PanelIDs, ","); got != "sluzby,latence,pamet" {
		t.Errorf("first tab holds %q, want sluzby,latence,pamet", got)
	}
	if got := strings.Join(tabs[1].PanelIDs, ","); got != "disk" {
		t.Errorf("second tab holds %q, want disk", got)
	}
}

func TestTabs_APageWithNoTabsHasNoBar(t *testing.T) {
	t.Parallel()

	doc, err := ParseDocument([]byte(validSpec))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	if tabs := Tabs(doc.Spec.Panels); tabs != nil {
		t.Fatalf("an untabbed page derived %d tabs; it must render exactly as it did before "+
			"tabs existed, with no bar at all: %+v", len(tabs), tabs)
	}
	for _, p := range doc.Spec.Panels {
		if p.Tab != "" {
			t.Errorf("panel %q gained a tab nobody declared: %q", p.ID, p.Tab)
		}
	}
}

func TestValidate_RefusesAnUnusableTabName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		tab  string
		want string
	}{
		{
			name: "blank",
			tab:  `"   "`,
			want: "blank tab",
		},
		{
			name: "a tab and nothing else",
			tab:  "\"\\t\"",
			want: "blank tab",
		},
		{
			name: "absurdly long",
			tab:  `"` + strings.Repeat("Odezva ", 10) + `"`,
			want: "the cap is 32",
		},
		{
			name: "carries a newline",
			tab:  `"Odezva\nDisk"`,
			want: "control character",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseDocument([]byte(specWithTab(tc.tab)))
			if err == nil {
				t.Fatalf("tab %s was accepted; a tab hides panels, so a name that cannot be "+
					"drawn hides them behind nothing", tc.tab)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal = %q, want it to say %q — an author reading this is fixing "+
					"their YAML", err, tc.want)
			}
			// And it names the panel, so the author knows which line to open.
			if !strings.Contains(err.Error(), "sluzby") {
				t.Errorf("refusal %q does not name the panel it refused", err)
			}
		})
	}
}

func TestValidate_RefusesTwoTabsThatDifferOnlyByCase(t *testing.T) {
	t.Parallel()

	panels := []PanelSpec{
		{ID: "a", Tab: "Odezva"},
		{ID: "b", Tab: "odezva"},
	}
	err := validatePageTabs(panels)
	if err == nil {
		t.Fatal("a bar with both Odezva and odezva on it was accepted; a reader sees one tab " +
			"and half the panels")
	}
	for _, want := range []string{"Odezva", "odezva", "differ only in case"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}
}

func TestValidate_TrimsButNeverFoldsATabName(t *testing.T) {
	t.Parallel()

	panels := []PanelSpec{{ID: "a", Tab: "  Odezva  "}}
	if err := validatePageTabs(panels); err != nil {
		t.Fatalf("validatePageTabs: %v", err)
	}
	if panels[0].Tab != "Odezva" {
		t.Errorf("tab = %q, want %q — what validates is what gets stored", panels[0].Tab, "Odezva")
	}
}

func TestValidate_RefusesMoreTabsThanTheBarCanHold(t *testing.T) {
	t.Parallel()

	panels := make([]PanelSpec, 0, MaxTabsPerPage+1)
	for i := 0; i <= MaxTabsPerPage; i++ {
		panels = append(panels, PanelSpec{ID: "p", Tab: string(rune('a' + i))})
	}
	err := validatePageTabs(panels)
	if err == nil {
		t.Fatalf("%d tabs were accepted; the cap is %d", MaxTabsPerPage+1, MaxTabsPerPage)
	}
	if !strings.Contains(err.Error(), "the cap is 8") {
		t.Errorf("refusal %q does not quote the cap", err)
	}

	// And exactly at the cap it is fine — an off-by-one here refuses a page
	// that is inside the documented limit.
	if err := validatePageTabs(panels[:MaxTabsPerPage]); err != nil {
		t.Errorf("%d tabs were refused at the cap: %v", MaxTabsPerPage, err)
	}
}
