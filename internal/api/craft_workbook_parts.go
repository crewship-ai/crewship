package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// craftWorkbookPartCode groups refusals without including a part name or workbook content.
func craftWorkbookPartCode(name string) string {
	lower := strings.ToLower(name)
	switch {
	case lower == "xl/vbaproject.bin", lower == "xl/_rels/vbaproject.bin.rels":
		return "vba_project"
	case strings.HasPrefix(lower, "xl/vbaprojectsignature") && strings.HasSuffix(lower, ".bin"):
		return "vba_signature"
	case strings.HasPrefix(lower, "_xmlsignatures/"):
		return "xml_signatures"
	case strings.HasPrefix(lower, "customxml/"):
		return "custom_xml"
	case lower == "[content_types].xml":
		return "content_types"
	case strings.HasSuffix(lower, ".rels"):
		return "relationships"
	case lower == "xl/workbook.xml":
		return "workbook"
	case lower == "xl/styles.xml":
		return "styles"
	default:
		return "package_parts"
	}
}

func craftWorkbookPartFiles(file []byte) (*zip.Reader, map[string]*zip.File, bool) {
	if craftWorkbookValidate(context.Background(), file) != nil {
		return nil, nil, false
	}
	zr, err := zip.NewReader(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		return nil, nil, false
	}
	files := make(map[string]*zip.File, len(zr.File))
	for _, part := range zr.File {
		if part.FileInfo().IsDir() {
			continue
		}
		if files[part.Name] != nil {
			return nil, nil, false
		}
		files[part.Name] = part
	}
	return zr, files, true
}

// Read to EOF so zip checks both the advertised size and the CRC. CheckArchive bounds the
// advertised sizes, and zip's reader refuses streams that exceed them.
func craftWorkbookPartSum(part *zip.File) ([32]byte, error) {
	r, err := part.Open()
	if err != nil {
		return [32]byte{}, err
	}
	defer r.Close()
	h := sha256.New()
	_, err = io.Copy(h, r)
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, err
}

// Only a mapped worksheet may change. All other parts, including those the editor does
// not understand, must keep their original bytes, content type and relationships.
func craftWorkbookPartsLost(base, candidate []byte, edits craftWorkbookEdits) []craftsession.FidelityPart {
	packageLoss := []craftsession.FidelityPart{{Code: "package", Count: 1}}
	beforeZip, before, ok := craftWorkbookPartFiles(base)
	if !ok {
		return packageLoss
	}
	_, after, ok := craftWorkbookPartFiles(candidate)
	if !ok {
		return packageLoss
	}
	sheets, err := craftsession.WorkbookWorksheetParts(beforeZip)
	if err != nil {
		return packageLoss
	}
	counts := map[string]int{}
	changed := false
	for name, part := range before {
		sum, err := craftWorkbookPartSum(part)
		if err != nil {
			return packageLoss
		}
		next := after[name]
		if next == nil {
			counts[craftWorkbookPartCode(name)]++
			continue
		}
		nextSum, err := craftWorkbookPartSum(next)
		if err != nil {
			return packageLoss
		}
		if sheet, ok := sheets[name]; ok {
			old, err := craftWorksheetProjection(part, sheet, edits)
			if err != nil {
				return packageLoss
			}
			if sum == nextSum {
				continue
			}
			changed = true
			new, err := craftWorksheetProjection(next, sheet, edits)
			if err != nil {
				return packageLoss
			}
			if !old.preservedBy(new) {
				counts["worksheet"]++
			}
			continue
		}
		if sum == nextSum {
			continue
		}
		changed = true
		counts[craftWorkbookPartCode(name)]++
	}
	for name, part := range after {
		if before[name] == nil {
			if _, err := craftWorkbookPartSum(part); err != nil {
				return packageLoss
			}
			counts[craftWorkbookPartCode(name)]++
		}
	}
	if changed {
		for _, code := range []string{"xml_signatures", "vba_signature"} {
			if counts[code] == 0 && craftWorkbookHasSignature(before, code) {
				counts[code] = 1
			}
		}
	}
	if len(counts) == 0 {
		return nil
	}
	out := make([]craftsession.FidelityPart, 0, len(counts))
	for code, n := range counts {
		out = append(out, craftsession.FidelityPart{Code: code, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// A signed workbook can still be opened and read. Agent edits are refused
// before the engine makes any changes, including inside a warm session.
func craftWorkbookEditSignatures(file []byte) []craftsession.FidelityPart {
	zr, err := zip.NewReader(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		return []craftsession.FidelityPart{{Code: "package", Count: 1}}
	}
	parts := make(map[string]*zip.File, len(zr.File))
	for _, part := range zr.File {
		parts[part.Name] = part
	}
	var refused []craftsession.FidelityPart
	for _, code := range []string{"vba_signature", "xml_signatures"} {
		if craftWorkbookHasSignature(parts, code) {
			refused = append(refused, craftsession.FidelityPart{Code: code, Count: 1})
		}
	}
	return refused
}

// A signature can be stored outside the conventional directory. Recognize its
// OPC declaration as well as the usual part names.
func craftWorkbookHasSignature(parts map[string]*zip.File, code string) bool {
	for name := range parts {
		if craftWorkbookPartCode(name) == code {
			return true
		}
	}
	types := parts["[Content_Types].xml"]
	if types == nil {
		return true
	}
	r, err := types.Open()
	if err != nil {
		return true
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return true
	}
	needle := "digital-signature"
	if code == "vba_signature" {
		needle = "vbaprojectsignature"
	}
	return strings.Contains(strings.ToLower(string(b)), needle)
}

// craftWorkbookSigned says the workbook file carries an XML package signature. Any change of its
// cells no longer matches that signature (a VBA signature covers only the VBA project).
func craftWorkbookSigned(file []byte) bool {
	zr, err := zip.NewReader(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		return false
	}
	for _, f := range zr.File {
		if craftWorkbookPartCode(f.Name) == "xml_signatures" {
			return true
		}
	}
	return false
}

// recordCraftSignatureBreak marks, in the version's own transaction, that version no longer
// matches the workbook's signature.
func recordCraftSignatureBreak(ctx context.Context, tx *sql.Tx, workspace, page string, version int64) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO craft_version_signature_breaks(workspace_id,page_id,version,created_at) VALUES(?,?,?,?)`, workspace, page, version, tsformat.Format(time.Now().UTC()))
	return err
}
