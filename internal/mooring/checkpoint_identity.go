package mooring

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf16"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"github.com/gowebpki/jcs"
)

const maxGenericCheckpointBytes = 8 * 1024 * 1024
const maxCheckpointPreimageBytes = 16 * 1024 * 1024

var ErrCheckpointIdentity = errors.New("invalid or oversized checkpoint identity")

// CheckpointIdentityLimits is trusted caller policy, never incoming JSON.
// JSON state additionally uses the existing bounded depth/structure profile.
type CheckpointIdentityLimits struct{ DecodedBytes, PreimageBytes int }

// CheckpointIdentity separates the generic full-tuple identity from exact opaque
// content SHA256. Neither authenticates an imported checkpoint or its assets.
type CheckpointIdentity struct {
	FullDigest, ContentDigest string
	State                     []byte
}

func checkpointDigestShape(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, b := range []byte(value[7:]) {
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return false
		}
	}
	return true
}

// Preflight alphabet, pad bits and decoded size BEFORE allocating output.
func checkpointBase64(text string, maximum int) ([]byte, error) {
	if len(text)%4 != 0 || len(text) > 4*((maximum+2)/3) {
		return nil, ErrCheckpointIdentity
	}
	padding := 0
	if strings.HasSuffix(text, "=") {
		padding++
	}
	if strings.HasSuffix(text, "==") {
		padding++
	}
	sextet := func(b byte) int {
		switch {
		case b >= 'A' && b <= 'Z':
			return int(b - 'A')
		case b >= 'a' && b <= 'z':
			return int(b-'a') + 26
		case b >= '0' && b <= '9':
			return int(b-'0') + 52
		case b == '+':
			return 62
		case b == '/':
			return 63
		default:
			return -1
		}
	}
	for i := 0; i < len(text); i++ {
		if i >= len(text)-padding {
			if text[i] != '=' {
				return nil, ErrCheckpointIdentity
			}
		} else if sextet(text[i]) < 0 {
			return nil, ErrCheckpointIdentity
		}
	}
	if padding == 1 && (len(text) < 4 || sextet(text[len(text)-2])&3 != 0) || padding == 2 && (len(text) < 4 || sextet(text[len(text)-3])&15 != 0) {
		return nil, ErrCheckpointIdentity
	}
	size := len(text)/4*3 - padding
	if size < 0 || size > maximum {
		return nil, ErrCheckpointIdentity
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(text)
	if err != nil || len(decoded) != size {
		return nil, ErrCheckpointIdentity
	}
	return decoded, nil
}

// ComputeCheckpointIdentity implements mooring.checkpoint.v0.4. The supplied
// Digest is excluded so a host can construct a new identity; Verify checks it.
// This helper never changes public frame admission or interprets app content.
func ComputeCheckpointIdentity(cp protocol.Checkpoint, limits CheckpointIdentityLimits) (CheckpointIdentity, error) {
	fail := func() (CheckpointIdentity, error) { return CheckpointIdentity{}, ErrCheckpointIdentity }
	if limits.DecodedBytes < 0 || limits.DecodedBytes > maxGenericCheckpointBytes || limits.PreimageBytes <= 0 || limits.PreimageBytes > maxCheckpointPreimageBytes {
		return fail()
	}
	for _, id := range []string{string(cp.Scope.ApplicationID), string(cp.Scope.WorkspaceID), string(cp.Scope.ArtifactID), string(cp.FormatVersion)} {
		if !ValidWireIdentifier(id) {
			return fail()
		}
	}
	for _, version := range []string{cp.Versions.ProtocolVersion, cp.Versions.ModelVersion, cp.Versions.EngineVersion} {
		if !ValidWireVersion(version) {
			return fail()
		}
	}
	for _, counter := range []protocol.Counter{cp.Revision, cp.Generation} {
		if _, err := batchCounter(string(counter)); err != nil {
			return fail()
		}
	}
	if cp.Assets == nil || len(cp.Assets) > 4096 {
		return fail()
	}
	type sortable struct {
		asset protocol.Asset
		key   []uint16
	}
	assets := make([]sortable, 0, len(cp.Assets))
	seen := make(map[string]bool, len(cp.Assets))
	for _, asset := range cp.Assets {
		id := string(asset.AssetID)
		if !ValidWireIdentifier(id) || !checkpointDigestShape(string(asset.Digest)) || seen[id] {
			return fail()
		}
		if _, err := batchCounter(string(asset.SizeBytes)); err != nil {
			return fail()
		}
		seen[id] = true
		assets = append(assets, sortable{asset, utf16.Encode([]rune(id))})
	}
	var state []byte
	switch cp.Encoding {
	case "base64":
		var err error
		state, err = checkpointBase64(cp.State, limits.DecodedBytes)
		if err != nil {
			return fail()
		}
	case "json":
		raw := []byte(cp.State)
		if canonicalPreflightLimit(raw, limits.DecodedBytes) != nil {
			return fail()
		}
		canonical, err := jcs.Transform(raw)
		if err != nil || !bytes.Equal(canonical, raw) {
			return fail()
		}
		state = append([]byte(nil), raw...)
	default:
		return fail()
	}
	stateBase64 := base64.StdEncoding.EncodeToString(state)
	if len(stateBase64) > limits.PreimageBytes {
		return fail()
	}
	slices.SortFunc(assets, func(a, b sortable) int { return slices.Compare(a.key, b.key) })
	ordered := make([]protocol.Asset, 0, len(assets))
	for _, asset := range assets {
		ordered = append(ordered, asset.asset)
	}
	preimage := struct {
		Domain        string              `json:"domain"`
		Scope         protocol.Scope      `json:"scope"`
		Revision      protocol.Counter    `json:"revision"`
		Generation    protocol.Counter    `json:"generation"`
		FormatVersion protocol.Identifier `json:"format_version"`
		Versions      protocol.Versions   `json:"versions"`
		State         string              `json:"state_base64"`
		Assets        []protocol.Asset    `json:"assets"`
	}{"mooring.checkpoint.v0.4", cp.Scope, cp.Revision, cp.Generation, cp.FormatVersion, cp.Versions, stateBase64, ordered}
	// This is a closed, already bounded typed tree, not arbitrary JSON. Marshal
	// can escape U+2028/HTML more than JCS; only the canonical size is the limit.
	raw, err := json.Marshal(preimage)
	if err != nil {
		return fail()
	}
	canonical, err := jcs.Transform(raw)
	if err != nil || len(canonical) > limits.PreimageBytes {
		return fail()
	}
	return CheckpointIdentity{FullDigest: canonicalDigest(canonical), ContentDigest: canonicalDigest(state), State: state}, nil
}
func VerifyCheckpointIdentity(cp protocol.Checkpoint, limits CheckpointIdentityLimits) (CheckpointIdentity, error) {
	if !checkpointDigestShape(string(cp.Digest)) {
		return CheckpointIdentity{}, ErrCheckpointIdentity
	}
	identity, err := ComputeCheckpointIdentity(cp, limits)
	if err != nil || identity.FullDigest != string(cp.Digest) {
		return CheckpointIdentity{}, ErrCheckpointIdentity
	}
	return identity, nil
}
