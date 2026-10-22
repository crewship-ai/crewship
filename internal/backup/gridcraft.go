package backup

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/crewship-ai/crewship/internal/appengine"
)

// Native workbook snapshots and receipts are UTF-8 JSON, so the existing TEXT
// normalizer preserves their bytes. Live intents are excluded separately.
func safeGridCraftRestoreRow(table string, row map[string]any) (map[string]any, error) {
	if table != "gridcraft_heads" && table != "gridcraft_operations" {
		return row, nil
	}
	copy := make(map[string]any, len(row))
	for key, value := range row {
		copy[key] = value
	}
	if table == "gridcraft_heads" {
		generation, err := gridcraftPositiveInteger(row["generation"])
		if err != nil || generation == math.MaxInt64 {
			return nil, fmt.Errorf("backup: invalid workbook generation")
		}
		snapshot, ok := row["snapshot"].(string)
		if !ok || len(snapshot) > appengine.MaxSnapshotBytes || !json.Valid([]byte(snapshot)) {
			return nil, fmt.Errorf("backup: invalid workbook snapshot")
		}
		copy["generation"] = generation + 1
	} else {
		token, err := gridcraftPositiveInteger(row["token"])
		if err != nil {
			return nil, err
		}
		if row["state"] == "applied" {
			raw, ok := row["receipt"].(string)
			if !ok {
				return nil, fmt.Errorf("backup: workbook receipt required")
			}
			var receipt appengine.Receipt
			if json.Unmarshal([]byte(raw), &receipt) != nil || receipt.OperationKey.MutationID != row["mutation_id"] || receipt.OperationKey.AuthorityID != row["authority_id"] || receipt.PayloadHash != row["payload_hash"] || receipt.Outcome != "applied" || receipt.Rev < 1 {
				return nil, fmt.Errorf("backup: invalid workbook receipt")
			}
		} else {
			if token == math.MaxInt64 {
				return nil, fmt.Errorf("backup: invalid workbook fencing token")
			}
			copy["state"], copy["token"], copy["lease_until"], copy["receipt"] = "failed", token+1, int64(0), nil
		}
	}
	return copy, nil
}
func gridcraftPositiveInteger(value any) (int64, error) {
	var n int64
	switch v := value.(type) {
	case int64:
		n = v
	case int:
		n = int64(v)
	case float64:
		if v < 1 || v >= float64(math.MaxInt64) || math.Trunc(v) != v {
			return 0, fmt.Errorf("backup: invalid workbook integer")
		}
		n = int64(v)
	case json.Number:
		var err error
		n, err = v.Int64()
		if err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("backup: missing workbook integer")
	}
	if n < 1 {
		return 0, fmt.Errorf("backup: invalid workbook integer")
	}
	return n, nil
}
func remapGridCraft(dump *DBDump, idMap map[string]map[string]string) {
	for _, table := range []string{"gridcraft_heads", "gridcraft_revisions", "gridcraft_operations", "gridcraft_audit", "gridcraft_outbox"} {
		for _, row := range dump.Tables[table] {
			for column, anchor := range map[string]string{"workspace_id": "workspaces", "page_id": "pages"} {
				if old, ok := row[column].(string); ok {
					if mapped := idMap[anchor][old]; mapped != "" {
						row[column] = mapped
					}
				}
			}
		}
	}
	// Receipts remain immutable source provenance. Their original payload hash
	// binds the old workspace/Page, so replay in the fork conflicts. Status must
	// also refuse presenting a source-Page receipt as a fork operation.
}
