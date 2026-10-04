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
const MaxArtifactBytes = 256 << 20

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
	EnvKeys    []string `json:"env_keys,omitempty"`
}

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

func ImagePath(p string) bool {
	return len(p) <= 512 && path.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n\t") &&
		(strings.HasPrefix(p, "/opt/") || strings.HasPrefix(p, "/usr/")) && p != LauncherPath
}

func (d Descriptor) Validate() error {
	if !ImagePath(d.Path) || !digestPattern.MatchString(d.SHA256) || d.Format != "static_elf" ||
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
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP || p.Type == elf.PT_DYNAMIC {
			return nil, errors.New("managed launch: dynamic ELF requires interpreter/library qualification")
		}
	}
	sum := sha256.Sum256(raw)
	return &Artifact{Path: p, SHA256: hex.EncodeToString(sum[:]), Format: "static_elf"}, nil
}
