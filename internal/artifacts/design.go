package artifacts

import (
	"fmt"
	"math"
	"regexp"
	"unicode/utf8"
)

// DesignScene is an in-memory declarative scene, not a wire/storage schema.
// Slice order defines artboard and sibling layer order. Identity, ACL, revision,
// viewport and editor selection belong to the host, outside the scene.
type DesignScene struct {
	StartArtboard string
	Artboards     []DesignArtboard
	Styles        []DesignStyle
	Assets        []string
	Links         []DesignLink
}

type DesignArtboard struct {
	ID, Name      string
	Width, Height float64
	Elements      []DesignElement
}

// ParentID identifies a group on the same artboard; empty means the artboard.
// Assets and fonts are opaque references. Validation does not authorize reads:
// the host must resolve each reference under the current viewer's authority.
type DesignElement struct {
	ID, ParentID, Kind     string
	X, Y, Width, Height    float64
	Text, StyleID, AssetID string
}

type DesignStyle struct {
	ID, Fill, Stroke, FontAssetID string
	FontSize, StrokeWidth         float64
}

type DesignLink struct{ ElementID, TargetArtboard string }

type DesignDiagnostic struct{ ElementID, TargetArtboard, Reason string }

// DesignLimits must be supplied by a qualified host profile. Zero is not
// unlimited. This package does not invent production capacity guarantees.
type DesignLimits struct {
	Artboards, Elements, Assets, Styles, Depth, TextBytes int
	Coordinate                                            float64
}

var designID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var designColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}([0-9a-fA-F]{2})?$`)

// ValidateDesign rejects unsupported structure without rewriting input. Missing
// prototype targets remain explicit diagnostics, so deleted anchors are not
// silently rebound to similar content. No source code or external URLs exist
// in this model; unknown element kinds fail closed.
func ValidateDesign(scene DesignScene, limits DesignLimits) ([]DesignDiagnostic, error) {
	if limits.Artboards <= 0 || limits.Elements <= 0 || limits.Assets <= 0 || limits.Styles <= 0 || limits.Depth <= 0 || limits.TextBytes <= 0 || !finite(limits.Coordinate) || limits.Coordinate <= 0 {
		return nil, fmt.Errorf("design requires positive finite limits")
	}
	if len(scene.Artboards) == 0 || len(scene.Artboards) > limits.Artboards || len(scene.Assets) > limits.Assets || len(scene.Styles) > limits.Styles {
		return nil, fmt.Errorf("design collection limit exceeded")
	}
	ids := make(map[string]bool)
	claim := func(id string) error {
		if !designID.MatchString(id) || ids[id] {
			return fmt.Errorf("invalid or duplicate design identity %q", id)
		}
		ids[id] = true
		return nil
	}
	textBytes := 0
	text := func(value string) error {
		if len(value) > limits.TextBytes-textBytes || !utf8.ValidString(value) {
			return fmt.Errorf("invalid or excessive design text")
		}
		textBytes += len(value)
		return nil
	}
	assets := make(map[string]bool)
	for _, id := range scene.Assets {
		if err := claim(id); err != nil {
			return nil, err
		}
		assets[id] = true
	}
	styles := make(map[string]bool)
	for _, style := range scene.Styles {
		if err := claim(style.ID); err != nil {
			return nil, err
		}
		if (style.Fill != "" && !designColor.MatchString(style.Fill)) || (style.Stroke != "" && !designColor.MatchString(style.Stroke)) || !extent(style.FontSize, limits.Coordinate) || !extent(style.StrokeWidth, limits.Coordinate) || (style.FontAssetID != "" && !assets[style.FontAssetID]) {
			return nil, fmt.Errorf("invalid design style %q", style.ID)
		}
		styles[style.ID] = true
	}
	artboards := make(map[string]bool)
	elementBoards := make(map[string]string)
	count := 0
	for _, board := range scene.Artboards {
		if err := claim(board.ID); err != nil {
			return nil, err
		}
		artboards[board.ID] = true
		if !extent(board.Width, limits.Coordinate) || !extent(board.Height, limits.Coordinate) || board.Width == 0 || board.Height == 0 {
			return nil, fmt.Errorf("invalid artboard dimensions %q", board.ID)
		}
		if err := text(board.Name); err != nil {
			return nil, err
		}
		if len(board.Elements) > limits.Elements-count {
			return nil, fmt.Errorf("design element limit exceeded")
		}
		count += len(board.Elements)
		elements := make(map[string]DesignElement, len(board.Elements))
		for _, element := range board.Elements {
			if err := claim(element.ID); err != nil {
				return nil, err
			}
			switch element.Kind {
			case "text", "rectangle", "ellipse", "image", "group":
			default:
				return nil, fmt.Errorf("unsupported design element %q", element.Kind)
			}
			if !coordinate(element.X, limits.Coordinate) || !coordinate(element.Y, limits.Coordinate) || !extent(element.Width, limits.Coordinate) || !extent(element.Height, limits.Coordinate) {
				return nil, fmt.Errorf("invalid element geometry %q", element.ID)
			}
			if element.StyleID != "" && !styles[element.StyleID] {
				return nil, fmt.Errorf("unknown element style %q", element.ID)
			}
			if (element.Kind == "image" && element.AssetID == "") || (element.AssetID != "" && (!assets[element.AssetID] || element.Kind != "image")) {
				return nil, fmt.Errorf("invalid image reference %q", element.ID)
			}
			if element.Kind != "text" && element.Text != "" {
				return nil, fmt.Errorf("text on non-text element %q", element.ID)
			}
			if err := text(element.Text); err != nil {
				return nil, err
			}
			elements[element.ID] = element
			elementBoards[element.ID] = board.ID
		}
		for _, element := range board.Elements {
			// Bounded ancestor walk detects cycles without recursion or stack growth.
			for depth, parent := 1, element.ParentID; parent != ""; depth++ {
				if depth >= limits.Depth || depth > len(elements) {
					return nil, fmt.Errorf("design hierarchy cycle or depth limit at %q", element.ID)
				}
				ancestor, ok := elements[parent]
				if !ok || ancestor.Kind != "group" {
					return nil, fmt.Errorf("invalid parent for %q", element.ID)
				}
				parent = ancestor.ParentID
			}
		}
	}
	if !artboards[scene.StartArtboard] {
		return nil, fmt.Errorf("design requires an existing start artboard")
	}
	// At most one internal navigation action per element bounds the graph by
	// element count and avoids ambiguous dispatch. Broken target IDs are retained.
	if len(scene.Links) > count {
		return nil, fmt.Errorf("design link limit exceeded")
	}
	links := make(map[string]bool)
	var diagnostics []DesignDiagnostic
	for _, link := range scene.Links {
		if elementBoards[link.ElementID] == "" || links[link.ElementID] || !designID.MatchString(link.TargetArtboard) {
			return nil, fmt.Errorf("invalid design link from %q", link.ElementID)
		}
		links[link.ElementID] = true
		if !artboards[link.TargetArtboard] {
			diagnostics = append(diagnostics, DesignDiagnostic{link.ElementID, link.TargetArtboard, "missing-artboard"})
		}
	}
	return diagnostics, nil
}

func finite(value float64) bool            { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func coordinate(value, limit float64) bool { return finite(value) && math.Abs(value) <= limit }
func extent(value, limit float64) bool     { return coordinate(value, limit) && value >= 0 }

// DesignPrototype captures only a validated internal graph. Its transient
// current artboard is separate from the scene and any editor selection.
// A host must discard this state on revision replacement or access revocation.
type DesignPrototype struct {
	start, current string
	boards         map[string]bool
	sources        map[string]string
	links          map[string]string
}

func NewDesignPrototype(scene DesignScene, limits DesignLimits) (*DesignPrototype, error) {
	if _, err := ValidateDesign(scene, limits); err != nil {
		return nil, err
	}
	p := &DesignPrototype{start: scene.StartArtboard, current: scene.StartArtboard, boards: make(map[string]bool), sources: make(map[string]string), links: make(map[string]string)}
	for _, board := range scene.Artboards {
		p.boards[board.ID] = true
		for _, element := range board.Elements {
			p.sources[element.ID] = board.ID
		}
	}
	for _, link := range scene.Links {
		p.links[link.ElementID] = link.TargetArtboard
	}
	return p, nil
}

func (p *DesignPrototype) Current() string { return p.current }
func (p *DesignPrototype) Reset()          { p.current = p.start }

// Activate is the shared action for pointer and keyboard activation. It never
// executes code, opens URLs or changes the source scene.
func (p *DesignPrototype) Activate(elementID string) error {
	target, ok := p.links[elementID]
	if !ok || p.sources[elementID] != p.current || !p.boards[target] {
		return fmt.Errorf("unavailable design prototype link %q", elementID)
	}
	p.current = target
	return nil
}
