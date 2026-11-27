package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/craftsession"
)

// policedSheetSession checks every string it is asked to write the way the server's engine
// session does: with the production input policy (formulas off) unless the context names another.
type policedSheetSession struct{ *fakeSheetSession }

func (s policedSheetSession) check(ctx context.Context, values ...any) error {
	policy := craftsession.PolicyFrom(ctx, craftsession.InputPolicy(CraftNativeAgentInputPolicy(false)))
	for _, v := range values {
		if text, ok := v.(string); ok && policy(text) != nil {
			return craftsession.ErrPolicy
		}
	}
	return nil
}

func (s policedSheetSession) SetCell(ctx context.Context, cell, input string) error {
	if err := s.check(ctx, input); err != nil {
		return err
	}
	return s.fakeSheetSession.SetCell(ctx, cell, input)
}

func (s policedSheetSession) WriteRange(ctx context.Context, rng string, values [][]any) (int, error) {
	for _, row := range values {
		if err := s.check(ctx, row...); err != nil {
			return 0, err
		}
	}
	return s.fakeSheetSession.WriteRange(ctx, rng, values)
}

func (s policedSheetSession) Apply(ctx context.Context, ops []CraftNativeSheetWrite) (int, error) {
	for _, op := range ops {
		if err := s.check(ctx, op.Input); err != nil {
			return 0, err
		}
		for _, row := range op.Values {
			if err := s.check(ctx, row...); err != nil {
				return 0, err
			}
		}
	}
	return s.fakeSheetSession.Apply(ctx, ops)
}

type policedSheetEngine struct{ session *fakeSheetSession }

func (e policedSheetEngine) Open(context.Context, []byte) (CraftNativeSheetSession, func(), error) {
	return policedSheetSession{e.session}, func() {}, nil
}
func (policedSheetEngine) Retain(CraftNativeSheetSession, []byte) {}
func (policedSheetEngine) Discard(CraftNativeSheetSession)        {}

// The undo of an agent's clear of two columns of 570 rows each, holding text and formulas, goes
// through the session's own input policy too: before the fix the formulas of the second column
// were refused there and the person got engine_failed with nothing in the log.
func TestUndoOfTwoClearedColumnsWithFormulasPassesTheSessionPolicy(t *testing.T) {
	ops := json.RawMessage(`[{"command":"edit.clearContents","params":{"range":"'L'!B8:B577"}},` +
		`{"command":"edit.clearContents","params":{"range":"'L'!E8:E577"}}]`)
	ids, sums, cleared := make([][]any, 570), make([][]any, 570), make([][]any, 570)
	restoredCells := make(sitCells, 1140)
	for r := range ids {
		ids[r] = []any{fmt.Sprintf("L-%04d it's", r)}
		sums[r] = []any{fmt.Sprintf("=C%d*D%d", r+8, r+8)}
		if r%7 == 0 {
			sums[r] = []any{fmt.Sprintf("%d.5", r)}
		}
		cleared[r] = []any{nil}
		restoredCells[sitAddress(2, r+8)] = ids[r][0].(string)
		sum := sums[r][0].(string)
		if strings.HasPrefix(sum, "=") {
			sum += "|0"
		}
		restoredCells[sitAddress(5, r+8)] = sum
	}
	old := [][][]any{ids, sums}
	inverse, ok := craftInverseFrom(ops, old)
	if !ok {
		t.Fatal("no inverse for two cleared columns")
	}
	now := [][][]any{cleared, cleared}
	commands, restored, skipped, ok := craftUndoCommands(ops, inverse, now, now, craftUndoTouched{})
	if !ok || restored != 1140 || skipped != 0 {
		t.Fatalf("undo commands: ok %v restored %d skipped %d", ok, restored, skipped)
	}
	p := CraftNativeDocumentProfile{Application: "gridcraft"}
	const sheet = "xl/worksheets/sheet1.xml"
	base := craftTestWorkbook(t, "xlsx", "before")
	raw := workbookEdited(t, base, "", map[string]string{sheet: sitSheetXML(nil)})
	snapshot := workbookEdited(t, raw, "", map[string]string{sheet: sitSheetXML(restoredCells)})
	session := &fakeSheetSession{snapshot: snapshot}
	adapter := NewCraftNativeSheetAgentAdapter(policedSheetEngine{session})
	batch, _ := json.Marshal(commands)
	if _, err := adapter.Apply(context.Background(), p, raw, batch); err == nil {
		t.Fatal("an agent change wrote the formulas: the session policy no longer guards agents")
	}
	if _, err := adapter.Apply(craftNativeUndoContext(context.Background()), p, raw, batch); err != nil {
		t.Fatalf("undo of the two columns: %v", err)
	}
	// Text is written cell by cell, the way a person types it; numbers and blanks go in blocks.
	written := append([]string{}, session.calls...)
	for _, row := range session.writes {
		for _, v := range row {
			written = append(written, fmt.Sprint(v))
		}
	}
	joined := strings.Join(written, "\n")
	for _, want := range []string{"'L'!B8=L-0000 it's", "'L'!E9==C9*D9", "'L'!E8=0.5", "'L'!B577=L-0569 it's", "'L'!E577==C577*D577"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the undo did not write %q back", want)
		}
	}
}
