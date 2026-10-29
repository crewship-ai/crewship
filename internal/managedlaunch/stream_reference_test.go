package managedlaunch

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"io"
	"path"
)

func legacyByteCapture(p string, raw []byte) (*Artifact, error) {
	if !ImagePath(p) || len(raw) == 0 || len(raw) > MaxArtifactBytes {
		return nil, errors.New("managed launch: invalid executable artifact")
	}
	f, err := elf.NewFile(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("managed launch: scripts and non-ELF executables are unsupported")
	}
	defer f.Close()
	if f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || (f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN) ||
		(f.Machine != elf.EM_X86_64 && f.Machine != elf.EM_AARCH64) {
		return nil, errors.New("managed launch: unsupported native executable")
	}
	dynamic := false
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return nil, errors.New("managed launch: dynamic ELF requires interpreter/library qualification")
		}
		if p.Type != elf.PT_DYNAMIC {
			continue
		}
		// Static PIE has relocation metadata but no external loader or
		// dependencies. Inspect the program segment itself: section headers
		// may be absent or disagree with what the kernel maps.
		if dynamic || f.Type != elf.ET_DYN || p.Filesz == 0 || p.Filesz%16 != 0 || p.Off > uint64(len(raw)) || p.Filesz > uint64(len(raw))-p.Off {
			return nil, errors.New("managed launch: malformed static PIE")
		}
		dynamic = true
		terminated := false
		for off := p.Off; off < p.Off+p.Filesz; off += 16 {
			tag := elf.DynTag(f.ByteOrder.Uint64(raw[off : off+8]))
			if tag == elf.DT_NULL {
				terminated = true
				break
			}
			if tag == elf.DT_NEEDED || tag == elf.DT_AUDIT || tag == elf.DT_DEPAUDIT || tag == elf.DT_FILTER || tag == elf.DT_AUXILIARY {
				return nil, errors.New("managed launch: dynamic ELF requires interpreter/library qualification")
			}
		}
		if !terminated {
			return nil, errors.New("managed launch: unterminated static PIE metadata")
		}
	}
	sum := sha256.Sum256(raw)
	return &Artifact{Path: p, SHA256: hex.EncodeToString(sum[:]), Format: "static_elf"}, nil
}
func legacyArtifactArchive(executable string, reader io.Reader) (*Artifact, error) {
	denied := errors.New("managed launch: invalid executable archive")
	if path.Base(executable) != "codex" && path.Base(executable) != "claude" {
		return nil, denied
	}
	limited := &io.LimitedReader{R: reader, N: MaxArtifactBytes + 8192}
	tr := tar.NewReader(limited)
	h, err := tr.Next()
	if err != nil || h.Typeflag != tar.TypeReg || h.Name != path.Base(executable) || (h.Uid != 0 && h.Uid != 1001) ||
		h.Mode&0022 != 0 || h.Mode&0111 == 0 || h.Size <= 0 || h.Size > MaxArtifactBytes {
		return nil, denied
	}
	raw, err := io.ReadAll(tr)
	if err != nil || int64(len(raw)) != h.Size {
		return nil, denied
	}
	if _, err := tr.Next(); err != io.EOF || limited.N <= 0 {
		return nil, denied
	}
	return legacyByteCapture(executable, raw)
}

// VerifyArchive compares one bounded file-copy entry with trusted host bytes.
// This is a consistency check, not proof of a running bind mount's inode.
func legacyLauncherArchive(reader io.Reader, name string, expected []byte) error {
	denied := errors.New("managed launch: invalid launcher archive")
	if len(expected) == 0 || len(expected) > MaxArtifactBytes || path.Base(name) != name || name == "." || name == ".." {
		return denied
	}
	limited := &io.LimitedReader{R: reader, N: MaxArtifactBytes + 8192}
	tr := tar.NewReader(limited)
	h, err := tr.Next()
	if err != nil || h.Typeflag != tar.TypeReg || h.Name != name || h.Size != int64(len(expected)) {
		return denied
	}
	raw, err := io.ReadAll(tr)
	if err != nil || !bytes.Equal(raw, expected) {
		return denied
	}
	if _, err := tr.Next(); err != io.EOF || limited.N <= 0 {
		return denied
	}
	return nil
}
