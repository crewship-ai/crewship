package slides

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"
)

var ErrConflict = errors.New("Slides precondition conflict")
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const MaxOperations = 100

type PatchRequest struct {
	PageID         string      `json:"page_id"`
	SchemaVersion  int         `json:"schema_version"`
	BaseRev        int64       `json:"base_rev"`
	MutationID     string      `json:"mutation_id"`
	IfOrderVersion *int64      `json:"if_order_version,omitempty"`
	Operations     []Operation `json:"operations"`
}
type Operation struct {
	Op        string   `json:"op"`
	SlideID   string   `json:"slide_id,omitempty"`
	ElementID string   `json:"element_id,omitempty"`
	IfHash    string   `json:"if_hash,omitempty"`
	Element   *Element `json:"element,omitempty"`
	Slide     *Slide   `json:"slide,omitempty"`
	Notes     *string  `json:"notes,omitempty"`
	Position  *int     `json:"position,omitempty"`
}
type ReorderRequest struct {
	PageID         string   `json:"page_id"`
	SchemaVersion  int      `json:"schema_version"`
	BaseRev        int64    `json:"base_rev"`
	MutationID     string   `json:"mutation_id"`
	IfOrderVersion int64    `json:"if_order_version"`
	SlideOrder     []string `json:"slide_order"`
}
type RestoreRequest struct {
	PageID         string `json:"page_id"`
	SchemaVersion  int    `json:"schema_version"`
	BaseRev        int64  `json:"base_rev"`
	MutationID     string `json:"mutation_id"`
	IfOrderVersion int64  `json:"if_order_version"`
	Revision       int64  `json:"revision"`
}
type InitializeRequest struct {
	PageID        string `json:"page_id"`
	SchemaVersion int    `json:"schema_version"`
	BaseRev       int64  `json:"base_rev"`
	MutationID    string `json:"mutation_id"`
	Deck          Deck   `json:"deck"`
}

func requestObject(raw []byte, required, optional []string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	fields := append([]string{}, required...)
	for _, field := range optional {
		if _, ok := object[field]; ok {
			fields = append(fields, field)
		}
	}
	return requiredObject(raw, fields...)
}
func strictRequest(raw []byte, target any, check func(map[string]json.RawMessage) error, required, optional []string) error {
	if len(raw) > MaxDeckBytes || !utf8.Valid(raw) {
		return errors.New("invalid Slides request byte budget or UTF-8")
	}
	if err := validJSONUnicode(raw); err != nil {
		return err
	}
	if err := uniqueKeys(raw); err != nil {
		return err
	}
	object, err := requestObject(raw, required, optional)
	if err != nil {
		return err
	}
	if check != nil {
		if err = check(object); err != nil {
			return err
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return errors.New("trailing Slides request JSON")
	}
	return nil
}
func validMutation(schema int, base int64, id, pageID string) error {
	if schema != SchemaVersion {
		return ErrSchema
	}
	if base < 0 || !identifier.MatchString(id) || !identifier.MatchString(pageID) {
		return errors.New("invalid mutation identity or base revision")
	}
	return nil
}
func DecodeInitialize(raw []byte) (InitializeRequest, error) {
	var r InitializeRequest
	err := strictRequest(raw, &r, func(object map[string]json.RawMessage) error { _, err := Decode(object["deck"]); return err }, []string{"page_id", "schema_version", "base_rev", "mutation_id", "deck"}, nil)
	if err != nil {
		return r, err
	}
	if err = validMutation(r.SchemaVersion, r.BaseRev, r.MutationID, r.PageID); err != nil {
		return r, err
	}
	if r.PageID != r.Deck.DeckID {
		return r, errors.New("initialize Page identity differs from deck")
	}
	if r.BaseRev != 0 {
		return r, ErrConflict
	}
	return r, nil
}
func DecodePatch(raw []byte) (PatchRequest, error) {
	var r PatchRequest
	err := strictRequest(raw, &r, func(object map[string]json.RawMessage) error {
		var operations []json.RawMessage
		if err := json.Unmarshal(object["operations"], &operations); err != nil {
			return err
		}
		if len(operations) < 1 || len(operations) > MaxOperations {
			return errors.New("patch requires 1–100 operations")
		}
		for _, rawOperation := range operations {
			var operation map[string]json.RawMessage
			if err := json.Unmarshal(rawOperation, &operation); err != nil {
				return err
			}
			var kind string
			if err := json.Unmarshal(operation["op"], &kind); err != nil {
				return err
			}
			fields := map[string][]string{
				"set_element":    {"op", "slide_id", "element_id", "if_hash", "element"},
				"insert_element": {"op", "slide_id", "if_hash", "element"},
				"delete_element": {"op", "slide_id", "element_id", "if_hash"},
				"set_notes":      {"op", "slide_id", "if_hash", "notes"},
				"insert_slide":   {"op", "slide", "position"},
				"delete_slide":   {"op", "slide_id", "if_hash"},
			}[kind]
			if fields == nil {
				return errors.New("unsupported Slides operation")
			}
			if _, err := requiredObject(rawOperation, fields...); err != nil {
				return err
			}
			// Nested payloads must go through the same exact model field/type checks.
			if nested, ok := operation["slide"]; ok {
				if err := validateRequestSlide(nested); err != nil {
					return err
				}
			}
			if nested, ok := operation["element"]; ok {
				var elementIdentity struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(nested, &elementIdentity); err != nil {
					return err
				}
				placeholder := "__request_slide"
				if elementIdentity.ID == placeholder {
					placeholder = "__request_slide2"
				}
				slide, _ := json.Marshal(map[string]any{"id": placeholder, "notes": "", "elements": []json.RawMessage{nested}})
				if err := validateRequestSlide(slide); err != nil {
					return err
				}
			}
		}
		return nil
	}, []string{"page_id", "schema_version", "base_rev", "mutation_id", "operations"}, []string{"if_order_version"})
	if err != nil {
		return r, err
	}
	if err = validMutation(r.SchemaVersion, r.BaseRev, r.MutationID, r.PageID); err != nil {
		return r, err
	}
	if r.IfOrderVersion != nil && *r.IfOrderVersion < 0 {
		return r, errors.New("invalid order precondition")
	}
	for _, op := range r.Operations {
		if op.Op != "insert_slide" && (!identifier.MatchString(op.SlideID) || !hashPattern.MatchString(op.IfHash)) {
			return r, errors.New("stable target and hash required")
		}
		if (op.Op == "set_element" || op.Op == "delete_element") && !identifier.MatchString(op.ElementID) {
			return r, errors.New("element target required")
		}
		if (op.Op == "insert_slide" || op.Op == "delete_slide") && r.IfOrderVersion == nil {
			return r, errors.New("structural operations require order precondition")
		}
		if op.Op == "set_notes" && (op.Notes == nil || !validText(*op.Notes, MaxNotesBytes)) {
			return r, errors.New("invalid notes")
		}
	}
	return r, nil
}
func validateRequestSlide(raw json.RawMessage) error {
	// Use a maximal legal frame for syntax/standalone geometry checks. The final
	// candidate is revalidated against the real deck dimensions and byte budget.
	envelope, _ := json.Marshal(map[string]any{"schema_version": SchemaVersion, "deck_id": "__request_deck", "width": 4096, "height": 4096, "theme": Theme{Background: "#FFFFFF", Color: "#000000", FontFamily: "Arial", FontSize: 24}, "slide_order": []string{}, "slides": []json.RawMessage{raw}})
	var slide struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &slide); err != nil {
		return err
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(envelope, &values); err != nil {
		return err
	}
	values["slide_order"], _ = json.Marshal([]string{slide.ID})
	envelope, _ = json.Marshal(values)
	_, err := Decode(envelope)
	return err
}
func DecodeReorder(raw []byte) (ReorderRequest, error) {
	var r ReorderRequest
	err := strictRequest(raw, &r, nil, []string{"page_id", "schema_version", "base_rev", "mutation_id", "if_order_version", "slide_order"}, nil)
	if err != nil {
		return r, err
	}
	if err = validMutation(r.SchemaVersion, r.BaseRev, r.MutationID, r.PageID); err != nil {
		return r, err
	}
	if r.IfOrderVersion < 0 || r.SlideOrder == nil || len(r.SlideOrder) > MaxSlides {
		return r, errors.New("invalid order precondition")
	}
	return r, nil
}
func DecodeRestore(raw []byte) (RestoreRequest, error) {
	var r RestoreRequest
	err := strictRequest(raw, &r, nil, []string{"page_id", "schema_version", "base_rev", "mutation_id", "if_order_version", "revision"}, nil)
	if err != nil {
		return r, err
	}
	if err = validMutation(r.SchemaVersion, r.BaseRev, r.MutationID, r.PageID); err != nil {
		return r, err
	}
	if r.IfOrderVersion < 0 || r.Revision < 1 {
		return r, errors.New("invalid restore precondition")
	}
	return r, nil
}
func SlideHash(slide Slide) (string, error) {
	data, err := canonicalValue(slide)
	if err != nil {
		return "", err
	}
	return hashBytes(data), nil
}
func ObjectIDs(deck Deck) map[string]string {
	ids := map[string]string{}
	for _, slide := range deck.Slides {
		ids[slide.ID] = "slide"
		for _, element := range slide.Elements {
			ids[element.ID] = "element"
		}
	}
	return ids
}
func ApplyPatch(current Deck, request PatchRequest) (Deck, error) {
	if request.PageID != current.DeckID {
		return Deck{}, ErrConflict
	}
	encoded, err := Canonical(current)
	if err != nil {
		return Deck{}, err
	}
	candidate, err := Decode(encoded)
	if err != nil {
		return Deck{}, err
	}
	used := ObjectIDs(candidate)
	for _, op := range request.Operations {
		index := -1
		for i, slide := range candidate.Slides {
			if slide.ID == op.SlideID {
				index = i
				break
			}
		}
		if op.Op == "insert_slide" {
			if op.Slide == nil || op.Position == nil || *op.Position < 0 || *op.Position > len(candidate.SlideOrder) {
				return Deck{}, errors.New("invalid slide insertion")
			}
			newIDs := ObjectIDs(Deck{Slides: []Slide{*op.Slide}})
			for id, kind := range newIDs {
				if _, exists := used[id]; exists {
					return Deck{}, ErrConflict
				}
				used[id] = kind
			}
			candidate.Slides = append(candidate.Slides, *op.Slide)
			position := *op.Position
			candidate.SlideOrder = append(candidate.SlideOrder, "")
			copy(candidate.SlideOrder[position+1:], candidate.SlideOrder[position:])
			candidate.SlideOrder[position] = op.Slide.ID
			continue
		}
		if index < 0 {
			return Deck{}, ErrConflict
		}
		slide := &candidate.Slides[index]
		if op.Op == "set_element" || op.Op == "delete_element" {
			elementIndex := -1
			for i, element := range slide.Elements {
				if element.ID == op.ElementID {
					elementIndex = i
					break
				}
			}
			if elementIndex < 0 {
				return Deck{}, ErrConflict
			}
			hash, _ := ElementHash(slide.Elements[elementIndex])
			if hash != op.IfHash {
				return Deck{}, ErrConflict
			}
			if op.Op == "delete_element" {
				slide.Elements = append(slide.Elements[:elementIndex], slide.Elements[elementIndex+1:]...)
			} else {
				if op.Element == nil || op.Element.ID != op.ElementID {
					return Deck{}, errors.New("replacement must preserve identity")
				}
				slide.Elements[elementIndex] = *op.Element
			}
			continue
		}
		hash, _ := SlideHash(*slide)
		if hash != op.IfHash {
			return Deck{}, ErrConflict
		}
		switch op.Op {
		case "insert_element":
			if op.Element == nil {
				return Deck{}, errors.New("element required")
			}
			if _, exists := used[op.Element.ID]; exists {
				return Deck{}, ErrConflict
			}
			used[op.Element.ID] = "element"
			slide.Elements = append(slide.Elements, *op.Element)
		case "set_notes":
			if op.Notes == nil {
				return Deck{}, errors.New("notes required")
			}
			slide.Notes = *op.Notes
		case "delete_slide":
			candidate.Slides = append(candidate.Slides[:index], candidate.Slides[index+1:]...)
			for i, id := range candidate.SlideOrder {
				if id == op.SlideID {
					candidate.SlideOrder = append(candidate.SlideOrder[:i], candidate.SlideOrder[i+1:]...)
					break
				}
			}
		default:
			return Deck{}, fmt.Errorf("unsupported operation %q", op.Op)
		}
	}
	if err := Validate(candidate); err != nil {
		return Deck{}, err
	}
	return candidate, nil
}
