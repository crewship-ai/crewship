//go:build craftpackages

package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/craftassets"
	"github.com/crewship-ai/crewship/internal/craftengine"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// With real release packages (CREWSHIP_CRAFT_TEST_PACKAGES=<directory with
// catalog.txt and the .craftpkg files of scripts/build-craft-components.py>),
// both install from their files: the editor origin verifies and serves each
// real editor (including its CSP and WebRTC checks) and each real WASM engine
// answers. It is built only with -tags craftpackages:
//
//	CREWSHIP_CRAFT_TEST_PACKAGES=/abs/dist go test -tags craftpackages ./internal/server -run ReleaseCraftPackages
func TestReleaseCraftPackagesInstallAndRun(t *testing.T) {
	dir := os.Getenv("CREWSHIP_CRAFT_TEST_PACKAGES")
	if dir == "" {
		t.Fatal("set CREWSHIP_CRAFT_TEST_PACKAGES to the directory of the release packages")
	}
	pins, err := os.ReadFile(filepath.Join(dir, "catalog.txt"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Server.Host = "127.0.0.1"
	cfg.CraftNative.ComponentsDir = t.TempDir()
	cfg.CraftNative.ComponentPins = strings.TrimSpace(string(pins))
	logger := slog.New(slog.DiscardHandler)
	craft := resolveCraftApps(cfg, logger)
	editor := newEditorOrigin(cfg, craft, logger)
	out, err := nativeCraftHTTPConfig(cfg, craft, editor)
	if err != nil || out.Install == nil {
		t.Fatalf("native config: %v", err)
	}
	for _, app := range craftapps.Applications {
		f, err := os.Open(filepath.Join(dir, craftassets.AssetName(app)))
		if err != nil {
			t.Fatal(err)
		}
		p, c, err := out.Install(context.Background(), app, f)
		f.Close()
		if err != nil || !editor.HasBundle(c.EditorBundle) {
			t.Fatalf("%s: %v", app, err)
		}
		var stdout bytes.Buffer
		if err := p.Engine.Run(context.Background(), t.TempDir(), []string{"--version"}, &stdout, &bytes.Buffer{}); err != nil || !strings.Contains(stdout.String(), app) {
			t.Fatalf("%s engine: %q %v", app, stdout.String(), err)
		}
		t.Logf("%s %s: %s", app, c.Version, strings.TrimSpace(stdout.String()))
		if app == "wordcraft" {
			t.Run("WordDOCXRoundTrip", func(t *testing.T) { testWordDOCXRoundTrip(t, p.Engine) })
		}
		if p.Workbook {
			t.Run("LegacySheetRoundTrip", func(t *testing.T) {
				source, err := os.ReadFile("../../lib/mooring/gridcraft-empty.ts")
				if err != nil {
					t.Fatal(err)
				}
				_, literal, found := strings.Cut(string(source), "const emptyWorkbook = ")
				literal, _, boundary := strings.Cut(literal, "\n\nexport function")
				var raw string
				if !found || !boundary || json.Unmarshal([]byte(strings.TrimSpace(literal)), &raw) != nil {
					t.Fatal("missing canonical legacy Sheet JSON")
				}
				if _, err := mooring.ConvertLegacyWorkbook(t.Context(), p.Engine, []byte(raw)); err != nil {
					t.Fatalf("canonical legacy Sheet: %v", err)
				}
			})
		}
	}
}

// The browser gate edits native JSON. Exercise the packaged CLI on both sides
// of that model with a synthetic Word package that has formatting and related parts.
func testWordDOCXRoundTrip(t *testing.T, engine craftengine.Engine) {
	t.Helper()
	dir := t.TempDir()
	var input bytes.Buffer
	writer := zip.NewWriter(&input)
	const wordNS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	const relNS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	const relsNS = "http://schemas.openxmlformats.org/package/2006/relationships"
	for name, xml := range map[string]string{
		"[Content_Types].xml":          `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/header1.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.header+xml"/></Types>`,
		"_rels/.rels":                  `<Relationships xmlns="` + relsNS + `"><Relationship Id="rId1" Type="` + relNS + `/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/_rels/document.xml.rels": `<Relationships xmlns="` + relsNS + `"><Relationship Id="rId1" Type="` + relNS + `/header" Target="header1.xml"/></Relationships>`,
		"word/document.xml":            `<w:document xmlns:w="` + wordNS + `" xmlns:r="` + relNS + `"><w:body><w:p><w:r><w:rPr><w:b/></w:rPr><w:t>Craft body marker</w:t></w:r></w:p><w:tbl><w:tblPr/><w:tblGrid><w:gridCol w:w="1800"/></w:tblGrid><w:tr><w:tc><w:tcPr><w:tcW w:w="1800" w:type="dxa"/></w:tcPr><w:p><w:r><w:t>Craft table marker</w:t></w:r></w:p></w:tc></w:tr></w:tbl><w:sectPr><w:headerReference w:type="default" r:id="rId1"/><w:pgSz w:w="12240" w:h="15840"/></w:sectPr></w:body></w:document>`,
		"word/header1.xml":             `<w:hdr xmlns:w="` + wordNS + `"><w:p><w:r><w:t>Craft header marker</w:t></w:r></w:p></w:hdr>`,
	} {
		part, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, xml); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "input.docx"), input.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	root := engine.Root(dir)
	for _, pair := range [][2]string{{"input.docx", "document.json"}, {"document.json", "output.docx"}} {
		var stdout, stderr bytes.Buffer
		if err := engine.Run(t.Context(), dir, []string{"convert", root + "/" + pair[0], root + "/" + pair[1]}, &stdout, &stderr); err != nil {
			t.Fatalf("convert %s to %s: %v (stdout %q, stderr %q)", pair[0], pair[1], err, stdout.String(), stderr.String())
		}
	}
	output, err := zip.OpenReader(filepath.Join(dir, "output.docx"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	body, err := fs.ReadFile(output, "word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Body struct {
			Paragraph struct {
				Run struct {
					Text       string `xml:"t"`
					Properties struct {
						Bold *struct{} `xml:"b"`
					} `xml:"rPr"`
				} `xml:"r"`
			} `xml:"p"`
			Table struct {
				Text string `xml:"tr>tc>p>r>t"`
			} `xml:"tbl"`
			Header *struct{} `xml:"sectPr>headerReference"`
		} `xml:"body"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Body.Paragraph.Run.Text != "Craft body marker" || doc.Body.Paragraph.Run.Properties.Bold == nil ||
		doc.Body.Table.Text != "Craft table marker" || doc.Body.Header == nil {
		t.Fatal("Word DOCX round trip lost body text, bold formatting, a table or the header reference")
	}
	headers, err := fs.Glob(output, "word/header*.xml")
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range headers {
		raw, err := fs.ReadFile(output, part)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("Craft header marker")) {
			return
		}
	}
	t.Fatal("Word DOCX round trip lost the header text")
}
