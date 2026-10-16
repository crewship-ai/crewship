// Package exportprofile embeds the exact trusted converter context, never author code.
package exportprofile

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
)

//go:embed package.json pnpm-lock.yaml pnpm-workspace.yaml worker.mjs entry.mjs profile.mjs profile.go Dockerfile fonts profile.json
var files embed.FS

type File struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	ProtocolVersion        int    `json:"protocol_version"`
	BaseImage              string `json:"base_image"`
	AdmissionSHA256        string `json:"admission_sha256"`
	LicenseGeneratorSHA256 string `json:"license_generator_sha256"`
	Files                  []File `json:"files"`
}

func ManifestBytes() ([]byte, error) { return files.ReadFile("profile.json") }
func Fingerprint() (string, error) {
	raw, err := ManifestBytes()
	if err != nil {
		return "", err
	}
	var m Manifest
	if json.Unmarshal(raw, &m) != nil || m.ProtocolVersion != 1 {
		return "", errors.New("invalid export profile")
	}
	for _, f := range m.Files {
		b, err := files.ReadFile(f.Path)
		if err != nil || int64(len(b)) != f.Bytes {
			return "", errors.New("embedded export profile mismatch")
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			return "", errors.New("embedded export profile mismatch")
		}
	}
	// The generated manifest is canonical ASCII-key JSON with integer fields.
	var generic any
	if json.Unmarshal(raw, &generic) != nil {
		return "", errors.New("invalid export profile")
	}
	canonical, err := json.Marshal(generic)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("crewship-slides-export-profile/v1\n"), canonical...))
	return hex.EncodeToString(sum[:]), nil
}

func FontProvenanceBytes() ([]byte, error) { return files.ReadFile("fonts/provenance.json") }
