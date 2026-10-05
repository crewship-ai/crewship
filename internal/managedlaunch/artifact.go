// Package managedlaunch implements the native-only managed launch boundary.
package managedlaunch

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"path"
	"regexp"
	"strings"
)

const LauncherPath = "/usr/local/bin/crewship-sidecar"
const MaxArtifactBytes = 512 << 20

// Artifact is host-captured executable evidence, separate from --version output.
// A1 deliberately supports static ELF only; scripts and dynamic ELF require A2.
type Artifact struct {
	Path   string `json:"path" yaml:"path"`
	SHA256 string `json:"sha256" yaml:"sha256"`
	Format string `json:"format" yaml:"format"`
}

type Descriptor struct {
	Artifact
	ImageID    string   `json:"image_id"`
	RevisionID string   `json:"revision_id"`
	LockSHA256 string   `json:"lock_sha256"`
	Binary     string   `json:"binary"`
	Version    string   `json:"version"`
	RunID      string   `json:"run_id,omitempty"`
	EnvKeys    []string `json:"env_keys,omitempty"`
}

var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func ValidRunID(id string) bool { return runIDPattern.MatchString(id) }

func DirectRunPIDFile(id string) string { return "/tmp/crewship-direct-" + id + ".pid" }

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

func ImagePath(p string) bool {
	return len(p) <= 512 && path.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n\t") &&
		(strings.HasPrefix(p, "/opt/") || strings.HasPrefix(p, "/usr/")) && p != LauncherPath
}

func (d Descriptor) Validate() error {
	if (d.RunID != "" && !ValidRunID(d.RunID)) || !ImagePath(d.Path) || !digestPattern.MatchString(d.SHA256) || d.Format != "static_elf" ||
		!strings.HasPrefix(d.ImageID, "sha256:") || !digestPattern.MatchString(strings.TrimPrefix(d.ImageID, "sha256:")) ||
		!digestPattern.MatchString(d.LockSHA256) || d.RevisionID == "" || len(d.RevisionID) > 128 ||
		(d.Binary != "claude" && d.Binary != "codex") || !versionPattern.MatchString(d.Version) {
		return errors.New("managed launch: missing or unsupported build descriptor")
	}
	return nil
}

// Capture parses bytes without executing image code or invoking host ldd.
func Capture(p string, raw []byte) (*Artifact, error) {
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
