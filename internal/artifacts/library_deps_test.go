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

const oflText = "Copyright 2020 The Fixture Project Authors\n\nThis Font Software is licensed under the SIL Open Font License, Version 1.1.\nThis license is copied below, and is also available with a FAQ at:\nhttps://openfontlicense.org\n\n-----------------------------------------------------------\nSIL OPEN FONT LICENSE Version 1.1 - 26 February 2007\n-----------------------------------------------------------\n"

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
	// Pin metadata and update reviews need not load the dependency bytes.
	if pin, err := s.ReadLibraryPin(ctx, consumer, a); err != nil || pinFile(t, pin.Files, "fonts/Inter.woff2").Status != LibraryFileCopied {
		t.Fatalf("pin metadata read: %v", err)
	}
	if _, err := s.CheckLibraryUpdate(ctx, consumer, a, func(string) LibraryVersionAuthorize { return read }); err != nil {
		t.Fatalf("pin check read the bytes: %v", err)
	}
	if _, _, err := s.PinLibrary(ctx, consumer, lib.PageID, LibraryPinExpect{Version: 1}, a, read); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("an unreviewed retry repaired damaged files: %v", err)
	}
	reviewed := LibraryPinExpect{Version: 1, PinnedLibrary: lib.PageID, PinnedVersion: 1}
	if _, changed, err := s.PinLibrary(ctx, consumer, lib.PageID, reviewed, a, read); err != nil || !changed {
		t.Fatalf("reviewed retry did not repair damaged files: changed=%v err=%v", changed, err)
	}
	if files, err := s.ReadLibraryPinFiles(ctx, consumer, a); err != nil || pinFile(t, files, "fonts/Inter.woff2").Status != LibraryFileCopied {
		t.Fatalf("repaired dependencies do not validate: %v", err)
	}
}

func licenceManifest(files map[string]string) VersionManifest {
	m := VersionManifest{Files: map[string]VersionFile{}}
	for name, content := range files {
		m.Files[name] = VersionFile{Content: []byte(content)}
	}
	return m
}

// The licence fallback is conservative (#5248 review): a root licence only
// lets undeclared fonts travel when it is itself a font licence, and only a
// licence's title line counts, never a mention of it.
func TestLibraryLicenceFallbackIsConservative(t *testing.T) {
	font := string(fontDataURL("font/woff2", fontBytes("wOF2", 40)))
	const mit = "MIT License\n\nCopyright (c) 2026 Fixture\n\nPermission is hereby granted, free of charge, to any person obtaining a copy\n"
	const apache = "                                 Apache License\n                           Version 2.0, January 2004\n"
	for name, c := range map[string]struct {
		files   map[string]string
		status  string
		licence string
	}{
		"root MIT LICENSE is the code's licence, not the font's":    {map[string]string{"LICENSE": mit, "fonts/A.woff2": font}, LibraryFileNotCopiedLicence, ""},
		"root Apache LICENSE is the code's licence, not the font's": {map[string]string{"LICENSE": apache, "fonts/A.woff2": font}, LibraryFileNotCopiedLicence, ""},
		"root OFL is a font licence":                                {map[string]string{"OFL.txt": oflText, "fonts/A.woff2": font}, LibraryFileCopied, "OFL-1.1"},
		"root UFL is a font licence":                                {map[string]string{"LICENCE.txt": "-------------------------------\nUBUNTU FONT LICENCE Version 1.0\n-------------------------------\n", "fonts/A.woff2": font}, LibraryFileCopied, "UFL-1.0"},
		"fonts.json declares the licence under a root MIT LICENSE":  {map[string]string{"LICENSE": mit, "fonts/A.woff2": font, "fonts.json": `{"schema_version":1,"fonts":[{"file":"fonts/A.woff2","family":"A","license":"OFL-1.1"}]}`}, LibraryFileCopied, "OFL-1.1"},
		"a sibling that only mentions the OFL":                      {map[string]string{"fonts/LICENSE.txt": "This font is not under the SIL Open Font License, Version 1.1. All rights reserved.\n", "fonts/A.woff2": font}, LibraryFileNotCopiedLicence, ""},
		"a root OFL mention is not the OFL":                         {map[string]string{"OFL.txt": "Unlike our other fonts, this one is not under the SIL Open Font License.\n", "fonts/A.woff2": font}, LibraryFileNotCopiedLicence, ""},
		"a sibling MIT licence titled as such":                      {map[string]string{"fonts/LICENSE": mit, "fonts/A.woff2": font}, LibraryFileCopied, "MIT"},
	} {
		t.Run(name, func(t *testing.T) {
			files, err := libraryDependencies(licenceManifest(c.files))
			if err != nil {
				t.Fatal(err)
			}
			if f := pinFile(t, files, "fonts/A.woff2"); f.Status != c.status || f.Licence != c.licence {
				t.Fatalf("font %+v, want %s %q", f, c.status, c.licence)
			}
		})
	}
}

// Licence files count inside the 4 MiB a pin may copy: four fonts of exactly
// 1 MiB fill it alone, so the licence they travel with leaves the last one out.
func TestLibraryPinTotalCountsLicenceFiles(t *testing.T) {
	files := map[string]string{"fonts/OFL.txt": oflText}
	for _, n := range []string{"A", "B", "C", "D"} {
		files["fonts/"+n+".woff2"] = string(fontDataURL("font/woff2", fontBytes("wOF2", MaxLibraryFontBytes)))
	}
	deps, err := libraryDependencies(licenceManifest(files))
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, f := range deps {
		total += f.Size
	}
	if total > MaxLibraryPinFileBytes {
		t.Fatalf("pin copies %d bytes, over %d", total, MaxLibraryPinFileBytes)
	}
	if got := strings.Join(notCopied(deps), ","); got != "fonts/D.woff2:too_large" {
		t.Fatalf("not copied %q", got)
	}
	if f := pinFile(t, deps, "fonts/OFL.txt"); f.Status != LibraryFileCopied {
		t.Fatalf("licence %+v", f)
	}
}

// A pin made before dependencies travelled (#5223) holds the published version
// but none of its files. The review says so and offers the copy; applying it
// at the same version carries the files.
func TestLibraryLegacyPinOffersItsMissingDependencies(t *testing.T) {
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
	if _, err := s.db.Exec(`DELETE FROM artifact_library_pin_files`); err != nil {
		t.Fatal(err)
	}
	review := func(string) LibraryVersionAuthorize { return read }
	up, err := s.CheckLibraryUpdate(ctx, consumer, a, review)
	if err != nil {
		t.Fatal(err)
	}
	if !up.UpdateAvailable || !up.DependenciesMissing || up.PinnedVersion != 1 || up.PublishedVersion != 1 {
		t.Fatalf("legacy pin review %+v", up)
	}
	if strings.Join(up.FileChanges.Added, ",") != "fonts/Extra.woff,fonts/Huge.ttf,fonts/Inter.woff2,fonts/OFL.txt,fonts/Secret.woff2" || len(up.Changes.Changed) != 0 {
		t.Fatalf("legacy pin changes %+v", up)
	}
	pin, changed, err := s.PinLibrary(ctx, consumer, lib.PageID, LibraryPinExpect{Version: 1, PinnedLibrary: lib.PageID, PinnedVersion: 1}, a, read)
	if err != nil || !changed || len(pin.Files) == 0 {
		t.Fatalf("legacy refresh %+v %v %v", pin, changed, err)
	}
	if up, err = s.CheckLibraryUpdate(ctx, consumer, a, review); err != nil || up.UpdateAvailable || up.DependenciesMissing {
		t.Fatalf("refreshed pin review %+v %v", up, err)
	}
}

// An older publication may hold a font that is not a data URL (it predates
// dependency validation). Reviewing a legacy pin of that version answers
// without dependencies_missing instead of failing the check.
func TestLibraryLegacyPinOfAnUnvalidatedPublicationStillReviews(t *testing.T) {
	s, lib, consumer, a, read := libraryFixture(t)
	ctx := t.Context()
	tokens := `{"colors":{"primary":"#111"}}`
	r := libraryWithFonts(0, tokens, fontBytes("wOF2", 300))
	r.Files["fonts/Inter.woff2"] = VersionFile{Content: []byte("https://fonts.example/Inter.woff2"), MediaType: "font/woff2"}
	if _, err := s.Apply(ctx, lib, r, a); err != nil {
		t.Fatal(err)
	}
	// What a release before #5248 left behind: a publication and a pin with
	// no dependency files, written without today's validation.
	if _, err := s.db.Exec(`INSERT INTO artifact_library_publications(workspace_id,page_id,version,published_by,published_at) VALUES(?,?,1,'user-1','2026-11-01T00:00:00.000Z')`, lib.WorkspaceID, lib.PageID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO artifact_library_pins(workspace_id,page_id,library_page_id,library_version,library_name,content,sha256,copied_by,copied_at) VALUES(?,?,?,1,'Library',?,?,'user-1','2026-11-01T00:00:00.000Z')`, consumer.WorkspaceID, consumer.PageID, lib.PageID, []byte(tokens), sha256Hex([]byte(tokens))); err != nil {
		t.Fatal(err)
	}
	up, err := s.CheckLibraryUpdate(ctx, consumer, a, func(string) LibraryVersionAuthorize { return read })
	if err != nil {
		t.Fatalf("review failed on an unvalidated publication: %v", err)
	}
	if up.UpdateAvailable || up.DependenciesMissing || up.PublishedVersion != 1 || !up.LibraryAvailable {
		t.Fatalf("review %+v", up)
	}
}

func TestLibraryFileReviewIncludesFontAndLicenceMetadata(t *testing.T) {
	old := LibraryPinFile{Path: "fonts/Brand.woff2", MediaType: "font/woff2", Status: LibraryFileCopied, Size: 100, SHA256: strings.Repeat("a", 64), Family: "Brand", Weight: 400, Style: "normal", Licence: "OFL-1.1"}
	for name, mutate := range map[string]func(*LibraryPinFile){
		"family":  func(f *LibraryPinFile) { f.Family = "Brand Text" },
		"weight":  func(f *LibraryPinFile) { f.Weight = 700 },
		"style":   func(f *LibraryPinFile) { f.Style = "italic" },
		"licence": func(f *LibraryPinFile) { f.Licence = "UFL-1.0" },
	} {
		t.Run(name, func(t *testing.T) {
			next := old
			mutate(&next)
			diff := diffLibraryFiles([]LibraryPinFile{old}, []LibraryPinFile{next})
			if strings.Join(diff.Changed, ",") != old.Path || libraryFilesEqual([]LibraryPinFile{old}, []LibraryPinFile{next}) {
				t.Fatalf("identical bytes hid metadata change: %+v", diff)
			}
		})
	}
}
