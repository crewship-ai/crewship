package backup

import (
	"encoding/json"
	"fmt"
	"github.com/crewship-ai/crewship/internal/slides"
	"math"
)

// Single rewrite authority for canonical Slides snapshots. A future asset
// integration must remap image references HERE before this one hash pass.
// Object IDs and tombstones are stable within the newly namespaced Page.
func remapSlides(dump *DBDump, idMap map[string]map[string]string) error {
	for _, table := range []string{"slides_heads", "slides_revisions", "slides_receipts", "slides_object_ids"} {
		for _, row := range dump.Tables[table] {
			for column, anchor := range map[string]string{"workspace_id": "workspaces", "page_id": "pages"} {
				if old, ok := row[column].(string); ok {
					if mapped := idMap[anchor][old]; mapped != "" {
						row[column] = mapped
					}
				}
			}
			if table != "slides_heads" && table != "slides_revisions" {
				continue
			}
			raw, ok := row["canonical_json"].(string)
			if !ok {
				return fmt.Errorf("backup: %s canonical JSON required", table)
			}
			deck, err := slides.Decode([]byte(raw))
			if err != nil {
				return fmt.Errorf("backup: %s invalid Slides snapshot: %w", table, err)
			}
			if err := validateSlidesRowSchema(row, deck.SchemaVersion); err != nil {
				return err
			}
			hash, err := slides.DeckHash(deck)
			if err != nil || hash != row["content_hash"] {
				return fmt.Errorf("backup: %s Slides hash mismatch", table)
			}
			mapped := idMap["pages"][deck.DeckID]
			if mapped == "" || mapped != row["page_id"] {
				return fmt.Errorf("backup: %s Slides Page identity mismatch", table)
			}
			deck.DeckID = mapped
			encoded, err := slides.Canonical(deck)
			if err != nil {
				return err
			}
			hash, err = slides.DeckHash(deck)
			if err != nil {
				return err
			}
			row["canonical_json"], row["content_hash"] = string(encoded), hash
		}
	}
	// Replay receipts are scoped to the original Page/actor capability. Keep the
	// immutable revision's mutation ID as a tombstone: replay in a fork gets409,
	// while an ordinary same-identity restore keeps live-authenticated replay.
	dump.Tables["slides_receipts"] = nil
	return nil
}
func validateSlidesRestoreRow(table string, row map[string]any) error {
	if table != "slides_heads" && table != "slides_revisions" {
		return nil
	}
	raw, ok := row["canonical_json"].(string)
	if !ok {
		return fmt.Errorf("backup: %s canonical JSON required", table)
	}
	deck, err := slides.Decode([]byte(raw))
	if err != nil {
		return fmt.Errorf("backup: %s invalid Slides snapshot: %w", table, err)
	}
	if err := validateSlidesRowSchema(row, deck.SchemaVersion); err != nil {
		return err
	}
	if deck.DeckID != row["page_id"] {
		return fmt.Errorf("backup: %s Slides Page identity mismatch", table)
	}
	hash, err := slides.DeckHash(deck)
	if err != nil || hash != row["content_hash"] {
		return fmt.Errorf("backup: %s Slides hash mismatch", table)
	}
	return nil
}

func validateSlidesRowSchema(row map[string]any, schema int) error {
	var version int64
	switch value := row["schema_version"].(type) {
	case int:
		version = int64(value)
	case int64:
		version = value
	case float64:
		if value < 1 || value > math.MaxInt32 || math.Trunc(value) != value {
			return fmt.Errorf("backup: invalid Slides schema version")
		}
		version = int64(value)
	case json.Number:
		var err error
		version, err = value.Int64()
		if err != nil {
			return fmt.Errorf("backup: invalid Slides schema version")
		}
	default:
		return fmt.Errorf("backup: missing Slides schema version")
	}
	if version != int64(schema) {
		return fmt.Errorf("backup: Slides row/snapshot schema mismatch")
	}
	return nil
}
