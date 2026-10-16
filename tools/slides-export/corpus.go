package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/slides"
)

func corpus() exportInput {
	text := func(id, value string, x, y, w, h, size int, font string) slides.Element {
		return slides.Element{ID: id, Type: "text", X: x, Y: y, Width: w, Height: h, Text: &slides.Text{Value: value, FontFamily: font, FontSize: size, Color: "#112233", Align: "left"}}
	}
	shape := func(id, kind string, x, y, w, h int) slides.Element {
		return slides.Element{ID: id, Type: "shape", X: x, Y: y, Width: w, Height: h, Shape: &slides.Shape{Kind: kind, Fill: "#ddeeff", Stroke: "#334455", StrokeWidth: 2}}
	}
	deck := slides.Deck{SchemaVersion: 2, DeckID: "s2_corpus_page", Width: 960, Height: 540, Theme: slides.Theme{Background: "#ffffff", Color: "#112233", FontFamily: "DejaVu Sans", FontSize: 24}, SlideOrder: []string{"czech", "long_text", "table", "raster", "font_fallback"}, Slides: []slides.Slide{
		{ID: "czech", Notes: "Česká poznámka\nSkutečný druhý řádek", Elements: []slides.Element{text("czech_title", "Příliš žluťoučký kůň úpěl ďábelské ódy\nDruhý řádek českého textu", 32, 30, 880, 110, 28, "DejaVu Sans"), shape("czech_rectangle", "rectangle", 40, 190, 320, 120), shape("czech_ellipse", "ellipse", 450, 190, 220, 120)}},
		{ID: "long_text", Notes: "Dlouhý odstavec se zachovanými LF", Elements: []slides.Element{text("long_body", strings.Repeat("Český dlouhý odstavec s přesnou editovatelnou sazbou. ", 30)+"\nZávěrečný řádek.", 32, 32, 896, 470, 16, "DejaVu Sans")}},
		{ID: "table", Notes: "Jednoduchá tabulka musí zůstat nativní", Elements: []slides.Element{{ID: "native_table", Type: "table", X: 48, Y: 48, Width: 840, Height: 300, Table: &slides.Table{Cells: [][]string{{"Město", "Rozpočet", "Poznámka"}, {"Plzeň", "120 000 Kč", "První řádek\nDruhý řádek"}, {"Brno", "98 000 Kč", "Bez přetečení"}, {"Žďár", "77 000 Kč", "České znaky"}}, FontFamily: "DejaVu Sans", FontSize: 18, Color: "#112233", Fill: "#f4f8ff", Border: "#334455"}}}},
		{ID: "raster", Notes: "Soukromý lokální PNG, žádná vzdálená adresa", Elements: []slides.Element{text("raster_title", "Ověřený lokální PNG", 32, 30, 880, 70, 26, "DejaVu Sans")}},
		{ID: "font_fallback", Notes: "Chybějící profil fontu je viditelný v reportu", Elements: []slides.Element{text("missing_font", "Český text s výslovně popsanou náhradou fontu.", 32, 40, 880, 160, 26, "Missing Export Font"), shape("fallback_shape", "rectangle", 48, 250, 400, 100)}},
	}}
	bitmap := image.NewNRGBA(image.Rect(0, 0, 96, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 96; x++ {
			bitmap.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 2), G: uint8(y * 3), B: 150, A: 255})
		}
	}
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, bitmap); err != nil {
		panic(err)
	}
	raw := imageBytes.Bytes()
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])
	id := "s2_private_png"
	deck.Slides[3].Elements = append(deck.Slides[3].Elements, slides.Element{ID: "private_png", Type: "image", X: 150, Y: 150, Width: 600, Height: 300, Image: &slides.Image{AssetID: id, ExpectedSHA256: sha, Alt: "Barevný testovací PNG"}})
	deck.Slides[4].Elements[1].Shape.StrokeWidth = 0
	hash, err := slides.DeckHash(deck)
	if err != nil {
		panic(err)
	}
	return exportInput{SchemaVersion: 1, Revision: 7, ContentHash: hash, Deck: deck, Assets: []exportAsset{{AssetID: id, ExpectedSHA256: sha, MIME: "image/png", Width: 96, Height: 64, Data: base64.StdEncoding.EncodeToString(raw)}}}
}
func writeCorpus(directory string) error {
	if err := os.Mkdir(directory, 0700); err != nil {
		return err
	}
	for _, kind := range []string{"supported", "overflow", "missing_glyph", "wide_glyph", "near_boundary"} {
		value := corpus()
		if kind == "overflow" {
			value.Deck.Slides[1].Elements[0].Height = 32
		}
		if kind == "near_boundary" {
			value.Deck.Slides[1].Elements[0].Height = 380
		}
		if kind == "wide_glyph" {
			value.Deck.Slides[0].Elements[0].Text.Value = "W"
			value.Deck.Slides[0].Elements[0].Text.FontSize = 160
			value.Deck.Slides[0].Elements[0].Width = 1
			value.Deck.Slides[0].Elements[0].Height = 300
		}
		if kind == "missing_glyph" {
			value.Deck.Slides[0].Elements[0].Text.Value = "Chybějící znak: \U0010ffff"
		}
		hash, err := slides.DeckHash(value.Deck)
		if err != nil {
			return err
		}
		value.ContentHash = hash
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(directory, kind+".json"), append(data, '\n'), 0600); err != nil {
			return err
		}
	}
	return nil
}
