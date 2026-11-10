package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Library dependencies (#5248). A pin copies, besides design-system.json,
// the files the system needs to render: its font files and the licence files
// that let them travel. Tokens are closed literals (hex colours, pixels, plain
// font-family names), so they cannot reference any other asset; component
// previews belong to the Library view and are not consumer dependencies.
//
// Project files are UTF-8 text, so a library stores a font as a canonical
// base64 data URL ("data:font/woff2;base64,…") under a font extension. The
// pin keeps the decoded bytes. An optional fonts.json maps font files to a
// family, weight, style and explicit licence; a font without a redistributable
// licence (an explicit SPDX id from the allowlist, else a sibling licence file
// titled with one, else a root font licence) is listed but never copied.
// Nothing is fetched.
const (
	LibraryFontsFile = "fonts.json"
	// MaxLibraryFontBytes caps one decoded font file.
	MaxLibraryFontBytes = 1 << 20
	// MaxLibraryPinFileBytes caps every copied dependency together, fonts and
	// the licence files they travel with.
	MaxLibraryPinFileBytes = 4 << 20
	// MaxLibraryPinFiles caps the dependency files a pin lists.
	MaxLibraryPinFiles     = 32
	maxLibraryLicenceBytes = 64 << 10
)

// Dependency status in a pin.
const (
	LibraryFileCopied           = "copied"
	LibraryFileNotCopiedLicence = "licence"
	LibraryFileNotCopiedSize    = "too_large"
)

type LibraryPinFile struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	Status    string `json:"status"`
	// Size and SHA256 describe the copied (decoded) bytes; both are empty for
	// a file that was not copied.
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Family  string `json:"family,omitempty"`
	Weight  int    `json:"weight,omitempty"`
	Style   string `json:"style,omitempty"`
	Licence string `json:"licence,omitempty"`
	Content []byte `json:"-"`
}

var (
	fontExtensions = map[string]string{".woff2": "font/woff2", ".woff": "font/woff", ".ttf": "font/ttf", ".otf": "font/otf"}
	// Redistributable font licences by SPDX id.
	redistributableLicences = map[string]bool{"OFL-1.1": true, "OFL-1.0": true, "Apache-2.0": true, "MIT": true, "UFL-1.0": true, "CC0-1.0": true, "Bitstream-Vera": true, "BSD-3-Clause": true}
	licenceFileName         = regexp.MustCompile(`(?i)^(licen[cs]e|ofl|copying)([-_.][a-z0-9.-]*)?$`)
	// licenceTitles match a licence by its title line (anchored at a line
	// start), never by a sentence that mentions it. font marks the licences
	// written for fonts, the only ones a root licence file may grant: a
	// repository's MIT or Apache LICENSE is usually its code's licence.
	licenceTitles = []struct {
		id   string
		font bool
		re   *regexp.Regexp
	}{
		{"OFL-1.1", true, regexp.MustCompile(`(?im)^[ \t]*SIL[ \t]+OPEN[ \t]+FONT[ \t]+LICEN[CS]E,?[ \t]+Version[ \t]+1\.1\b`)},
		{"UFL-1.0", true, regexp.MustCompile(`(?im)^[ \t]*UBUNTU[ \t]+FONT[ \t]+LICEN[CS]E,?[ \t]+Version[ \t]+1\.0\b`)},
		{"Apache-2.0", false, regexp.MustCompile(`(?im)^[ \t]*Apache[ \t]+License,?[ \t]*(\r?\n[ \t]*)?Version[ \t]+2\.0\b`)},
		{"MIT", false, regexp.MustCompile(`(?im)^[ \t]*(The[ \t]+)?MIT[ \t]+Licen[cs]e[ \t]*\r?$`)},
		{"CC0-1.0", false, regexp.MustCompile(`(?im)^[ \t]*(Creative[ \t]+Commons[ \t]+)?(Legal[ \t]+Code[ \t]+)?CC0[ \t]+1\.0[ \t]+Universal\b`)},
	}
	fontFamily = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]{0,63}$`)
	base64Body = regexp.MustCompile(`^[A-Za-z0-9+/]*={0,2}$`)
)

type libraryFontDecl struct {
	File    string `json:"file"`
	Family  string `json:"family"`
	Weight  int    `json:"weight"`
	Style   string `json:"style"`
	License string `json:"license"`
}

func decodeStrict(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return errors.New("trailing data")
	}
	return nil
}

func libraryFontDecls(m VersionManifest) (map[string]libraryFontDecl, error) {
	f, ok := m.Files[LibraryFontsFile]
	if !ok {
		return map[string]libraryFontDecl{}, nil
	}
	var doc struct {
		SchemaVersion int               `json:"schema_version"`
		Fonts         []libraryFontDecl `json:"fonts"`
	}
	if len(f.Content) > MaxLibraryBytes || decodeStrict(f.Content, &doc) != nil {
		return nil, fmt.Errorf("%w: %s is not a valid fonts file", ErrVersionInvalid, LibraryFontsFile)
	}
	if doc.SchemaVersion != 1 {
		return nil, fmt.Errorf("%w: %s schema_version must be 1", ErrVersionInvalid, LibraryFontsFile)
	}
	out := make(map[string]libraryFontDecl, len(doc.Fonts))
	for _, d := range doc.Fonts {
		if _, ok := fontExtensions[strings.ToLower(path.Ext(d.File))]; !ok {
			return nil, fmt.Errorf("%w: %s names %q, which is not a font file", ErrVersionInvalid, LibraryFontsFile, d.File)
		}
		if _, ok := m.Files[d.File]; !ok {
			return nil, fmt.Errorf("%w: %s names %q, which the library does not contain", ErrVersionInvalid, LibraryFontsFile, d.File)
		}
		if _, dup := out[d.File]; dup || !fontFamily.MatchString(d.Family) || (d.Weight != 0 && (d.Weight < 100 || d.Weight > 900 || d.Weight%100 != 0)) || (d.Style != "" && d.Style != "normal" && d.Style != "italic") || len(d.License) > 64 {
			return nil, fmt.Errorf("%w: %s entry for %q is invalid (family: letters, digits, space, - or _; weight 100-900 in steps of 100; style normal or italic)", ErrVersionInvalid, LibraryFontsFile, d.File)
		}
		out[d.File] = d
	}
	return out, nil
}

// decodeLibraryFont reads a font file's data URL and checks the bytes start
// like the font its extension names.
func decodeLibraryFont(name string, f VersionFile) ([]byte, string, error) {
	media := fontExtensions[strings.ToLower(path.Ext(name))]
	prefix := "data:" + media + ";base64,"
	body, ok := bytes.CutPrefix(f.Content, []byte(prefix))
	if !ok || !base64Body.Match(body) {
		return nil, "", fmt.Errorf("%w: %s must be a %s base64 data URL", ErrVersionInvalid, name, prefix)
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(string(body))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %s is not valid base64", ErrVersionInvalid, name)
	}
	var magic []string
	switch media {
	case "font/woff2":
		magic = []string{"wOF2"}
	case "font/woff":
		magic = []string{"wOFF"}
	default:
		magic = []string{"\x00\x01\x00\x00", "OTTO", "true"}
	}
	for _, m := range magic {
		if bytes.HasPrefix(raw, []byte(m)) {
			return raw, media, nil
		}
	}
	return nil, "", fmt.Errorf("%w: %s is not a %s font", ErrVersionInvalid, name, media)
}

// libraryLicenceFor finds the licence that lets a font travel. An explicit
// SPDX id in fonts.json wins, redistributable or not. Otherwise a licence file
// next to the font may grant any listed licence by its title line, and a
// licence file at the library root only a font licence (OFL or UFL): the root
// LICENSE of a repository is usually its code's, not its fonts'.
func libraryLicenceFor(m VersionManifest, font string, decl libraryFontDecl) (id, file string) {
	if decl.License != "" {
		if redistributableLicences[decl.License] {
			return decl.License, ""
		}
		return "", ""
	}
	dirs := []string{path.Dir(font)}
	if dirs[0] != "." {
		dirs = append(dirs, ".")
	}
	for _, dir := range dirs {
		names := make([]string, 0)
		for name := range m.Files {
			if path.Dir(name) == dir && licenceFileName.MatchString(path.Base(name)) && len(m.Files[name].Content) <= maxLibraryLicenceBytes {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			for _, l := range licenceTitles {
				if (dir != "." || l.font) && l.re.Match(m.Files[name].Content) {
					return l.id, name
				}
			}
		}
	}
	return "", ""
}

func sha256Hex(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

// libraryDependencies lists what a pin of this version copies, in path order.
// An invalid fonts.json or font file is ErrVersionInvalid; a font without a
// redistributable licence, or over a size cap, is listed as not copied.
func libraryDependencies(m VersionManifest) ([]LibraryPinFile, error) {
	decls, err := libraryFontDecls(m)
	if err != nil {
		return nil, err
	}
	fonts := make([]string, 0)
	for name := range m.Files {
		if _, ok := fontExtensions[strings.ToLower(path.Ext(name))]; ok {
			fonts = append(fonts, name)
		}
	}
	sort.Strings(fonts)
	var out []LibraryPinFile
	licences := map[string]bool{}
	// total counts every copied byte, licence files included, against
	// MaxLibraryPinFileBytes; a licence is counted once, with the first font
	// it lets travel.
	var total int64
	licenceCost := func(file string) int64 {
		if file == "" || licences[file] {
			return 0
		}
		return int64(len(m.Files[file].Content))
	}
	for _, name := range fonts {
		raw, media, err := decodeLibraryFont(name, m.Files[name])
		if err != nil {
			return nil, err
		}
		d := decls[name]
		f := LibraryPinFile{Path: name, MediaType: media, Family: d.Family, Weight: d.Weight, Style: d.Style}
		id, file := libraryLicenceFor(m, name, d)
		switch {
		case id == "":
			f.Status = LibraryFileNotCopiedLicence
		case len(raw) > MaxLibraryFontBytes || total+int64(len(raw))+licenceCost(file) > MaxLibraryPinFileBytes:
			f.Status = LibraryFileNotCopiedSize
		default:
			f.Status, f.Licence, f.Content, f.Size, f.SHA256 = LibraryFileCopied, id, raw, int64(len(raw)), sha256Hex(raw)
			total += f.Size + licenceCost(file)
			if file != "" {
				licences[file] = true
			}
		}
		out = append(out, f)
	}
	names := make([]string, 0, len(licences))
	for name := range licences {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := m.Files[name].Content
		out = append(out, LibraryPinFile{Path: name, MediaType: "text/plain", Status: LibraryFileCopied, Content: c, Size: int64(len(c)), SHA256: sha256Hex(c)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if len(out) > MaxLibraryPinFiles {
		return nil, fmt.Errorf("%w: a library pin carries at most %d dependency files", ErrVersionInvalid, MaxLibraryPinFiles)
	}
	return out, nil
}

func writeLibraryPinFiles(ctx context.Context, tx *sql.Tx, k VersionKey, files []LibraryPinFile) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM artifact_library_pin_files WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID); err != nil {
		return err
	}
	for _, f := range files {
		content := f.Content
		if content == nil {
			content = []byte{}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_library_pin_files(workspace_id,page_id,path,media_type,status,content,size,sha256,family,weight,style,licence) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, k.WorkspaceID, k.PageID, f.Path, f.MediaType, f.Status, content, f.Size, f.SHA256, f.Family, f.Weight, f.Style, f.Licence); err != nil {
			return err
		}
	}
	return nil
}

// readLibraryPinFiles returns the pin's dependency files with their bytes,
// each re-checked against its SHA-256.
func readLibraryPinFiles(ctx context.Context, tx *sql.Tx, k VersionKey) ([]LibraryPinFile, error) {
	rows, err := tx.QueryContext(ctx, `SELECT path,media_type,status,content,size,sha256,family,weight,style,licence FROM artifact_library_pin_files WHERE workspace_id=? AND page_id=? ORDER BY path`, k.WorkspaceID, k.PageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LibraryPinFile{}
	for rows.Next() {
		var f LibraryPinFile
		if err = rows.Scan(&f.Path, &f.MediaType, &f.Status, &f.Content, &f.Size, &f.SHA256, &f.Family, &f.Weight, &f.Style, &f.Licence); err != nil {
			return nil, err
		}
		if f.Status == LibraryFileCopied {
			if int64(len(f.Content)) != f.Size || sha256Hex(f.Content) != f.SHA256 {
				return nil, ErrLibraryPinCorrupt
			}
		} else {
			f.Content = nil
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func withoutContent(files []LibraryPinFile) []LibraryPinFile {
	out := make([]LibraryPinFile, len(files))
	for i, f := range files {
		f.Content = nil
		out[i] = f
	}
	return out
}

// readLibraryPinFileMeta lists the pin's dependency files without loading
// their bytes. Bytes are loaded, and their SHA-256 re-checked, only where they
// are served (readLibraryPinFiles) and before a retried apply is considered
// a no-op. Reading the pin or reviewing an update needs metadata only.
func readLibraryPinFileMeta(ctx context.Context, tx *sql.Tx, k VersionKey) ([]LibraryPinFile, error) {
	rows, err := tx.QueryContext(ctx, `SELECT path,media_type,status,size,sha256,family,weight,style,licence FROM artifact_library_pin_files WHERE workspace_id=? AND page_id=? ORDER BY path`, k.WorkspaceID, k.PageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LibraryPinFile{}
	for rows.Next() {
		var f LibraryPinFile
		if err = rows.Scan(&f.Path, &f.MediaType, &f.Status, &f.Size, &f.SHA256, &f.Family, &f.Weight, &f.Style, &f.Licence); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ReadLibraryPinFiles returns the consumer's pinned dependency bytes under
// the consumer's own read authority; the library is not read.
func (s *VersionStore) ReadLibraryPinFiles(ctx context.Context, consumer VersionKey, a VersionAuthorize) ([]LibraryPinFile, error) {
	_, files, err := s.ReadLibraryPinSnapshot(ctx, consumer, a)
	return files, err
}

// ReadLibraryPinSnapshot returns verified tokens and dependencies from one
// consumer snapshot, so a concurrent repin cannot mix two versions.
func (s *VersionStore) ReadLibraryPinSnapshot(ctx context.Context, consumer VersionKey, a VersionAuthorize) (pin LibraryPin, files []LibraryPinFile, err error) {
	err = s.transaction(ctx, consumer, a, func(tx *sql.Tx, _ string) error {
		current, found, e := readLibraryPin(ctx, tx, consumer)
		if e != nil {
			return e
		}
		if !found {
			return ErrVersionNotFound
		}
		pin = current
		files, e = readLibraryPinFiles(ctx, tx, consumer)
		return e
	})
	if err != nil {
		return LibraryPin{}, nil, err
	}
	return
}

// diffLibraryFiles compares bytes and the metadata that affects rendering
// or licence review. The content hash represents the bytes.
func diffLibraryFiles(from, to []LibraryPinFile) LibraryChanges {
	type descriptor struct {
		media, status, sha, family, style, licence string
		size                                       int64
		weight                                     int
	}
	key := func(f LibraryPinFile) descriptor {
		return descriptor{f.MediaType, f.Status, f.SHA256, f.Family, f.Style, f.Licence, f.Size, f.Weight}
	}
	a, b := map[string]descriptor{}, map[string]descriptor{}
	for _, f := range from {
		a[f.Path] = key(f)
	}
	for _, f := range to {
		b[f.Path] = key(f)
	}
	out := LibraryChanges{Added: []string{}, Removed: []string{}, Changed: []string{}}
	for p, v := range b {
		if old, ok := a[p]; !ok {
			out.Added = append(out.Added, p)
		} else if old != v {
			out.Changed = append(out.Changed, p)
		}
	}
	for p := range a {
		if _, ok := b[p]; !ok {
			out.Removed = append(out.Removed, p)
		}
	}
	for _, list := range []*[]string{&out.Added, &out.Removed, &out.Changed} {
		sort.Strings(*list)
	}
	return out
}

func notCopied(files []LibraryPinFile) []string {
	out := []string{}
	for _, f := range files {
		if f.Status != LibraryFileCopied {
			out = append(out, f.Path+":"+f.Status)
		}
	}
	return out
}

func libraryFilesEqual(a, b []LibraryPinFile) bool {
	d := diffLibraryFiles(a, b)
	return len(d.Added)+len(d.Removed)+len(d.Changed) == 0
}
