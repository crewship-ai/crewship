// Package slides owns the bounded typed Slides authority. It has no renderer,
// author-supplied code, image transport or writable HTML mirror.
package slides

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	SchemaVersion         = 1
	ImageSchemaVersion    = 2
	MaxImageAltBytes      = 2048
	MaxImageAssets        = 32
	MaxImageAssetBytes    = 32 << 20
	MaxImageAssetPixels   = 32 * 1024 * 1024
	ImageHashDomain       = "crewship-slides-canonical-json/v2\n"
	MaxDeckBytes          = 2 << 20
	MaxSlides             = 100
	MaxElements           = 1000
	MaxTextBytes          = 32 << 10
	MaxNotesBytes         = 16 << 10
	MaxTableCellsPerDeck  = 10000
	MaxTableCellsPerSlide = 2000
	HashDomain            = "crewship-slides-canonical-json/v1\n"
)

var ErrSchema = errors.New("unsupported Slides schema")
var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var color = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Theme struct {
	Background string `json:"background"`
	Color      string `json:"color"`
	FontFamily string `json:"font_family"`
	FontSize   int    `json:"font_size"`
}

type Deck struct {
	SchemaVersion int      `json:"schema_version"`
	DeckID        string   `json:"deck_id"`
	Width         int      `json:"width"`
	Height        int      `json:"height"`
	Theme         Theme    `json:"theme"`
	SlideOrder    []string `json:"slide_order"`
	Slides        []Slide  `json:"slides"`
}
type Slide struct {
	ID       string    `json:"id"`
	Notes    string    `json:"notes"`
	Elements []Element `json:"elements"`
}
type Element struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Text   *Text  `json:"text,omitempty"`
	Shape  *Shape `json:"shape,omitempty"`
	Table  *Table `json:"table,omitempty"`
	Image  *Image `json:"image,omitempty"`
}
type Image struct {
	AssetID        string `json:"asset_id"`
	ExpectedSHA256 string `json:"expected_sha256"`
	Alt            string `json:"alt"`
}

func SupportedSchema(version int) bool {
	return version == SchemaVersion || version == ImageSchemaVersion
}

type Text struct {
	Value      string `json:"value"`
	FontFamily string `json:"font_family"`
	FontSize   int    `json:"font_size"`
	Color      string `json:"color"`
	Align      string `json:"align"`
}
type Shape struct {
	Kind        string `json:"kind"`
	Fill        string `json:"fill"`
	Stroke      string `json:"stroke"`
	StrokeWidth int    `json:"stroke_width"`
}
type Table struct {
	Cells      [][]string `json:"cells"`
	FontFamily string     `json:"font_family"`
	FontSize   int        `json:"font_size"`
	Color      string     `json:"color"`
	Fill       string     `json:"fill"`
	Border     string     `json:"border"`
}

// Decode rejects oversized, duplicate-key, unknown-field and trailing input.
// Duplicate keys are rejected before encoding/json could silently overwrite.
func Decode(data []byte) (Deck, error) {
	var deck Deck
	if len(data) > MaxDeckBytes || !utf8.Valid(data) {
		return deck, errors.New("invalid Slides byte budget or UTF-8")
	}
	if err := validJSONUnicode(data); err != nil {
		return deck, err
	}
	if err := uniqueKeys(data); err != nil {
		return deck, err
	}
	if err := requiredModelFields(data); err != nil {
		return deck, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&deck); err != nil {
		return deck, fmt.Errorf("Slides JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return deck, errors.New("trailing Slides JSON")
	}
	if err := Validate(deck); err != nil {
		return deck, err
	}
	return deck, nil
}
func uniqueKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var visit func(int) error
	visit = func(depth int) error {
		if depth > 32 {
			return errors.New("Slides JSON nesting exceeds 32")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate Slides JSON key")
				}
				seen[name] = true
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case json.Delim('['):
			for decoder.More() {
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		}
		return nil
	}
	if err := visit(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing Slides JSON")
	}
	return nil
}
func validText(value string, limit int) bool {
	if !utf8.ValidString(value) || len(value) > limit {
		return false
	}
	for _, r := range value {
		if r == '\r' || (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return false
		}
	}
	return true
}
func validFont(family string, size int) bool {
	return validText(family, 128) && strings.TrimSpace(family) != "" && !strings.ContainsAny(family, "\n\t") && size >= 1 && size <= 160
}
func Validate(deck Deck) error {
	if !SupportedSchema(deck.SchemaVersion) {
		return ErrSchema
	}
	if !identifier.MatchString(deck.DeckID) || deck.Width < 64 || deck.Width > 4096 || deck.Height < 64 || deck.Height > 4096 || deck.Slides == nil || deck.SlideOrder == nil || len(deck.Slides) < 1 || len(deck.Slides) > MaxSlides || len(deck.SlideOrder) != len(deck.Slides) {
		return errors.New("invalid Slides deck structure")
	}
	if !color.MatchString(deck.Theme.Background) || !color.MatchString(deck.Theme.Color) || !validFont(deck.Theme.FontFamily, deck.Theme.FontSize) {
		return errors.New("invalid deck theme")
	}
	ids := map[string]bool{}
	slides := map[string]bool{}
	count := 0
	tableCells := 0
	images := map[string]string{}
	for _, slide := range deck.Slides {
		slideCells := 0
		if !identifier.MatchString(slide.ID) || ids[slide.ID] || slide.Elements == nil || !validText(slide.Notes, MaxNotesBytes) {
			return errors.New("invalid slide identity, notes or elements")
		}
		ids[slide.ID] = true
		slides[slide.ID] = true
		for _, element := range slide.Elements {
			count++
			if count > MaxElements || !identifier.MatchString(element.ID) || ids[element.ID] {
				return errors.New("invalid or duplicate element identity")
			}
			ids[element.ID] = true
			if element.X < 0 || element.Y < 0 || element.Width < 1 || element.Height < 1 || element.Width > deck.Width || element.Height > deck.Height || element.X > deck.Width-element.Width || element.Y > deck.Height-element.Height {
				return errors.New("element outside bounded deck geometry")
			}
			switch element.Type {
			case "text":
				v := element.Text
				if v == nil || element.Shape != nil || element.Table != nil || element.Image != nil || !validText(v.Value, MaxTextBytes) || !validFont(v.FontFamily, v.FontSize) || !color.MatchString(v.Color) || (v.Align != "left" && v.Align != "center" && v.Align != "right") {
					return errors.New("invalid text element")
				}
			case "shape":
				v := element.Shape
				if v == nil || element.Text != nil || element.Table != nil || element.Image != nil || (v.Kind != "rectangle" && v.Kind != "ellipse") || !color.MatchString(v.Fill) || !color.MatchString(v.Stroke) || v.StrokeWidth < 0 || v.StrokeWidth > 16 {
					return errors.New("invalid shape element")
				}
			case "table":
				v := element.Table
				if v == nil || element.Text != nil || element.Shape != nil || element.Image != nil || len(v.Cells) < 1 || len(v.Cells) > 30 || len(v.Cells[0]) < 1 || len(v.Cells[0]) > 20 || !validFont(v.FontFamily, v.FontSize) || !color.MatchString(v.Color) || !color.MatchString(v.Fill) || !color.MatchString(v.Border) {
					return errors.New("invalid table element")
				}
				for _, row := range v.Cells {
					if len(row) != len(v.Cells[0]) {
						return errors.New("nonrectangular table")
					}
					for _, cell := range row {
						slideCells++
						tableCells++
						if slideCells > MaxTableCellsPerSlide {
							return errors.New("slide exceeds 2000 total table cells")
						}
						if tableCells > MaxTableCellsPerDeck {
							return errors.New("deck exceeds 10000 total table cells")
						}
						if !validText(cell, 4096) {
							return errors.New("invalid table cell")
						}
					}
				}
			case "image":
				v := element.Image
				if deck.SchemaVersion != ImageSchemaVersion || v == nil || element.Text != nil || element.Shape != nil || element.Table != nil || !identifier.MatchString(v.AssetID) || !hashPattern.MatchString(v.ExpectedSHA256) || !validText(v.Alt, MaxImageAltBytes) {
					return errors.New("invalid schema-2 image element")
				}
				if sha, exists := images[v.AssetID]; exists && sha != v.ExpectedSHA256 {
					return errors.New("conflicting image asset identity")
				}
				images[v.AssetID] = v.ExpectedSHA256
				if len(images) > MaxImageAssets {
					return errors.New("too many unique image assets")
				}
			default:
				return errors.New("unsupported element type")
			}
		}
	}
	for _, id := range deck.SlideOrder {
		if !slides[id] {
			return errors.New("slide order is not a unique complete permutation")
		}
		delete(slides, id)
	}
	encoded, err := Canonical(deck)
	if err != nil {
		return err
	}
	if len(encoded) > MaxDeckBytes {
		return errors.New("Slides deck exceeds byte budget")
	}
	return nil
}

// Canonical bytes are UTF-8 JSON with ASCII object keys sorted, integer numbers,
// no HTML escaping or Unicode normalization, and slides sorted by stable ID.
// slide_order alone owns slide position. Element array order owns paint order.
// Strings retain code points (including supplementary chars); no UTF-16 offsets
// occur in this foundation. Hash has a separate versioned domain prefix.
func Canonical(deck Deck) ([]byte, error) {
	deck.Slides = append([]Slide{}, deck.Slides...)
	sort.Slice(deck.Slides, func(i, j int) bool { return deck.Slides[i].ID < deck.Slides[j].ID })
	return canonicalValue(deck)
}
func canonicalValue(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var generic any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&generic); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(generic); err != nil {
		return nil, err
	}
	data := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	return data, nil
}
func hashDomainBytes(domain string, data []byte) string {
	hash := sha256.Sum256(append([]byte(domain), data...))
	return hex.EncodeToString(hash[:])
}
func hashBytes(data []byte) string { return hashDomainBytes(HashDomain, data) }

func DeckHash(deck Deck) (string, error) {
	data, err := Canonical(deck)
	if err != nil {
		return "", err
	}
	if deck.SchemaVersion == ImageSchemaVersion {
		return hashDomainBytes(ImageHashDomain, data), nil
	}
	return hashBytes(data), nil
}
func ElementHash(element Element) (string, error) {
	data, err := canonicalValue(element)
	if err != nil {
		return "", err
	}
	if element.Type == "image" {
		return hashDomainBytes(ImageHashDomain, data), nil
	}
	return hashBytes(data), nil
}

// Go's JSON decoder replaces lone UTF-16 surrogates. Reject them at admission
// instead so an agent never hashes silently normalized text.
func validJSONUnicode(data []byte) error {
	for i := 0; i < len(data); i++ {
		if data[i] != '"' {
			continue
		}
		i++
		for i < len(data) && data[i] != '"' {
			if data[i] != '\\' {
				i++
				continue
			}
			i++
			if i >= len(data) {
				return errors.New("invalid JSON string")
			}
			if data[i] != 'u' {
				i++
				continue
			}
			if i+4 >= len(data) {
				return errors.New("invalid Unicode escape")
			}
			value, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
			if err != nil {
				return err
			}
			i += 5
			if value >= 0xD800 && value <= 0xDBFF {
				if i+5 >= len(data) || data[i] != '\\' || data[i+1] != 'u' {
					return errors.New("unpaired Unicode surrogate")
				}
				low, err := strconv.ParseUint(string(data[i+2:i+6]), 16, 16)
				if err != nil || low < 0xDC00 || low > 0xDFFF {
					return errors.New("unpaired Unicode surrogate")
				}
				i += 6
			} else if value >= 0xDC00 && value <= 0xDFFF {
				return errors.New("unpaired Unicode surrogate")
			}
		}
	}
	return nil
}
func requiredObject(raw json.RawMessage, fields ...string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("required Slides object")
	}
	if len(object) != len(fields) {
		return nil, errors.New("Slides object contains unknown or incorrectly cased fields")
	}
	for _, field := range fields {
		value, ok := object[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("required Slides field %s", field)
		}
	}
	return object, nil
}
func requiredModelFields(raw []byte) error {
	deck, err := requiredObject(raw, "schema_version", "deck_id", "width", "height", "slide_order", "slides", "theme")
	if err != nil {
		return err
	}
	if _, err := requiredObject(deck["theme"], "background", "color", "font_family", "font_size"); err != nil {
		return err
	}
	var slides []json.RawMessage
	if err = json.Unmarshal(deck["slides"], &slides); err != nil {
		return err
	}
	for _, rawSlide := range slides {
		slide, err := requiredObject(rawSlide, "id", "notes", "elements")
		if err != nil {
			return err
		}
		var elements []json.RawMessage
		if err = json.Unmarshal(slide["elements"], &elements); err != nil {
			return err
		}
		for _, rawElement := range elements {
			var element map[string]json.RawMessage
			if err = json.Unmarshal(rawElement, &element); err != nil {
				return err
			}
			var kind string
			if err = json.Unmarshal(element["type"], &kind); err != nil {
				return err
			}
			fields := map[string][]string{"text": {"value", "font_family", "font_size", "color", "align"}, "shape": {"kind", "fill", "stroke", "stroke_width"}, "table": {"cells", "font_family", "font_size", "color", "fill", "border"}, "image": {"asset_id", "expected_sha256", "alt"}}[kind]
			if fields == nil {
				return errors.New("unsupported element type")
			}
			if _, err = requiredObject(rawElement, "id", "type", "x", "y", "width", "height", kind); err != nil {
				return err
			}
			payload, err := requiredObject(element[kind], fields...)
			if err != nil {
				return err
			}
			if kind == "table" {
				var cells [][]json.RawMessage
				if err = json.Unmarshal(payload["cells"], &cells); err != nil {
					return err
				}
				for _, row := range cells {
					for _, cell := range row {
						trimmed := bytes.TrimSpace(cell)
						if len(trimmed) == 0 || trimmed[0] != '"' {
							return errors.New("table cells must be JSON strings")
						}
					}
				}
			}
		}
	}
	return nil
}

// ImageReferences returns unique asset identities from an already validated deck.
// The transaction hook must still authorize every reference and verify bytes.
func ImageReferences(deck Deck) map[string]string {
	refs := map[string]string{}
	for _, slide := range deck.Slides {
		for _, element := range slide.Elements {
			if element.Type == "image" && element.Image != nil {
				refs[element.Image.AssetID] = element.Image.ExpectedSHA256
			}
		}
	}
	return refs
}
