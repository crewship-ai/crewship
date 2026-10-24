package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const maxMooringRawDataBytes = 262144

var mooringRawDataColumns = []string{"page_id", "workspace_id", "change_id", "format", "protocol_version", "model_version", "engine_version", "raw_atomic", "raw_digest", "canonical_atomic", "canonical_digest"}

// Restore preflight bounds the decoded allocation before the shared tagged
// binary decoder. These bytes are content data, never original-source proof.
func restoreMooringRawDataBinary(value any) ([]byte, error) {
	switch v := value.(type) {
	case []byte:
		if len(v) < 1 || len(v) > maxMooringRawDataBytes {
			return nil, fmt.Errorf("invalid raw atomic byte length")
		}
	case map[string]any:
		data, ok := v["data"].(string)
		if !ok || len(data) < 4 || len(data) > ((maxMooringRawDataBytes+2)/3)*4 {
			return nil, fmt.Errorf("invalid raw atomic encoded length")
		}
	default:
		return nil, fmt.Errorf("invalid raw atomic binary representation")
	}
	raw, err := restoreDocumentBinary(value)
	if err != nil {
		return nil, err
	}
	if len(raw) < 1 || len(raw) > maxMooringRawDataBytes {
		return nil, fmt.Errorf("invalid raw atomic byte length")
	}
	return raw, nil
}

// Compare restored content digests only after bounded decode. This is integrity
// checking, not proof of author/source provenance or permission to replay.
func validateMooringRawRestoreRow(row map[string]any) (map[string]any, error) {
	copy := make(map[string]any, len(row))
	for k, v := range row {
		copy[k] = v
	}
	for _, pair := range [][2]string{{"raw_atomic", "raw_digest"}, {"canonical_atomic", "canonical_digest"}} {
		raw, err := restoreMooringRawDataBinary(row[pair[0]])
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		digest, ok := row[pair[1]].(string)
		if !ok || digest != "sha256:"+hex.EncodeToString(sum[:]) {
			return nil, fmt.Errorf("raw atomic digest mismatch")
		}
		copy[pair[0]] = raw
	}
	return copy, nil
}
