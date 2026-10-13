package pages

import (
	"encoding/json"
	"errors"
	"fmt"
)

func validateBoardBatchShape(batch BoardBatch) error {
	if batch.BaseSeq < 0 || batch.BaseSeq >= 9007199254740991 {
		return errors.New("invalid board base sequence")
	}
	count := len(batch.Upserts) + len(batch.Deletes)
	if count < 1 || count > MaxBoardBatchOperations {
		return errors.New("board batch requires 1 to 500 operations")
	}
	bytes := 0
	seen := map[string]bool{}
	for _, upsert := range batch.Upserts {
		bytes += len(upsert.Element)
		if bytes > MaxBoardBatchBytes {
			return errors.New("board batch exceeds byte limit")
		}
		element, err := ValidateBoardElement(upsert.Element)
		if err != nil {
			return err
		}
		if upsert.ExpectedRev < 0 || upsert.ExpectedRev >= 9007199254740991 || seen[element.ID] {
			return errors.New("invalid or duplicate board upsert")
		}
		seen[element.ID] = true
	}
	for _, deleted := range batch.Deletes {
		if !boardID.MatchString(deleted.ID) || deleted.ExpectedRev < 1 || deleted.ExpectedRev >= 9007199254740991 || seen[deleted.ID] {
			return errors.New("invalid or duplicate board delete")
		}
		seen[deleted.ID] = true
	}
	return nil
}

// ApplyBoardBatch returns a new scene and never mutates current, even if a
// later operation or final graph check fails. Excalidraw version/versionNonce
// never participate in authority: only server sequence and element revisions do.
func ApplyBoardBatch(current BoardScene, batch BoardBatch) (BoardScene, error) {
	if err := validateBoardBatchShape(batch); err != nil {
		return BoardScene{}, err
	}
	if current.Seq != batch.BaseSeq {
		return BoardScene{}, fmt.Errorf("%w: expected sequence %d, current %d", ErrBoardConflict, batch.BaseSeq, current.Seq)
	}
	if len(current.Records) > MaxBoardRecords {
		return BoardScene{}, errors.New("board record limit exceeded")
	}
	next := BoardScene{Seq: current.Seq + 1, Records: make(map[string]BoardRecord, len(current.Records)+len(batch.Upserts))}
	for id, record := range current.Records {
		if record.ElementRev < 1 || record.ElementRev >= 9007199254740991 || !boardID.MatchString(id) {
			return BoardScene{}, errors.New("invalid canonical board record")
		}
		record.Element = append(json.RawMessage(nil), record.Element...)
		next.Records[id] = record
	}
	for _, upsert := range batch.Upserts {
		element, err := ValidateBoardElement(upsert.Element)
		if err != nil {
			return BoardScene{}, err
		}
		existing, present := current.Records[element.ID]
		if (!present && upsert.ExpectedRev != 0) || (present && existing.ElementRev != upsert.ExpectedRev) {
			return BoardScene{}, fmt.Errorf("%w: element %s changed", ErrBoardConflict, element.ID)
		}
		// Tombstoned IDs cannot be silently reused. An explicit matching revision
		// permits a deliberate restore/undo, still under the whole-board CAS.
		next.Records[element.ID] = BoardRecord{Element: append(json.RawMessage(nil), upsert.Element...), ElementRev: upsert.ExpectedRev + 1}
	}
	for _, deleted := range batch.Deletes {
		record, present := current.Records[deleted.ID]
		if !present || record.Deleted || record.ElementRev != deleted.ExpectedRev {
			return BoardScene{}, fmt.Errorf("%w: element %s changed", ErrBoardConflict, deleted.ID)
		}
		next.Records[deleted.ID] = BoardRecord{ElementRev: record.ElementRev + 1, Deleted: true}
	}
	if len(next.Records) > MaxBoardRecords {
		return BoardScene{}, errors.New("board record limit exceeded")
	}
	if err := ValidateBoardScene(next); err != nil {
		return BoardScene{}, err
	}
	return next, nil
}

// ValidateBoardScene checks the complete post-batch graph, so deletion cannot
// leave a live arrow, label or child pointing at an absent/deleted element.
func ValidateBoardScene(scene BoardScene) error {
	if scene.Seq < 0 || scene.Seq > 9007199254740991 || len(scene.Records) > MaxBoardRecords {
		return errors.New("invalid board scene bounds")
	}
	elements := map[string]BoardElement{}
	totalBytes := 0
	for id, record := range scene.Records {
		if !boardID.MatchString(id) || record.ElementRev < 1 || record.ElementRev >= 9007199254740991 {
			return errors.New("invalid canonical board identity")
		}
		if record.Deleted {
			if len(record.Element) != 0 {
				return errors.New("tombstones cannot retain element payloads")
			}
			continue
		}
		totalBytes += len(record.Element)
		if totalBytes > MaxBoardBatchBytes {
			return errors.New("board scene exceeds 2 MiB")
		}
		element, err := ValidateBoardElement(record.Element)
		if err != nil {
			return err
		}
		if element.ID != id {
			return errors.New("board record and element identities differ")
		}
		elements[id] = element
	}
	if len(elements) > MaxBoardElements {
		return errors.New("board active element limit exceeded")
	}
	for id, element := range elements {
		if element.FrameID != nil {
			parent, ok := elements[*element.FrameID]
			if !ok || parent.Type != "frame" {
				return fmt.Errorf("element %s has absent or non-frame parent", id)
			}
		}
		if element.ContainerID != nil {
			parent, ok := elements[*element.ContainerID]
			if !ok || !member(parent.Type, "rectangle", "ellipse", "diamond", "arrow") || !boardHasBound(parent, id, "text") {
				return fmt.Errorf("text %s has absent or invalid container", id)
			}
		}
		for _, binding := range []*BoardBinding{element.StartBinding, element.EndBinding} {
			if binding != nil {
				target, ok := elements[binding.ElementID]
				if !ok || !member(target.Type, "rectangle", "ellipse", "diamond", "text", "frame", "image") || !boardHasBound(target, id, element.Type) {
					return fmt.Errorf("element %s has absent or invalid binding", id)
				}
			}
		}
		for _, bound := range element.BoundElements {
			target, ok := elements[bound.ID]
			if !ok || target.Type != bound.Type || !boardReferences(target, id) {
				return fmt.Errorf("element %s has absent or mistyped bound element", id)
			}
		}
		seen := map[string]bool{id: true}
		parent := element.FrameID
		for parent != nil {
			if seen[*parent] {
				return errors.New("cyclic frame ancestry")
			}
			seen[*parent] = true
			ancestor, ok := elements[*parent]
			if !ok {
				return errors.New("missing frame ancestor")
			}
			parent = ancestor.FrameID
		}
	}
	return nil
}

func boardHasBound(e BoardElement, id, kind string) bool {
	for _, b := range e.BoundElements {
		if b.ID == id && b.Type == kind {
			return true
		}
	}
	return false
}
func boardReferences(e BoardElement, id string) bool {
	if e.Type == "text" {
		return e.ContainerID != nil && *e.ContainerID == id
	}
	return (e.StartBinding != nil && e.StartBinding.ElementID == id) || (e.EndBinding != nil && e.EndBinding.ElementID == id)
}
