package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// The agent reads a Sheet kept as its workbook file as that file.
func TestNativeAgentDocumentBytesReadsAWorkbookSheet(t *testing.T) {
	for _, f := range []struct{ name, media string }{{"workbook.xlsx", mooring.WorkbookMediaTypeXLSX}, {"workbook.xlsm", mooring.WorkbookMediaTypeXLSM}} {
		state := artifacts.VersionState{Manifest: artifacts.VersionManifest{EntryPoint: f.name, Files: map[string]artifacts.VersionFile{f.name: {Content: []byte("PK\x03\x04wb"), MediaType: f.media}}}}
		got, err := nativeAgentDocumentBytes(state)
		if err != nil || string(got) != "PK\x03\x04wb" {
			t.Fatalf("%s: %q %v", f.name, got, err)
		}
	}
	odd := artifacts.VersionState{Manifest: artifacts.VersionManifest{EntryPoint: "workbook.xlsx", Files: map[string]artifacts.VersionFile{"workbook.xlsx": {Content: []byte("PK"), MediaType: "application/zip"}}}}
	if _, err := nativeAgentDocumentBytes(odd); err == nil {
		t.Fatal("a workbook entry with another media type was read")
	}
}

// Over a workbook file the agent clears a range (range.clear), the open check of the native JSON
// does not apply (the save's fidelity check does), and a save that would lose parts comes back as
// that loss, not as a generic engine failure.
func TestSheetSessionAppliesToAWorkbookAndKeepsAFidelityLoss(t *testing.T) {
	raw := craftTestWorkbook(t, "xlsx", "before")
	parts := sitParts(t, raw)
	part := string(parts["xl/worksheets/sheet1.xml"])
	sheet := strings.Replace(part, "</row>", `<c r="C1" t="inlineStr"><is><t>done</t></is></c></row>`, 1)
	after := workbookEdited(t, raw, "", map[string]string{"xl/worksheets/sheet1.xml": sheet})
	engine := &fakeSheetEngine{session: &fakeSheetSession{snapshot: after}}
	checked := 0
	a := NewCraftNativeSheetAgentAdapter(engine, WithCraftNativeSheetOpenCheck(func(base, snapshot []byte) error { checked++; return errors.New("not JSON") }))
	p := CraftNativeDocumentProfile{Application: "gridcraft"}
	commands := json.RawMessage(`[{"command":"range.clear","params":{"range":"B8:B577","sheet":"L"}},{"command":"cell.set","params":{"cell":"C1","input":"done","sheet":"L"}}]`)
	out, err := a.Apply(context.Background(), p, raw, commands)
	if err != nil || !bytes.Equal(out, after) || checked != 0 {
		t.Fatalf("apply %q %v, open checks %d", out, err, checked)
	}
	if want := "clear 'L'!B8:B577|set 'L'!C1=done|snapshot"; strings.Join(engine.session.calls, "|") != want {
		t.Fatalf("calls %v", engine.session.calls)
	}
	loss := &craftsession.FidelityLossError{Parts: []craftsession.FidelityPart{{Code: "pivot_fields", Count: 2}}}
	engine.session = &fakeSheetSession{snapshot: nil, failOn: "snapshot"}
	failing := &lossySession{fakeSheetSession: engine.session, err: loss}
	engine.session = failing.fakeSheetSession
	a2 := NewCraftNativeSheetAgentAdapter(&lossyEngine{failing})
	_, err = a2.Apply(context.Background(), p, raw, commands)
	var got *craftsession.FidelityLossError
	if !errors.As(err, &got) || got.Parts[0].Code != "pivot_fields" {
		t.Fatalf("loss %v", err)
	}
	// In a workbook file every write names its sheet: the file's active sheet on the server is
	// not the sheet the person sees. Refused before any session opens.
	opened := &fakeSheetEngine{session: &fakeSheetSession{snapshot: after}}
	for _, unnamed := range []string{`[{"command":"cell.set","params":{"cell":"C1","input":"x"}}]`, `[{"command":"range.clear","params":{"range":"B8:B9"}}]`, `[{"command":"range.setValues","params":{"range":"A1:A1","values":[[1]]}}]`} {
		if _, err := NewCraftNativeSheetAgentAdapter(opened).Apply(context.Background(), p, raw, json.RawMessage(unnamed)); !errors.Is(err, errCraftNativeSheetNameRequired) || len(opened.session.calls) != 0 {
			t.Fatalf("%s: %v %v", unnamed, err, opened.session.calls)
		}
	}
	// The agent is told what to do: invalid, with the sheet to name; a loss names its parts.
	w := httptest.NewRecorder()
	if !nativeAgentChangeRefused(w, errCraftNativeSheetNameRequired) || w.Code != 400 || !strings.Contains(w.Body.String(), `name the sheet in every write: params.sheet`) || !strings.Contains(w.Body.String(), `'L'!B8`) {
		t.Fatalf("unnamed reply %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	if !nativeAgentChangeRefused(w, loss) || w.Code != 422 || !strings.Contains(w.Body.String(), `"reason":"fidelity_loss"`) || !strings.Contains(w.Body.String(), `"code":"pivot_fields"`) {
		t.Fatalf("loss reply %d %s", w.Code, w.Body)
	}
	if got := craftNativeChatReason(422, w.Body.Bytes(), craftNativeAgentToolRequest{}, true); got != CraftSheetReasonFidelityLoss {
		t.Fatalf("chat reason %q", got)
	}
	// range.clear is a change command only, with an address.
	if _, err := nativeAgentEngineCommandsFor("gridcraft", json.RawMessage(`[{"command":"range.clear","params":{"range":"B8:B577","extra":1}}]`), false, nativeAgentEngineRules{}); err == nil {
		t.Fatal("unknown parameter admitted")
	}
}

type lossySession struct {
	*fakeSheetSession
	err error
}

func (s *lossySession) Snapshot(context.Context) ([]byte, error) { return nil, s.err }

type lossyEngine struct{ s *lossySession }

func (e *lossyEngine) Open(context.Context, []byte) (CraftNativeSheetSession, func(), error) {
	return e.s, func() {}, nil
}
func (e *lossyEngine) Retain(CraftNativeSheetSession, []byte) {}
func (e *lossyEngine) Discard(CraftNativeSheetSession)        {}
