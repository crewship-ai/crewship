package backup

import (
	"encoding/json"
	"fmt"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Remap only typed approval references in the fork bundle, before its canonical
// journal rechain. Never rewrite historical human/run proof or operation hashes.
// Deleted sources have no PK mapping: both their history and retirement marker
// retain the same original ID, preserving private classification together.
func remapApprovalSources(dump *DBDump, ids map[string]map[string]string) error {
	approvals := ids["approvals_queue"]
	for _, row := range dump.Tables["journal_entries"] {
		if err := remapApprovalJSONID(row, "refs", approvals); err != nil {
			return err
		}
		typeName, _ := row["entry_type"].(string)
		switch journal.EntryType(typeName) {
		case journal.EntryApprovalRequest, journal.EntryApprovalGranted, journal.EntryApprovalDenied, journal.EntryApprovalTimeout, journal.EntryApprovalCancelled, journal.EntryApprovalSourceRetired:
			if err := remapApprovalJSONID(row, "payload", approvals); err != nil {
				return err
			}
		}
	}
	return nil
}

func remapApprovalJSONID(row map[string]any, column string, approvals map[string]string) error {
	raw, _ := row[column].(string)
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &object) != nil {
		return nil
	}
	var original string
	if json.Unmarshal(object["approval_id"], &original) != nil || approvals[original] == "" {
		return nil
	}
	object["approval_id"], _ = json.Marshal(approvals[original])
	encoded, err := json.Marshal(object)
	if err != nil {
		return fmt.Errorf("backup: remap approval %s reference: %w", column, err)
	}
	row[column] = string(encoded)
	return nil
}
