package mooring

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Deck archives are parsed before invoking the original engine. These bounds
// deliberately do not inherit the desktop loader's multi-gigabyte allowances.
const (
	deckRawLimit       = 32 << 20
	deckManifestLimit  = 16 << 20
	deckExpandedLimit  = 128 << 20
	deckEntryLimit     = 2048
	deckJSONDepthLimit = 128
	deckMIME           = "application/vnd.storyteller.deckcraft+zip"
)

type deckArchive struct {
	manifest any
	assets   map[string]deckAsset
}
type deckAsset struct {
	size   uint64
	digest [32]byte
}

func parseDeckArchive(raw []byte) (*deckArchive, error) {
	if len(raw) == 0 || len(raw) > deckRawLimit {
		return nil, ErrNativeValidation
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(zr.File) < 2 || len(zr.File) > deckEntryLimit {
		return nil, ErrNativeValidation
	}
	result := &deckArchive{assets: make(map[string]deckAsset)}
	seen := make(map[string]bool, len(zr.File))
	var expanded uint64
	for i, f := range zr.File {
		if seen[f.Name] || f.Flags&(1|1<<6|1<<13) != 0 || !f.Mode().IsRegular() || (f.Method != zip.Store && f.Method != zip.Deflate) {
			return nil, ErrNativeValidation
		}
		seen[f.Name] = true
		limit := uint64(deckRawLimit)
		switch f.Name {
		case "mimetype":
			if i != 0 || f.Method != zip.Store {
				return nil, ErrNativeValidation
			}
			limit = uint64(len(deckMIME))
		case "presentation.json":
			limit = deckManifestLimit
		default:
			if !deckAssetName(f.Name) {
				return nil, ErrNativeValidation
			}
		}
		if f.UncompressedSize64 > limit || f.UncompressedSize64 > deckExpandedLimit-expanded {
			return nil, ErrNativeValidation
		}
		r, err := f.Open()
		if err != nil {
			return nil, ErrNativeValidation
		}
		// Reading to EOF verifies the CRC as well as the actual decompressed size.
		// Do not allocate asset buffers: retain only their length and digest.
		h := sha256.New()
		var body bytes.Buffer
		var dst io.Writer = h
		if f.Name == "mimetype" || f.Name == "presentation.json" {
			dst = io.MultiWriter(h, &body)
		}
		n, readErr := io.Copy(dst, io.LimitReader(r, int64(limit)+1))
		closeErr := r.Close()
		if readErr != nil || closeErr != nil || uint64(n) > limit || uint64(n) != f.UncompressedSize64 || uint64(n) > deckExpandedLimit-expanded {
			return nil, ErrNativeValidation
		}
		expanded += uint64(n)
		switch f.Name {
		case "mimetype":
			if body.String() != deckMIME {
				return nil, ErrNativeValidation
			}
		case "presentation.json":
			result.manifest, err = decodeDeckManifest(body.Bytes())
			if err != nil {
				return nil, ErrNativeValidation
			}
		default:
			var digest [32]byte
			copy(digest[:], h.Sum(nil))
			result.assets[f.Name] = deckAsset{size: uint64(n), digest: digest}
		}
	}
	if !seen["mimetype"] || result.manifest == nil {
		return nil, ErrNativeValidation
	}
	return result, nil
}

func deckAssetName(name string) bool {
	prefix, id, ok := strings.Cut(name, "/")
	if !ok || (prefix != "media" && prefix != "chart-parts") || id == "" {
		return false
	}
	n, err := strconv.ParseUint(id, 10, 32)
	return err == nil && strconv.FormatUint(n, 10) == id
}

// Decode tokens rather than Unmarshal into maps, which silently drops duplicate
// authored keys. Limit nesting before recursion and reject invalid UTF-8 too.
func decodeDeckManifest(raw []byte) (any, error) {
	if !utf8.Valid(raw) || !deckJSONSurrogates(raw) {
		return nil, ErrNativeValidation
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	value, err := deckJSONValue(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrNativeValidation
	}
	m, ok := value.(map[string]any)
	if !ok || m["format"] != "deckcraft" || m["version"] != json.Number("1") {
		return nil, ErrNativeValidation
	}
	if _, ok = m["presentation"].(map[string]any); !ok {
		return nil, ErrNativeValidation
	}
	return value, nil
}
func deckJSONValue(d *json.Decoder, depth int) (any, error) {
	if depth > deckJSONDepthLimit {
		return nil, ErrNativeValidation
	}
	token, err := d.Token()
	if err != nil {
		return nil, ErrNativeValidation
	}
	delim, container := token.(json.Delim)
	if !container {
		return token, nil
	}
	switch delim {
	case '{':
		m := make(map[string]any)
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, ErrNativeValidation
			}
			k, ok := key.(string)
			if !ok {
				return nil, ErrNativeValidation
			}
			if _, exists := m[k]; exists {
				return nil, ErrNativeValidation
			}
			value, err := deckJSONValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			m[k] = value
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, ErrNativeValidation
		}
		return m, nil
	case '[':
		a := make([]any, 0)
		for d.More() {
			v, err := deckJSONValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, ErrNativeValidation
		}
		return a, nil
	default:
		return nil, ErrNativeValidation
	}
}

func preservesDeckArchive(before, after *deckArchive) bool {
	if before == nil || after == nil || len(before.assets) != len(after.assets) || !nativeModelSubset(before.manifest, after.manifest, 0) {
		return false
	}
	for name, asset := range before.assets {
		if other, ok := after.assets[name]; !ok || asset != other {
			return false
		}
	}
	return true
}

// encoding/json replaces unpaired UTF-16 escapes with U+FFFD. Refuse that
// lossy input before decoding, while accepting valid surrogate pairs.
func deckJSONSurrogates(raw []byte) bool {
	inString := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		n, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xDC00 && n <= 0xDFFF {
			return false
		}
		if n < 0xD800 || n > 0xDBFF {
			continue
		}
		if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
		if err != nil || low < 0xDC00 || low > 0xDFFF {
			return false
		}
		i += 6
	}
	return true
}

// validateDeck validates the native ZIP with the pinned original CLI, but never
// replaces the caller's bytes with that validation-only engine export.
func (v NativeCLIValidator) validateDeck(ctx context.Context, raw []byte) error {
	before, err := parseDeckArchive(raw)
	if err != nil || runtime.GOOS != "linux" || v.Application != "deckcraft" || !filepath.IsAbs(v.BinaryPath) || len(v.SHA256) != 64 {
		return ErrNativeValidation
	}
	expected, err := hex.DecodeString(v.SHA256)
	if err != nil {
		return ErrNativeValidation
	}
	timeout := v.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if timeout <= 0 || timeout > time.Minute {
		return ErrNativeValidation
	}
	f, err := os.Open(v.BinaryPath)
	if err != nil {
		return ErrNativeValidation
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 512<<20 {
		return ErrNativeValidation
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (512<<20)+1))
	if err != nil || n != info.Size() || !bytes.Equal(h.Sum(nil), expected) {
		return ErrNativeValidation
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return ErrNativeValidation
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "craft-native-deck-")
	if err != nil {
		return ErrNativeValidation
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "candidate.deckcraft")
	output := filepath.Join(dir, "validated.deckcraft")
	if err = os.WriteFile(input, raw, 0600); err != nil {
		return ErrNativeValidation
	}
	// Hash and execute the same opened descriptor, not a path reopened by exec.
	cmd := exec.CommandContext(ctx, "/proc/self/fd/3", "convert", input, output)
	cmd.ExtraFiles = []*os.File{f}
	cmd.Dir = dir
	cmd.Env = []string{"LANG=C.UTF-8", "HOME=" + dir, "TMPDIR=" + dir}
	cmd.WaitDelay = 100 * time.Millisecond
	stdout := &nativeEngineOutput{limit: 1 << 20}
	stderr := &nativeEngineOutput{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err = cmd.Run(); err != nil || stderr.Len() != 0 || stdout.String() != output+"\n" {
		return ErrNativeValidation
	}
	// Refuse symlinks and nonregular outputs; keep the actual read bounded even
	// if a malformed engine output changes size after inspection.
	outputInfo, err := os.Lstat(output)
	if err != nil || !outputInfo.Mode().IsRegular() || outputInfo.Size() <= 0 || outputInfo.Size() > deckRawLimit {
		return ErrNativeValidation
	}
	out, err := os.Open(output)
	if err != nil {
		return ErrNativeValidation
	}
	defer out.Close()
	openedInfo, err := out.Stat()
	if err != nil || !os.SameFile(outputInfo, openedInfo) {
		return ErrNativeValidation
	}
	converted, err := io.ReadAll(io.LimitReader(out, deckRawLimit+1))
	if err != nil || len(converted) > deckRawLimit || int64(len(converted)) != outputInfo.Size() {
		return ErrNativeValidation
	}
	after, err := parseDeckArchive(converted)
	if err != nil || !preservesDeckArchive(before, after) {
		return ErrNativeValidation
	}
	return nil
}
