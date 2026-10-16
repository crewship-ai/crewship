package slides

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf16"
)

const (
	SelectedRequestProtocolVersion = 1
	SelectionSchemaVersion         = 1
	MaxSelectedRequestBytes        = 16 << 10
	MaxSelectedInstructionBytes    = 4096
	MaxSelectedQuoteUnits          = 4096
	MaxStoredSelectedInputBytes    = 64 << 10
	MaxVerifiedSelectionBytes      = 100 << 10
)

type TextSelection struct {
	SelectionSchemaVersion int    `json:"selection_schema_version"`
	Kind                   string `json:"kind"`
	SlideID                string `json:"slide_id"`
	ElementID              string `json:"element_id"`
	ElementHash            string `json:"element_hash"`
	From                   int    `json:"from"`
	To                     int    `json:"to"`
	Quote                  string `json:"quote"`
}
type SelectedRequestInput struct {
	PageID        string        `json:"page_id"`
	TypeID        string        `json:"type_id"`
	SchemaVersion int           `json:"schema_version"`
	BaseRev       int64         `json:"base_rev"`
	SubmissionID  string        `json:"submission_id"`
	ChatID        string        `json:"chat_id"`
	Instruction   string        `json:"instruction"`
	Selection     TextSelection `json:"selection"`
}

// VerifiedTextSelection is produced only from a validated immutable Store state.
// OriginalElement retains style/geometry so replacement cannot widen authority.
type VerifiedTextSelection struct {
	Selection         TextSelection `json:"selection"`
	OriginalElement   Element       `json:"original_element"`
	DeckSchemaVersion int           `json:"deck_schema_version"`
	ContentHash       string        `json:"content_hash"`
}

func DecodeSelectedRequest(raw []byte) (SelectedRequestInput, error) {
	if len(raw) > MaxSelectedRequestBytes {
		return SelectedRequestInput{}, errors.New("selected request exceeds16KiB")
	}
	return decodeStoredSelectedInput(raw)
}

// Persisted canonical JSON can escape Unicode separators more compact wire JSON
// represented literally. Its own bound is separate from original HTTP admission.
func decodeStoredSelectedInput(raw []byte) (SelectedRequestInput, error) {
	var request SelectedRequestInput
	if len(raw) > MaxStoredSelectedInputBytes {
		return request, errors.New("stored selected input exceeds budget")
	}
	err := strictRequest(raw, &request, func(object map[string]json.RawMessage) error {
		_, err := requiredObject(object["selection"], "selection_schema_version", "kind", "slide_id", "element_id", "element_hash", "from", "to", "quote")
		return err
	}, []string{"page_id", "type_id", "schema_version", "base_rev", "submission_id", "chat_id", "instruction", "selection"}, nil)
	if err != nil {
		return request, err
	}
	return request, validateSelectedRequest(request)
}
func decodeVerifiedTextSelection(raw []byte) (VerifiedTextSelection, error) {
	var verified VerifiedTextSelection
	if len(raw) > MaxVerifiedSelectionBytes {
		return verified, errors.New("stored verified selection exceeds budget")
	}
	err := strictRequest(raw, &verified, func(object map[string]json.RawMessage) error {
		if _, err := requiredObject(object["selection"], "selection_schema_version", "kind", "slide_id", "element_id", "element_hash", "from", "to", "quote"); err != nil {
			return err
		}
		var version int
		if err := json.Unmarshal(object["deck_schema_version"], &version); err != nil {
			return err
		}
		var identity struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(object["original_element"], &identity); err != nil {
			return err
		}
		placeholder := "__selected_original"
		if identity.ID == placeholder {
			placeholder = "__selected_original2"
		}
		slide, _ := json.Marshal(map[string]any{"id": placeholder, "notes": "", "elements": []json.RawMessage{object["original_element"]}})
		return validateRequestSlide(slide, version)
	}, []string{"selection", "original_element", "deck_schema_version", "content_hash"}, nil)
	if err != nil {
		return verified, err
	}
	if err = validateTextSelection(verified.Selection); err != nil {
		return verified, err
	}
	if !SupportedSchema(verified.DeckSchemaVersion) || !hashPattern.MatchString(verified.ContentHash) || verified.OriginalElement.Type != "text" || verified.OriginalElement.Text == nil {
		return verified, errors.New("invalid verified original text element")
	}
	return verified, nil
}
func validateSelectedRequest(request SelectedRequestInput) error {
	if request.SchemaVersion != SelectedRequestProtocolVersion || request.TypeID != "slides" || request.BaseRev < 1 || !identifier.MatchString(request.PageID) || !identifier.MatchString(request.ChatID) || !identifier.MatchString(request.SubmissionID) || strings.TrimSpace(request.Instruction) == "" || !validText(request.Instruction, MaxSelectedInstructionBytes) {
		return errors.New("invalid selected request protocol, identity or instruction")
	}
	return validateTextSelection(request.Selection)
}
func validateTextSelection(selection TextSelection) error {
	quoteUnits := len(utf16.Encode([]rune(selection.Quote)))
	if selection.SelectionSchemaVersion != SelectionSchemaVersion || selection.Kind != "text_range" || !identifier.MatchString(selection.SlideID) || !identifier.MatchString(selection.ElementID) || !hashPattern.MatchString(selection.ElementHash) || selection.From < 0 || selection.To <= selection.From || !validText(selection.Quote, MaxTextBytes) || quoteUnits < 1 || quoteUnits > MaxSelectedQuoteUnits {
		return errors.New("invalid immutable text selection")
	}
	return nil
}
func selectionOffsets(text string, selection TextSelection) ([]uint16, error) {
	units := utf16.Encode([]rune(text))
	boundary := func(n int) bool {
		return n >= 0 && n <= len(units) && (n == 0 || n == len(units) || !(utf16.IsSurrogate(rune(units[n-1])) && units[n-1] >= 0xd800 && units[n-1] <= 0xdbff && units[n] >= 0xdc00 && units[n] <= 0xdfff))
	}
	if selection.From >= selection.To || !boundary(selection.From) || !boundary(selection.To) || string(utf16.Decode(units[selection.From:selection.To])) != selection.Quote {
		return nil, errors.New("selection quote or UTF16 boundary changed")
	}
	return units, nil
}
func VerifyTextSelection(state State, request SelectedRequestInput) (VerifiedTextSelection, error) {
	var result VerifiedTextSelection
	if err := validateSelectedRequest(request); err != nil {
		return result, err
	}
	if state.Deck.DeckID != request.PageID || state.Revision != request.BaseRev || state.SchemaVersion != state.Deck.SchemaVersion || !hashPattern.MatchString(state.ContentHash) {
		return result, ErrConflict
	}
	for _, slide := range state.Deck.Slides {
		if slide.ID == request.Selection.SlideID {
			for _, element := range slide.Elements {
				if element.ID == request.Selection.ElementID {
					if element.Type != "text" || element.Text == nil {
						return result, errors.New("selected target must be an existing text element")
					}
					hash, err := ElementHash(element)
					if err != nil || hash != request.Selection.ElementHash {
						return result, ErrConflict
					}
					if _, err = selectionOffsets(element.Text.Value, request.Selection); err != nil {
						return result, ErrConflict
					}
					return VerifiedTextSelection{Selection: request.Selection, OriginalElement: element, DeckSchemaVersion: state.Deck.SchemaVersion, ContentHash: state.ContentHash}, nil
				}
			}
		}
	}
	return result, ErrConflict
}

// SelectedReplacementPatch never accepts new preconditions or a whole element
// from the run. A read cannot refresh its original target or base revision.
func SelectedReplacementPatch(pinned State, pageID, requestID string, baseRev int64, verified VerifiedTextSelection, replacement string) (PatchRequest, error) {
	var result PatchRequest
	if !identifier.MatchString(pageID) || !identifier.MatchString(requestID+"-patch") || baseRev < 1 || !SupportedSchema(verified.DeckSchemaVersion) || verified.Selection.SelectionSchemaVersion != SelectionSchemaVersion || verified.Selection.Kind != "text_range" || !identifier.MatchString(verified.Selection.SlideID) || !validText(replacement, MaxTextBytes) || verified.OriginalElement.Type != "text" || verified.OriginalElement.Text == nil || verified.OriginalElement.ID != verified.Selection.ElementID {
		return result, errors.New("invalid selected replacement")
	}
	if err := ValidateVerifiedTextSelection(pinned, pageID, baseRev, verified); err != nil {
		return result, err
	}
	original := verified.OriginalElement
	hash, err := ElementHash(original)
	if err != nil || hash != verified.Selection.ElementHash {
		return result, ErrConflict
	}
	units, err := selectionOffsets(original.Text.Value, verified.Selection)
	if err != nil {
		return result, ErrConflict
	}
	text := *original.Text
	text.Value = string(utf16.Decode(units[:verified.Selection.From])) + replacement + string(utf16.Decode(units[verified.Selection.To:]))
	if !validText(text.Value, MaxTextBytes) {
		return result, errors.New("selected replacement exceeds final text budget")
	}
	original.Text = &text
	return PatchRequest{PageID: pageID, SchemaVersion: verified.DeckSchemaVersion, BaseRev: baseRev, MutationID: requestID + "-patch", Operations: []Operation{{Op: "set_element", SlideID: verified.Selection.SlideID, ElementID: verified.Selection.ElementID, IfHash: verified.Selection.ElementHash, Element: &original}}}, nil
}

// ValidateVerifiedTextSelection checks persisted host metadata against the exact
// pinned original revision, never the latest head. The actual request authority
// repeats this under its writer fence before every write or receipt replay.
func ValidateVerifiedTextSelection(pinned State, pageID string, baseRev int64, verified VerifiedTextSelection) error {
	if err := validateTextSelection(verified.Selection); err != nil {
		return err
	}
	if pinned.Deck.DeckID != pageID || pinned.Revision != baseRev || pinned.SchemaVersion != pinned.Deck.SchemaVersion || pinned.SchemaVersion != verified.DeckSchemaVersion || pinned.ContentHash != verified.ContentHash || !hashPattern.MatchString(verified.ContentHash) {
		return ErrConflict
	}
	hash, err := DeckHash(pinned.Deck)
	if err != nil || hash != verified.ContentHash {
		return ErrConflict
	}
	for _, slide := range pinned.Deck.Slides {
		if slide.ID == verified.Selection.SlideID {
			for _, element := range slide.Elements {
				if element.ID == verified.Selection.ElementID {
					before, e := canonicalValue(element)
					if e != nil {
						return ErrConflict
					}
					stored, e := canonicalValue(verified.OriginalElement)
					if e != nil || string(before) != string(stored) {
						return ErrConflict
					}
					if element.Type != "text" || element.Text == nil {
						return ErrConflict
					}
					if _, e = selectionOffsets(element.Text.Value, verified.Selection); e != nil {
						return ErrConflict
					}
					elementHash, e := ElementHash(element)
					if e != nil || elementHash != verified.Selection.ElementHash {
						return ErrConflict
					}
					return nil
				}
			}
		}
	}
	return ErrConflict
}
