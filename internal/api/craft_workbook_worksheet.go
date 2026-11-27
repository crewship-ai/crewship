package api

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"hash"
	"io"
	"sort"
	"strconv"
	"strings"
)

const craftSpreadsheetNS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"

var craftXMLSpace = xml.Name{Space: "http://www.w3.org/XML/1998/namespace", Local: "space"}

var errCraftWorksheetXML = errors.New("cannot prove the worksheet's structure and cells")

type craftWorkbookEdits struct {
	cells             map[string][]nativeAgentEngineArea
	allowScalarValues bool // a human save may lack a precise list of edited values
	observe           func(string, int, int, [32]byte) error
}

func (e *craftWorkbookEdits) add(address string) bool {
	sheet, area, ok := craftSheetAddress(address)
	if !ok {
		return false
	}
	if e.cells == nil {
		e.cells = make(map[string][]nativeAgentEngineArea)
	}
	sheet = strings.ToLower(sheet)
	e.cells[sheet] = append(e.cells[sheet], area)
	return true
}

func (e craftWorkbookEdits) includes(sheet string, col, row int) bool {
	for _, area := range e.cells[sheet] {
		if col >= area.col1 && col <= area.col2 && row >= area.row1 && row <= area.row2 {
			return true
		}
	}
	return false
}

type craftWorksheetState struct {
	content   [32]byte
	dimension nativeAgentEngineArea
	cells     nativeAgentEngineArea
}

func craftWorksheetEnclose(a, b nativeAgentEngineArea) nativeAgentEngineArea {
	if a == (nativeAgentEngineArea{}) {
		return b
	}
	if b == (nativeAgentEngineArea{}) {
		return a
	}
	return nativeAgentEngineArea{
		col1: min(a.col1, b.col1),
		row1: min(a.row1, b.row1),
		col2: max(a.col2, b.col2),
		row2: max(a.row2, b.row2),
	}
}

func (before craftWorksheetState) preservedBy(after craftWorksheetState) bool {
	if before.content != after.content {
		return false
	}
	if before.dimension == (nativeAgentEngineArea{}) {
		return after.dimension == (nativeAgentEngineArea{})
	}
	// The writer may expand an existing dimension to fit the cells it added.
	// The old range must already enclose its cells. The entire expansion must
	// be justified by actual cells in the new worksheet, whose contents remain
	// subject to the edit scope in the projection.
	return craftWorksheetEnclose(before.dimension, before.cells) == before.dimension &&
		craftWorksheetEnclose(before.dimension, after.cells) == after.dimension
}

// A projection keeps all worksheet markup except permitted edits to simple cell
// contents, and the cached value of an unchanged, ordinary formula. Formulas with
// attributes (e.g. shared, array and data-table formulas) are kept together with
// their cached results. Formula expressions are compared as decoded XML text.
func craftWorksheetProjection(part *zip.File, sheet string, edits craftWorkbookEdits) (craftWorksheetState, error) {
	r, err := part.Open()
	if err != nil {
		return craftWorksheetState{}, err
	}
	defer r.Close()
	d := xml.NewDecoder(r)
	out := craftWorksheetHash{sha256.New()}
	var state craftWorksheetState
	var stack []xml.Name
	var row, lastRow, lastCol int
	var rowStart *xml.StartElement
	seenRoot, seenData := false, false
	flushRow := func() {
		if rowStart != nil {
			out.token(*rowStart)
			rowStart = nil
		}
	}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			if !seenRoot || len(stack) != 0 {
				return craftWorksheetState{}, errCraftWorksheetXML
			}
			state.content = out.sum()
			return state, nil
		}
		if err != nil {
			return craftWorksheetState{}, err
		}
		switch el := tok.(type) {
		case xml.StartElement:
			if len(stack) >= 256 {
				return craftWorksheetState{}, errCraftWorksheetXML
			}
			switch {
			case len(stack) == 0:
				if seenRoot || el.Name != (xml.Name{Space: craftSpreadsheetNS, Local: "worksheet"}) {
					return craftWorksheetState{}, errCraftWorksheetXML
				}
				seenRoot = true
			case len(stack) == 1 && el.Name == (xml.Name{Space: craftSpreadsheetNS, Local: "dimension"}):
				if state.dimension != (nativeAgentEngineArea{}) || len(el.Attr) != 1 {
					return craftWorksheetState{}, errCraftWorksheetXML
				}
				area, ok := nativeAgentEngineRange(json.RawMessage(strconv.Quote(craftWorkbookAttr(el, "ref"))))
				if !ok {
					return craftWorksheetState{}, errCraftWorksheetXML
				}
				state.dimension = area
				// Keep the element and its position in the XML projection. Compare
				// the range itself to the observed cell bounds after scanning.
				el.Attr = []xml.Attr{{Name: xml.Name{Local: "ref"}}}
				tok = el
			case len(stack) == 1 && el.Name == (xml.Name{Space: craftSpreadsheetNS, Local: "sheetData"}):
				if seenData {
					return craftWorksheetState{}, errCraftWorksheetXML
				}
				seenData = true
			case len(stack) == 2 && stack[1] == (xml.Name{Space: craftSpreadsheetNS, Local: "sheetData"}) &&
				el.Name == (xml.Name{Space: craftSpreadsheetNS, Local: "row"}):
				n, err := strconv.Atoi(craftWorkbookAttr(el, "r"))
				if err != nil || n <= lastRow || n > nativeAgentMaxRows {
					return craftWorksheetState{}, errCraftWorksheetXML
				}
				row, lastRow, lastCol = n, n, 0
				if len(el.Attr) == 1 {
					copy := el.Copy()
					rowStart = &copy
					stack = append(stack, el.Name)
					continue
				}
			case row != 0 && len(stack) == 3 && el.Name == (xml.Name{Space: craftSpreadsheetNS, Local: "c"}):
				col, cellRow, ok := nativeAgentEngineAddress(craftWorkbookAttr(el, "r"))
				if !ok || cellRow != row || col <= lastCol {
					return craftWorksheetState{}, errCraftWorksheetXML
				}
				lastCol = col
				cellBounds := nativeAgentEngineArea{col1: col, row1: row, col2: col, row2: row}
				state.cells = craftWorksheetEnclose(state.cells, cellBounds)
				addressed := edits.includes(sheet, col, row)
				var observe func([32]byte) error
				if addressed && edits.observe != nil {
					observe = func(input [32]byte) error { return edits.observe(sheet, col, row, input) }
				}
				cell, keep, err := craftWorksheetCell(d, el, addressed, edits.allowScalarValues, observe)
				if err != nil {
					return craftWorksheetState{}, err
				}
				if keep {
					flushRow()
					out.field("cell")
					_, _ = out.Write(cell[:])
				}
				continue
			}
			flushRow()
			stack = append(stack, el.Name)
		case xml.EndElement:
			if len(stack) == 3 && row != 0 {
				row = 0
				stack = stack[:2]
				if rowStart != nil {
					rowStart = nil
					continue
				}
				out.token(el)
				continue
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if strings.TrimSpace(string(el)) == "" && (len(stack) <= 1 || len(stack) == 2 && stack[1].Local == "sheetData" || len(stack) == 3 && row != 0) {
				continue
			}
			if len(stack) == 0 {
				return craftWorksheetState{}, errCraftWorksheetXML
			}
			flushRow()
		case xml.Directive:
			return craftWorksheetState{}, errCraftWorksheetXML
		default:
			flushRow()
		}
		out.token(tok)
	}
}

// A simple cell has a known scalar type, optional style, a plain formula or
// value, and no unknown attributes or child elements. An edit can replace its
// content, while its style and address stay in the projection.
func craftWorksheetCell(d *xml.Decoder, el xml.StartElement, addressed, allowScalar bool, observe func([32]byte) error) ([32]byte, bool, error) {
	cellType := craftWorkbookAttr(el, "t")
	simple := true
	switch cellType {
	case "", "n", "b", "e", "str", "s", "inlineStr", "d":
	default:
		simple = false
	}
	meta := craftWorksheetHash{sha256.New()}
	for _, a := range el.Attr {
		if a.Name.Space != "" || (a.Name.Local != "r" && a.Name.Local != "s" && a.Name.Local != "t") {
			simple = false
		}
	}
	meta.token(el)
	all := craftWorksheetHash{sha256.New()}
	var formula, value, inline craftCellXML
	for {
		tok, err := d.Token()
		if err != nil {
			return [32]byte{}, false, err
		}
		if _, ok := tok.(xml.EndElement); ok {
			break
		}
		if space, ok := tok.(xml.CharData); ok && strings.TrimSpace(string(space)) == "" {
			continue
		}
		child, ok := tok.(xml.StartElement)
		if !ok {
			return [32]byte{}, false, errCraftWorksheetXML
		}
		node, err := craftReadCellXML(d, child)
		if err != nil {
			return [32]byte{}, false, err
		}
		_, _ = all.Write(node.digest[:])
		if child.Name.Space == craftSpreadsheetNS {
			var field *craftCellXML
			switch child.Name.Local {
			case "f":
				field = &formula
			case "v":
				field = &value
			case "is":
				field = &inline
			}
			if field != nil && !field.present {
				*field = node
				continue
			}
		}
		simple = false
	}
	simple = simple && (!formula.present || formula.plain && formula.nonempty) &&
		(!value.present || value.plain) && (!inline.present || inline.plain && !formula.present && !value.present)
	if simple && observe != nil {
		var input [32]byte
		switch {
		case formula.present:
			input = formula.digest
		case inline.present:
			input = inline.digest
		case value.nonempty:
			h := craftWorksheetHash{sha256.New()}
			if cellType == "n" {
				cellType = ""
			}
			h.field(cellType)
			_, _ = h.Write(value.digest[:])
			input = h.sum()
		}
		if err := observe(input); err != nil {
			return [32]byte{}, false, err
		}
	}
	if simple && (addressed || allowScalar && !formula.present) {
		style := craftWorkbookAttr(el, "s")
		if style == "" || style == "0" {
			return [32]byte{}, false, nil
		}
		edited := craftWorksheetHash{sha256.New()}
		edited.field(craftWorkbookAttr(el, "r"))
		edited.field(style)
		return edited.sum(), true, nil
	}
	if simple && formula.present && value.nonempty && cellType != "e" {
		meta.field("recalculated formula")
		_, _ = meta.Write(formula.digest[:])
	} else {
		_, _ = meta.Write(all.Sum(nil))
	}
	return meta.sum(), true, nil
}

type craftCellXML struct {
	present, plain, nonempty bool
	digest                   [32]byte
}

func craftReadCellXML(d *xml.Decoder, el xml.StartElement) (craftCellXML, error) {
	out := craftWorksheetHash{sha256.New()}
	out.token(el)
	result := craftCellXML{present: true, plain: len(el.Attr) == 0}
	inline := el.Name == (xml.Name{Space: craftSpreadsheetNS, Local: "is"})
	var normalized craftWorksheetHash
	if inline && result.plain {
		normalized.Hash = sha256.New()
		normalized.token(el)
	}
	depth, children := 1, 0
	for depth > 0 {
		tok, err := d.Token()
		if err != nil {
			return craftCellXML{}, err
		}
		canonical := tok
		switch child := tok.(type) {
		case xml.StartElement:
			if depth >= 256 {
				return craftCellXML{}, errCraftWorksheetXML
			}
			children++
			result.plain = result.plain && inline && depth == 1 && children == 1 &&
				child.Name == (xml.Name{Space: craftSpreadsheetNS, Local: "t"}) &&
				(len(child.Attr) == 0 || len(child.Attr) == 1 &&
					child.Attr[0].Name == craftXMLSpace)
			if result.plain && len(child.Attr) == 1 && child.Attr[0].Value == "preserve" {
				child.Attr = nil
				canonical = child
			} else {
				normalized.Hash = nil
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			// OOXML escapes can encode whitespace even when the XML text has
			// none. Keep the original attribute when either is possible.
			if normalized.Hash != nil && depth == 2 && strings.ContainsAny(string(child), " \t\r\n_") {
				normalized.Hash = nil
			}
			if strings.TrimSpace(string(child)) != "" {
				result.nonempty = true
				if inline && depth == 1 {
					result.plain = false
				}
			}
		case xml.Directive:
			return craftCellXML{}, errCraftWorksheetXML
		default:
			result.plain = false
		}
		out.token(tok)
		if normalized.Hash != nil && result.plain {
			normalized.token(canonical)
		}
	}
	result.plain = result.plain && (!inline || children == 1)
	result.digest = out.sum()
	// GridCraft marks inline text with xml:space="preserve" when replay
	// exports it. For a plain string without whitespace this is redundant.
	if result.plain && normalized.Hash != nil {
		result.digest = normalized.sum()
	}
	return result, nil
}

func craftWorkbookAttr(el xml.StartElement, name string) string {
	for _, a := range el.Attr {
		if a.Name == (xml.Name{Local: name}) {
			return a.Value
		}
	}
	return ""
}

type craftWorksheetHash struct{ hash.Hash }

func (h craftWorksheetHash) field(value string) {
	_, _ = io.WriteString(h, strconv.Itoa(len(value))+":")
	_, _ = io.WriteString(h, value)
}

func (h craftWorksheetHash) sum() (sum [32]byte) {
	h.Sum(sum[:0])
	return sum
}

func (h craftWorksheetHash) token(tok xml.Token) {
	name := func(n xml.Name) {
		h.field(n.Space)
		h.field(n.Local)
	}
	switch tok := tok.(type) {
	case xml.StartElement:
		h.field("start")
		name(tok.Name)
		sort.Slice(tok.Attr, func(i, j int) bool {
			if tok.Attr[i].Name.Space != tok.Attr[j].Name.Space {
				return tok.Attr[i].Name.Space < tok.Attr[j].Name.Space
			}
			return tok.Attr[i].Name.Local < tok.Attr[j].Name.Local
		})
		h.field(strconv.Itoa(len(tok.Attr)))
		for _, attr := range tok.Attr {
			name(attr.Name)
			h.field(attr.Value)
		}
	case xml.EndElement:
		h.field("end")
		name(tok.Name)
	case xml.CharData:
		h.field("text")
		h.field(string(tok))
	case xml.Comment:
		h.field("comment")
		h.field(string(tok))
	case xml.ProcInst:
		h.field("instruction")
		h.field(tok.Target)
		h.field(string(tok.Inst))
	}
}
