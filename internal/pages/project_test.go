package pages

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func testSourceProject() *SourceProject {
	return &SourceProject{
		Format: SourceProjectFormat, Runtime: SourceProjectRuntime,
		Files: []ProjectFile{
			{Path: "src/main.tsx", Encoding: "utf8", Content: "export const title = 'Stav databáze';\n"},
			{Path: "package.json", Encoding: "utf8", Content: "{}\n"},
			{Path: "pnpm-lock.yaml", Encoding: "utf8", Content: "lockfileVersion: '9.0'\n"},
			{Path: "index.html", Encoding: "utf8", Content: "<div id=\"root\"></div>\n"},
			{Path: "public/logo.bin", Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte{0, 255, 17})},
		},
	}
}

func TestSourceProjectRoundTrip(t *testing.T) {
	p := testSourceProject()
	wantDigest, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	// Fixed independently from canonical JSON with sorted paths and base64
	// bytes. Changing the wire digest requires a source format version change.
	if wantDigest != "2a001fa34624f6dd6b551199ce2e91ef1e556a607deee4cd85f831b67eb04743" {
		t.Fatalf("source digest contract changed: %s", wantDigest)
	}
	b, err := p.MarshalYAMLSource()
	if err != nil {
		t.Fatal(err)
	}
	if p.Files[0].Path != "src/main.tsx" {
		t.Fatal("export mutated caller")
	}
	got, err := ParseSourceProject(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := got.Digest()
	if err != nil || digest != wantDigest {
		t.Fatalf("digest %q, error %v", digest, err)
	}
	again, err := got.MarshalYAMLSource()
	if err != nil || !bytes.Equal(b, again) {
		t.Fatalf("unstable YAML: %v", err)
	}
	for _, original := range p.Files {
		found := false
		for _, f := range got.Files {
			if f.Path == original.Path {
				want, _ := original.Bytes()
				actual, err := f.Bytes()
				if err != nil || !bytes.Equal(want, actual) {
					t.Fatalf("file %s lost bytes", f.Path)
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("lost file %s", original.Path)
		}
	}
	jsonBytes, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSourceProject(bytes.NewReader(jsonBytes)); err != nil {
		t.Fatal(err)
	}
}

func TestSourceProjectRejectsUnsafePaths(t *testing.T) {
	for _, name := range []string{"", "/etc/passwd", "../escape", "src/../../escape", "src/./file", "src//file", `C:\escape`, "src\\file", "file:stream", ".git/config", "node_modules/a.js", "dist/main.js", ".env", "src/.env.local", ".npmrc", ".ssh/id_rsa", "src/CON.txt", "nul", "LPT1.png", "trailing.", "trailing ", "src/\nfile"} {
		t.Run(name, func(t *testing.T) {
			p := testSourceProject()
			p.Files[0].Path = name
			if err := p.Validate(); err == nil {
				t.Fatal("accepted unsafe path")
			}
		})
	}
}

func TestSourceProjectRejectsCollisions(t *testing.T) {
	for _, name := range []string{"src/main.tsx", "SRC/Main.tsx", "src", "SRC/main.tsx/child"} {
		t.Run(name, func(t *testing.T) {
			p := testSourceProject()
			p.Files = append(p.Files, ProjectFile{Path: name, Encoding: "utf8", Content: "x"})
			if err := p.Validate(); err == nil {
				t.Fatal("accepted colliding paths")
			}
		})
	}
}

func TestSourceProjectStrictParsing(t *testing.T) {
	b, err := testSourceProject().MarshalYAMLSource()
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]string{
		"unknown":      string(b) + "credentials: secret\n",
		"multiple":     string(b) + "---\n{}\n",
		"empty second": string(b) + "---\n",
		"duplicate":    string(b) + "runtime: other\n",
		"anchor":       strings.Replace(string(b), "files:", "files: &files", 1),
		"alias":        "files: &files [*files]\n",
		"merge":        string(b) + "<<: {runtime: other}\n",
		"oversize":     strings.Repeat(" ", MaxProjectDocumentBytes+1),
		"empty":        "",
		"future":       strings.Replace(string(b), SourceProjectFormat, "crewship-page-source/v99", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSourceProject(strings.NewReader(input)); err == nil {
				t.Fatal("accepted invalid import")
			}
		})
	}
}

func TestSourceProjectLimitsAndEncodings(t *testing.T) {
	for name, change := range map[string]func(*SourceProject){
		"missing package": func(p *SourceProject) { p.Files[1].Path = "other.json" },
		"missing lock":    func(p *SourceProject) { p.Files[2].Path = "other.yaml" },
		"missing entry":   func(p *SourceProject) { p.Files[3].Path = "other.html" },
		"case required":   func(p *SourceProject) { p.Files[1].Path = "Package.json" },
		"runtime":         func(p *SourceProject) { p.Runtime = "unknown" },
		"encoding":        func(p *SourceProject) { p.Files[0].Encoding = "gzip" },
		"bad base64":      func(p *SourceProject) { p.Files[4].Content = "%%%" },
		"base64 newline":  func(p *SourceProject) { p.Files[4].Content += "\n" },
		"utf8":            func(p *SourceProject) { p.Files[0].Content = string([]byte{255}) },
		"nul":             func(p *SourceProject) { p.Files[0].Content = "a\x00b" },
		"file cap":        func(p *SourceProject) { p.Files[0].Content = strings.Repeat("x", MaxProjectFileBytes+1) },
		"base64 cap": func(p *SourceProject) {
			p.Files[4].Content = base64.StdEncoding.EncodeToString(make([]byte, MaxProjectFileBytes+1))
		},
		"count cap": func(p *SourceProject) { p.Files = make([]ProjectFile, MaxProjectFiles+1) },
		"total cap": func(p *SourceProject) {
			for i := range p.Files {
				p.Files[i].Encoding = "utf8"
				p.Files[i].Content = strings.Repeat("x", MaxProjectFileBytes)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := testSourceProject()
			change(p)
			if err := p.Validate(); err == nil {
				t.Fatal("accepted invalid project")
			}
			if _, err := p.MarshalYAMLSource(); err == nil {
				t.Fatal("export accepted invalid project")
			}
		})
	}
}

func TestSourceProjectDigestUsesBytesAndPaths(t *testing.T) {
	p := testSourceProject()
	initial, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	p.Files[0].Content = base64.StdEncoding.EncodeToString([]byte(p.Files[0].Content))
	p.Files[0].Encoding = "base64"
	encoded, err := p.Digest()
	if err != nil || encoded != initial {
		t.Fatalf("encoding changed digest: %v", err)
	}
	p.Files[0].Path = "src/other.tsx"
	renamed, err := p.Digest()
	if err != nil || renamed == initial {
		t.Fatalf("rename did not change digest: %v", err)
	}
	p.Files[0].Content = base64.StdEncoding.EncodeToString([]byte("changed"))
	changed, err := p.Digest()
	if err != nil || changed == renamed {
		t.Fatalf("content did not change digest: %v", err)
	}
}

func TestSourceProjectEncodedExportLimit(t *testing.T) {
	p := testSourceProject()
	// Legal decoded content can exceed the wire budget after YAML escaping.
	p.Files[0].Content = strings.Repeat("\x01", MaxProjectFileBytes)
	p.Files[1].Content = strings.Repeat("\x01", MaxProjectFileBytes)
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.MarshalYAMLSource(); err == nil {
		t.Fatal("export exceeded wire budget")
	}
}

func FuzzParseSourceProject(f *testing.F) {
	b, err := testSourceProject().MarshalYAMLSource()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(b)
	f.Add([]byte("format: unknown\n"))
	f.Add([]byte("files: &files [*files]\n"))
	f.Fuzz(func(t *testing.T, input []byte) {
		p, err := ParseSourceProject(bytes.NewReader(input))
		if err != nil {
			return
		}
		encoded, err := p.MarshalYAMLSource()
		if err != nil {
			// YAML normalization can increase the encoded size past the cap.
			return
		}
		again, err := ParseSourceProject(bytes.NewReader(encoded))
		if err != nil {
			t.Fatalf("valid export could not be imported: %v", err)
		}
		before, err := p.Digest()
		if err != nil {
			t.Fatal(err)
		}
		after, err := again.Digest()
		if err != nil || before != after {
			t.Fatalf("source changed across round trip: %v", err)
		}
	})
}

func TestSourceProjectBoundsEscapedJSON(t *testing.T) {
	p := testSourceProject()
	content := strings.Repeat("<", MaxProjectFileBytes)
	p.Files = append(p.Files, ProjectFile{Path: "public/a.txt", Encoding: "utf8", Content: content}, ProjectFile{Path: "public/b.txt", Encoding: "utf8", Content: content})
	if _, err := p.MarshalYAMLSource(); err == nil {
		t.Fatal("accepted source larger than JSON wire budget")
	}
	for i := len(p.Files) - 2; i < len(p.Files); i++ {
		p.Files[i].Encoding = "base64"
		p.Files[i].Content = base64.StdEncoding.EncodeToString([]byte(content))
	}
	if _, err := p.MarshalYAMLSource(); err != nil {
		t.Fatalf("bounded base64 alternative: %v", err)
	}
}
