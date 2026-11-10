package backup

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"math"
	"strings"
)

// Native document bytes and project assets are not necessarily UTF-8. Keep
// their representation explicit rather
// than passing them through the legacy TEXT normalizer and JSON replacement.
func documentBinaryColumn(table, column string) bool {
	switch table {
	case "artifact_design_documents":
		return column == "state"
	case "artifact_version_blobs", "craft_native_proposals", "artifact_library_pins":
		return column == "content"
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
	if generation, ok := row["generation"].(float64); ok {
		if generation < 0 || generation >= float64(math.MaxInt64) || math.Trunc(generation) != generation {
			return nil, fmt.Errorf("backup: invalid document generation")
		}
		copy["generation"] = int64(generation) + 1
	} else if generation, ok := row["generation"].(int64); ok {
		if generation < 0 || generation == math.MaxInt64 {
			return nil, fmt.Errorf("backup: invalid document generation")
		}
		copy["generation"] = generation + 1
	}
	// Restored content is a new content lineage for the public Docs epoch
	// (#5163); bundles from before the column omit it and land at 0, which the
	// rotated database identity already separates.
	if value, exists := row["content_generation"]; exists {
		var generation int64
		switch value := value.(type) {
		case int64:
			generation = value
		case int:
			generation = int64(value)
		case float64:
			if value < 0 || value >= float64(math.MaxInt64) || math.Trunc(value) != value {
				return nil, fmt.Errorf("backup: invalid document content generation")
			}
			generation = int64(value)
		default:
			return nil, fmt.Errorf("backup: invalid document content generation")
		}
		if generation < 0 || generation == math.MaxInt64 {
			return nil, fmt.Errorf("backup: invalid document content generation")
		}
		copy["content_generation"] = generation + 1
	}
	copy["writer"], copy["owner"], copy["lease_until"], copy["epoch"] = "", "", int64(0), epoch+1
	return copy, nil
}

// rotateDocumentDatabaseIdentity gives the restored database a new identity,
// inside the restore transaction. The public Docs epoch is derived from it, so
// no browser replays pending edits onto restored content. Only restore paths
// call this; a plain restart keeps the identity.
func rotateDocumentDatabaseIdentity(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	if _, err := db.ExecContext(ctx, `INSERT INTO document_database_identity(id,value) VALUES(1,lower(hex(randomblob(16)))) ON CONFLICT(id) DO UPDATE SET value=excluded.value`); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			// A database from before the table gets the empty table from its
			// migration, and first use creates a fresh identity.
			return nil
		}
		return fmt.Errorf("backup: rotate document database identity: %w", err)
	}
	return nil
}
