package pages

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"unicode/utf8"
)

const (
	// BoardFormat is the deliberately limited Excalidraw 0.18.1 data profile.
	// Runtime assets, URLs, image files and client-authoritative provenance are not
	// part of this scene format. Authorization remains the future API's job.
	BoardFormat             = "crewship-board-excalidraw-0.18.1-v1"
	MaxBoardBatchBytes      = 2 << 20
	MaxBoardElements        = 500
	MaxBoardRecords         = 1000
	MaxBoardBatchOperations = 500
	MaxBoardTextBytes       = 20000
	MaxBoardPoints          = 10000
	boardCoordinateLimit    = 1000000
)

var ErrBoardConflict = errors.New("board conflict")
var boardID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var boardColor = regexp.MustCompile(`^(transparent|#[0-9a-fA-F]{3}|#[0-9a-fA-F]{4}|#[0-9a-fA-F]{6}|#[0-9a-fA-F]{8})$`)
var boardJSONFields = func() map[string]bool {
	fields := map[string]bool{}
	for _, value := range []any{BoardElement{}, BoardBatch{}, BoardUpsert{}, BoardDelete{}, BoardBinding{}, BoardBoundElement{}, BoardRoundness{}, BoardFixedSegment{}} {
		typ := reflect.TypeOf(value)
		for i := 0; i < typ.NumField(); i++ {
			name := typ.Field(i).Tag.Get("json")
			for j, c := range name {
				if c == ',' {
					name = name[:j]
					break
				}
			}
			fields[name] = true
		}
	}
	return fields
}()
var boardIndex = regexp.MustCompile(`^[A-Za-z0-9]{1,128}$`)

type BoardPressure float64

func (p *BoardPressure) UnmarshalJSON(raw []byte) error {
	var value *float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value == nil {
		return errors.New("pressure must be a number")
	}
	*p = BoardPressure(*value)
	return nil
}

type BoardPoint [2]float64
type BoardBoundElement struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}
type BoardBinding struct {
	ElementID  string      `json:"elementId"`
	Focus      float64     `json:"focus"`
	Gap        float64     `json:"gap"`
	FixedPoint *BoardPoint `json:"fixedPoint,omitempty"`
}
type BoardRoundness struct {
	Type  int      `json:"type"`
	Value *float64 `json:"value,omitempty"`
}
type BoardFixedSegment struct {
	Start BoardPoint `json:"start"`
	End   BoardPoint `json:"end"`
	Index int        `json:"index"`
}

// Field names mirror the pinned public element schema. Type-specific fields
// are checked separately; accepting one here does not permit it on every type.
type BoardElement struct {
	FileID             *string             `json:"fileId,omitempty"`
	ImageStatus        string              `json:"status,omitempty"`
	Scale              []float64           `json:"scale,omitempty"`
	Crop               json.RawMessage     `json:"crop,omitempty"`
	CustomData         json.RawMessage     `json:"customData,omitempty"`
	ID                 string              `json:"id"`
	Type               string              `json:"type"`
	X                  *float64            `json:"x"`
	Y                  *float64            `json:"y"`
	Width              *float64            `json:"width"`
	Height             *float64            `json:"height"`
	Angle              float64             `json:"angle"`
	StrokeColor        string              `json:"strokeColor"`
	BackgroundColor    string              `json:"backgroundColor"`
	FillStyle          string              `json:"fillStyle"`
	StrokeWidth        float64             `json:"strokeWidth"`
	StrokeStyle        string              `json:"strokeStyle"`
	Roughness          float64             `json:"roughness"`
	Opacity            float64             `json:"opacity"`
	Seed               int64               `json:"seed"`
	Version            int64               `json:"version"`
	VersionNonce       int64               `json:"versionNonce"`
	Index              *string             `json:"index"`
	IsDeleted          bool                `json:"isDeleted"`
	GroupIDs           []string            `json:"groupIds"`
	FrameID            *string             `json:"frameId"`
	BoundElements      []BoardBoundElement `json:"boundElements"`
	Updated            int64               `json:"updated"`
	Link               *string             `json:"link"`
	Locked             bool                `json:"locked"`
	Roundness          *BoardRoundness     `json:"roundness"`
	Name               *string             `json:"name,omitempty"`
	FontSize           float64             `json:"fontSize,omitempty"`
	FontFamily         int                 `json:"fontFamily,omitempty"`
	Text               string              `json:"text,omitempty"`
	OriginalText       string              `json:"originalText,omitempty"`
	TextAlign          string              `json:"textAlign,omitempty"`
	VerticalAlign      string              `json:"verticalAlign,omitempty"`
	ContainerID        *string             `json:"containerId,omitempty"`
	AutoResize         bool                `json:"autoResize,omitempty"`
	LineHeight         float64             `json:"lineHeight,omitempty"`
	Points             []BoardPoint        `json:"points,omitempty"`
	LastCommittedPoint *BoardPoint         `json:"lastCommittedPoint,omitempty"`
	StartBinding       *BoardBinding       `json:"startBinding,omitempty"`
	EndBinding         *BoardBinding       `json:"endBinding,omitempty"`
	StartArrowhead     *string             `json:"startArrowhead,omitempty"`
	EndArrowhead       *string             `json:"endArrowhead,omitempty"`
	Pressures          []BoardPressure     `json:"pressures,omitempty"`
	SimulatePressure   bool                `json:"simulatePressure,omitempty"`
	Elbowed            bool                `json:"elbowed,omitempty"`
	FixedSegments      []BoardFixedSegment `json:"fixedSegments,omitempty"`
	StartIsSpecial     *bool               `json:"startIsSpecial,omitempty"`
	EndIsSpecial       *bool               `json:"endIsSpecial,omitempty"`
}
type BoardUpsert struct {
	Element     json.RawMessage `json:"element"`
	ExpectedRev int64           `json:"expected_rev"`
}
type BoardDelete struct {
	ID          string `json:"id"`
	ExpectedRev int64  `json:"expected_rev"`
}
type BoardBatch struct {
	BaseSeq int64         `json:"base_seq"`
	Upserts []BoardUpsert `json:"upserts"`
	Deletes []BoardDelete `json:"deletes"`
}
type BoardRecord struct {
	Element    json.RawMessage `json:"element,omitempty"`
	ElementRev int64           `json:"element_rev"`
	Deleted    bool            `json:"deleted"`
}
type BoardScene struct {
	Seq     int64                  `json:"seq"`
	Records map[string]BoardRecord `json:"records"`
}

func decodeBoardJSON(raw []byte, target any) error {
	if !utf8.Valid(raw) {
		return errors.New("board JSON must be UTF-8")
	}
	if len(raw) > MaxBoardBatchBytes {
		return errors.New("board payload exceeds 2 MiB")
	}
	// Reject duplicate keys and deep objects rather than depending on differing
	// browser/server parser behavior for ambiguous JSON.
	tokens := json.NewDecoder(bytes.NewReader(raw))
	tokens.UseNumber()
	remaining := 200000
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 32 {
			return errors.New("board JSON nesting exceeds limit")
		}
		remaining--
		if remaining < 0 {
			return errors.New("board JSON token limit exceeded")
		}
		token, err := tokens.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for tokens.More() {
				key, err := tokens.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || !boardJSONFields[name] || seen[name] {
					return errors.New("duplicate or invalid board JSON key")
				}
				seen[name] = true
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for tokens.More() {
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid board JSON delimiter")
		}
		_, err = tokens.Token()
		return err
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := tokens.Token(); err != io.EOF {
		return errors.New("one board JSON value required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("one board JSON value required")
	}
	return nil
}
func DecodeBoardBatch(raw []byte) (BoardBatch, error) {
	var batch BoardBatch
	if err := decodeBoardJSON(raw, &batch); err != nil {
		return batch, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return batch, err
	}
	if !requiredBoardJSON(fields, "base_seq") {
		return batch, errors.New("explicit base_seq required")
	}
	for _, key := range []string{"upserts", "deletes"} {
		var operations []map[string]json.RawMessage
		if err := json.Unmarshal(fields[key], &operations); err != nil && fields[key] != nil {
			return batch, err
		}
		for _, operation := range operations {
			if !requiredBoardJSON(operation, "expected_rev") {
				return batch, errors.New("explicit expected_rev required")
			}
		}
	}
	if err := validateBoardBatchShape(batch); err != nil {
		return batch, err
	}
	return batch, nil
}
func requiredBoardJSON(fields map[string]json.RawMessage, key string) bool {
	raw, ok := fields[key]
	return ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
func finiteRange(v, min, max float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= min && v <= max
}
func validBoardPoint(p BoardPoint) bool {
	return finiteRange(p[0], -boardCoordinateLimit, boardCoordinateLimit) && finiteRange(p[1], -boardCoordinateLimit, boardCoordinateLimit)
}
func member(s string, allowed ...string) bool {
	for _, v := range allowed {
		if s == v {
			return true
		}
	}
	return false
}
func boundedBoardText(s string, max int) bool { return utf8.ValidString(s) && len(s) <= max }

func ValidateBoardElement(raw json.RawMessage) (BoardElement, error) {
	var e BoardElement
	if err := decodeBoardJSON(raw, &e); err != nil {
		return e, err
	}
	invalid := func(message string) (BoardElement, error) {
		return e, fmt.Errorf("board element %q: %s", e.ID, message)
	}
	if !boardID.MatchString(e.ID) || !member(e.Type, "rectangle", "ellipse", "diamond", "line", "arrow", "freedraw", "text", "frame", "image") {
		return invalid("unsupported id or type")
	}
	for _, v := range []*float64{e.X, e.Y, e.Width, e.Height} {
		if v == nil || !finiteRange(*v, -boardCoordinateLimit, boardCoordinateLimit) {
			return invalid("finite bounded geometry required")
		}
	}
	if *e.Width < 0 || *e.Height < 0 || !finiteRange(e.Angle, -2*math.Pi, 2*math.Pi) || !finiteRange(e.StrokeWidth, 0, 100) || !finiteRange(e.Roughness, 0, 10) || !finiteRange(e.Opacity, 0, 100) {
		return invalid("invalid visual dimensions")
	}
	if !boardColor.MatchString(e.StrokeColor) || !boardColor.MatchString(e.BackgroundColor) || !member(e.FillStyle, "hachure", "cross-hatch", "solid", "zigzag") || !member(e.StrokeStyle, "solid", "dashed", "dotted") {
		return invalid("invalid presentation fields")
	}
	if (len(e.CustomData) > 0 && !bytes.Equal(bytes.TrimSpace(e.CustomData), []byte("null"))) || e.Link != nil || e.IsDeleted {
		return invalid("links and upserted deleted elements are disabled")
	}
	const safeInteger = int64(9007199254740991)
	if e.Version < 1 || e.Version > safeInteger || e.VersionNonce < -safeInteger || e.VersionNonce > safeInteger || e.Seed < -safeInteger || e.Seed > safeInteger || e.Updated < 0 || e.Updated > safeInteger {
		return invalid("invalid compatibility metadata")
	}
	if e.Index != nil && !boardIndex.MatchString(*e.Index) {
		return invalid("invalid fractional index")
	}
	if len(e.GroupIDs) > 20 || len(e.BoundElements) > MaxBoardElements {
		return invalid("too many relationships")
	}
	seen := map[string]bool{}
	for _, id := range e.GroupIDs {
		if !boardID.MatchString(id) || seen[id] {
			return invalid("invalid group id")
		}
		seen[id] = true
	}
	if e.FrameID != nil && (!boardID.MatchString(*e.FrameID) || *e.FrameID == e.ID) {
		return invalid("invalid frame parent")
	}
	if e.Roundness != nil {
		if e.Roundness.Type < 1 || e.Roundness.Type > 3 || (e.Roundness.Value != nil && !finiteRange(*e.Roundness.Value, 0, boardCoordinateLimit)) {
			return invalid("invalid roundness")
		}
	}
	seen = map[string]bool{}
	for _, bound := range e.BoundElements {
		if !boardID.MatchString(bound.ID) || bound.ID == e.ID || seen[bound.ID] || !member(bound.Type, "arrow", "text") {
			return invalid("invalid bound element")
		}
		seen[bound.ID] = true
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for _, key := range []string{"x", "y", "width", "height", "angle", "strokeWidth", "roughness", "opacity", "seed", "version", "versionNonce", "updated"} {
		if !requiredBoardJSON(fields, key) {
			return invalid("explicit numeric field required: " + key)
		}
	}
	for _, key := range []string{"fontSize", "fontFamily", "lineHeight"} {
		if e.Type == "text" && !requiredBoardJSON(fields, key) {
			return invalid("explicit text number required: " + key)
		}
	}
	for _, key := range []string{"startBinding", "endBinding", "roundness"} {
		if bytes.Equal(bytes.TrimSpace(fields[key]), []byte("null")) || fields[key] == nil {
			continue
		}
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(fields[key], &nested); err != nil {
			return invalid("invalid nested object")
		}
		required := []string{"focus", "gap"}
		if key == "roundness" {
			required = []string{"type"}
		}
		for _, number := range required {
			if !requiredBoardJSON(nested, number) {
				return invalid("explicit nested number required")
			}
		}
		if value, ok := nested["value"]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return invalid("roundness value cannot be null")
		}
	}
	if segments, present := fields["fixedSegments"]; present && !bytes.Equal(bytes.TrimSpace(segments), []byte("null")) {
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(segments, &entries); err != nil {
			return invalid("invalid fixed segments")
		}
		for _, entry := range entries {
			for _, key := range []string{"start", "end", "index"} {
				if !requiredBoardJSON(entry, key) {
					return invalid("explicit fixed segment fields required")
				}
			}
		}
	}
	families := map[string][]string{"image": {"fileId", "status", "scale", "crop"}, "text": {"fontSize", "fontFamily", "text", "originalText", "textAlign", "verticalAlign", "containerId", "autoResize", "lineHeight"}, "linear": {"points", "lastCommittedPoint", "startBinding", "endBinding", "startArrowhead", "endArrowhead"}, "freedraw": {"pressures", "simulatePressure"}, "arrow": {"elbowed", "fixedSegments", "startIsSpecial", "endIsSpecial"}, "frame": {"name"}}
	for family, keys := range families {
		allowed := family == e.Type || (family == "linear" && member(e.Type, "line", "arrow", "freedraw"))
		for _, key := range keys {
			if _, present := fields[key]; present && !allowed {
				return invalid("field is not valid for this element type: " + key)
			}
		}
	}
	if e.Type == "freedraw" && (e.StartBinding != nil || e.EndBinding != nil || e.StartArrowhead != nil || e.EndArrowhead != nil) {
		return invalid("freehand strokes cannot bind or have arrowheads")
	}
	if e.Type == "text" {
		if !boundedBoardText(e.Text, MaxBoardTextBytes) || !boundedBoardText(e.OriginalText, MaxBoardTextBytes) || !finiteRange(e.FontSize, 1, 1000) || !member(fmt.Sprint(e.FontFamily), "1", "2", "3", "5", "6", "7", "8", "9") || !member(e.TextAlign, "left", "center", "right") || !member(e.VerticalAlign, "top", "middle", "bottom") || !finiteRange(e.LineHeight, 0.1, 10) {
			return invalid("invalid text profile")
		}
		if e.ContainerID != nil && (!boardID.MatchString(*e.ContainerID) || *e.ContainerID == e.ID) {
			return invalid("invalid text container")
		}
	}
	if e.Type == "image" {
		if e.FileID == nil || !boardID.MatchString(*e.FileID) || e.ImageStatus != "saved" || len(e.Scale) != 2 || (e.Scale[0] != 1 && e.Scale[0] != -1) || (e.Scale[1] != 1 && e.Scale[1] != -1) || (len(e.Crop) > 0 && !bytes.Equal(bytes.TrimSpace(e.Crop), []byte("null"))) {
			return invalid("invalid image profile")
		}
	}
	if e.Name != nil && !boundedBoardText(*e.Name, 256) {
		return invalid("frame name too long")
	}
	if member(e.Type, "line", "arrow", "freedraw") {
		if len(e.Points) < 2 || len(e.Points) > MaxBoardPoints {
			return invalid("bounded points required")
		}
		for _, p := range e.Points {
			if !validBoardPoint(p) {
				return invalid("invalid point")
			}
		}
		if e.LastCommittedPoint != nil && !validBoardPoint(*e.LastCommittedPoint) {
			return invalid("invalid last point")
		}
	}
	for _, binding := range []*BoardBinding{e.StartBinding, e.EndBinding} {
		if binding != nil {
			if !boardID.MatchString(binding.ElementID) || binding.ElementID == e.ID || !finiteRange(binding.Focus, -1, 1) || !finiteRange(binding.Gap, 0, boardCoordinateLimit) || (binding.FixedPoint != nil && (!finiteRange((*binding.FixedPoint)[0], 0, 1) || !finiteRange((*binding.FixedPoint)[1], 0, 1))) {
				return invalid("invalid endpoint binding")
			}
		}
	}
	arrowheads := []string{"arrow", "bar", "dot", "circle", "circle_outline", "triangle", "triangle_outline", "diamond", "diamond_outline", "crowfoot_one", "crowfoot_many", "crowfoot_one_or_many"}
	for _, head := range []*string{e.StartArrowhead, e.EndArrowhead} {
		if head != nil && !member(*head, arrowheads...) {
			return invalid("unsupported arrowhead")
		}
	}
	if e.Type == "freedraw" {
		if len(e.Pressures) != 0 && len(e.Pressures) != len(e.Points) {
			return invalid("pressure count does not match points")
		}
		for _, v := range e.Pressures {
			if !finiteRange(float64(v), 0, 1) {
				return invalid("invalid pressure")
			}
		}
	}
	if len(e.FixedSegments) > MaxBoardPoints {
		return invalid("too many fixed segments")
	}
	if !e.Elbowed && len(e.FixedSegments) > 0 {
		return invalid("fixed segments require elbowed arrow")
	}
	for _, segment := range e.FixedSegments {
		if !validBoardPoint(segment.Start) || !validBoardPoint(segment.End) || segment.Index < 1 || segment.Index >= len(e.Points) {
			return invalid("invalid fixed segment")
		}
	}
	return e, nil
}

func (p *BoardPoint) UnmarshalJSON(raw []byte) error {
	var values []*float64
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	if len(values) != 2 || values[0] == nil || values[1] == nil {
		return errors.New("board point must contain exactly two numbers")
	}
	*p = BoardPoint{*values[0], *values[1]}
	return nil
}
