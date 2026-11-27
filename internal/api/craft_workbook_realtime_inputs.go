package api

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/xml"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Input equality is independent of the worksheet's structural digest. In
// particular, xml:space is part of the XML projection and never part of the
// typed input. An inline string and a reference to the same shared text agree.
func craftTypedWorksheetInput(kind string, formula, value, inline craftCellXML, sharedString func(int) (string, error)) ([32]byte, error) {
	var text string
	switch {
	case formula.present:
		kind, text = "formula", formula.text
	case inline.present:
		kind, text = "text", inline.text
	case kind == "str" && value.present:
		kind, text = "text", value.text
	case !value.nonempty:
		return [32]byte{}, nil
	default:
		text = value.text
		switch kind {
		case "s":
			index, err := strconv.Atoi(text)
			if err != nil || index < 0 || sharedString == nil {
				return [32]byte{}, errCraftWorksheetXML
			}
			text, err = sharedString(index)
			if err != nil {
				return [32]byte{}, err
			}
			kind = "text"
		case "", "n":
			number, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
			if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
				return [32]byte{}, errCraftWorksheetXML
			}
			kind = "number"
			if number == 0 {
				text = "0"
			} else {
				text = strconv.FormatFloat(number, 'g', -1, 64)
			}
		case "b":
			switch text {
			case "0", "false":
				text = "false"
			case "1", "true":
				text = "true"
			default:
				return [32]byte{}, errCraftWorksheetXML
			}
			kind = "bool"
		}
	}
	if kind == "text" {
		text = craftSpreadsheetText(text)
	}
	hash := craftWorksheetHash{sha256.New()}
	hash.field(kind)
	hash.field(text)
	return hash.sum(), nil
}

// ECMA-376 strings can represent a UTF-16 unit as _xHHHH_, including
// whitespace. Decode once so an escaped underscore doesn't expose a second
// escape. Leave isolated surrogates intact.
func craftSpreadsheetText(text string) string {
	if !strings.Contains(text, "_x") {
		return text
	}
	unit := func(s string) (rune, bool) {
		if len(s) < 7 || s[:2] != "_x" || s[6] != '_' {
			return 0, false
		}
		n, err := strconv.ParseUint(s[2:6], 16, 16)
		return rune(n), err == nil
	}
	var decoded strings.Builder
	for len(text) > 0 {
		r, ok := unit(text)
		if !ok {
			decoded.WriteByte(text[0])
			text = text[1:]
			continue
		}
		size := 7
		if utf16.IsSurrogate(r) {
			second, ok := unit(text[7:])
			if !ok || utf16.DecodeRune(r, second) == '\uFFFD' {
				decoded.WriteString(text[:7])
				text = text[7:]
				continue
			}
			r, size = utf16.DecodeRune(r, second), 14
		}
		decoded.WriteRune(r)
		text = text[size:]
	}
	return decoded.String()
}

func craftSharedStringReader(parts map[string]*zip.File) func(int) (string, error) {
	var table []string
	var loadErr error
	loaded := false
	return func(index int) (string, error) {
		if !loaded {
			table, loadErr = craftWorkbookSharedStrings(parts)
			loaded = true
		}
		if loadErr != nil {
			return "", loadErr
		}
		if index < 0 || index >= len(table) {
			return "", errCraftWorksheetXML
		}
		return table[index], nil
	}
}

func craftWorkbookSharedStrings(parts map[string]*zip.File) ([]string, error) {
	part, err := craftSharedStringsPart(parts)
	if err != nil {
		return nil, err
	}
	r, err := part.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	d := xml.NewDecoder(r)
	var table struct {
		XMLName xml.Name
		Items   []struct {
			Text string `xml:"t"`
			Runs []struct {
				Text string `xml:"t"`
			} `xml:"r"`
		} `xml:"si"`
	}
	if err := d.Decode(&table); err != nil || table.XMLName != (xml.Name{Space: craftSpreadsheetNS, Local: "sst"}) {
		return nil, errCraftWorksheetXML
	}
	stringsTable := make([]string, 0, len(table.Items))
	for _, item := range table.Items {
		var content strings.Builder
		content.WriteString(item.Text)
		for _, run := range item.Runs {
			content.WriteString(run.Text)
		}
		stringsTable = append(stringsTable, content.String())
	}
	if _, err := io.Copy(io.Discard, r); err != nil {
		return nil, err
	}
	return stringsTable, nil
}

func craftSharedStringsPart(parts map[string]*zip.File) (*zip.File, error) {
	rels := parts[craftWorkbookRelsPart]
	if rels == nil {
		return nil, errCraftWorksheetXML
	}
	r, err := rels.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	d := xml.NewDecoder(r)
	var links struct {
		XMLName xml.Name
		Items   []struct {
			Type       string `xml:"Type,attr"`
			Target     string `xml:"Target,attr"`
			TargetMode string `xml:"TargetMode,attr"`
		} `xml:"Relationship"`
	}
	if err := d.Decode(&links); err != nil || links.XMLName != (xml.Name{Space: craftPackageRelsNS, Local: "Relationships"}) {
		return nil, errCraftWorksheetXML
	}
	const relationship = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings"
	var targetPart *zip.File
	for _, link := range links.Items {
		if link.Type != relationship {
			continue
		}
		if targetPart != nil || link.Target == "" || link.TargetMode != "" && link.TargetMode != "Internal" {
			return nil, errCraftWorksheetXML
		}
		target := path.Join("xl", link.Target)
		if strings.HasPrefix(link.Target, "/") {
			target = strings.TrimPrefix(path.Clean(link.Target), "/")
		}
		targetPart = parts[target]
		if targetPart == nil {
			return nil, errCraftWorksheetXML
		}
	}
	if targetPart == nil {
		return nil, errCraftWorksheetXML
	}
	return targetPart, nil
}
