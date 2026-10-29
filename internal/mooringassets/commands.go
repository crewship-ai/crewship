package mooringassets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"path"
	"unicode/utf8"
)

const CommandFormat = "wordcraft-c1-v2"
const CommandEngineRevision = "7ef2633a7e84956522d1ca47b99fe6feef16c1db"
const CommandClockPatchSHA256 = "9c2ff901ae408c72d12bbb864895a63791f000551978bf615734a6b41e381581"

type CommandsManifest struct {
	Format             string `json:"format"`
	EngineRevision     string `json:"engine_revision"`
	ClockPatchSHA256   string `json:"clock_patch_sha256"`
	HostSHA256         string `json:"host_sha256"`
	EngineSHA256       string `json:"engine_sha256"`
	EditorBundleDigest string `json:"editor_bundle_digest"`
}

func readCommands(source fs.FS, manifest Manifest) (map[string][]byte, error) {
	c := manifest.Commands
	if c.Format != CommandFormat || c.EngineRevision != CommandEngineRevision || c.ClockPatchSHA256 != CommandClockPatchSHA256 || !shaPattern.MatchString(c.HostSHA256) || !shaPattern.MatchString(c.EngineSHA256) || !shaPattern.MatchString(c.EditorBundleDigest) || c.EditorBundleDigest == manifest.EditorBundleDigest {
		return nil, ErrIntegrity
	}
	binaries := map[string][]byte{}
	for name, want := range map[string]string{"command-host": c.HostSHA256, "command-engine": c.EngineSHA256} {
		body, err := boundedFile(source, "embedded/generated/"+name, 64<<20)
		// Both C1 executables are native ELF64 little-endian x86_64 executables;
		// the legacy child's existing format contract is intentionally unchanged.
		if err != nil || len(body) < 64 || len(body) > 64<<20 || !bytes.Equal(body[:4], []byte{0x7f, 'E', 'L', 'F'}) || body[4] != 2 || body[5] != 1 || body[6] != 1 || (body[16] != 2 && body[16] != 3) || body[17] != 0 || body[18] != 62 || body[19] != 0 {
			return nil, ErrIntegrity
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != want {
			return nil, ErrIntegrity
		}
		binaries[name] = body
	}
	if err := verifyCommandEditor(source, manifest); err != nil {
		return nil, err
	}
	return binaries, nil
}
func verifyCommandEditor(source fs.FS, m Manifest) error {
	folder := "embedded/generated/editor/" + m.Commands.EditorBundleDigest
	raw, err := boundedFile(source, folder+"/manifest.json", 1<<20)
	if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return ErrIntegrity
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != m.Commands.EditorBundleDigest {
		return ErrIntegrity
	}
	var fields map[string]json.RawMessage
	if uniqueJSON(raw) != nil || json.Unmarshal(raw, &fields) != nil || len(fields) != 3 || fields["files"] == nil || fields["source_commit"] == nil || fields["toolchain"] == nil {
		return ErrIntegrity
	}
	var bundle struct {
		Files        map[string]string `json:"files"`
		SourceCommit string            `json:"source_commit"`
		Toolchain    json.RawMessage   `json:"toolchain"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&bundle) != nil || !json.Valid(raw) || bundle.SourceCommit != m.SourceSHA || len(bundle.Files) == 0 || len(bundle.Files) > 512 {
		return ErrIntegrity
	}
	total := len(raw)
	for name, want := range bundle.Files {
		if !assetNamePattern.MatchString(name) || !shaPattern.MatchString(want) || name == "manifest.json" {
			return ErrIntegrity
		}
		body, e := boundedFile(source, folder+"/"+name, int64((64<<20)-total))
		if e != nil {
			return ErrIntegrity
		}
		total += len(body)
		if total > 64<<20 {
			return ErrIntegrity
		}
		s := sha256.Sum256(body)
		if hex.EncodeToString(s[:]) != want {
			return ErrIntegrity
		}
	}
	for _, name := range []string{"index.html", "child.js", "wordcraft_web.js", "wordcraft_web_bg.wasm", "build-provenance.json"} {
		if _, ok := bundle.Files[name]; !ok {
			return ErrIntegrity
		}
	}
	// The release provenance binds the separate renderer to the same release and
	// actual engine/clock profile, rather than silently reusing the legacy SDK.
	body, err := boundedFile(source, path.Join(folder, "build-provenance.json"), 1<<20)
	if err != nil {
		return ErrIntegrity
	}
	var p struct {
		CrewshipSource string `json:"crewship_source"`
		NativeSource   string `json:"native_upstream_source"`
		ClockPatch     string `json:"clock_patch_sha256"`
		Format         string `json:"format"`
	}
	if uniqueJSON(body) != nil || json.Unmarshal(body, &p) != nil || p.CrewshipSource != m.SourceSHA || p.NativeSource != CommandEngineRevision || p.ClockPatch != CommandClockPatchSHA256 || p.Format != CommandFormat {
		return ErrIntegrity
	}
	return nil
}

func boundedFile(source fs.FS, name string, limit int64) ([]byte, error) {
	f, err := source.Open(name)
	if err != nil {
		return nil, ErrIntegrity
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return nil, ErrIntegrity
	}
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, ErrIntegrity
	}
	return body, nil
}

// Reject ambiguous manifests before decoding. Sizes are bounded by callers;
// recursion is bounded independently, including toolchain/provenance objects.
func uniqueJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return ErrIntegrity
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return ErrIntegrity
		}
		t, err := d.Token()
		if err != nil {
			return ErrIntegrity
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				k, ok := key.(string)
				if e != nil || !ok || seen[k] {
					return ErrIntegrity
				}
				seen[k] = true
				if e = value(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := value(depth + 1); e != nil {
					return e
				}
			}
		default:
			return ErrIntegrity
		}
		end, e := d.Token()
		if e != nil || (delim == '{' && end != json.Delim('}')) || (delim == '[' && end != json.Delim(']')) {
			return ErrIntegrity
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrIntegrity
	}
	return nil
}
