package mooring

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

type deckTestPart struct {
	name, body string
	method     uint16
	mode       uint32
}

const deckTestManifest = `{"format":"deckcraft","version":1,"generator":"DeckCraft 0.11.1","presentation":{"slides":[{"id":1,"text":"Authored","media":7}],"exact":9007199254740993}}`

func deckTestZIP(t *testing.T, parts ...deckTestPart) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, p := range parts {
		h := &zip.FileHeader{Name: p.name, Method: p.method}
		if p.mode != 0 {
			h.ExternalAttrs = p.mode << 16
			h.CreatorVersion = 3 << 8
		}
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(p.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func deckTestParts() []deckTestPart {
	return []deckTestPart{{name: "mimetype", body: deckMIME}, {name: "presentation.json", body: deckTestManifest, method: zip.Deflate}, {name: "media/7", body: "asset bytes"}, {name: "chart-parts/0", body: "chart bytes"}}
}
func TestDeckArchivePreservesAuthoredContent(t *testing.T) {
	raw := deckTestZIP(t, deckTestParts()...)
	a, err := parseDeckArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !preservesDeckArchive(a, a) {
		t.Fatal("same archive rejected")
	}
	for _, tc := range []struct {
		name string
		edit func([]deckTestPart) []deckTestPart
		want bool
	}{
		{"added default", func(p []deckTestPart) []deckTestPart {
			p[1].body = strings.Replace(p[1].body, `"exact":`, `"added":true,"exact":`, 1)
			return p
		}, true},
		{"lost authored text", func(p []deckTestPart) []deckTestPart {
			p[1].body = strings.Replace(p[1].body, "Authored", "", 1)
			return p
		}, false},
		{"large integer changed", func(p []deckTestPart) []deckTestPart {
			p[1].body = strings.Replace(p[1].body, "9007199254740993", "9007199254740992", 1)
			return p
		}, false},
		{"missing asset", func(p []deckTestPart) []deckTestPart { return p[:3] }, false},
		{"asset changed", func(p []deckTestPart) []deckTestPart { p[2].body = "asset byteS"; return p }, false},
		{"extra asset", func(p []deckTestPart) []deckTestPart {
			return append(p, deckTestPart{name: "media/8", body: "unexpected"})
		}, false},
		{"id repaired", func(p []deckTestPart) []deckTestPart {
			p[1].body = strings.Replace(p[1].body, `"id":1`, `"id":2`, 1)
			return p
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, e := parseDeckArchive(deckTestZIP(t, tc.edit(deckTestParts())...))
			if e != nil {
				t.Fatal(e)
			}
			if got := preservesDeckArchive(a, b); got != tc.want {
				t.Fatalf("preserved=%v want %v", got, tc.want)
			}
		})
	}
}
func TestDeckArchiveRejectsUnsafeZIP(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func([]deckTestPart) []deckTestPart
	}{
		{"duplicate", func(p []deckTestPart) []deckTestPart { return append(p, p[2]) }},
		{"traversal", func(p []deckTestPart) []deckTestPart { p[2].name = "media/../7"; return p }},
		{"absolute", func(p []deckTestPart) []deckTestPart { p[2].name = "/media/7"; return p }},
		{"backslash", func(p []deckTestPart) []deckTestPart { p[2].name = `media\7`; return p }},
		{"noncanonical id", func(p []deckTestPart) []deckTestPart { p[2].name = "media/007"; return p }},
		{"overflow id", func(p []deckTestPart) []deckTestPart { p[2].name = "media/4294967296"; return p }},
		{"unknown part", func(p []deckTestPart) []deckTestPart { return append(p, deckTestPart{name: "other.xml", body: "x"}) }},
		{"directory", func(p []deckTestPart) []deckTestPart { return append(p, deckTestPart{name: "media/"}) }},
		{"symlink", func(p []deckTestPart) []deckTestPart { p[2].mode = 0120777; return p }},
		{"compressed mime", func(p []deckTestPart) []deckTestPart { p[0].method = zip.Deflate; return p }},
		{"wrong mime", func(p []deckTestPart) []deckTestPart { p[0].body = "application/zip"; return p }},
		{"mime not first", func(p []deckTestPart) []deckTestPart { p[0], p[1] = p[1], p[0]; return p }},
		{"missing manifest", func(p []deckTestPart) []deckTestPart { return append(p[:1], p[2:]...) }},
		{"wrong format", func(p []deckTestPart) []deckTestPart {
			p[1].body = strings.Replace(p[1].body, "deckcraft", "other", 1)
			return p
		}},
		{"unsupported version", func(p []deckTestPart) []deckTestPart {
			p[1].body = strings.Replace(p[1].body, `"version":1`, `"version":2`, 1)
			return p
		}},
		{"duplicate json key", func(p []deckTestPart) []deckTestPart {
			p[1].body = strings.Replace(p[1].body, `"id":1`, `"id":9,"id":1`, 1)
			return p
		}},
		{"trailing json", func(p []deckTestPart) []deckTestPart { p[1].body += " {}"; return p }},
		{"depth", func(p []deckTestPart) []deckTestPart {
			p[1].body = `{"format":"deckcraft","version":1,"presentation":{"x":` + strings.Repeat("[", 129) + "0" + strings.Repeat("]", 129) + "}}"
			return p
		}},
		{"entry count", func(p []deckTestPart) []deckTestPart {
			for i := 0; i < deckEntryLimit; i++ {
				p = append(p, deckTestPart{name: fmt.Sprintf("media/%d", i+10)})
			}
			return p
		}},
		{"manifest expanded bound", func(p []deckTestPart) []deckTestPart {
			p[1].body = strings.Repeat(" ", deckManifestLimit) + deckTestManifest
			return p
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := parseDeckArchive(deckTestZIP(t, tc.edit(deckTestParts())...)); e == nil {
				t.Fatal("unsafe ZIP accepted")
			}
		})
	}
	raw := deckTestZIP(t, deckTestParts()...)
	t.Run("crc", func(t *testing.T) {
		b := append([]byte(nil), raw...)
		i := bytes.Index(b, []byte("asset bytes"))
		b[i] ^= 1
		if _, e := parseDeckArchive(b); e == nil {
			t.Fatal("bad CRC accepted")
		}
	})
	t.Run("encrypted", func(t *testing.T) {
		b := append([]byte(nil), raw...)
		for i := 0; i+46 < len(b); i++ {
			if binary.LittleEndian.Uint32(b[i:]) == 0x02014b50 {
				b[i+8] |= 1
				break
			}
		}
		if _, e := parseDeckArchive(b); e == nil {
			t.Fatal("encrypted accepted")
		}
	})
	t.Run("raw bound", func(t *testing.T) {
		if _, e := parseDeckArchive(make([]byte, deckRawLimit+1)); e == nil {
			t.Fatal("oversize accepted")
		}
	})
	t.Run("truncated", func(t *testing.T) {
		if _, e := parseDeckArchive(raw[:len(raw)-8]); e == nil {
			t.Fatal("truncated accepted")
		}
	})
}

func TestDeckArchiveExpansionBounds(t *testing.T) {
	t.Run("single asset", func(t *testing.T) {
		p := deckTestParts()
		p[2].body = strings.Repeat("x", deckRawLimit+1)
		p[2].method = zip.Deflate
		if _, err := parseDeckArchive(deckTestZIP(t, p...)); err == nil {
			t.Fatal("oversize expanded asset accepted")
		}
	})
	t.Run("aggregate assets", func(t *testing.T) {
		p := deckTestParts()[:2]
		chunk := strings.Repeat("x", 27<<20)
		for i := 0; i < 5; i++ {
			p = append(p, deckTestPart{name: fmt.Sprintf("media/%d", i), body: chunk, method: zip.Deflate})
		}
		raw := deckTestZIP(t, p...)
		if len(raw) > deckRawLimit {
			t.Fatal("fixture must fit raw bound")
		}
		if _, err := parseDeckArchive(raw); err == nil {
			t.Fatal("aggregate expanded limit ignored")
		}
	})
}

func TestDeckManifestRejectsAmbiguousJSON(t *testing.T) {
	for _, raw := range []string{
		`{"format":"deckcraft","version":1,"presentation":{"text":"\uD800"}}`,
		`{"format":"deckcraft","version":1,"presentation":{"text":"\uDC00"}}`,
		`{"format":"deckcraft","version":1,"presentation":{"text":"\uD800\u0041"}}`,
		`{"format":"deckcraft","version":1,"presentation":{"x":1,"\u0078":2}}`,
		`{"format":"deckcraft","version":1,"presentation":[]}`,
		`{"format":"deckcraft","version":1,"presentation":{}} {}`,
		"{\"format\":\"deckcraft\",\"version\":1,\"presentation\":{\"text\":\"\xff\"}}",
	} {
		if _, err := decodeDeckManifest([]byte(raw)); err == nil {
			t.Fatal("ambiguous JSON accepted")
		}
	}
}

func TestDeckManifestAcceptsValidSurrogatePair(t *testing.T) {
	if _, err := decodeDeckManifest([]byte(`{"format":"deckcraft","version":1,"presentation":{"text":"\uD83D\uDE00"}}`)); err != nil {
		t.Fatal(err)
	}
}

func TestDeckArchiveRejectsEncryptionVariantFlags(t *testing.T) {
	for _, flag := range []uint16{1 << 6, 1 << 13} {
		t.Run(fmt.Sprint(flag), func(t *testing.T) {
			b := deckTestZIP(t, deckTestParts()...)
			for i := 0; i+46 < len(b); i++ {
				if binary.LittleEndian.Uint32(b[i:]) == 0x02014b50 {
					binary.LittleEndian.PutUint16(b[i+8:], binary.LittleEndian.Uint16(b[i+8:])|flag)
					break
				}
			}
			if _, err := parseDeckArchive(b); err == nil {
				t.Fatal("encryption variant flag accepted")
			}
		})
	}
}
