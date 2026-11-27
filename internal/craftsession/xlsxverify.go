package craftsession

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

// After the engine converted a workbook, every cell of the file that carries something is looked
// up in the native document at the same sheet (by name) and the same address. A cell may be
// missing only when the engine provably drops it as equivalent: no value, no formula, no style
// and no cell metadata (the engine's cell store removes such a blank cell, CellStore::set). Any
// other difference refuses the import and says where, instead of storing a shortened workbook.

// LostCells counts one sheet's cells of one kind that the engine did not carry over.
type LostCells struct {
	Sheet int    `json:"sheet"` // position in the workbook, from 1
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
}

// Kinds of LostCells.
const (
	LostValue        = "value"        // a value cell is missing, or its value has another type
	LostFormula      = "formula"      // a formula cell is missing or kept without its formula
	LostStyledEmpty  = "styled_empty" // an empty cell with a style is missing
	LostArrayResult  = "array_result" // a cached result inside an array formula's range is missing
	LostDuplicate    = "duplicate"    // the file has two cells at one address; one would be overwritten
	LostMetadata     = "metadata"     // a cell with value metadata (an in-cell picture) is missing
	LostAddress      = "address"      // a cell with content has an address the engine cannot read
	LostSheet        = "sheet"        // a worksheet of the workbook is not in the document
	maxLostReported  = 64
	maxSheetRelation = 512
)

// TruncatedError is ErrTruncated with what was lost, per sheet and kind.
type TruncatedError struct{ Lost []LostCells }

func (e *TruncatedError) Error() string {
	var b strings.Builder
	b.WriteString(ErrTruncated.Error())
	for i, l := range e.Lost {
		if i == 8 {
			fmt.Fprintf(&b, "; and %d more", len(e.Lost)-i)
			break
		}
		fmt.Fprintf(&b, "; sheet %d: %d %s", l.Sheet, l.Count, l.Kind)
	}
	return b.String()
}
func (e *TruncatedError) Unwrap() error { return ErrTruncated }

// xlsxCell is one <c> of a worksheet part, without its content.
type xlsxCell struct {
	row, col uint32
	address  bool   // the r attribute (or the position) is a readable address
	typ      string // the t attribute, "n" by default
	value    bool   // a non-blank <v>, or an <is>
	formula  bool   // an <f> the engine keeps as a formula
	styled   bool   // s other than 0
	metadata bool   // vm other than 0
	array    string // the ref of an array or dynamic-array formula anchored here
}

// blank is a cell the engine drops as equivalent to no cell.
func (c xlsxCell) blank() bool {
	return !c.value && !c.formula && !c.styled && !c.metadata
}

// nativeKind is the typed value tag the engine writes for a cell of this XML type.
func (c xlsxCell) nativeKind() string {
	switch c.typ {
	case "s", "str", "inlineStr":
		return "Text"
	case "b":
		return "Bool"
	case "e":
		return "Error"
	default: // "n", "d" and anything unknown are read as numbers
		return "Number"
	}
}

// workbookSheet is a sheet listed in xl/workbook.xml with the part its relationship names.
type workbookSheet struct {
	name, part, relationship string
}

// workbookSheets lists the sheets in workbook order. A sheet whose relationship is missing is an
// unreadable workbook, not an empty sheet.
func workbookSheets(zr *zip.Reader) ([]workbookSheet, error) {
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	read := func(name string) ([]byte, error) {
		f := files[name]
		if f == nil || f.UncompressedSize64 > 16<<20 {
			return nil, ErrNotXLSX
		}
		rc, err := f.Open()
		if err != nil {
			return nil, ErrNotXLSX
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			return nil, ErrNotXLSX
		}
		return b, nil
	}
	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	var rels struct {
		Rel []struct {
			ID     string `xml:"Id,attr"`
			Type   string `xml:"Type,attr"`
			Target string `xml:"Target,attr"`
			Mode   string `xml:"TargetMode,attr"`
		} `xml:"Relationship"`
	}
	raw, err := read("xl/workbook.xml")
	if err != nil || xml.Unmarshal(raw, &wb) != nil {
		return nil, ErrNotXLSX
	}
	raw, err = read("xl/_rels/workbook.xml.rels")
	if err != nil || xml.Unmarshal(raw, &rels) != nil {
		return nil, ErrNotXLSX
	}
	target := map[string]workbookSheet{}
	for _, r := range rels.Rel {
		if strings.EqualFold(r.Mode, "External") {
			continue
		}
		p := strings.ReplaceAll(r.Target, "\\", "/")
		if strings.HasPrefix(p, "/") {
			p = path.Clean(strings.TrimPrefix(p, "/"))
		} else {
			p = path.Clean("xl/" + p)
		}
		if _, exists := target[r.ID]; r.ID == "" || exists {
			return nil, ErrNotXLSX
		}
		target[r.ID] = workbookSheet{part: p, relationship: r.Type}
	}
	if len(wb.Sheets) == 0 || len(wb.Sheets) > maxSheetRelation {
		return nil, ErrNotXLSX
	}
	out := make([]workbookSheet, 0, len(wb.Sheets))
	for _, s := range wb.Sheets {
		entry, ok := target[s.RID]
		if !ok || files[entry.part] == nil {
			return nil, ErrNotXLSX
		}
		entry.name = s.Name
		out = append(out, entry)
	}
	return out, nil
}

// WorkbookWorksheetParts maps internal worksheet parts to their original, case-preserving sheet names.
func WorkbookWorksheetParts(zr *zip.Reader) (map[string]string, error) {
	sheets, err := workbookSheets(zr)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(sheets))
	names := make(map[string]bool, len(sheets))
	for _, sheet := range sheets {
		if sheet.relationship != "http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" {
			continue
		}
		name := strings.ToLower(sheet.name)
		if name == "" || names[name] || out[sheet.part] != "" || !strings.HasPrefix(sheet.part, "xl/worksheets/") {
			return nil, ErrNotXLSX
		}
		names[name] = true
		out[sheet.part] = sheet.name
	}
	return out, nil
}

// readCells streams a worksheet part and calls fn for every <c>.
func readCells(f *zip.File, max int64, fn func(xlsxCell) error) error {
	rc, err := f.Open()
	if err != nil {
		return ErrNotXLSX
	}
	defer rc.Close()
	dec := xml.NewDecoder(io.LimitReader(rc, max+1))
	dec.Strict = false
	var cur *xlsxCell
	inV, inF := false, false
	var text, ftext strings.Builder
	var fkind, fref string
	var fsi bool
	row, nextCol := uint32(0), uint32(0)
	nextRow := uint32(0)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return ErrNotXLSX
		}
		switch el := tok.(type) {
		case xml.StartElement:
			switch el.Name.Local {
			case "row":
				row = nextRow
				for _, a := range el.Attr {
					if a.Name.Local == "r" {
						if n, err := strconv.ParseUint(strings.TrimSpace(a.Value), 10, 32); err == nil && n > 0 {
							row = uint32(n - 1)
						}
					}
				}
				nextRow, nextCol = row+1, 0
			case "c":
				c := xlsxCell{typ: "n", address: true, row: row, col: nextCol}
				for _, a := range el.Attr {
					switch a.Name.Local {
					case "r":
						r, col, ok := parseCellRef(a.Value)
						c.row, c.col, c.address = r, col, ok
					case "t":
						c.typ = a.Value
					case "s":
						c.styled = strings.TrimSpace(a.Value) != "" && strings.TrimSpace(a.Value) != "0"
					case "vm":
						c.metadata = strings.TrimSpace(a.Value) != "0"
					}
				}
				if c.address {
					nextCol = c.col + 1
				}
				cur = &c
			case "v":
				if cur != nil {
					inV = true
					text.Reset()
				}
			case "is":
				if cur != nil {
					cur.value = true
				}
			case "f":
				if cur != nil {
					inF = true
					ftext.Reset()
					fkind, fref, fsi = "normal", "", false
					for _, a := range el.Attr {
						switch a.Name.Local {
						case "t":
							fkind = a.Value
						case "ref":
							fref = a.Value
						case "si":
							fsi = true
						}
					}
				}
			}
		case xml.CharData:
			if inV {
				text.Write(el)
			} else if inF {
				ftext.Write(el)
			}
		case xml.EndElement:
			switch el.Name.Local {
			case "v":
				if cur != nil && strings.TrimSpace(text.String()) != "" {
					cur.value = true
				}
				inV = false
			case "f":
				if cur != nil {
					body := strings.TrimSpace(ftext.String()) != ""
					switch fkind {
					case "shared":
						cur.formula = fsi || body // a dependent cell takes its master's formula
					case "dataTable": // kept as values, the engine warns
					case "array":
						cur.formula = body
						if body {
							cur.array = fref
						}
					default:
						cur.formula = body
					}
				}
				inF = false
			case "c":
				if cur != nil {
					if err := fn(*cur); err != nil {
						return err
					}
					cur = nil
				}
			}
		}
	}
}

// parseCellRef reads "AB12" (or "$AB$12") into zero-based row and column.
func parseCellRef(ref string) (uint32, uint32, bool) {
	ref = strings.ReplaceAll(strings.TrimSpace(ref), "$", "")
	i, col := 0, 0
	for i < len(ref) && i < 3 && (ref[i] >= 'A' && ref[i] <= 'Z' || ref[i] >= 'a' && ref[i] <= 'z') {
		col = col*26 + int((ref[i]|0x20)-'a'+1)
		i++
	}
	if i == 0 || i == len(ref) {
		return 0, 0, false
	}
	r, err := strconv.ParseUint(ref[i:], 10, 32)
	if err != nil || r == 0 || r > 1_048_576 || col > 16_384 {
		return 0, 0, false
	}
	return uint32(r - 1), uint32(col - 1), true
}

// cellRange is an inclusive rectangle of zero-based addresses.
type cellRange struct{ r0, c0, r1, c1 uint32 }

func parseRange(ref string) (cellRange, bool) {
	a, b, found := strings.Cut(ref, ":")
	r0, c0, ok := parseCellRef(a)
	if !ok {
		return cellRange{}, false
	}
	r1, c1 := r0, c0
	if found {
		if r1, c1, ok = parseCellRef(b); !ok {
			return cellRange{}, false
		}
	}
	if r1 < r0 {
		r0, r1 = r1, r0
	}
	if c1 < c0 {
		c0, c1 = c1, c0
	}
	return cellRange{r0, c0, r1, c1}, true
}

func (r cellRange) holds(row, col uint32) bool {
	return row >= r.r0 && row <= r.r1 && col >= r.c0 && col <= r.c1
}

// nativeCell is what the check reads of a native cell record.
type nativeCell struct {
	kind    string
	formula bool
	style   bool
}

type nativePresence bool

func (p *nativePresence) UnmarshalJSON(b []byte) error {
	*p = nativePresence(!bytes.Equal(bytes.TrimSpace(b), []byte("null")))
	return nil
}

// nativeSheets reads the sheets of a native document: name and cell records by address.
func nativeSheets(doc []byte) (map[string]map[uint64]nativeCell, error) {
	var parsed struct {
		Sheets []struct {
			Name  string `json:"name"`
			Cells struct {
				Bands map[string]struct {
					Rows map[string]map[string]struct {
						Value struct {
							T string `json:"t"`
						} `json:"value"`
						Formula nativePresence `json:"formula"`
						Style   int64          `json:"style"`
					} `json:"rows"`
				} `json:"bands"`
			} `json:"cells"`
		} `json:"sheets"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		return nil, ErrImportFailed
	}
	out := make(map[string]map[uint64]nativeCell, len(parsed.Sheets))
	for _, s := range parsed.Sheets {
		if _, dup := out[s.Name]; dup {
			return nil, ErrImportFailed
		}
		cells := map[uint64]nativeCell{}
		for _, band := range s.Cells.Bands {
			for r, cols := range band.Rows {
				row, err := strconv.ParseUint(r, 10, 32)
				if err != nil {
					return nil, ErrImportFailed
				}
				for c, cell := range cols {
					col, err := strconv.ParseUint(c, 10, 32)
					if err != nil {
						return nil, ErrImportFailed
					}
					cells[row<<32|col] = nativeCell{kind: cell.Value.T, formula: bool(cell.Formula), style: cell.Style != 0}
				}
			}
		}
		out[s.Name] = cells
	}
	return out, nil
}

// verifyKept compares the workbook with the engine's native document and returns a
// *TruncatedError naming what was not carried over.
func verifyKept(data, doc []byte, maxPart int64) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ErrNotXLSX
	}
	sheets, err := workbookSheets(zr)
	if err != nil {
		return err
	}
	native, err := nativeSheets(doc)
	if err != nil {
		return err
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	var lost []LostCells
	for i, s := range sheets {
		if !strings.HasPrefix(s.part, "xl/worksheets/") {
			continue // chart, dialog and macro sheets hold no cells
		}
		kept, ok := native[s.name]
		counts := map[string]int64{}
		seen := map[uint64]bool{}
		var arrays []cellRange
		err := readCells(files[s.part], maxPart, func(c xlsxCell) error {
			if c.blank() {
				return nil
			}
			if !ok {
				counts[LostSheet]++
				return nil
			}
			if !c.address {
				counts[LostAddress]++
				return nil
			}
			key := uint64(c.row)<<32 | uint64(c.col)
			if seen[key] {
				counts[LostDuplicate]++
				return nil
			}
			seen[key] = true
			if c.array != "" {
				if r, ok := parseRange(c.array); ok && (r.r0 != r.r1 || r.c0 != r.c1) {
					arrays = append(arrays, r)
				}
			}
			n, present := kept[key]
			switch {
			case c.formula && (!present || !n.formula):
				counts[LostFormula]++
			case c.formula:
				// the cached result of a formula is the engine's to recompute
			case c.value && (!present || n.kind != c.nativeKind()) && inRanges(arrays, c.row, c.col):
				counts[LostArrayResult]++
			case c.value && (!present || n.kind != c.nativeKind()):
				counts[LostValue]++
			case !present && c.metadata:
				counts[LostMetadata]++
			case !present && c.styled:
				counts[LostStyledEmpty]++
			}
			return nil
		})
		if err != nil {
			return err
		}
		kinds := make([]string, 0, len(counts))
		for k := range counts {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			lost = append(lost, LostCells{Sheet: i + 1, Name: s.name, Kind: k, Count: counts[k]})
		}
	}
	if len(lost) > 0 {
		if len(lost) > maxLostReported {
			lost = lost[:maxLostReported]
		}
		return &TruncatedError{Lost: lost}
	}
	return nil
}

func inRanges(rs []cellRange, row, col uint32) bool {
	for _, r := range rs {
		if r.holds(row, col) {
			return true
		}
	}
	return false
}
