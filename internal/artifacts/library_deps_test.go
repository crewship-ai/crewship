package artifacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

const oflText = "Copyright 2020 The Fixture Project Authors\n\nThis Font Software is licensed under the SIL Open Font License, Version 1.1.\n"

func fontBytes(magic string, size int) []byte {
	b := bytes.Repeat([]byte{0x2a}, size)
	copy(b, magic)
	return b
}

func fontDataURL(media string, b []byte) []byte {
	return []byte("data:" + media + ";base64," + base64.StdEncoding.EncodeToString(b))
}

// libraryWithFonts is a library version that declares three fonts: one under
// a sibling OFL file, one marked proprietary and one over the size cap, plus
// an undeclared licensed font.
func libraryWithFonts(base int64, tokens string, inter []byte) VersionRequest {
	r := libraryRequest(base, tokens)
	r.Files["fonts.json"] = VersionFile{Content: []byte(`{"schema_version":1,"fonts":[
		{"file":"fonts/Inter.woff2","family":"Inter","weight":400,"style":"normal"},
		{"file":"fonts/Secret.woff2","family":"Secret","license":"LicenseRef-Proprietary"},
		{"file":"fonts/Huge.ttf","family":"Huge","license":"OFL-1.1"}]}`), MediaType: "application/json"}
	r.Files["fonts/Inter.woff2"] = VersionFile{Content: fontDataURL("font/woff2", inter), MediaType: "font/woff2"}
	r.Files["fonts/OFL.txt"] = VersionFile{Content: []byte(oflText), MediaType: "text/plain"}
	r.Files["fonts/Secret.woff2"] = VersionFile{Content: fontDataURL("font/woff2", fontBytes("wOF2", 64)), MediaType: "font/woff2"}
	r.Files["fonts/Huge.ttf"] = VersionFile{Content: fontDataURL("font/ttf", fontBytes("\x00\x01\x00\x00", MaxLibraryFontBytes+1)), MediaType: "font/ttf"}
	r.Files["fonts/Extra.woff"] = VersionFile{Content: fontDataURL("font/woff", fontBytes("wOFF", 32)), MediaType: "font/woff"}
	return r
}

func pinFile(t *testing.T, files []LibraryPinFile, path string) LibraryPinFile {
	t.Helper()
	for _, f := range files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no pinned file %s in %+v", path, files)
	return LibraryPinFile{}
}

func TestLibraryPinCopiesFontsWithinLicenceAndSizeLimits(t *testing.T) {
	s, lib, consumer, a, read := libraryFixture(t)
	ctx := t.Context()
	inter := fontBytes("wOF2", 300)
	if _, err := s.Apply(ctx, lib, libraryWithFonts(0, `{"colors":{"primary":"#111"}}`, inter), a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishLibrary(ctx, lib, 1, a); err != nil {
		t.Fatal(err)
	}
	pin, _, err := s.PinLibrary(ctx, consumer, lib.PageID, LibraryPinExpect{Version: 1}, a, read)
	if err != nil {
		t.Fatal(err)
	}
	f := pinFile(t, pin.Files, "fonts/Inter.woff2")
	sum := sha256.Sum256(inter)
	if f.Status != LibraryFileCopied || f.MediaType != "font/woff2" || f.Size != int64(len(inter)) || f.SHA256 != hex.EncodeToString(sum[:]) || f.Family != "Inter" || f.Weight != 400 || f.Style != "normal" {
		t.Fatalf("Inter %+v", f)
	}
	// The licence that lets the font travel travels with it.
	if l := pinFile(t, pin.Files, "fonts/OFL.txt"); l.Status != LibraryFileCopied || l.MediaType != "text/plain" {
		t.Fatalf("licence %+v", l)
	}
	// An explicit non-redistributable licence wins over the sibling OFL file.
	if f := pinFile(t, pin.Files, "fonts/Secret.woff2"); f.Status != LibraryFileNotCopiedLicence || f.Size != 0 || f.SHA256 != "" {
		t.Fatalf("Secret %+v", f)
	}
	if f := pinFile(t, pin.Files, "fonts/Huge.ttf"); f.Status != LibraryFileNotCopiedSize {
		t.Fatalf("Huge %+v", f)
	}
	// An undeclared font under the same licence is copied, without a family.
	if f := pinFile(t, pin.Files, "fonts/Extra.woff"); f.Status != LibraryFileCopied || f.Family != "" {
		t.Fatalf("Extra %+v", f)
	}
	files, err := s.ReadLibraryPinFiles(ctx, consumer, a)
	if err != nil {
		t.Fatal(err)
	}
	if got := pinFile(t, files, "fonts/Inter.woff2"); !bytes.Equal(got.Content, inter) {
		t.Fatalf("pinned bytes differ: %d", len(got.Content))
	}
	if got := pinFile(t, files, "fonts/Secret.woff2"); got.Content != nil {
		t.Fatalf("a font that was not copied has bytes")
	}

	// Editing, then restoring, the live library never changes the copy.
	if _, err = s.Apply(ctx, lib, libraryWithFonts(1, `{"colors":{"primary":"#222"}}`, fontBytes("wOF2", 500)), a); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Restore(ctx, lib, 2, 1, a); err != nil {
		t.Fatal(err)
	}
	files, err = s.ReadLibraryPinFiles(ctx, consumer, a)
	if err != nil || !bytes.Equal(pinFile(t, files, "fonts/Inter.woff2").Content, inter) {
		t.Fatalf("pin changed with the library: %v", err)
	}
}

func TestLibraryPinUpdateReviewListsFileChanges(t *testing.T) {
	s, lib, consumer, a, read := libraryFixture(t)
	ctx := t.Context()
	if _, err := s.Apply(ctx, lib, libraryWithFonts(0, `{"colors":{"primary":"#111"}}`, fontBytes("wOF2", 300)), a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishLibrary(ctx, lib, 1, a); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PinLibrary(ctx, consumer, lib.PageID, LibraryPinExpect{Version: 1}, a, read); err != nil {
		t.Fatal(err)
	}
	next := libraryWithFonts(1, `{"colors":{"primary":"#111"}}`, fontBytes("wOF2", 400))
	delete(next.Files, "fonts/Extra.woff")
	next.Files["fonts/Mono.otf"] = VersionFile{Content: fontDataURL("font/otf", fontBytes("OTTO", 40)), MediaType: "font/otf"}
	if _, err := s.Apply(ctx, lib, next, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishLibrary(ctx, lib, 2, a); err != nil {
		t.Fatal(err)
	}
	up, err := s.CheckLibraryUpdate(ctx, consumer, a, func(string) LibraryVersionAuthorize { return read })
	if err != nil || !up.UpdateAvailable {
		t.Fatalf("update %+v %v", up, err)
	}
	fc := up.FileChanges
	if strings.Join(fc.Added, ",") != "fonts/Mono.otf" || strings.Join(fc.Removed, ",") != "fonts/Extra.woff" || strings.Join(fc.Changed, ",") != "fonts/Inter.woff2" {
		t.Fatalf("file changes %+v", fc)
	}
	if strings.Join(up.NotCopied, ",") != "fonts/Huge.ttf:too_large,fonts/Secret.woff2:licence" {
		t.Fatalf("not copied %v", up.NotCopied)
	}
}

func TestLibraryDependencyValidation(t *testing.T) {
	for name, mutate := range map[string]func(VersionRequest){
		"font that is not a font": func(r VersionRequest) {
			r.Files["fonts/Inter.woff2"] = VersionFile{Content: fontDataURL("font/woff2", []byte("<script>x</script>")), MediaType: "font/woff2"}
		},
		"font that is not base64": func(r VersionRequest) {
			r.Files["fonts/Inter.woff2"] = VersionFile{Content: []byte("data:font/woff2;base64,%%%"), MediaType: "font/woff2"}
		},
		"declared font missing": func(r VersionRequest) { delete(r.Files, "fonts/Inter.woff2") },
		"family with CSS in it": func(r VersionRequest) {
			r.Files["fonts.json"] = VersionFile{Content: []byte(`{"schema_version":1,"fonts":[{"file":"fonts/Inter.woff2","family":"x;}body{color:red"}]}`), MediaType: "application/json"}
		},
		"unknown fonts.json key": func(r VersionRequest) {
			r.Files["fonts.json"] = VersionFile{Content: []byte(`{"schema_version":1,"fonts":[],"url":"https://x"}`), MediaType: "application/json"}
		},
		"newer fonts.json schema": func(r VersionRequest) {
			r.Files["fonts.json"] = VersionFile{Content: []byte(`{"schema_version":2,"fonts":[]}`), MediaType: "application/json"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, lib, _, a, _ := libraryFixture(t)
			ctx := t.Context()
			r := libraryWithFonts(0, `{"colors":{"primary":"#111"}}`, fontBytes("wOF2", 300))
			mutate(r)
			receipt, err := s.Apply(ctx, lib, r, a)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.PublishLibrary(ctx, lib, receipt.Version, a); !errors.Is(err, ErrVersionInvalid) {
				t.Fatalf("publish accepted an invalid dependency: %v", err)
			}
		})
	}
}

func TestLibraryPinFilesRejectACorruptCopy(t *testing.T) {
	s, lib, consumer, a, read := libraryFixture(t)
	ctx := t.Context()
	if _, err := s.Apply(ctx, lib, libraryWithFonts(0, `{"colors":{"primary":"#111"}}`, fontBytes("wOF2", 300)), a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishLibrary(ctx, lib, 1, a); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PinLibrary(ctx, consumer, lib.PageID, LibraryPinExpect{Version: 1}, a, read); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE artifact_library_pin_files SET content=zeroblob(size) WHERE path='fonts/Inter.woff2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadLibraryPinFiles(ctx, consumer, a); !errors.Is(err, ErrLibraryPinCorrupt) {
		t.Fatalf("corrupt file read: %v", err)
	}
	if _, err := s.ReadLibraryPin(ctx, consumer, a); !errors.Is(err, ErrLibraryPinCorrupt) {
		t.Fatalf("corrupt pin read: %v", err)
	}
}
