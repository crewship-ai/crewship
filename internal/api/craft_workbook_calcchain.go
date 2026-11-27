package api

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

const (
	craftCalcChainPart         = "xl/calcChain.xml"
	craftContentTypesPart      = "[Content_Types].xml"
	craftWorkbookRelsPart      = "xl/_rels/workbook.xml.rels"
	craftCalcChainType         = "application/vnd.openxmlformats-officedocument.spreadsheetml.calcChain+xml"
	craftCalcChainRelationship = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/calcChain"
	craftContentTypesNS        = "http://schemas.openxmlformats.org/package/2006/content-types"
	craftPackageRelsNS         = "http://schemas.openxmlformats.org/package/2006/relationships"
)

// A calc chain is an optional calculation-order cache. The only package rewrite
// we admit is dropping the part and exactly its two declarations, byte for byte.
func craftCalcChainDropped(before, after map[string]*zip.File) bool {
	if before[craftCalcChainPart] == nil || after[craftCalcChainPart] != nil {
		return false
	}
	for _, part := range []struct {
		name, ns, root, child string
		matches               func(xml.StartElement) bool
	}{
		{craftContentTypesPart, craftContentTypesNS, "Types", "Override", func(el xml.StartElement) bool {
			return craftWorkbookAttr(el, "PartName") == "/"+craftCalcChainPart &&
				craftWorkbookAttr(el, "ContentType") == craftCalcChainType
		}},
		{craftWorkbookRelsPart, craftPackageRelsNS, "Relationships", "Relationship", func(el xml.StartElement) bool {
			target := craftWorkbookAttr(el, "Target")
			mode := craftWorkbookAttr(el, "TargetMode")
			return (target == "calcChain.xml" || target == "/"+craftCalcChainPart) &&
				craftWorkbookAttr(el, "Type") == craftCalcChainRelationship &&
				(mode == "" || mode == "Internal")
		}},
	} {
		original := craftCalcMetadata(before[part.name])
		candidate := craftCalcMetadata(after[part.name])
		if original == nil || candidate == nil {
			return false
		}
		start, end, ok := craftCalcDeclaration(original, part.ns, part.root, part.child, part.matches)
		if !ok || len(candidate) != len(original)-(end-start) ||
			!bytes.Equal(original[:start], candidate[:start]) ||
			!bytes.Equal(original[end:], candidate[start:]) {
			return false
		}
	}
	return true
}

func craftCalcMetadata(part *zip.File) []byte {
	const limit = 16 << 20
	if part == nil || part.UncompressedSize64 > limit {
		return nil
	}
	r, err := part.Open()
	if err != nil {
		return nil
	}
	defer r.Close()
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || len(raw) > limit {
		return nil
	}
	return raw
}

func craftCalcDeclaration(raw []byte, ns, root, child string, matches func(xml.StartElement) bool) (start, end int, ok bool) {
	d := xml.NewDecoder(bytes.NewReader(raw))
	depth, roots := 0, 0
	for {
		offset := int(d.InputOffset())
		token, err := d.Token()
		if err == io.EOF {
			return start, end, depth == 0 && roots == 1 && end > start
		}
		if err != nil {
			return 0, 0, false
		}
		switch el := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots != 1 || el.Name != (xml.Name{Space: ns, Local: root}) {
					return 0, 0, false
				}
			}
			if depth == 1 && el.Name == (xml.Name{Space: ns, Local: child}) && matches(el) {
				if end != 0 {
					return 0, 0, false
				}
				start = offset
				next, err := d.Token()
				if close, ok := next.(xml.EndElement); err != nil || !ok || close.Name != el.Name {
					return 0, 0, false
				}
				end = int(d.InputOffset())
				continue
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(el)) != "" {
				return 0, 0, false
			}
		case xml.Directive:
			return 0, 0, false
		}
	}
}
