package artifacts

import (
	"math"
	"reflect"
	"testing"
)

func designFixture() (DesignScene, DesignLimits) {
	return DesignScene{StartArtboard: "home", Artboards: []DesignArtboard{
		{ID: "home", Name: "Úvod", Width: 800, Height: 600, Elements: []DesignElement{
			{ID: "group", Kind: "group", Width: 400, Height: 300},
			{ID: "title", ParentID: "group", Kind: "text", Text: "Dlouhý český nadpis", Width: 300, Height: 50},
			{ID: "next", Kind: "rectangle", Width: 100, Height: 40},
			{ID: "photo-group", Kind: "group", Width: 120, Height: 120},
			{ID: "photo", ParentID: "photo-group", Kind: "image", AssetID: "png", Width: 100, Height: 100},
			{ID: "dot", Kind: "ellipse", StyleID: "accent", Width: 10, Height: 10},
		}},
		{ID: "details", Name: "Detail", Width: 800, Height: 600},
		{ID: "done", Name: "Hotovo", Width: 800, Height: 600},
	}, Assets: []string{"png", "font"}, Styles: []DesignStyle{{ID: "accent", Fill: "#123456", Stroke: "#abcdef88", FontAssetID: "font", FontSize: 14, StrokeWidth: 1}}, Links: []DesignLink{{ElementID: "next", TargetArtboard: "details"}, {ElementID: "title", TargetArtboard: "removed"}}}, DesignLimits{
		Artboards: 10, Elements: 100, Assets: 10, Styles: 10, Depth: 10, TextBytes: 10000, Coordinate: 10000,
	}
}

func TestDesignDeclarativePrototype(t *testing.T) {
	scene, limits := designFixture()
	want, _ := designFixture()
	diagnostics, err := ValidateDesign(scene, limits)
	if err != nil || len(diagnostics) != 1 || diagnostics[0].ElementID != "title" {
		t.Fatalf("dangling link must remain explicit: %v %v", diagnostics, err)
	}
	prototype, err := NewDesignPrototype(scene, limits)
	if err != nil {
		t.Fatal(err)
	}
	if prototype.Current() != "home" {
		t.Fatal("missing explicit start")
	}
	if err := prototype.Activate("title"); err == nil || prototype.Current() != "home" {
		t.Fatal("broken link navigated")
	}
	if err := prototype.Activate("next"); err != nil || prototype.Current() != "details" {
		t.Fatalf("navigation: %v", err)
	}
	if err := prototype.Activate("next"); err == nil {
		t.Fatal("inactive artboard link accepted")
	}
	prototype.Reset()
	if prototype.Current() != "home" || !reflect.DeepEqual(scene, want) {
		t.Fatal("prototype changed source")
	}
	// The navigator captures its graph; subsequent editor state is independent.
	scene.Links[0].TargetArtboard = "done"
	if err := prototype.Activate("next"); err != nil || prototype.Current() != "details" {
		t.Fatal("source change rewired pinned prototype")
	}
}

func TestDesignRejectsInvalidScenes(t *testing.T) {
	for name, change := range map[string]func(*DesignScene, *DesignLimits){
		"duplicate identity":  func(s *DesignScene, _ *DesignLimits) { s.Artboards[1].ID = "title" },
		"missing start":       func(s *DesignScene, _ *DesignLimits) { s.StartArtboard = "missing" },
		"script":              func(s *DesignScene, _ *DesignLimits) { s.Artboards[0].Elements[0].Kind = "script" },
		"unknown parent":      func(s *DesignScene, _ *DesignLimits) { s.Artboards[0].Elements[1].ParentID = "unknown" },
		"cycle":               func(s *DesignScene, _ *DesignLimits) { s.Artboards[0].Elements[0].ParentID = "group" },
		"non group parent":    func(s *DesignScene, _ *DesignLimits) { s.Artboards[0].Elements[0].ParentID = "title" },
		"nonfinite":           func(s *DesignScene, _ *DesignLimits) { s.Artboards[0].Width = math.NaN() },
		"negative extent":     func(s *DesignScene, _ *DesignLimits) { s.Artboards[0].Elements[0].Width = -1 },
		"oversize geometry":   func(s *DesignScene, _ *DesignLimits) { s.Artboards[0].Elements[0].X = 10001 },
		"element quota":       func(_ *DesignScene, l *DesignLimits) { l.Elements = 1 },
		"artboard quota":      func(_ *DesignScene, l *DesignLimits) { l.Artboards = 1 },
		"asset quota":         func(_ *DesignScene, l *DesignLimits) { l.Assets = 1 },
		"depth quota":         func(_ *DesignScene, l *DesignLimits) { l.Depth = 1 },
		"text quota":          func(_ *DesignScene, l *DesignLimits) { l.TextBytes = 1 },
		"unset limits":        func(_ *DesignScene, l *DesignLimits) { *l = DesignLimits{} },
		"missing style":       func(s *DesignScene, _ *DesignLimits) { s.Artboards[0].Elements[1].StyleID = "missing" },
		"unsafe style":        func(s *DesignScene, _ *DesignLimits) { s.Styles[0].Fill = "url(https://example.com)" },
		"missing font":        func(s *DesignScene, _ *DesignLimits) { s.Styles[0].FontAssetID = "unknown" },
		"missing image":       func(s *DesignScene, _ *DesignLimits) { s.Artboards[0].Elements[1].Kind = "image" },
		"path asset":          func(s *DesignScene, _ *DesignLimits) { s.Assets = []string{"/etc/passwd"} },
		"duplicate asset":     func(s *DesignScene, _ *DesignLimits) { s.Assets[1] = "png" },
		"invalid utf8":        func(s *DesignScene, _ *DesignLimits) { s.Artboards[0].Name = "\xff" },
		"unknown link source": func(s *DesignScene, _ *DesignLimits) { s.Links[0].ElementID = "missing" },
		"duplicate link":      func(s *DesignScene, _ *DesignLimits) { s.Links = append(s.Links, s.Links[0]) },
		"external link":       func(s *DesignScene, _ *DesignLimits) { s.Links[0].TargetArtboard = "https://example.com" },
	} {
		t.Run(name, func(t *testing.T) {
			scene, limits := designFixture()
			change(&scene, &limits)
			if _, err := ValidateDesign(scene, limits); err == nil {
				t.Fatal("invalid scene accepted")
			}
			if _, err := NewDesignPrototype(scene, limits); err == nil {
				t.Fatal("invalid prototype accepted")
			}
		})
	}
}
