// Package mooringassets holds optional release-built assets. Ordinary Go
// builds embed only an unavailable marker and need no Rust toolchain.
package mooringassets

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

//go:embed embedded
var embedded embed.FS

var ErrUnavailable = errors.New("Mooring runtime assets were not built")
var ErrIntegrity = errors.New("invalid Mooring runtime asset integrity")
var shaPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var assetNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var sourcePattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

type Versions struct {
	Protocol string `json:"protocol_version"`
	Model    string `json:"model_version"`
	Engine   string `json:"engine_version"`
}
type Manifest struct {
	SchemaVersion int               `json:"schema_version"`
	SourceSHA     string            `json:"source_sha"`
	Target        string            `json:"target"`
	Versions      Versions          `json:"versions"`
	BinarySHA256  string            `json:"binary_sha256"`
	SDK           map[string]string `json:"sdk"`
	SDKPrefix     string            `json:"sdk_prefix"`
	Tools         json.RawMessage   `json:"tools"`
}

func read(source fs.FS) (Manifest, []byte, error) {
	var manifest Manifest
	raw, err := fs.ReadFile(source, "embedded/generated/manifest.json")
	if errors.Is(err, fs.ErrNotExist) {
		return manifest, nil, ErrUnavailable
	}
	if err != nil || len(raw) > 64*1024 || !json.Valid(raw) {
		return manifest, nil, ErrIntegrity
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || manifest.SchemaVersion != 1 || !sourcePattern.MatchString(manifest.SourceSHA) || manifest.Target != "x86_64-unknown-linux-gnu" || !shaPattern.MatchString(manifest.BinarySHA256) || manifest.Versions.Protocol == "" || manifest.Versions.Model == "" || manifest.Versions.Engine == "" {
		return Manifest{}, nil, ErrIntegrity
	}
	if len(manifest.SDK) == 0 || len(manifest.SDK) > 16 || !shaPattern.MatchString(filepath.Base(manifest.SDKPrefix)) || manifest.SDKPrefix != "/mooring-sdk/generated/"+filepath.Base(manifest.SDKPrefix) {
		return Manifest{}, nil, ErrIntegrity
	}
	for name, digest := range manifest.SDK {
		if !fs.ValidPath(name) || !assetNamePattern.MatchString(name) || !shaPattern.MatchString(digest) {
			return Manifest{}, nil, ErrIntegrity
		}
	}
	// ASCII asset names and hex digests have identical compact sorted JSON in
	// the Python release recipe and encoding/json. Bind the URL to this map.
	sdkJSON, err := json.Marshal(manifest.SDK)
	if err != nil {
		return Manifest{}, nil, ErrIntegrity
	}
	sdkHash := sha256.Sum256(sdkJSON)
	if manifest.SDKPrefix != "/mooring-sdk/generated/"+hex.EncodeToString(sdkHash[:]) {
		return Manifest{}, nil, ErrIntegrity
	}
	binary, err := fs.ReadFile(source, "embedded/generated/mooring-collab")
	if err != nil || len(binary) < 4 || len(binary) > 64*1024*1024 || !bytes.Equal(binary[:4], []byte{0x7f, 'E', 'L', 'F'}) {
		return Manifest{}, nil, ErrIntegrity
	}
	hash := sha256.Sum256(binary)
	if hex.EncodeToString(hash[:]) != manifest.BinarySHA256 {
		return Manifest{}, nil, ErrIntegrity
	}
	return manifest, binary, nil
}

// Inspect verifies embedded binary identity. It does not authorize an Artifact,
// qualify an application adapter or enable a process/route.
func Inspect() (Manifest, error) { manifest, _, err := read(embedded); return manifest, err }

type Prepared struct {
	Manifest  Manifest
	Path      string
	directory string
	ownedPath string
}

// Close removes only the exact file and private directory created here. The
// caller must stop its runtime before cleanup; no other cache paths are owned.
func (p *Prepared) Close() error {
	if err := os.Remove(p.ownedPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Remove(p.directory); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Prepare extracts a verified child into a new private directory. The host
// supervisor separately checks versions, authority and its runtime lifecycle.
func Prepare(parent string) (*Prepared, error) { return prepare(embedded, parent) }
func prepare(source fs.FS, parent string) (*Prepared, error) {
	manifest, binary, err := read(source)
	if err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp(parent, "mooring-runtime-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "mooring-collab")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o500)
	if err != nil {
		_ = os.Remove(directory)
		return nil, err
	}
	_, writeErr := file.Write(binary)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(path)
		_ = os.Remove(directory)
		return nil, writeErr
	}
	return &Prepared{Manifest: manifest, Path: path, directory: directory, ownedPath: path}, nil
}
