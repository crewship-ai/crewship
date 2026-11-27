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

	"github.com/crewship-ai/crewship/internal/tsformat"

	"github.com/crewship-ai/crewship/internal/craftsession"
)

// craftWorkbookKeptPart names a part of a workbook package that neither the editor nor the engine
// changes: the VBA project and its signatures, XML package signatures, custom XML. It is kept
// byte for byte, whatever the engine reports; "" for any other part.
func craftWorkbookKeptPart(name string) string {
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
	}
	return ""
}

// craftWorkbookPartSums hashes the kept parts of a workbook file; not ok when it is no zip.
func craftWorkbookPartSums(file []byte) (map[string][32]byte, bool) {
	zr, err := zip.NewReader(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		return nil, false
	}
	sums := map[string][32]byte{}
	for _, f := range zr.File {
		if craftWorkbookKeptPart(f.Name) == "" {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return nil, false
		}
		h := sha256.New()
		_, err = io.Copy(h, io.LimitReader(r, 64<<20))
		r.Close()
		if err != nil {
			return nil, false
		}
		var sum [32]byte
		copy(sum[:], h.Sum(nil))
		sums[f.Name] = sum
	}
	return sums, true
}

// craftWorkbookPartsLost names, by code and count, the kept parts of base that candidate lost or
// changed; nil when it kept them all.
func craftWorkbookPartsLost(base, candidate []byte) []craftsession.FidelityPart {
	before, ok := craftWorkbookPartSums(base)
	if !ok || len(before) == 0 {
		return nil
	}
	after, ok := craftWorkbookPartSums(candidate)
	if !ok {
		return []craftsession.FidelityPart{{Code: "package", Count: 1}}
	}
	counts := map[string]int{}
	for name, sum := range before {
		if got, ok := after[name]; !ok || got != sum {
			counts[craftWorkbookKeptPart(name)]++
		}
	}
	out := make([]craftsession.FidelityPart, 0, len(counts))
	for code, n := range counts {
		out = append(out, craftsession.FidelityPart{Code: code, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	if len(out) == 0 {
		return nil
	}
	return out
}

// craftWorkbookSigned says the workbook file carries an XML package signature. Any change of its
// cells no longer matches that signature (a VBA signature covers only the VBA project).
func craftWorkbookSigned(file []byte) bool {
	zr, err := zip.NewReader(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		return false
	}
	for _, f := range zr.File {
		if craftWorkbookKeptPart(f.Name) == "xml_signatures" {
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
