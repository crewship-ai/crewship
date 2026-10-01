//go:build linux

package restrictedruntime

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"sort"
	"strings"
)

type NativeInputData struct {
	VersionID string
	Content   []byte
}

// nativeInputArchive creates only regular immutable files and traversal-safe
// directories. Docker receives the bounded archive through stdin; no content or
// filename is interpreted by a shell or used as a host filesystem path.
func nativeInputArchive(m *NativeInputManifest, data []NativeInputData) ([]byte, error) {
	if !m.valid() || len(data) != len(m.Files) {
		return nil, ErrDenied
	}
	content := map[string][]byte{}
	for _, d := range data {
		if _, exists := content[d.VersionID]; exists {
			return nil, ErrDenied
		}
		content[d.VersionID] = d.Content
	}
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	dirs := map[string]bool{}
	for _, f := range m.Files {
		b, exists := content[f.VersionID]
		h := sha256.Sum256(b)
		if !exists || int64(len(b)) != f.Size || hex.EncodeToString(h[:]) != f.SHA256 {
			return nil, ErrDenied
		}
		name := f.VersionID + "/" + f.Name
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}
	ordered := make([]string, 0, len(dirs))
	for dir := range dirs {
		ordered = append(ordered, dir)
	}
	sort.Strings(ordered)
	for _, dir := range ordered {
		if err := w.WriteHeader(&tar.Header{Name: dir + "/", Typeflag: tar.TypeDir, Mode: 0500, Uid: 1001, Gid: 1001}); err != nil {
			return nil, err
		}
	}
	for _, f := range m.Files {
		name := f.VersionID + "/" + f.Name
		if strings.HasPrefix(name, "/") || path.Clean(name) != name {
			return nil, ErrDenied
		}
		if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0400, Uid: 1001, Gid: 1001, Size: f.Size}); err != nil {
			return nil, err
		}
		if _, err := w.Write(content[f.VersionID]); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if out.Len() > int(NativeInputCapacity)+(2<<20) {
		return nil, ErrDenied
	}
	return out.Bytes(), nil
}
