package slides

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func richFixture() Deck {
	return Deck{SchemaVersion: RichSchemaVersion, DeckID: "rich", Width: 1920, Height: 1080,
		Theme: Theme{Background: "#FFFFFF", Color: "#111111", FontFamily: "Go Regular", FontSize: 32}, SlideOrder: []string{"s"},
		Slides: []Slide{{ID: "s", Transition: "fade", Elements: []Element{
			{ID: "t", Type: "text", X: 0, Y: 0, Width: 1000, Height: 300, Text: &Text{Value: "Crewship", FontFamily: "IBM Plex Sans", FontSize: 240, Color: "#111111", Align: "left", FontWeight: 700, LetterSpacing: 4, LineHeight: 105}},
			{ID: "r", Type: "shape", X: 0, Y: 400, Width: 400, Height: 200, Shape: &Shape{Kind: "rectangle", Fill: "#16304F", Stroke: "#16304F", CornerRadius: 24, Gradient: &Gradient{Angle: 160, Stops: []GradientStop{{"#16304F", 0}, {"#0F1F33", 70}}}}},
			{ID: "l", Type: "shape", X: 500, Y: 400, Width: 96, Height: 24, Shape: &Shape{Kind: "line", Fill: "#E8833A", Stroke: "#E8833A", StrokeWidth: 4, Line: &Line{X1: 0, Y1: 12, X2: 96, Y2: 12, Start: "none", End: "triangle"}}},
			{ID: "i", Type: "icon", X: 700, Y: 400, Width: 72, Height: 72, Icon: &Icon{Name: "users", Color: "#E8833A"}},
			{ID: "m", Type: "table", X: 0, Y: 700, Width: 400, Height: 100, Table: &Table{Cells: [][]string{{"a"}}, FontFamily: "JetBrains Mono", FontSize: 24, Color: "#111111", Fill: "#FFFFFF", Border: "#000000"}},
		}}}}
}

func TestValidateProjectionRichProfile(t *testing.T) {
	if err := ValidateProjection(richFixture()); err != nil {
		t.Fatalf("rich fixture refused: %v", err)
	}
	el := func(d *Deck, id string) *Element {
		for i := range d.Slides[0].Elements {
			if d.Slides[0].Elements[i].ID == id {
				return &d.Slides[0].Elements[i]
			}
		}
		panic(id)
	}
	tests := []struct {
		name   string
		mutate func(*Deck)
		ok     bool
	}{
		{"max rich font size", func(d *Deck) { el(d, "t").Text.FontSize = MaxRichFontSize }, true},
		{"font size above rich max", func(d *Deck) { el(d, "t").Text.FontSize = MaxRichFontSize + 1 }, false},
		{"unknown family", func(d *Deck) { el(d, "t").Text.FontFamily = "Arial" }, false},
		{"family case", func(d *Deck) { el(d, "t").Text.FontFamily = "ibm plex sans" }, false},
		{"theme family outside allow-list", func(d *Deck) { d.Theme.FontFamily = "Comic Sans" }, false},
		{"weight missing for family", func(d *Deck) { el(d, "t").Text.FontFamily = "JetBrains Mono" }, false},
		{"semibold mono", func(d *Deck) { el(d, "t").Text.FontFamily = "JetBrains Mono"; el(d, "t").Text.FontWeight = 600 }, true},
		{"go regular bold", func(d *Deck) { el(d, "t").Text.FontFamily = "Go Regular" }, false},
		{"weight 500", func(d *Deck) { el(d, "t").Text.FontWeight = 500 }, false},
		{"explicit weight 400", func(d *Deck) { el(d, "t").Text.FontWeight = 400 }, true},
		{"letter spacing min", func(d *Deck) { el(d, "t").Text.LetterSpacing = MinLetterSpacing }, true},
		{"letter spacing below", func(d *Deck) { el(d, "t").Text.LetterSpacing = MinLetterSpacing - 1 }, false},
		{"letter spacing above", func(d *Deck) { el(d, "t").Text.LetterSpacing = MaxLetterSpacing + 1 }, false},
		{"negative spacing at half size", func(d *Deck) { el(d, "t").Text.FontSize = 20; el(d, "t").Text.LetterSpacing = -10 }, true},
		{"negative spacing beyond half size", func(d *Deck) { el(d, "t").Text.FontSize = 20; el(d, "t").Text.LetterSpacing = -11 }, false},
		{"line height below", func(d *Deck) { el(d, "t").Text.LineHeight = MinLineHeight - 1 }, false},
		{"line height above", func(d *Deck) { el(d, "t").Text.LineHeight = MaxLineHeight + 1 }, false},
		{"radius half of short side", func(d *Deck) { el(d, "r").Shape.CornerRadius = 100 }, true},
		{"radius above half", func(d *Deck) { el(d, "r").Shape.CornerRadius = 101 }, false},
		{"negative radius", func(d *Deck) { el(d, "r").Shape.CornerRadius = -1 }, false},
		{"ellipse radius", func(d *Deck) { el(d, "r").Shape.Kind = "ellipse" }, false},
		{"ellipse gradient", func(d *Deck) { el(d, "r").Shape.Kind = "ellipse"; el(d, "r").Shape.CornerRadius = 0 }, true},
		{"one stop", func(d *Deck) { g := el(d, "r").Shape.Gradient; g.Stops = g.Stops[:1] }, false},
		{"nine stops", func(d *Deck) {
			g := el(d, "r").Shape.Gradient
			for len(g.Stops) < 9 {
				g.Stops = append(g.Stops, GradientStop{"#000000", 100})
			}
		}, false},
		{"stop colour name", func(d *Deck) { el(d, "r").Shape.Gradient.Stops[0].Color = "red" }, false},
		{"stop colour css injection", func(d *Deck) { el(d, "r").Shape.Gradient.Stops[0].Color = "#000000);x:url(a" }, false},
		{"stops decreasing", func(d *Deck) { el(d, "r").Shape.Gradient.Stops[0].Position = 80 }, false},
		{"stop above 100", func(d *Deck) { el(d, "r").Shape.Gradient.Stops[1].Position = 101 }, false},
		{"angle 360", func(d *Deck) { el(d, "r").Shape.Gradient.Angle = 360 }, false},
		{"negative angle", func(d *Deck) { el(d, "r").Shape.Gradient.Angle = -1 }, false},
		{"line without points", func(d *Deck) { el(d, "l").Shape.Line = nil }, false},
		{"line point outside box", func(d *Deck) { el(d, "l").Shape.Line.X2 = 97 }, false},
		{"line zero length", func(d *Deck) { l := el(d, "l").Shape.Line; l.X2, l.Y2 = l.X1, l.Y1 }, false},
		{"line zero stroke", func(d *Deck) { el(d, "l").Shape.StrokeWidth = 0 }, false},
		{"line fill differs", func(d *Deck) { el(d, "l").Shape.Fill = "#000000" }, false},
		{"line gradient", func(d *Deck) { el(d, "l").Shape.Gradient = el(d, "r").Shape.Gradient }, false},
		{"unknown arrow", func(d *Deck) { el(d, "l").Shape.Line.End = "diamond" }, false},
		{"empty arrow", func(d *Deck) { el(d, "l").Shape.Line.Start = "" }, false},
		{"rectangle with line", func(d *Deck) { el(d, "r").Shape.Line = el(d, "l").Shape.Line }, false},
		{"unknown kind", func(d *Deck) { el(d, "r").Shape.Kind = "star" }, false},
		{"unknown icon", func(d *Deck) { el(d, "i").Icon.Name = "skull-and-bones" }, false},
		{"icon path traversal", func(d *Deck) { el(d, "i").Icon.Name = "../users" }, false},
		{"icon colour", func(d *Deck) { el(d, "i").Icon.Color = "currentColor" }, false},
		{"icon missing payload", func(d *Deck) { el(d, "i").Icon = nil }, false},
		{"icon with text", func(d *Deck) { el(d, "i").Text = el(d, "t").Text }, false},
		{"text with icon", func(d *Deck) { el(d, "t").Icon = &Icon{Name: "users", Color: "#000000"} }, false},
		{"push transition", func(d *Deck) { d.Slides[0].Transition = "push" }, true},
		{"unknown transition", func(d *Deck) { d.Slides[0].Transition = "spin" }, false},
		{"table family outside allow-list", func(d *Deck) { el(d, "m").Table.FontFamily = "Arial" }, false},
		{"image in schema 3", func(d *Deck) {
			*el(d, "i") = Element{ID: "i", Type: "image", X: 0, Y: 0, Width: 10, Height: 10, Image: &Image{AssetID: "a", ExpectedSHA256: strings.Repeat("a", 64)}}
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := richFixture()
			tc.mutate(&d)
			if err := ValidateProjection(d); (err == nil) != tc.ok {
				t.Fatalf("ValidateProjection = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

// Rich fields never leak into the stored schemas, and the stored authority
// never admits schema 3.
func TestRichFieldsStayOutOfStoredSchemas(t *testing.T) {
	if err := Validate(richFixture()); !errors.Is(err, ErrSchema) {
		t.Fatalf("Validate(schema 3) = %v, want ErrSchema", err)
	}
	base, _, _ := modelFixture(t)
	if err := ValidateProjection(base); err != nil {
		t.Fatalf("schema-1 fixture refused by projection: %v", err)
	}
	two := base
	two.SchemaVersion = ImageSchemaVersion
	if err := ValidateProjection(two); !errors.Is(err, ErrSchema) {
		t.Fatalf("ValidateProjection(schema 2) = %v, want ErrSchema", err)
	}
	firstText := func(d *Deck) *Text {
		for i := range d.Slides {
			for j := range d.Slides[i].Elements {
				if d.Slides[i].Elements[j].Text != nil {
					return d.Slides[i].Elements[j].Text
				}
			}
		}
		panic("no text")
	}
	firstShape := func(d *Deck) *Shape {
		for i := range d.Slides {
			for j := range d.Slides[i].Elements {
				if d.Slides[i].Elements[j].Shape != nil {
					return d.Slides[i].Elements[j].Shape
				}
			}
		}
		panic("no shape")
	}
	mutations := map[string]func(*Deck){
		"weight":      func(d *Deck) { firstText(d).FontWeight = 700 },
		"spacing":     func(d *Deck) { firstText(d).LetterSpacing = 2 },
		"line height": func(d *Deck) { firstText(d).LineHeight = 140 },
		"font size":   func(d *Deck) { firstText(d).FontSize = 200 },
		"radius":      func(d *Deck) { firstShape(d).CornerRadius = 2 },
		"gradient": func(d *Deck) {
			firstShape(d).Gradient = &Gradient{Angle: 90, Stops: []GradientStop{{"#000000", 0}, {"#FFFFFF", 100}}}
		},
		"line kind":  func(d *Deck) { firstShape(d).Kind = "line" },
		"transition": func(d *Deck) { d.Slides[0].Transition = "fade" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			d, _, _ := modelFixture(t)
			mutate(&d)
			if err := Validate(d); err == nil {
				t.Error("stored-schema Validate admitted a schema-3 field")
			}
			if err := ValidateProjection(d); err == nil {
				t.Error("schema-1 projection admitted a schema-3 field")
			}
		})
	}
}

// The native strict decoder keeps refusing rich JSON keys, and a schema-1 deck
// still encodes to the same canonical bytes (the fixture test pins the hash).
func TestNativeDecodeRefusesRichJSON(t *testing.T) {
	deck, canonical, _ := modelFixture(t)
	raw, _ := json.Marshal(deck)
	if strings.Contains(string(raw), "font_weight") || strings.Contains(string(raw), "transition") || strings.Contains(string(raw), "corner_radius") {
		t.Fatalf("schema-1 JSON carries a rich key: %s", raw)
	}
	encoded, _ := Canonical(deck)
	if string(encoded) != canonical {
		t.Fatal("schema-1 canonical bytes changed")
	}
	source := string(raw)
	for name, mutated := range map[string]string{
		"weight":     strings.Replace(source, `"font_size":28`, `"font_size":28,"font_weight":700`, 1),
		"transition": strings.Replace(source, `"notes":`, `"transition":"fade","notes":`, 1),
		"schema 3":   strings.Replace(source, `"schema_version":1`, `"schema_version":3`, 1),
	} {
		if mutated == source {
			t.Fatalf("%s: mutation did not apply", name)
		}
		if _, err := Decode([]byte(mutated)); err == nil {
			t.Errorf("%s: native Decode admitted it", name)
		}
	}
	rich, _ := json.Marshal(richFixture())
	if _, err := Decode(rich); err == nil {
		t.Error("native Decode admitted a schema-3 deck")
	}
}

func TestRichHashDomain(t *testing.T) {
	d := richFixture()
	hash, err := DeckHash(d)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := Canonical(d)
	if hash != hashDomainBytes(RichHashDomain, data) || hash == hashBytes(data) {
		t.Fatal("schema-3 hash must use its own domain")
	}
}

func TestEmbeddedIconSet(t *testing.T) {
	names := IconNames()
	if len(names) < 40 {
		t.Fatalf("%d icons", len(names))
	}
	for _, name := range []string{"users", "message-circle", "play", "send"} {
		if _, ok := IconNodes(name); !ok {
			t.Errorf("%s missing", name)
		}
	}
	bad := map[string]string{
		"script tag":    `{"view_box":24,"stroke_width":2,"icons":{"a":[["script",{"d":"M0 0"}]]}}`,
		"event handler": `{"view_box":24,"stroke_width":2,"icons":{"a":[["path",{"onload":"x"}]]}}`,
		"url in value":  `{"view_box":24,"stroke_width":2,"icons":{"a":[["path",{"d":"url(#x)"}]]}}`,
		"href":          `{"view_box":24,"stroke_width":2,"icons":{"a":[["path",{"href":"x"}]]}}`,
		"bad name":      `{"view_box":24,"stroke_width":2,"icons":{"a/b":[["path",{"d":"M0 0"}]]}}`,
		"viewbox":       `{"view_box":32,"stroke_width":2,"icons":{"a":[["path",{"d":"M0 0"}]]}}`,
	}
	for name, raw := range bad {
		if _, err := parseIcons([]byte(raw)); err == nil {
			t.Errorf("%s: parseIcons admitted it", name)
		}
	}
}
