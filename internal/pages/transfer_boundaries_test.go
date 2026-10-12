package pages

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestProjectTransferPreservesPortableSourceAndBindings(t *testing.T) {
	bundle := TransferBundle{Format: TransferV2, Page: TransferPage{Name: "Portable", Slug: "portable", Owner: "crew/observer", Panels: []TransferPanel{{ID: "health", Schema: "status", Owner: "crew/observer", Producer: "script/check", SLASeconds: 30}}}, Project: testSourceProject(), Refs: []TransferReference{{Ref: "crew/observer", Kind: "crew", Bindable: true, UsedBy: []string{"health"}}}, Meta: TransferMetadata{PanelCount: 1}}
	encoded, err := MarshalProjectTransfer(bundle)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := ParseProjectTransfer(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(imported.Page, bundle.Page) || !reflect.DeepEqual(imported.Refs, bundle.Refs) || imported.Meta != bundle.Meta {
		t.Fatalf("portable metadata changed: %#v", imported)
	}
	want, err := bundle.Project.Digest()
	if err != nil {
		t.Fatal(err)
	}
	got, err := imported.Project.Digest()
	if err != nil || got != want {
		t.Fatalf("source digest %s, want %s: %v", got, want, err)
	}
	imported.Slug = "new-page"
	imported.Bind = map[string]string{"crew/observer": "crew/target"}
	encoded, err = json.Marshal(imported)
	if err != nil {
		t.Fatal(err)
	}
	var decoded TransferImport
	if err = DecodeProjectJSON(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Slug != "new-page" || decoded.Bind["crew/observer"] != "crew/target" {
		t.Fatalf("lost import overrides: %#v", decoded)
	}
}

func TestProjectTransferRejectsAmbiguousAndUnsafeDocuments(t *testing.T) {
	for name, document := range map[string]string{
		"invalid JSON":     `{"format":`,
		"duplicate field":  `{"format":"crewship-page-bundle/v2","format":"other"}`,
		"unknown field":    `{"format":"crewship-page-bundle/v2","publish":true}`,
		"wrong field type": `{"format":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var req TransferImport
			if err := DecodeProjectJSON([]byte(document), &req); err == nil {
				t.Fatal("accepted ambiguous or invalid JSON")
			}
		})
	}
	for name, document := range map[string]string{
		"invalid YAML":             "format: [",
		"unknown field":            "format: crewship-page-bundle/v2\npublish: true\n",
		"multiple documents":       "format: crewship-page-bundle/v2\n---\nformat: other\n",
		"malformed extra document": "format: crewship-page-bundle/v2\n---\n[",
		"anchor":                   "format: &format crewship-page-bundle/v2\n",
		"merge":                    "format: crewship-page-bundle/v2\npage:\n  <<: {name: borrowed}\n",
		"wrong format":             "format: crewship-page-bundle/v1\n",
		"missing project":          "format: crewship-page-bundle/v2\n",
		"deep document":            "bind: " + strings.Repeat("[", 20) + "0" + strings.Repeat("]", 20),
		"too large":                strings.Repeat(" ", MaxTransferBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if req, err := ParseProjectTransfer(strings.NewReader(document)); err == nil || req != nil {
				t.Fatalf("accepted document: %#v, %v", req, err)
			}
		})
	}
	sentinel := errors.New("transfer interrupted")
	if req, err := ParseProjectTransfer(transferFailureReader{sentinel}); !errors.Is(err, sentinel) || req != nil {
		t.Fatalf("read failure lost: %#v, %v", req, err)
	}
}

type transferFailureReader struct{ err error }

func (r transferFailureReader) Read([]byte) (int, error) { return 0, r.err }

var _ io.Reader = transferFailureReader{}

func TestProjectTransferRejectsUnsafeExportAndOversizeMetadata(t *testing.T) {
	unsafe := testSourceProject()
	unsafe.Files[0].Path = "../escape.tsx"
	for name, bundle := range map[string]TransferBundle{
		"wrong format":      {Format: "old", Project: testSourceProject()},
		"missing source":    {Format: TransferV2},
		"unsafe source":     {Format: TransferV2, Project: unsafe},
		"oversize metadata": {Format: TransferV2, Project: testSourceProject(), Page: TransferPage{Description: strings.Repeat("x", MaxTransferBytes)}},
	} {
		t.Run(name, func(t *testing.T) {
			if data, err := MarshalProjectTransfer(bundle); err == nil || data != nil {
				t.Fatalf("unsafe export returned %d bytes: %v", len(data), err)
			}
		})
	}
	encoded, err := json.Marshal(TransferBundle{Format: TransferV2, Project: unsafe})
	if err != nil {
		t.Fatal(err)
	}
	if req, err := ParseProjectTransfer(bytes.NewReader(encoded)); err == nil || req != nil {
		t.Fatalf("accepted path traversal: %#v, %v", req, err)
	}
}
