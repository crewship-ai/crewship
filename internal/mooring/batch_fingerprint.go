package mooring

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/gowebpki/jcs"
)

const (
	// These are admission limits, checked before invoking the canonicalizer.
	MaxCanonicalBytes   = 1 << 20
	MaxCanonicalDepth   = 64
	MaxAtomicOperations = 256
	// Bound wide collections as well as recursion before allocating a JSON tree.
	MaxCanonicalStructure = 32768
)

var ErrCanonicalInput = errors.New("invalid or oversized canonical JSON input")

// canonicalPreflight bounds untrusted input before recursive parsing. It is
// deliberately not a JSON validator; the canonicalizer validates syntax next.
func canonicalPreflight(raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxCanonicalBytes || !utf8.Valid(raw) {
		return ErrCanonicalInput
	}
	depth, structure := 0, 0
	quoted, escaped := false, false
	for _, b := range raw {
		if quoted {
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				quoted = false
			}
			continue
		}
		if b == '{' || b == '[' || b == ',' || b == ':' {
			structure++
			if structure > MaxCanonicalStructure {
				return ErrCanonicalInput
			}
		}
		switch b {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			if depth > MaxCanonicalDepth {
				return ErrCanonicalInput
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return ErrCanonicalInput
			}
		}
	}
	return nil
}

// CanonicalJSON implements the RFC 8785 JSON profile. The returned bytes are
// the ONLY permitted semantic input to a qualified adapter: JSON numbers have
// already been normalized to binary64. Exact integers beyond binary64 precision
// must be represented as strings by the application schema. No Unicode
// normalization is performed. Errors never include document content.
//
// This unused helper does not perform protocol or application schema validation;
// those checks must consume its normalized result, before application semantics.
func CanonicalJSON(raw []byte) ([]byte, error) {
	if err := canonicalPreflight(raw); err != nil {
		return nil, err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil || len(canonical) > MaxCanonicalBytes {
		return nil, ErrCanonicalInput
	}
	return canonical, nil
}

func canonicalDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// AtomicFingerprints binds the entire immutable change, including task,
// authors, dependencies and scope. Transport retry fields are not part of this
// preimage. CanonicalInput must be used for subsequent schema/semantic checks.
// This value proves byte identity, not authorization or schema conformance.
type AtomicFingerprints struct {
	CanonicalInput json.RawMessage
	MembershipHash string
	Operations     map[string]string
}

// FingerprintAtomicJSON takes the protocol's mooring.atomic.v0.4 preimage,
// not a transport envelope. Full protocol validation is a separate required
// admission step. Size, nesting and operation count are bounded before JCS.
func FingerprintAtomicJSON(raw []byte) (AtomicFingerprints, error) {
	if err := canonicalPreflight(raw); err != nil {
		return AtomicFingerprints{}, err
	}
	var shape struct {
		Domain     string `json:"domain"`
		Operations []struct {
			ID string `json:"op_id"`
		} `json:"operations_without_fingerprints"`
	}
	if json.Unmarshal(raw, &shape) != nil || shape.Domain != "mooring.atomic.v0.4" || len(shape.Operations) == 0 || len(shape.Operations) > MaxAtomicOperations {
		return AtomicFingerprints{}, ErrCanonicalInput
	}
	canonical, err := CanonicalJSON(raw)
	if err != nil {
		return AtomicFingerprints{}, err
	}
	result := AtomicFingerprints{CanonicalInput: canonical, MembershipHash: canonicalDigest(canonical), Operations: make(map[string]string, len(shape.Operations))}
	for _, op := range shape.Operations {
		if len(op.ID) == 0 || len(op.ID) > 200 || strings.ContainsRune(op.ID, 0) {
			return AtomicFingerprints{}, ErrCanonicalInput
		}
		if _, duplicate := result.Operations[op.ID]; duplicate {
			return AtomicFingerprints{}, ErrCanonicalInput
		}
		preimage, err := json.Marshal(struct {
			Domain     string `json:"domain"`
			Membership string `json:"membership_hash"`
			ID         string `json:"op_id"`
		}{"mooring.operation.v0.4", result.MembershipHash, op.ID})
		if err != nil {
			return AtomicFingerprints{}, ErrCanonicalInput
		}
		normalized, err := CanonicalJSON(preimage)
		if err != nil {
			return AtomicFingerprints{}, err
		}
		result.Operations[op.ID] = canonicalDigest(normalized)
	}
	return result, nil
}
