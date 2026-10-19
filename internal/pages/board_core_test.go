package pages

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func coreBoardElement(t *testing.T, id string, changes map[string]any) json.RawMessage {
	t.Helper()
	fields := map[string]any{
		"id": id, "type": "rectangle", "x": 10, "y": 20, "width": 100, "height": 80,
		"angle": 0, "strokeColor": "#1e1e1e", "backgroundColor": "transparent", "fillStyle": "hachure",
		"strokeWidth": 1, "strokeStyle": "solid", "roughness": 1, "opacity": 100,
		"seed": 1, "version": 1, "versionNonce": 1, "index": "a0", "isDeleted": false,
		"groupIds": []string{}, "frameId": nil, "boundElements": nil, "updated": 1,
		"link": nil, "locked": false, "roundness": nil,
	}
	for key, value := range changes {
		fields[key] = value
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func coreBoardScene(t *testing.T, elements ...json.RawMessage) BoardScene {
	t.Helper()
	scene := BoardScene{Seq: 1, Records: map[string]BoardRecord{}}
	for _, raw := range elements {
		element, err := ValidateBoardElement(raw)
		if err != nil {
			t.Fatal(err)
		}
		scene.Records[element.ID] = BoardRecord{Element: raw, ElementRev: 1}
	}
	return scene
}
func TestBoardCoreDecodeAndClosure(t *testing.T) {
	raw := coreBoardElement(t, "a", nil)
	good := `{"base_seq":1,"upserts":[{"element":` + string(raw) + `,"expected_rev":1}],"deletes":[]}`
	if _, err := DecodeBoardBatch([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		strings.Replace(good, `"base_seq":1`, `"base_seq":null`, 1),
		strings.Replace(good, `"expected_rev":1`, `"untrusted":1`, 1),
		strings.Replace(good, `"base_seq":1`, `"base_seq":1,"base_seq":2`, 1), good + `{}`,
	} {
		if _, err := DecodeBoardBatch([]byte(invalid)); err == nil {
			t.Fatal("ambiguous batch accepted")
		}
	}
	for _, change := range []map[string]any{{"link": "https://example.invalid"}, {"type": "embeddable"}, {"evil": true}, {"x": nil}, {"width": -1}, {"text": "hidden"}, {"customData": map[string]any{"html": "script"}}} {
		if _, err := ValidateBoardElement(coreBoardElement(t, "a", change)); err == nil {
			t.Fatalf("unsafe element accepted: %v", change)
		}
	}

}
func TestBoardCoreCASAtomicityAndOwnership(t *testing.T) {
	current := coreBoardScene(t, coreBoardElement(t, "a", nil), coreBoardElement(t, "b", nil))
	before, _ := json.Marshal(current)
	batch := BoardBatch{BaseSeq: 1, Upserts: []BoardUpsert{{Element: coreBoardElement(t, "a", map[string]any{"x": 30, "version": 999}), ExpectedRev: 1}, {Element: coreBoardElement(t, "b", nil), ExpectedRev: 2}}}
	if _, err := ApplyBoardBatch(current, batch); !errors.Is(err, ErrBoardConflict) {
		t.Fatalf("want record conflict: %v", err)
	}
	after, _ := json.Marshal(current)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("partial batch mutated input")
	}
	batch.Upserts = batch.Upserts[:1]
	next, err := ApplyBoardBatch(current, batch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyBoardBatch(next, batch); !errors.Is(err, ErrBoardConflict) {
		t.Fatal("stale base accepted")
	}
	next.Records["b"].Element[0] = 'x'
	if current.Records["b"].Element[0] != '{' {
		t.Fatal("unchanged output aliases input")
	}

}
func TestBoardCoreTombstoneAndExplicitRestore(t *testing.T) {
	current := coreBoardScene(t, coreBoardElement(t, "a", nil))
	deleted, err := ApplyBoardBatch(current, BoardBatch{BaseSeq: 1, Deletes: []BoardDelete{{ID: "a", ExpectedRev: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if !deleted.Records["a"].Deleted || deleted.Records["a"].ElementRev != 2 || len(deleted.Records["a"].Element) != 0 {
		t.Fatal("invalid tombstone")
	}
	restore := BoardBatch{BaseSeq: 2, Upserts: []BoardUpsert{{Element: coreBoardElement(t, "a", nil), ExpectedRev: 0}}}
	if _, err := ApplyBoardBatch(deleted, restore); !errors.Is(err, ErrBoardConflict) {
		t.Fatal("tombstone silently reused")
	}
	restore.Upserts[0].ExpectedRev = 2
	restored, err := ApplyBoardBatch(deleted, restore)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Seq != 3 || restored.Records["a"].Deleted || restored.Records["a"].ElementRev != 3 {
		t.Fatal("explicit restore lost identity/revision")
	}
}
func TestBoardCoreDanglingAndCyclicGraph(t *testing.T) {
	frame := coreBoardElement(t, "frame", map[string]any{"type": "frame", "name": "Frame"})
	child := coreBoardElement(t, "child", map[string]any{"frameId": "frame"})
	current := coreBoardScene(t, frame, child)
	if _, err := ApplyBoardBatch(current, BoardBatch{BaseSeq: 1, Deletes: []BoardDelete{{ID: "frame", ExpectedRev: 1}}}); err == nil {
		t.Fatal("dangling child accepted")
	}
	cycle := coreBoardScene(t, coreBoardElement(t, "a", map[string]any{"type": "frame", "frameId": "b"}), coreBoardElement(t, "b", map[string]any{"type": "frame", "frameId": "a"}))
	if err := ValidateBoardScene(cycle); err == nil {
		t.Fatal("cyclic ancestry accepted")
	}
	arrow := coreBoardElement(t, "arrow", map[string]any{"type": "arrow", "points": [][]int{{0, 0}, {1, 1}}, "endBinding": map[string]any{"elementId": "absent", "focus": 0, "gap": 0}})
	if err := ValidateBoardScene(coreBoardScene(t, arrow)); err == nil {
		t.Fatal("dangling binding accepted")
	}
}
