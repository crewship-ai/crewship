package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

// Server integration tests of a workbook Sheet (xlsx/xlsm kept as the file itself): the agent's
// whole path from a chat message to a stored version and its operations, concurrency, security
// and fidelity. Real: the chat bridge, the run admission, the sidecar's MCP server and its
// transport, the router fence, the Sheet tools, the lease, the version store, craft_version_ops
// and the person's document API. Replaced: the model (the test calls the tools), the container
// runtime and the GridCraft engine, whose stand-in edits the worksheet XML of the real archive and
// copies every other part byte for byte. Every workbook is synthetic.

// sitPart is one part of a synthetic workbook.
type sitPart struct{ name, body string }

// sitCells is one sheet's cells by address ("B8"), as a person typed them.
type sitCells map[string]string

// sitWorkbook is a synthetic xlsm: sheet L (a header row Name|Status and eight rows), sheet Other,
// an unsigned VBA project and two pivot caches: one over
// L!A1:B9, one over Other!A1:A3.
func sitWorkbook(l, other sitCells, extra ...sitPart) []byte {
	parts := sitBaseParts()
	parts = append(parts, sitPart{"xl/worksheets/sheet1.xml", sitSheetXML(l)}, sitPart{"xl/worksheets/sheet2.xml", sitSheetXML(other)})
	parts = append(parts, extra...)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range parts {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: p.name, Method: zip.Deflate, Modified: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			panic(err)
		}
		_, _ = w.Write([]byte(p.body))
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func sitBaseParts() []sitPart {
	vba := make([]byte, 0, 4096)
	vba = append(vba, "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"...)
	for i := 0; len(vba) < 4096; i++ {
		vba = append(vba, byte(i*31+7))
	}
	return []sitPart{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="bin" ContentType="application/vnd.ms-office.vbaProject"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.ms-excel.sheet.macroEnabled.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/worksheets/sheet2.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="L" sheetId="1" r:id="rId1"/><sheet name="Other" sheetId="2" r:id="rId2"/></sheets><pivotCaches><pivotCache cacheId="1" r:id="rId4"/><pivotCache cacheId="2" r:id="rId5"/></pivotCaches></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/><Relationship Id="rId3" Type="http://schemas.microsoft.com/office/2006/relationships/vbaProject" Target="vbaProject.bin"/><Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/pivotCacheDefinition" Target="pivotCache/pivotCacheDefinition1.xml"/><Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/pivotCacheDefinition" Target="pivotCache/pivotCacheDefinition2.xml"/></Relationships>`},
		{"xl/vbaProject.bin", string(vba)},
		{"xl/pivotCache/pivotCacheDefinition1.xml", `<?xml version="1.0" encoding="UTF-8"?><pivotCacheDefinition xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" recordCount="8"><cacheSource type="worksheet"><worksheetSource ref="A1:B9" sheet="L"/></cacheSource><cacheFields count="2"><cacheField name="Name"/><cacheField name="Status"/></cacheFields></pivotCacheDefinition>`},
		{"xl/pivotCache/pivotCacheDefinition2.xml", `<?xml version="1.0" encoding="UTF-8"?><pivotCacheDefinition xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" recordCount="2"><cacheSource type="worksheet"><worksheetSource ref="A1:A3" sheet="Other"/></cacheSource><cacheFields count="1"><cacheField name="Code"/></cacheFields></pivotCacheDefinition>`},
		{"customXml/item1.xml", `<?xml version="1.0" encoding="UTF-8"?><sit:data xmlns:sit="urn:synthetic:sit">synthetic custom data</sit:data>`},
		{"customXml/itemProps1.xml", `<?xml version="1.0" encoding="UTF-8"?><ds:datastoreItem xmlns:ds="http://schemas.openxmlformats.org/officeDocument/2006/customXml" ds:itemID="{00000000-0000-0000-0000-0000000000T2}"/>`},
		{"customXml/_rels/item1.xml.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/customXmlProps" Target="itemProps1.xml"/></Relationships>`},
		{"xl/pivotTables/pivotTable1.xml", `<?xml version="1.0" encoding="UTF-8"?><pivotTableDefinition xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" name="P1" cacheId="1"/>`},
	}
}

var sitCellRef = regexp.MustCompile(`^([A-Z]{1,3})([0-9]{1,7})$`)

func sitColRow(address string) (int, int) {
	m := sitCellRef.FindStringSubmatch(address)
	if m == nil {
		return 0, 0
	}
	col := 0
	for _, r := range m[1] {
		col = col*26 + int(r-'A') + 1
	}
	row, _ := strconv.Atoi(m[2])
	return col, row
}

func sitAddress(col, row int) string {
	name := ""
	for n := col; n > 0; {
		n--
		name = string(rune('A'+n%26)) + name
		n /= 26
	}
	return name + strconv.Itoa(row)
}

// sitSheetXML writes the cells in row order: numbers as <v>, everything else as an inline string.
func sitSheetXML(cells sitCells) string {
	type cell struct {
		col, row int
		value    string
	}
	var list []cell
	for a, v := range cells {
		if v == "" {
			continue
		}
		c, r := sitColRow(a)
		list = append(list, cell{c, r, v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].row != list[j].row {
			return list[i].row < list[j].row
		}
		return list[i].col < list[j].col
	})
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	row := 0
	for _, c := range list {
		if c.row != row {
			if row != 0 {
				b.WriteString(`</row>`)
			}
			row = c.row
			fmt.Fprintf(&b, `<row r="%d">`, row)
		}
		if expr, cached, ok := sitFormula(c.value); ok {
			var f, v bytes.Buffer
			_ = xmlEscapeText(&f, expr)
			_ = xmlEscapeText(&v, cached)
			fmt.Fprintf(&b, `<c r="%s" t="str"><f>%s</f><v>%s</v></c>`, sitAddress(c.col, c.row), f.String(), v.String())
		} else if _, err := strconv.ParseFloat(c.value, 64); err == nil {
			fmt.Fprintf(&b, `<c r="%s"><v>%s</v></c>`, sitAddress(c.col, c.row), c.value)
		} else {
			// A leading ' keeps a number-like input as text (the person's "001").
			var esc bytes.Buffer
			_ = xmlEscapeText(&esc, strings.TrimPrefix(c.value, "'"))
			fmt.Fprintf(&b, `<c r="%s" t="inlineStr"><is><t>%s</t></is></c>`, sitAddress(c.col, c.row), esc.String())
		}
	}
	if row != 0 {
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

func xmlEscapeText(w io.Writer, s string) error {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	_, err := io.WriteString(w, r.Replace(s))
	return err
}

var sitCellXML = regexp.MustCompile(`<c r="([A-Z]+[0-9]+)"(?: t="inlineStr")?>(?:<v>([^<]*)</v>|<is><t>([^<]*)</t></is>)</c>`)

var sitFormulaXML = regexp.MustCompile(`<c r="([A-Z]+[0-9]+)" t="str"><f>([^<]*)</f><v>([^<]*)</v></c>`)

// sitFormula splits a formula cell, kept as "=EXPR|cached", into its expression and cached value.
func sitFormula(v string) (expr, cached string, ok bool) {
	if !strings.HasPrefix(v, "=") || !strings.Contains(v, "|") {
		return "", "", false
	}
	i := strings.LastIndex(v, "|")
	return v[1:i], v[i+1:], true
}

// sitRecalc is the stand-in for the engine's calculation: a formula Sheet!A1&"+"&Sheet!B2 joins the
// values it names with "+". Every formula cell of every sheet gets its cached value again.
func sitRecalc(sheets map[string]sitCells) {
	for _, cells := range sheets {
		for a, v := range cells {
			expr, _, ok := sitFormula(v)
			if !ok {
				continue
			}
			var parts []string
			for _, ref := range strings.Split(expr, `&"+"&`) {
				sheet, cell, _ := strings.Cut(ref, "!")
				parts = append(parts, strings.TrimPrefix(sheets[sheet][cell], "'"))
			}
			cells[a] = "=" + expr + "|" + strings.Join(parts, "+")
		}
	}
}

func sitParseSheet(x string) sitCells { return sitReadCells(x, false) }

func sitReadCells(x string, keepInputType bool) sitCells {
	out := sitCells{}
	unescape := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")
	for _, m := range sitFormulaXML.FindAllStringSubmatch(x, -1) {
		out[m[1]] = "=" + unescape.Replace(m[2]) + "|" + unescape.Replace(m[3])
	}
	for _, m := range sitCellXML.FindAllStringSubmatch(x, -1) {
		value := unescape.Replace(m[2] + m[3])
		if keepInputType && m[3] != "" {
			if _, err := strconv.ParseFloat(value, 64); err == nil || strings.HasPrefix(value, "=") || strings.HasPrefix(value, "'") {
				value = "'" + value
			}
		}
		out[m[1]] = value
	}
	return out
}

// sitParts reads every part of an archive: name → bytes.
func sitParts(t testing.TB, raw []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = b
	}
	return out
}

func sitSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// sitSheets maps the sheet names of the synthetic workbook to their parts.
var sitSheets = map[string]string{"L": "xl/worksheets/sheet1.xml", "Other": "xl/worksheets/sheet2.xml"}

// sitEngine stands in for the GridCraft engine over a real archive: it opens exactly the bytes it is
// given, edits only the worksheet XML, and writes every other part back byte for byte (raw copy).
// drop, when set, names parts its snapshot leaves out while still reporting no loss (a faulty
// engine); loss, when set, is the fidelity loss its snapshot reports.
type sitEngine struct {
	mu      sync.Mutex
	opens   int
	applies int
	drop    map[string]bool
	edit    func(part, xml string) string
	loss    *craftsession.FidelityLossError
}

func (e *sitEngine) Open(_ context.Context, base []byte) (CraftNativeSheetSession, func(), error) {
	zr, err := zip.NewReader(bytes.NewReader(base), int64(len(base)))
	if err != nil {
		return nil, nil, err
	}
	s := &sitSession{engine: e, base: base, cells: map[string]sitCells{}}
	for name, part := range sitSheets {
		for _, f := range zr.File {
			if f.Name == part {
				rc, _ := f.Open()
				b, _ := io.ReadAll(rc)
				rc.Close()
				s.cells[name] = sitReadCells(string(b), true)
			}
		}
	}
	e.mu.Lock()
	e.opens++
	e.mu.Unlock()
	return s, func() {}, nil
}
func (e *sitEngine) Retain(CraftNativeSheetSession, []byte) {}
func (e *sitEngine) Discard(CraftNativeSheetSession)        {}

func (e *sitEngine) count() (opens, applies int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.opens, e.applies
}

type sitSession struct {
	engine *sitEngine
	base   []byte
	cells  map[string]sitCells
}

var sitQualified = regexp.MustCompile(`^'([^']+)'!([A-Z]+[0-9]+)(?::([A-Z]+[0-9]+))?$`)

// split reads 'Sheet'!A1[:B2]; an address without its sheet is refused, like a workbook file on the
// server has no active sheet the person sees.
func (s *sitSession) split(address string) (sheet string, c0, r0, c1, r1 int, err error) {
	m := sitQualified.FindStringSubmatch(address)
	if m == nil || s.cells[m[1]] == nil {
		return "", 0, 0, 0, 0, fmt.Errorf("sit engine: unqualified or unknown address %q", address)
	}
	c0, r0 = sitColRow(m[2])
	c1, r1 = c0, r0
	if m[3] != "" {
		c1, r1 = sitColRow(m[3])
	}
	return m[1], c0, r0, c1, r1, nil
}

func (s *sitSession) GetCell(_ context.Context, cell string) (CraftNativeSheetCell, error) {
	sheet, c, r, _, _, err := s.split(cell)
	if err != nil {
		return CraftNativeSheetCell{}, err
	}
	v := s.cells[sheet][sitAddress(c, r)]
	if expr, cached, ok := sitFormula(v); ok {
		return CraftNativeSheetCell{Cell: cell, Input: "=" + expr, Formula: "=" + expr, Value: cached}, nil
	}
	return CraftNativeSheetCell{Cell: cell, Input: v, Value: v}, nil
}
func (s *sitSession) ReadRange(_ context.Context, rng string, formulas bool) (CraftNativeSheetValues, error) {
	sheet, c0, r0, c1, r1, err := s.split(rng)
	if err != nil {
		return CraftNativeSheetValues{}, err
	}
	out := CraftNativeSheetValues{Range: rng, Sheet: sheet}
	for r := r0; r <= r1; r++ {
		var line []any
		for c := c0; c <= c1; c++ {
			v := s.cells[sheet][sitAddress(c, r)]
			if expr, cached, ok := sitFormula(v); ok {
				if formulas {
					v = "=" + expr
				} else {
					v = cached
				}
			}
			line = append(line, v)
		}
		out.Values = append(out.Values, line)
	}
	return out, nil
}
func (s *sitSession) SetCell(_ context.Context, cell, input string) error {
	sheet, c, r, _, _, err := s.split(cell)
	if err != nil {
		return err
	}
	s.cells[sheet][sitAddress(c, r)] = sitTyped(input)
	return nil
}

// sitTyped keeps a written formula as one (its value is calculated at the snapshot).
func sitTyped(input string) string {
	if strings.HasPrefix(input, "=") && !strings.Contains(input, "|") {
		return input + "|"
	}
	return input
}
func (s *sitSession) WriteRange(_ context.Context, rng string, values [][]any) (int, error) {
	sheet, c0, r0, _, _, err := s.split(rng)
	if err != nil {
		return 0, err
	}
	n := 0
	for i, row := range values {
		for j, v := range row {
			text := ""
			if v != nil {
				text = fmt.Sprint(v)
			}
			s.cells[sheet][sitAddress(c0+j, r0+i)] = sitTyped(text)
			n++
		}
	}
	return n, nil
}
func (s *sitSession) Apply(ctx context.Context, ops []CraftNativeSheetWrite) (int, error) {
	s.engine.mu.Lock()
	s.engine.applies++
	s.engine.mu.Unlock()
	for i, op := range ops {
		var err error
		if op.Cell != "" {
			err = s.SetCell(ctx, op.Cell, op.Input)
		} else {
			_, err = s.WriteRange(ctx, op.Range, op.Values)
		}
		if err != nil {
			return i, err
		}
	}
	return len(ops), nil
}
func (s *sitSession) ClearRange(_ context.Context, rng string) error {
	sheet, c0, r0, c1, r1, err := s.split(rng)
	if err != nil {
		return err
	}
	for r := r0; r <= r1; r++ {
		for c := c0; c <= c1; c++ {
			delete(s.cells[sheet], sitAddress(c, r))
		}
	}
	return nil
}

// ColumnsByHeader finds header in any row of the named sheet (or every sheet) and answers the column
// under it down to its last filled cell, as {columns:[{sheet,headerCell,range}]}.
func (s *sitSession) ColumnsByHeader(_ context.Context, header, sheet string, matchCase bool) (json.RawMessage, error) {
	type column struct {
		Sheet      string  `json:"sheet"`
		HeaderCell string  `json:"headerCell"`
		Range      *string `json:"range"`
	}
	out := struct {
		Columns []column `json:"columns"`
	}{Columns: []column{}}
	for _, name := range []string{"L", "Other"} {
		if sheet != "" && sheet != name {
			continue
		}
		for a, v := range s.cells[name] {
			if v != header && (matchCase || !strings.EqualFold(v, header)) {
				continue
			}
			c, r := sitColRow(a)
			last := 0
			for rr := r + 1; rr <= 1_000; rr++ {
				if s.cells[name][sitAddress(c, rr)] != "" {
					last = rr
				}
			}
			col := column{Sheet: name, HeaderCell: a}
			if last > 0 {
				rng := sitAddress(c, r+1) + ":" + sitAddress(c, last)
				col.Range = &rng
			}
			out.Columns = append(out.Columns, col)
		}
	}
	return json.Marshal(out)
}
func (s *sitSession) Sheets(context.Context) ([]CraftNativeSheetInfo, error) {
	return []CraftNativeSheetInfo{{Name: "L", Used: "A1:B9"}, {Name: "Other", Used: "A1:A3"}}, nil
}
func (s *sitSession) Snapshot(context.Context) ([]byte, error) {
	if s.engine.loss != nil {
		return nil, s.engine.loss
	}
	sitRecalc(s.cells)
	zr, err := zip.NewReader(bytes.NewReader(s.base), int64(len(s.base)))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		if s.engine.drop[f.Name] {
			continue
		}
		sheet := ""
		for name, part := range sitSheets {
			if part == f.Name {
				sheet = name
			}
		}
		if sheet == "" {
			if err := zw.Copy(f); err != nil {
				return nil, err
			}
			continue
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Deflate, Modified: f.Modified})
		if err != nil {
			return nil, err
		}
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		original, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			return nil, err
		}
		prefix, _, ok := strings.Cut(string(original), "<sheetData>")
		_, suffix, closed := strings.Cut(string(original), "</sheetData>")
		_, edited, _ := strings.Cut(sitSheetXML(s.cells[sheet]), "<sheetData>")
		data, _, _ := strings.Cut(edited, "</sheetData>")
		if !ok || !closed {
			return nil, errors.New("fixture missing sheetData")
		}
		part := prefix + "<sheetData>" + data + "</sheetData>" + suffix
		if s.engine.edit != nil {
			part = s.engine.edit(f.Name, part)
		}
		_, _ = io.WriteString(w, part)
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// sitInitialL is sheet L of version 1: Name | Status, eight rows.
func sitInitialL() sitCells {
	c := sitCells{"A1": "Name", "B1": "Status"}
	for i := 2; i <= 9; i++ {
		c[sitAddress(1, i)] = fmt.Sprintf("Item %d", i-1)
		c[sitAddress(2, i)] = "open"
	}
	return c
}

func sitInitialOther() sitCells { return sitCells{"A1": "Code", "A2": "X1", "A3": "X2"} }

// sitScenario is chatRunScenario over a workbook Sheet: page sheet-own, version 1 the synthetic
// xlsm, the production session adapter (with the production open check) over sitEngine.
type sitScenario struct {
	*chatRunScenario
	wb     *sitEngine
	v1     []byte
	store  mooring.NativeStoreConfig
	agentN string
}

func newSheetITScenario(t *testing.T, initial ...[]byte) *sitScenario {
	t.Helper()
	if len(initial) == 0 {
		return newSheetITScenarioWithWorkbook(t, sitWorkbook(sitInitialL(), sitInitialOther()), "workbook.xlsm", mooring.WorkbookMediaTypeXLSM)
	}
	format, err := mooring.DetectWorkbook(initial[0])
	if err != nil {
		t.Fatal(err)
	}
	if format == "xlsx" {
		return newSheetITScenarioWithWorkbook(t, initial[0], "workbook.xlsx", mooring.WorkbookMediaTypeXLSX)
	}
	return newSheetITScenarioWithWorkbook(t, initial[0], "workbook.xlsm", mooring.WorkbookMediaTypeXLSM)
}

func newSheetITScenarioWithWorkbook(t *testing.T, v1 []byte, entryPoint, mediaType string) *sitScenario {
	t.Helper()
	const master = "synthetic-sit-workbook-master"
	f, source := originalSessionFixture(t)
	db, workspace := f.rig.db, f.a.WorkspaceID
	fullSynchronousPool(t, db)
	mustExec(t, db, `UPDATE chats SET visibility='private' WHERE id=?`, f.a.ChatID)
	mustExec(t, db, `DELETE FROM chat_participants WHERE chat_id=?`, f.a.ChatID)
	mustExec(t, db, `INSERT INTO workspace_artifact_creation_policy(workspace_id,enabled,version,updated_at) VALUES(?,1,1,'2026-10-17T00:00:00Z')`, workspace)
	mustExec(t, db, `INSERT INTO workspace_artifact_creation_crews(workspace_id,crew_id) VALUES(?,?)`, workspace, f.a.CrewID)
	seedScenarioSheet(t, f, "sheet-own", f.a.HumanUserID)
	mustExec(t, db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('sit-flag-on','ff_artifact_agent_access_modes',?,1)`, workspace)
	craftNativeEnableVersionHistory(t, db, true)
	versions := artifacts.NewVersionStore(db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	if _, err := versions.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: workspace, PageID: "sheet-own"}, artifacts.VersionRequest{EntryPoint: entryPoint, Files: map[string]artifacts.VersionFile{entryPoint: {MediaType: mediaType, Content: v1}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.a.HumanUserID, nil }); err != nil {
		t.Fatal(err)
	}

	f.pages.artifactMasterToken = master
	h := newCraftNativeHTTP(f.pages, true, &collaborationAdmissionTestClock{boot: "sit-workbook", now: time.Second})
	store := mooring.NativeStoreConfig{Application: "gridcraft", Format: mooring.NativeFormatWorkbook, EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: craftWorkbookValidate}
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"gridcraft": {Store: store, EditorURL: "/_editor/b/" + store.EditorBundle + "/index.html"}})
	d.profiles = map[string]CraftNativeDocumentProfile{"gridcraft": {Application: "gridcraft"}}
	engine := &sitEngine{}
	// The production wiring: the session adapter with the open check of nativeCraftAgentEngine.
	adapter := NewCraftNativeSheetAgentAdapter(engine, WithCraftNativeSheetOpenCheck(CraftNativeSheetSnapshotKeepsEverything))
	m := newCraftNativeAgentManager(h, d, CraftNativeHTTPConfig{AgentsEnabled: true})
	m.config.AgentApply = func(ctx context.Context, p CraftNativeDocumentProfile, base []byte, commands json.RawMessage) ([]byte, error) {
		if !CraftNativeChatRun(ctx) && !craftNativeUndo(ctx) {
			return nil, errors.New("neither a chat run nor a person's undo")
		}
		out, err := adapter.Apply(ctx, p, base, commands)
		if err != nil {
			t.Logf("sit apply: %v (base %d bytes)", err, len(base))
		}
		return out, err
	}
	// Reads are not gated, like production (internal/server nativeCraftAgentEngine): the person's
	// undo reads the agent's ranges outside a chat run.
	m.config.AgentInspect = func(ctx context.Context, p CraftNativeDocumentProfile, base []byte, query json.RawMessage) (json.RawMessage, error) {
		out, err := adapter.Inspect(ctx, p, base, query)
		if err != nil {
			t.Logf("sit inspect: %v (base %d bytes)", err, len(base))
		}
		return out, err
	}
	h.agents = m

	writer := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-sit-chain"), FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	internal := NewInternalHandler(db, master, newTestLogger())
	internal.SetJournal(writer)
	router := &Router{db: db, logger: newTestLogger(), mux: newRouteMux()}
	guard := func(next http.HandlerFunc) http.Handler { return internal.requireInternal(withHostChatOrigin(next)) }
	router.mux.Handle("POST /api/v1/internal/craft-native/chat-run", guard(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		m.chatRunTool(rec, r)
		if rec.Code >= 300 {
			t.Logf("sit chat-run tool answered %d %s", rec.Code, rec.Body)
		}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	router.mux.Handle("POST /api/v1/internal/runs", guard(internal.CreateRun))
	host := httptest.NewServer(router)
	t.Cleanup(host.Close)

	transcript := conversation.NewStore(filepath.Join(t.TempDir(), "transcript"), newTestLogger(), conversation.WithDB(db))
	t.Cleanup(transcript.Close)
	resolver := &scenarioResolver{IPCResolver: chatbridge.NewIPCResolver(host.URL, master, newTestLogger()), f: f, internal: internal, source: source, admitted: make(chan struct{}, 8)}
	runtime := &sitRuntime{replayRuntime: &replayRuntime{}}
	bridge := chatbridge.New(orchestrator.New(runtime, nil, newTestLogger()), runtime, transcript, nil, resolver, chatbridge.BridgeConfig{}, newTestLogger())
	bridge.SetArtifactRefRecorder(f.pages)

	u := &AuthUser{ID: source.UserID, SessionID: source.ID, OriginalCredential: source}
	human := auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), ctxWorkspaceID, workspace), *source)
	var agentName string
	_ = db.QueryRow(`SELECT name FROM agents WHERE id=?`, f.a.AgentID).Scan(&agentName)
	return &sitScenario{chatRunScenario: &chatRunScenario{t: t, f: f, m: m, resolver: resolver, human: human, bridge: bridge, host: host}, wb: engine, v1: v1, store: store, agentN: agentName}
}

// sitRuntime is replayRuntime whose crew container stays running, so a second message of the same
// chat reuses it instead of asking an absent provider.
type sitRuntime struct{ *replayRuntime }

func (r *sitRuntime) ContainerStatus(_ context.Context, id string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{ID: id, State: "running"}, nil
}

// read is the agent's craft_sheet_read; it returns the text and whether it was refused.
func (s *sitScenario) read(call func(string, any) (string, bool, json.RawMessage), query map[string]any) (string, bool) {
	text, isError, _ := call("tools/call", map[string]any{"name": "craft_sheet_read", "arguments": map[string]any{"query": query}})
	return text, isError
}

// toolCall is any tool call, refusal or not.
func (s *sitScenario) toolCall(call func(string, any) (string, bool, json.RawMessage), name string, arguments map[string]any) (string, bool) {
	text, isError, _ := call("tools/call", map[string]any{"name": name, "arguments": arguments})
	return text, isError
}

func (s *sitScenario) acquire(call func(string, any) (string, bool, json.RawMessage)) string {
	s.t.Helper()
	var lease struct {
		Generation string `json:"generation"`
	}
	if got := s.tool(call, "craft_sheet_acquire", map[string]any{}); json.Unmarshal([]byte(got), &lease) != nil || lease.Generation == "" {
		s.t.Fatalf("acquire: %s", got)
	}
	return lease.Generation
}

// humanGet is the person's GET of a document route with a query string.
func (s *sitScenario) humanGet(token, query string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	s.t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pages/sheet-own/craft/document"+query, nil).WithContext(s.human)
	r.SetPathValue("slug", "sheet-own")
	r.Header.Set("X-Craft-Session", token)
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

// stored is the bytes of one stored version.
func (s *sitScenario) stored(version int64) []byte {
	s.t.Helper()
	v := artifacts.NewVersionStore(s.f.rig.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	m, err := v.ReadVersion(s.t.Context(), artifacts.VersionKey{WorkspaceID: s.f.a.WorkspaceID, PageID: "sheet-own"}, version, func(context.Context, *sql.Tx) (string, error) { return "user:" + s.f.a.HumanUserID, nil })
	if err != nil {
		s.t.Fatalf("version %d: %v", version, err)
	}
	return m.Files[m.EntryPoint].Content
}

type sitOpsRow struct {
	Version, Base                               int64
	Kind, ActorID, Name, OperationID, Pin, Calc string
	Replayable                                  bool
	Ops, Inverse                                string
}

func (s *sitScenario) opsRows() []sitOpsRow {
	s.t.Helper()
	rows, err := s.f.rig.db.Query(`SELECT version,base_version,actor_kind,actor_id,actor_name,operation_id,engine_pin,calculate,replayable,ops,inverse FROM craft_version_ops WHERE page_id='sheet-own' ORDER BY version`)
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	var out []sitOpsRow
	for rows.Next() {
		var r sitOpsRow
		if err := rows.Scan(&r.Version, &r.Base, &r.Kind, &r.ActorID, &r.Name, &r.OperationID, &r.Pin, &r.Calc, &r.Replayable, &r.Ops, &r.Inverse); err != nil {
			s.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// sitCommands is a JSON command list for the apply tool.
func sitCommands(commands ...map[string]any) []any {
	out := make([]any, 0, len(commands))
	for _, c := range commands {
		out = append(out, c)
	}
	return out
}

func sitCmd(name string, params map[string]any) map[string]any {
	return map[string]any{"command": name, "params": params}
}
