package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ValidateSlidesArtifactManifest rejects ambiguous stored JSON before SQL and Go
// inspect it independently. Duplicate or case-aliased fields must not let their
// file selections disagree across the source admission and VersionStore load.
func ValidateSlidesArtifactManifest(raw []byte) bool {
	if len(raw) == 0 || len(raw) > 128<<10 || !validDataObject(raw) || !canonicalDataFields(raw, "entry_point", "files") {
		return false
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || len(envelope) != 2 {
		return false
	}
	var entry string
	if json.Unmarshal(envelope["entry_point"], &entry) != nil || entry == "" {
		return false
	}
	var files map[string]json.RawMessage
	if json.Unmarshal(envelope["files"], &files) != nil || len(files) < 2 || len(files) > 101 {
		return false
	}
	for _, rawFile := range files {
		if !canonicalDataFields(rawFile, "hash", "media_type") {
			return false
		}
		var file map[string]json.RawMessage
		if json.Unmarshal(rawFile, &file) != nil || len(file) != 2 {
			return false
		}
		var hash, media string
		if json.Unmarshal(file["hash"], &hash) != nil || json.Unmarshal(file["media_type"], &media) != nil || len(hash) != 64 || media == "" {
			return false
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return false
		}
	}
	return true
}

// IsSlidesArtifactIndex classifies a selected immutable manifest without loading
// its HTML bodies. Full projection admission remains mandatory before preview.
func IsSlidesArtifactIndex(rawManifest, index []byte) bool {
	if !ValidateSlidesArtifactManifest(rawManifest) {
		return false
	}
	order, err := artifactSlideOrder(index)
	if err != nil {
		return false
	}
	var manifest storedManifest
	if json.Unmarshal(rawManifest, &manifest) != nil || len(manifest.Files) != len(order)+1 {
		return false
	}
	file, ok := manifest.Files["deck.json"]
	sum := sha256.Sum256(index)
	if !ok || file.MediaType != "application/json" || file.Hash != hex.EncodeToString(sum[:]) {
		return false
	}
	for _, id := range order {
		f, ok := manifest.Files["slides/"+id+".html"]
		_, hashErr := hex.DecodeString(f.Hash)
		if !ok || f.MediaType != "text/html" || len(f.Hash) != 64 || hashErr != nil {
			return false
		}
	}
	_, ok = manifest.Files[manifest.EntryPoint]
	return ok
}
