package craftassets

import (
	"strings"
	"testing"
)

// newAppPkg is newPkg for another application, with its own paths.
func newAppPkg(t *testing.T, app string) *pkg {
	t.Helper()
	p := newPkg(t)
	c := &p.manifest.Component
	move := func(f File) File {
		data := p.files[f.Path]
		delete(p.files, f.Path)
		f.Path = app + strings.TrimPrefix(f.Path, "wordcraft")
		p.files[f.Path] = data
		return f
	}
	c.Application, c.Engine, c.Notices[0], c.Editor.Files[0] = app, move(c.Engine), move(c.Notices[0]), move(c.Editor.Files[0])
	return p
}

// document_format "workbook" marks a Sheet editor that opens and saves the xlsx or xlsm file
// itself; only a Sheet package may carry it, and no other value is accepted.
func TestManifestDocumentFormat(t *testing.T) {
	for _, tc := range []struct {
		app, format string
		ok          bool
	}{{"gridcraft", "workbook", true}, {"gridcraft", "", true}, {"wordcraft", "", true}, {"wordcraft", "workbook", false}, {"gridcraft", "native", false}, {"gridcraft", "Workbook", false}} {
		p := newAppPkg(t, tc.app)
		p.manifest.Component.DocumentFormat = tc.format
		pin := Pin{Application: tc.app, Version: p.manifest.Component.Version, ManifestSHA256: digest(p.manifestBytes(t))}
		m, err := parseManifest(p.manifestBytes(t), pin)
		if (err == nil) != tc.ok || err == nil && m.Component.DocumentFormat != tc.format {
			t.Errorf("%s %q: %v", tc.app, tc.format, err)
		}
	}
	if raw := string(newAppPkg(t, "gridcraft").manifestBytes(t)); strings.Contains(raw, "document_format") {
		t.Errorf("an unset document_format is written: %s", raw)
	}
}
