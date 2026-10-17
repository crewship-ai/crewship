package backup

import (
	"encoding/base64"
	"fmt"
	"math"
)

// Document CRDT bytes are not UTF-8. Keep their representation explicit rather
// than passing them through the legacy TEXT normalizer and JSON replacement.
func documentBinaryColumn(table, column string) bool {
	switch table {
	case "documents", "document_checkpoints":
		return column == "snapshot" || column == "projection"
	case "document_updates":
		return column == "payload"
	}
	return false
}
func normalizeDocumentScan(table, column string, value any) any {
	if !documentBinaryColumn(table, column) {
		return normalizeScan(value)
	}
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return value
	}
	return map[string]any{"encoding": "document-base64-v1", "data": base64.StdEncoding.EncodeToString(bytes)}
}
func restoreDocumentBinary(value any) ([]byte, error) {
	// Direct in-process dumps can supply bytes; serialized bundles must use the
	// tagged representation so a future text representation cannot be guessed.
	if bytes, ok := value.([]byte); ok {
		return bytes, nil
	}
	encoded, ok := value.(map[string]any)
	if !ok || len(encoded) != 2 || encoded["encoding"] != "document-base64-v1" {
		return nil, fmt.Errorf("invalid document binary encoding")
	}
	data, ok := encoded["data"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid document binary data")
	}
	bytes, err := base64.StdEncoding.Strict().DecodeString(data)
	if err != nil {
		return nil, fmt.Errorf("invalid document base64: %w", err)
	}
	return bytes, nil
}
func safeDocumentRestoreRow(table string, row map[string]any) (map[string]any, error) {
	if table != "documents" {
		return row, nil
	}
	var epoch int64
	switch value := row["epoch"].(type) {
	case int64:
		epoch = value
	case int:
		epoch = int64(value)
	case float64:
		if value < 0 || value >= float64(math.MaxInt64) || math.Trunc(value) != value {
			return nil, fmt.Errorf("backup: invalid document epoch")
		}
		epoch = int64(value)
	default:
		return nil, fmt.Errorf("backup: missing document epoch")
	}
	if epoch < 0 || epoch == math.MaxInt64 {
		return nil, fmt.Errorf("backup: invalid document epoch")
	}
	copy := make(map[string]any, len(row))
	for key, value := range row {
		copy[key] = value
	}
	copy["writer"], copy["owner"], copy["lease_until"], copy["epoch"] = "", "", int64(0), epoch+1
	return copy, nil
}
