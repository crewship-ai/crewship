package craftsession

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/craftengine"
)

// fakeExport stands in for `run --in doc.json --out out.xlsx`: a workbook part naming each sheet
// of doc.json and a sheet part carrying its first formula, or a failure.
func fakeExport(mode string) {
	switch mode {
	case "crash":
		os.Exit(1)
	case "loss": // --fidelity found a loss: says what, writes nothing
		_, _ = os.Stdout.WriteString(`{"parts":[{"code":"pivot_fields","count":120},{"code":"charts","count":1}]}` + "\n")
		os.Exit(3)
	case "silent3": // exit 3 without a fidelity line: not a fidelity answer
		os.Exit(3)
	case "nothing":
		return
	case "garbage":
		_ = os.WriteFile("out.xlsx", []byte("not a workbook"), 0o600)
		return
	case "noworkbook":
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		w, _ := zw.Create("[Content_Types].xml")
		_, _ = w.Write([]byte("<Types/>"))
		_ = zw.Close()
		_ = os.WriteFile("out.xlsx", b.Bytes(), 0o600)
		return
	}
	args, _ := json.Marshal(os.Args[1:])
	_ = os.WriteFile("args.json", args, 0o600)
	raw, _ := os.ReadFile("doc.json")
	var doc struct {
		Sheets []struct {
			Name    string `json:"name"`
			Formula string `json:"formula"`
		} `json:"sheets"`
	}
	_ = json.Unmarshal(raw, &doc)
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	put := func(name, body string) {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	put("[Content_Types].xml", "<Types/>")
	names := ""
	for i, s := range doc.Sheets {
		names += `<sheet name="` + s.Name + `"/>`
		put("xl/worksheets/sheet"+string(rune('1'+i))+".xml", `<worksheet><c><f>`+s.Formula+`</f></c></worksheet>`)
	}
	put("xl/workbook.xml", "<workbook><sheets>"+names+"</sheets></workbook>")
	if mode == "big" {
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: "xl/pad.bin", Method: zip.Store}) // stored: the size counts
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}
	_ = zw.Close()
	_ = os.WriteFile("out.xlsx", b.Bytes(), 0o600)
	if mode == "faithful" {
		_, _ = os.Stdout.WriteString(`{"parts":[]}` + "\n")
	}
}

func exportLauncher(mode string, seen *[]string) Launcher {
	return func(ctx context.Context, dir string, engineArgs []string) (*craftengine.Worker, error) {
		*seen = engineArgs
		exe, _ := os.Executable()
		return craftengine.StartWorker(craftengine.WorkerSpec{Exe: exe, Env: []string{"CRAFTSESSION_FAKE_EXPORT=" + mode}, Application: "gridcraft",
			ModulePath: "/nonexistent", SHA256: strings.Repeat("0", 64), Dir: dir, EngineArgs: engineArgs})
	}
}

func zipPart(t *testing.T, data []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == name {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			return string(b)
		}
	}
	t.Fatalf("no %s", name)
	return ""
}

// ExportXLSX writes the native document for a one-shot engine, has it write the workbook and
// returns those bytes, leaving no files behind.
func TestExportXLSXReturnsTheEngineWorkbook(t *testing.T) {
	root := t.TempDir()
	var args []string
	native := []byte(`{"sheets":[{"name":"Data","formula":""},{"name":"Calc","formula":"SUM(Data!A1:A3)"}]}`)
	out, err := ExportXLSX(context.Background(), exportLauncher("ok", &args), root, native, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"run", "--in", "/work/doc.json", "--quiet", "--out", "/work/out.xlsx"}; !slices.Equal(args, want) {
		t.Fatalf("engine args %q", args)
	}
	if wb := zipPart(t, out, "xl/workbook.xml"); !strings.Contains(wb, `name="Data"`) || !strings.Contains(wb, `name="Calc"`) {
		t.Fatalf("workbook %s", wb)
	}
	if s := zipPart(t, out, "xl/worksheets/sheet2.xml"); !strings.Contains(s, "<f>SUM(Data!A1:A3)</f>") {
		t.Fatalf("sheet %s", s)
	}
	if left, _ := filepath.Glob(filepath.Join(root, "*")); len(left) != 0 {
		t.Fatalf("left behind %v", left)
	}
}

// Anything but a workbook within the limit is refused, never handed out.
func TestExportXLSXRefusesWhatIsNotAWorkbook(t *testing.T) {
	native := []byte(`{"sheets":[{"name":"S","formula":"1+1"}]}`)
	for mode, want := range map[string]error{"crash": ErrExportFailed, "nothing": ErrExportFailed, "garbage": ErrExportFailed, "noworkbook": ErrExportFailed, "big": ErrTooLarge} {
		var args []string
		_, err := ExportXLSX(context.Background(), exportLauncher(mode, &args), t.TempDir(), native, 1024)
		if !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", mode, err, want)
		}
	}
	var args []string
	if _, err := ExportXLSX(context.Background(), exportLauncher("ok", &args), t.TempDir(), nil, 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty document: %v", err)
	}
}

// A faithful export asks the engine to compare (--fidelity): a workbook that would lose parts is
// refused with what and how many, nothing else; a clean one is the engine's workbook, and an
// engine that does not confirm the comparison is no faithful export.
func TestExportXLSXFaithfulRefusesALoss(t *testing.T) {
	native := []byte(`{"sheets":[{"name":"Data","formula":""}]}`)
	var args []string
	out, err := ExportXLSXFaithful(context.Background(), exportLauncher("faithful", &args), t.TempDir(), native, 0)
	if err != nil || !strings.Contains(zipPart(t, out, "xl/workbook.xml"), `name="Data"`) {
		t.Fatalf("clean export %v", err)
	}
	if want := []string{"run", "--in", "/work/doc.json", "--quiet", "--out", "/work/out.xlsx", "--fidelity"}; !slices.Equal(args, want) {
		t.Fatalf("engine args %q", args)
	}
	_, err = ExportXLSXFaithful(context.Background(), exportLauncher("loss", &args), t.TempDir(), native, 0)
	var loss *FidelityLossError
	if !errors.As(err, &loss) || !errors.Is(err, ErrFidelityLoss) || !slices.Equal(loss.Parts, []FidelityPart{{"pivot_fields", 120}, {"charts", 1}}) {
		t.Fatalf("loss %v", err)
	}
	for _, mode := range []string{"ok", "silent3", "crash"} {
		if _, err = ExportXLSXFaithful(context.Background(), exportLauncher(mode, &args), t.TempDir(), native, 0); err == nil || errors.Is(err, ErrFidelityLoss) {
			t.Errorf("%s: %v", mode, err)
		}
	}
}
