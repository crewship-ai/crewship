package pages

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func boardFixture(t *testing.T, id string, changes map[string]any) json.RawMessage {
	t.Helper()
	fields := map[string]any{"id": id, "type": "rectangle", "x": 10, "y": 20, "width": 100, "height": 80, "angle": 0, "strokeColor": "#1e1e1e", "backgroundColor": "transparent", "fillStyle": "hachure", "strokeWidth": 1, "strokeStyle": "solid", "roughness": 1, "opacity": 100, "seed": 1, "version": 1, "versionNonce": 1, "index": "a0", "isDeleted": false, "groupIds": []string{}, "frameId": nil, "boundElements": nil, "updated": 1, "link": nil, "locked": false, "roundness": nil}
	for k, v := range changes {
		fields[k] = v
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func boardScene(t *testing.T, elements ...json.RawMessage) BoardScene {
	t.Helper()
	s := BoardScene{Seq: 1, Records: map[string]BoardRecord{}}
	for _, raw := range elements {
		e, err := ValidateBoardElement(raw)
		if err != nil {
			t.Fatal(err)
		}
		s.Records[e.ID] = BoardRecord{Element: raw, ElementRev: 1}
	}
	return s
}
func TestBoardElementUnsafeInput(t *testing.T) {
	for name, change := range map[string]map[string]any{
		"image": {"type": "image"}, "iframe": {"type": "iframe"}, "embed": {"type": "embeddable"}, "custom": {"customData": map[string]any{"html": "<script>"}}, "link": {"link": "https://example.com"}, "css": {"backgroundColor": "url(https://example.com)"}, "coordinate": {"x": 1000001}, "negative size": {"width": -1}, "unknown": {"evil": true}, "wrong family": {"text": "hidden"}, "bad nested": {"roundness": map[string]any{"type": 1, "evil": true}}, "bad type": {"type": "selection"}, "bad geometry": {"x": nil},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateBoardElement(boardFixture(t, "a", change)); err == nil {
				t.Fatal("unsafe element accepted")
			}
		})
	}
	raw := boardFixture(t, "a", nil)
	for name, input := range map[string][]byte{"duplicate": []byte(strings.Replace(string(raw), `"id":"a"`, `"id":"a","id":"b"`, 1)), "trailing": append(append([]byte{}, raw...), []byte(` {}`)...), "invalid utf8": append(raw, 0xff), "oversized": []byte(strings.Repeat(" ", MaxBoardBatchBytes+1))} {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateBoardElement(input); err == nil {
				t.Fatal("ambiguous input accepted")
			}
		})
	}
}
func TestBoardLinearAndTextBounds(t *testing.T) {
	valid := map[string]any{"type": "arrow", "points": [][]int{{0, 0}, {20, 20}}, "startBinding": nil, "endBinding": nil, "startArrowhead": nil, "endArrowhead": "arrow", "elbowed": false}
	if _, err := ValidateBoardElement(boardFixture(t, "arrow", valid)); err != nil {
		t.Fatal(err)
	}
	valid["points"] = [][]int{{0, 0, 9}, {20, 20}}
	if _, err := ValidateBoardElement(boardFixture(t, "arrow", valid)); err == nil {
		t.Fatal("three dimensional point accepted")
	}
	text := map[string]any{"type": "text", "fontSize": 20, "fontFamily": 5, "text": "<script>literal canvas text</script>", "originalText": "literal", "textAlign": "left", "verticalAlign": "top", "containerId": nil, "autoResize": true, "lineHeight": 1.25}
	if _, err := ValidateBoardElement(boardFixture(t, "text", text)); err != nil {
		t.Fatal(err)
	}
	text["text"] = strings.Repeat("x", MaxBoardTextBytes+1)
	if _, err := ValidateBoardElement(boardFixture(t, "text", text)); err == nil {
		t.Fatal("oversized text accepted")
	}
}
func TestBoardBatchCASAndAtomicity(t *testing.T) {
	current := boardScene(t, boardFixture(t, "a", nil), boardFixture(t, "b", nil))
	before, _ := json.Marshal(current)
	batch := BoardBatch{BaseSeq: 1, Upserts: []BoardUpsert{{Element: boardFixture(t, "a", map[string]any{"x": 30, "version": 999999, "versionNonce": 999999}), ExpectedRev: 1}, {Element: boardFixture(t, "b", nil), ExpectedRev: 2}}}
	if _, err := ApplyBoardBatch(current, batch); !errors.Is(err, ErrBoardConflict) {
		t.Fatalf("want conflict: %v", err)
	}
	after, _ := json.Marshal(current)
	if string(before) != string(after) {
		t.Fatal("partial batch mutated input")
	}
	batch.Upserts = batch.Upserts[:1]
	next, err := ApplyBoardBatch(current, batch)
	if err != nil {
		t.Fatal(err)
	}
	if next.Seq != 2 || next.Records["a"].ElementRev != 2 {
		t.Fatal("server revisions not advanced")
	}
	if _, err := ApplyBoardBatch(next, batch); !errors.Is(err, ErrBoardConflict) {
		t.Fatalf("stale sequence accepted: %v", err)
	}
	batch.BaseSeq = 2
	batch.Upserts[0].ExpectedRev = 1
	if _, err := ApplyBoardBatch(next, batch); !errors.Is(err, ErrBoardConflict) {
		t.Fatalf("rendering metadata overrode server revision: %v", err)
	}
	// The returned payload is independently owned.
	next.Records["b"].Element[0] = 'x'
	if !reflect.DeepEqual(before, after) || current.Records["b"].Element[0] != '{' {
		t.Fatal("scene payload aliases input")
	}
}
func TestBoardDeletionAndExplicitRestore(t *testing.T) {
	current := boardScene(t, boardFixture(t, "a", nil))
	deleted, err := ApplyBoardBatch(current, BoardBatch{BaseSeq: current.Seq, Deletes: []BoardDelete{{ID: "a", ExpectedRev: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if !deleted.Records["a"].Deleted || deleted.Records["a"].ElementRev != 2 {
		t.Fatal("missing tombstone")
	}
	restore := BoardBatch{BaseSeq: 2, Upserts: []BoardUpsert{{Element: boardFixture(t, "a", nil), ExpectedRev: 0}}}
	if _, err := ApplyBoardBatch(deleted, restore); !errors.Is(err, ErrBoardConflict) {
		t.Fatal("tombstone reused without expected revision")
	}
	restore.Upserts[0].ExpectedRev = 2
	restored, err := ApplyBoardBatch(deleted, restore)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Records["a"].Deleted || restored.Records["a"].ElementRev != 3 {
		t.Fatal("restore revision incorrect")
	}
}
func TestBoardGraphValidation(t *testing.T) {
	frame := boardFixture(t, "frame", map[string]any{"type": "frame", "name": "Frame"})
	child := boardFixture(t, "child", map[string]any{"frameId": "frame"})
	current := boardScene(t, frame, child)
	if err := ValidateBoardScene(current); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyBoardBatch(current, BoardBatch{BaseSeq: current.Seq, Deletes: []BoardDelete{{ID: "frame", ExpectedRev: 1}}}); err == nil {
		t.Fatal("dangling frame accepted")
	}
	next, err := ApplyBoardBatch(current, BoardBatch{BaseSeq: current.Seq, Deletes: []BoardDelete{{ID: "frame", ExpectedRev: 1}}, Upserts: []BoardUpsert{{Element: boardFixture(t, "child", nil), ExpectedRev: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateBoardScene(next); err != nil {
		t.Fatal(err)
	}
	cycle := boardScene(t, boardFixture(t, "a", map[string]any{"type": "frame", "frameId": "b"}), boardFixture(t, "b", map[string]any{"type": "frame", "frameId": "a"}))
	if err := ValidateBoardScene(cycle); err == nil {
		t.Fatal("cyclic frames accepted")
	}
	arrow := boardFixture(t, "arrow", map[string]any{"type": "arrow", "points": [][]int{{0, 0}, {1, 1}}, "endBinding": map[string]any{"elementId": "absent", "focus": 0, "gap": 0}})
	if err := ValidateBoardScene(boardScene(t, arrow)); err == nil {
		t.Fatal("dangling arrow accepted")
	}
}
func TestBoardDecodeBatchStrict(t *testing.T) {
	raw := boardFixture(t, "a", nil)
	good := `{"base_seq":0,"upserts":[{"element":` + string(raw) + `,"expected_rev":0}],"deletes":[]}`
	if _, err := DecodeBoardBatch([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{strings.Replace(good, `"base_seq":0`, `"base_seq":0,"extra":true`, 1), strings.Replace(good, `"base_seq":0`, `"base_seq":0,"base_seq":1`, 1), good + `{}`, `{"base_seq":0,"upserts":[],"deletes":[]}`} {
		if _, err := DecodeBoardBatch([]byte(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	batch := BoardBatch{Upserts: []BoardUpsert{{Element: raw}, {Element: raw}}}
	if _, err := ApplyBoardBatch(BoardScene{}, batch); err == nil {
		t.Fatal("duplicate operation accepted")
	}
}

func TestBoardPinnedBrowserFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/board-excalidraw-0.18.1.json")
	if err != nil {
		t.Fatal(err)
	}
	var elements []json.RawMessage
	if err = json.Unmarshal(raw, &elements); err != nil {
		t.Fatal(err)
	}
	if len(elements) != 8 {
		t.Fatalf("expected real eight-element fixture, got %d", len(elements))
	}
	if err := ValidateBoardScene(boardScene(t, elements...)); err != nil {
		t.Fatal(err)
	}
}
func TestBoardNullAndMissingAuthorityRejected(t *testing.T) {
	raw := boardFixture(t, "a", nil)
	for _, prefix := range []string{``, `"base_seq":null,`} {
		input := `{` + prefix + `"upserts":[{"expected_rev":0,"element":` + string(raw) + `}]}`
		if _, err := DecodeBoardBatch([]byte(input)); err == nil {
			t.Fatal("missing/null sequence accepted")
		}
	}
	for _, expected := range []string{``, `"expected_rev":null,`} {
		input := `{"base_seq":0,"upserts":[{` + expected + `"element":` + string(raw) + `}]}`
		if _, err := DecodeBoardBatch([]byte(input)); err == nil {
			t.Fatal("missing/null element revision accepted")
		}
	}
	for _, input := range []string{`null`, `{}`} {
		if _, err := DecodeBoardBatch([]byte(input)); err == nil {
			t.Fatal("absent batch accepted")
		}
	}
	for _, key := range []string{"angle", "opacity", "versionNonce", "strokeWidth"} {
		if _, err := ValidateBoardElement(boardFixture(t, "a", map[string]any{key: nil})); err == nil {
			t.Fatalf("null %s accepted", key)
		}
	}
	if _, err := ValidateBoardElement(boardFixture(t, "a", map[string]any{"type": "line", "points": []any{[]any{nil, 1}, []int{0, 1}}})); err == nil {
		t.Fatal("null coordinate accepted")
	}
	if _, err := ValidateBoardElement(boardFixture(t, "a", map[string]any{"customData": nil})); err != nil {
		t.Fatal(err)
	}
}
func TestBoardRelationshipReciprocity(t *testing.T) {
	a := boardFixture(t, "a", nil)
	arrow := boardFixture(t, "arrow", map[string]any{"type": "arrow", "points": [][]int{{0, 0}, {1, 1}}, "endBinding": map[string]any{"elementId": "a", "focus": 0, "gap": 0}})
	if err := ValidateBoardScene(boardScene(t, a, arrow)); err == nil {
		t.Fatal("one-way binding accepted")
	}
	a = boardFixture(t, "a", map[string]any{"boundElements": []map[string]string{{"id": "arrow", "type": "arrow"}}})
	if err := ValidateBoardScene(boardScene(t, a, arrow)); err != nil {
		t.Fatal(err)
	}
	arrow = boardFixture(t, "arrow", map[string]any{"type": "arrow", "points": [][]int{{0, 0}, {1, 1}}})
	if err := ValidateBoardScene(boardScene(t, a, arrow)); err == nil {
		t.Fatal("orphan boundElement accepted")
	}
}

func TestBoardSceneAndBatchLimits(t *testing.T) {
	s := BoardScene{Seq: 1, Records: map[string]BoardRecord{}}
	for i := 0; i < MaxBoardElements; i++ {
		id := fmt.Sprintf("e%d", i)
		s.Records[id] = BoardRecord{Element: boardFixture(t, id, nil), ElementRev: 1}
	}
	if err := ValidateBoardScene(s); err != nil {
		t.Fatal(err)
	}
	s.Records["extra"] = BoardRecord{Element: boardFixture(t, "extra", nil), ElementRev: 1}
	if err := ValidateBoardScene(s); err == nil {
		t.Fatal("active count limit not enforced")
	}
	delete(s.Records, "extra")
	for i := MaxBoardElements; i < MaxBoardRecords; i++ {
		s.Records[fmt.Sprintf("e%d", i)] = BoardRecord{Deleted: true, ElementRev: 1}
	}
	if err := ValidateBoardScene(s); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyBoardBatch(s, BoardBatch{Upserts: []BoardUpsert{{Element: boardFixture(t, "new", nil)}}}); err == nil {
		t.Fatal("record limit not enforced")
	}
	ops := make([]BoardDelete, MaxBoardBatchOperations+1)
	for i := range ops {
		ops[i] = BoardDelete{ID: fmt.Sprintf("e%d", i), ExpectedRev: 1}
	}
	if _, err := ApplyBoardBatch(s, BoardBatch{BaseSeq: s.Seq, Deletes: ops}); err == nil {
		t.Fatal("operation limit not enforced")
	}
}
func TestBoardExactFieldNamesAndPressureNumbers(t *testing.T) {
	raw := boardFixture(t, "a", nil)
	raw = []byte(strings.Replace(string(raw), `"id":"a"`, `"ID":"a"`, 1))
	if _, err := ValidateBoardElement(raw); err == nil {
		t.Fatal("case-insensitive field alias accepted")
	}
	if _, err := ValidateBoardElement(boardFixture(t, "a", map[string]any{"type": "freedraw", "points": [][]int{{0, 0}, {1, 1}}, "pressures": []any{nil, 0.5}})); err == nil {
		t.Fatal("null pressure accepted")
	}
}

// Captured from the real pinned React editor by library.spec.ts. This proves
// the public converter's grouped library output satisfies our server profile.
func TestBoardLocalLibraryBrowserFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/board-library-0.18.1.json")
	if err != nil {
		t.Fatal(err)
	}
	var elements []json.RawMessage
	if err = json.Unmarshal(raw, &elements); err != nil {
		t.Fatal(err)
	}
	if len(elements) != 32 {
		t.Fatalf("unexpected library fixture size: %d", len(elements))
	}
	if err := ValidateBoardScene(boardScene(t, elements...)); err != nil {
		t.Fatal(err)
	}
}
