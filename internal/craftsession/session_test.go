package craftsession

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/craftengine"
)

// The tests run the test binary as the engine worker: a small fake MCP server
// with a two-cell model (B1 = A1 * 2), so the process boundary, the kill and
// the bounds are real and no compiled engine is needed.
func TestMain(m *testing.M) {
	if os.Getenv("CRAFTSESSION_FAKE_ENGINE") == "1" {
		fakeEngine()
		return
	}
	if mode := os.Getenv("CRAFTSESSION_FAKE_EXPORT"); mode != "" {
		fakeExport(mode)
		return
	}
	if mode := os.Getenv("CRAFTSESSION_FAKE_ONESHOT"); mode != "" {
		fakeOneShot(mode)
		return
	}
	os.Exit(m.Run())
}

func fakeEngine() {
	cells := map[string]float64{"A1": 1}
	slow := false
	mode, recalcs, full := "automatic", 0, 0 // full: whole-workbook recalculations
	failRestore := false
	workbookLoss := false // fake.loss: the next fidelity-checked save would lose a pivot field
	doc := os.Args
	_ = doc
	in := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	reply := func(id json.RawMessage, text string) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": false}})
		out.Write(b)
		out.WriteByte('\n')
		out.Flush()
	}
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string         `json:"name"`
				Args map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &req) != nil {
			continue
		}
		if req.Method == "initialize" {
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": map[string]any{}})
			out.Write(b)
			out.WriteByte('\n')
			out.Flush()
			continue
		}
		a := req.Params.Args
		switch req.Params.Name {
		case "set_cell":
			in := fmt.Sprint(a["input"])
			switch in {
			case "CRASH":
				os.Exit(1)
			case "HANG":
				select {}
			case "SLOWSNAP":
				slow = true
				reply(req.ID, `{"cell":"A1","value":"x"}`)
				continue
			case "BIG":
				reply(req.ID, strings.Repeat("x", 2<<20))
				continue
			}
			if in == "FAIL" {
				b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "refused"}}, "isError": true}})
				out.Write(b)
				out.WriteByte('\n')
				out.Flush()
				continue
			}
			if mode == "automatic" {
				recalcs++
			}
			v, _ := strconv.ParseFloat(in, 64)
			cells["A1"] = v
			cells["B1"] = v * 2
			reply(req.ID, `{"cell":"A1","value":"`+in+`"}`)
		case "read_range":
			reply(req.ID, fmt.Sprintf(`{"range":"A1:B1","sheet":"Sheet1","values":[[%v,%v]]}`, cells["A1"], cells["A1"]*2))
		case "get_cell":
			reply(req.ID, fmt.Sprintf(`{"cell":"B1","input":"=A1*2","formula":"=A1*2","value":%v}`, cells["A1"]*2))
		case "write_range":
			if mode == "automatic" {
				recalcs++ // the engine recalculates what depends on the block
			}
			reply(req.ID, `{"cells":2}`)
		case "execute_command":
			switch a["command"] {
			case "file.options":
				reply(req.ID, `{"calculation":{"mode":"`+mode+`"}}`)
				continue
			case "formulas.calculationOptions":
				p, _ := a["params"].(map[string]any)
				if m, ok := p["mode"].(string); ok {
					if m == "automatic" && failRestore {
						b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "refused"}}, "isError": true}})
						out.Write(b)
						out.WriteByte('\n')
						out.Flush()
						continue
					}
					if m == "automatic" {
						recalcs++
						full++
					}
					mode = m
				}
				reply(req.ID, `{"mode":"`+mode+`"}`)
				continue
			case "formulas.calculateNow":
				recalcs++
				full++
				reply(req.ID, `{"cells":1}`)
				continue
			case "fake.stats":
				reply(req.ID, fmt.Sprintf(`{"mode":%q,"recalcs":%d,"full":%d}`, mode, recalcs, full))
				continue
			case "fake.failrestore":
				failRestore = true
				reply(req.ID, `{}`)
				continue
			case "fake.except":
				mode = "automaticExceptTables"
				reply(req.ID, `{}`)
				continue
			case "manual.start":
				mode = "manual"
				reply(req.ID, `{}`)
				continue
			case "sheet.read":
				p, _ := a["params"].(map[string]any)
				if p["formulas"] == true {
					reply(req.ID, `{"range":"A1:B1","sheet":"Sheet1","values":[["1","=A1*2"]]}`)
				} else {
					reply(req.ID, `{"range":"A1:B1","sheet":"Sheet1","values":[[1,2]]}`)
				}
				continue
			case "formulas.traceDependents":
				reply(req.ID, `[{"cell":"B1","sheet":"Sheet1"},{"cell":"C1","sheet":"Other"}]`)
				continue
			case "fake.loss":
				workbookLoss = true
				reply(req.ID, `{}`)
				continue
			case "file.saveBytes":
				p, _ := a["params"].(map[string]any)
				if f, _ := p["format"].(string); f == "xlsx" || f == "xlsm" {
					if p["fidelity"] != true {
						reply(req.ID, `{"error":"fidelity not asked"}`)
					} else if workbookLoss {
						reply(req.ID, `{"format":"`+f+`","parts":[{"code":"pivot_fields","count":2}]}`)
					} else {
						// The workbook names the file the engine opened and the current A1.
						body := "PK\x03\x04 " + strings.Join(os.Args[slices.Index(os.Args, "--")+1:], " ") + " A1=" + strconv.FormatFloat(cells["A1"], 'f', -1, 64)
						reply(req.ID, `{"base64":"`+base64.StdEncoding.EncodeToString([]byte(body))+`","format":"`+f+`","parts":[]}`)
					}
					continue
				}
			case "document.inspect":
				reply(req.ID, `{"sheets":[{"name":"Data","used_range":"A1:L14001"},{"name":"O'Brien","used_range":"A1:B2"}]}`)
				continue
			}
			if slow {
				time.Sleep(500 * time.Millisecond)
			}
			doc, _ := json.Marshal(cells)
			pretty := strings.ReplaceAll(string(doc), ",", ",\n ")
			reply(req.ID, `{"base64":"`+base64.StdEncoding.EncodeToString([]byte(pretty))+`"}`)
		default:
			reply(req.ID, `{}`)
		}
	}
}

var launches atomic.Int32

func fakeLauncher(maxLine int) Launcher {
	return func(ctx context.Context, dir string, engineArgs []string) (*craftengine.Worker, error) {
		launches.Add(1)
		exe, _ := os.Executable()
		return craftengine.StartWorker(craftengine.WorkerSpec{Exe: exe, Env: []string{"CRAFTSESSION_FAKE_ENGINE=1"}, Application: "gridcraft",
			ModulePath: "/nonexistent", SHA256: strings.Repeat("0", 64), Dir: dir, EngineArgs: engineArgs, MaxLineBytes: maxLine})
	}
}

func newMgr(t *testing.T, mutate func(*Config)) *Manager {
	t.Helper()
	cfg := Config{Launch: fakeLauncher(1 << 20), TempRoot: t.TempDir(), MaxSessions: 2, IdleTTL: time.Minute, OpenTimeout: 20 * time.Second}
	if mutate != nil {
		mutate(&cfg)
	}
	m := NewManager(cfg)
	t.Cleanup(m.Close)
	return m
}

func doc(string) func() ([]byte, error) {
	return func() ([]byte, error) { return []byte(`{"sheets":[]}`), nil }
}

func TestEditRecalculatesDependentsAndSnapshotIsCompact(t *testing.T) {
	m := newMgr(t, nil)
	ctx := context.Background()
	s, release, err := m.Acquire(ctx, "doc1", doc(""))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err = s.SetCell(ctx, "A1", "21"); err != nil {
		t.Fatal(err)
	}
	v, err := s.ReadRange(ctx, "A1:B1")
	if err != nil || len(v.Values) != 1 || v.Values[0][1].(float64) != 42 {
		t.Fatalf("dependent not recalculated: %+v %v", v, err)
	}
	snap, err := s.Snapshot(ctx)
	if err != nil || strings.ContainsAny(string(snap), "\n ") || !json.Valid(snap) {
		t.Fatalf("snapshot %q %v", snap, err)
	}
}

func TestRequestsOutsideTheContractNeverReachTheEngine(t *testing.T) {
	m := newMgr(t, nil)
	ctx := context.Background()
	s, release, _ := m.Acquire(ctx, "doc1", doc(""))
	defer release()
	for _, rng := range []string{"", "A1:ZZZ9999999", "A1:A2\n", "=1+1", "Sheet1!A1:XFD1048576"} {
		if _, err := s.ReadRange(ctx, rng); !errors.Is(err, ErrInvalid) {
			t.Errorf("range %q: %v", rng, err)
		}
	}
	if err := s.SetCell(ctx, "A1", "bad\x00input"); !errors.Is(err, ErrInvalid) {
		t.Errorf("control character: %v", err)
	}
	if err := s.SetCell(ctx, "A1", strings.Repeat("x", MaxInputBytes+1)); !errors.Is(err, ErrInvalid) {
		t.Errorf("long input: %v", err)
	}
	if _, err := s.WriteRange(ctx, "A1:A2", [][]any{{map[string]any{"x": 1}}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("object value: %v", err)
	}
	big := make([][]any, 1)
	big[0] = make([]any, MaxWriteCells+1)
	if _, err := s.WriteRange(ctx, "A1:A2", big); !errors.Is(err, ErrInvalid) {
		t.Errorf("too many cells: %v", err)
	}
	if !s.Alive() {
		t.Fatal("a refused request ended the session")
	}
}

func TestCrashLosesTheSessionButNotTheSavedVersion(t *testing.T) {
	m := newMgr(t, nil)
	ctx := context.Background()
	s, release, _ := m.Acquire(ctx, "doc1", doc(""))
	if err := s.SetCell(ctx, "A1", "CRASH"); !errors.Is(err, ErrLost) {
		t.Fatalf("crash: %v", err)
	}
	release()
	if s.Alive() {
		t.Fatal("session alive after the engine died")
	}
	before := launches.Load()
	s2, release2, err := m.Acquire(ctx, "doc1", doc(""))
	if err != nil {
		t.Fatal(err)
	}
	defer release2()
	if s2 == s || launches.Load() != before+1 {
		t.Fatal("a dead session was handed out again")
	}
	if v, err := s2.ReadRange(ctx, "A1:B1"); err != nil || v.Values[0][0].(float64) != 1 {
		t.Fatalf("reopened session does not hold the saved version: %+v %v", v, err)
	}
}

func TestMissedDeadlineKillsTheEngine(t *testing.T) {
	m := newMgr(t, nil)
	s, release, _ := m.Acquire(context.Background(), "doc1", doc(""))
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := s.SetCell(ctx, "A1", "HANG"); !errors.Is(err, ErrLost) {
		t.Fatalf("hang: %v", err)
	}
	if time.Since(started) > 5*time.Second || s.Alive() {
		t.Fatalf("not killed in time (%s, alive=%v)", time.Since(started), s.Alive())
	}
}

func TestOversizedAnswerIsFatalToTheSession(t *testing.T) {
	m := newMgr(t, func(c *Config) { c.Launch = fakeLauncher(64 << 10) })
	s, release, _ := m.Acquire(context.Background(), "doc1", doc(""))
	defer release()
	if err := s.SetCell(context.Background(), "A1", "BIG"); !errors.Is(err, ErrLost) {
		t.Fatalf("oversized answer: %v", err)
	}
}

func TestOneSessionPerKeyAndASlotIsFreedForTheLeastRecentlyUsed(t *testing.T) {
	m := newMgr(t, nil)
	ctx := context.Background()
	a, ra, _ := m.Acquire(ctx, "a", doc(""))
	a2, ra2, _ := m.Acquire(ctx, "a", doc(""))
	if a != a2 {
		t.Fatal("two sessions for one document")
	}
	ra2()
	b, rb, _ := m.Acquire(ctx, "b", doc(""))
	// both slots in use: a third document must wait for one to be given up
	if _, _, err := m.Acquire(ctx, "c", doc("")); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	ra()
	time.Sleep(10 * time.Millisecond)
	c, rc, err := m.Acquire(ctx, "c", doc(""))
	if err != nil {
		t.Fatalf("a freed slot was not reused: %v", err)
	}
	if a.Alive() && a != c {
		deadline := time.Now().Add(5 * time.Second)
		for a.Alive() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if a.Alive() || !b.Alive() {
		t.Fatalf("wrong session evicted: a alive=%v b alive=%v", a.Alive(), b.Alive())
	}
	rb()
	rc()
}

func TestIdleSessionIsClosedAndItsDirectoryRemoved(t *testing.T) {
	root := t.TempDir()
	m := newMgr(t, func(c *Config) { c.IdleTTL = 200 * time.Millisecond; c.TempRoot = root })
	s, release, err := m.Acquire(t.Context(), "doc1", doc(""))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("expected one live session directory in the configured root, got %v (%v)", entries, err)
	}
	release()

	// Worker.Done can close before Session.Close finishes removing the
	// private work directory. Wait for both observable postconditions.
	deadline := time.Now().Add(10 * time.Second)
	for {
		entries, err = os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		if !s.Alive() && len(entries) == 0 {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("idle session cleanup did not finish: alive=%v, directory entries=%v", s.Alive(), entries)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestConcurrentAcquireOpensOnce(t *testing.T) {
	m := newMgr(t, nil)
	before := launches.Load()
	done := make(chan *Session, 6)
	for i := 0; i < 6; i++ {
		go func() {
			s, rel, err := m.Acquire(context.Background(), "same", doc(""))
			if err != nil {
				done <- nil
				return
			}
			defer rel()
			done <- s
		}()
	}
	var first *Session
	for i := 0; i < 6; i++ {
		s := <-done
		if s == nil {
			t.Fatal("acquire failed")
		}
		if first == nil {
			first = s
		} else if s != first {
			t.Fatal("two sessions for one key")
		}
	}
	if launches.Load() != before+1 {
		t.Fatalf("opened %d times", launches.Load()-before)
	}
}

func TestFormulasAreRefusedUnlessAPolicyAllowsThem(t *testing.T) {
	ctx := context.Background()
	m := newMgr(t, nil)
	s, release, _ := m.Acquire(ctx, "doc1", doc(""))
	defer release()
	for _, in := range []string{"=1+1", "=WEBSERVICE(\"http://x\")", "@SUM(A1)", "+SUM(A1)", "-SUM(A1)"} {
		if err := s.SetCell(ctx, "A1", in); !errors.Is(err, ErrPolicy) {
			t.Errorf("%q: %v", in, err)
		}
	}
	if _, err := s.WriteRange(ctx, "A1:A2", [][]any{{"=A1+1"}}); !errors.Is(err, ErrPolicy) {
		t.Errorf("formula in a block: %v", err)
	}
	for _, in := range []string{"12", "-3.5", "+7", "plain text", ""} {
		if err := s.SetCell(ctx, "A1", in); err != nil {
			t.Errorf("%q refused: %v", in, err)
		}
	}
	allowSum := func(in string) error {
		if strings.HasPrefix(in, "=SUM(") {
			return nil
		}
		return ErrPolicy
	}
	m2 := newMgr(t, func(c *Config) { c.InputPolicy = allowSum })
	s2, release2, _ := m2.Acquire(ctx, "doc2", doc(""))
	defer release2()
	if err := s2.SetCell(ctx, "A1", "=SUM(B1:B2)"); err != nil {
		t.Errorf("allowed formula refused: %v", err)
	}
	if err := s2.SetCell(ctx, "A1", "=HYPERLINK(1)"); !errors.Is(err, ErrPolicy) {
		t.Errorf("function outside the list: %v", err)
	}
}

func TestOnlyOneSnapshotRunsAtATime(t *testing.T) {
	ctx := context.Background()
	m := newMgr(t, nil)
	a, ra, _ := m.Acquire(ctx, "a", doc(""))
	b, rb, _ := m.Acquire(ctx, "b", doc(""))
	defer ra()
	defer rb()
	for _, s := range []*Session{a, b} {
		if err := s.SetCell(ctx, "A1", "SLOWSNAP"); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	done := make(chan error, 2)
	for _, s := range []*Session{a, b} {
		go func() { _, err := s.Snapshot(ctx); done <- err }()
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if took := time.Since(started); took < 950*time.Millisecond {
		t.Fatalf("two snapshots overlapped (%s)", took)
	}
	// a caller that cannot wait is told so instead of queueing a second copy
	c := make(chan error, 1)
	go func() { _, err := a.Snapshot(ctx); c <- err }()
	time.Sleep(100 * time.Millisecond)
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := b.Snapshot(short); !errors.Is(err, ErrSnapshotBusy) {
		t.Fatalf("waiting snapshot: %v", err)
	}
	<-c
}

func TestCompactJSONKeepsStringsIntact(t *testing.T) {
	var out bytes.Buffer
	in := "{\n  \"a b\": \"x  \\\" y\\\\\",\n  \"n\": [1, 2]\n}"
	if err := compactJSON(&out, strings.NewReader(in)); err != nil || out.String() != `{"a b":"x  \" y\\","n":[1,2]}` {
		t.Fatalf("%q %v", out.String(), err)
	}
}

func TestReadWithFormulasAndSheetList(t *testing.T) {
	ctx := context.Background()
	m := newMgr(t, nil)
	s, release, _ := m.Acquire(ctx, "doc1", doc(""))
	defer release()
	v, err := s.ReadRangeWith(ctx, "A1:B1", ReadOptions{Formulas: true})
	if err != nil || v.Values[0][1] != "=A1*2" {
		t.Fatalf("%+v %v", v, err)
	}
	sheets, err := s.Sheets(ctx)
	if err != nil || len(sheets) != 2 || sheets[0].Name != "Data" || sheets[0].Used != "A1:L14001" || sheets[1].Name != "O'Brien" {
		t.Fatalf("%+v %v", sheets, err)
	}
}

func TestSheetNamesWithAnApostropheAreAddressable(t *testing.T) {
	ctx := context.Background()
	m := newMgr(t, nil)
	s, release, _ := m.Acquire(ctx, "doc1", doc(""))
	defer release()
	for _, rng := range []string{"'O''Brien'!A1:B2", "'My sheet'!C3", "Data!A1"} {
		if _, err := s.ReadRange(ctx, rng); err != nil {
			t.Errorf("%q: %v", rng, err)
		}
	}
	for _, rng := range []string{"'O'Brien'!A1", "'x'!", "'a\nb'!A1"} {
		if _, err := s.ReadRange(ctx, rng); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q accepted: %v", rng, err)
		}
	}
}

func TestRekeyMovesTheWarmSessionToTheNewVersion(t *testing.T) {
	ctx := context.Background()
	m := newMgr(t, nil)
	s, release, _ := m.Acquire(ctx, "v1", doc(""))
	release()
	_, rb, _ := m.Acquire(ctx, "other", doc(""))
	defer rb()
	if m.Rekey("v1", "other") || m.Rekey("nope", "x") || m.Rekey("v1", "v1") {
		t.Fatal("rekey onto a taken or unknown key")
	}
	if !m.Rekey("v1", "v2") {
		t.Fatal("rekey refused")
	}
	before := launches.Load()
	s2, release2, err := m.Acquire(ctx, "v2", doc(""))
	if err != nil || s2 != s || launches.Load() != before {
		t.Fatalf("the warm session was not reused: %v", err)
	}
	release2()
	// the old key is free again and opens a separate session
	s3, release3, err := m.Acquire(ctx, "v1", func() ([]byte, error) { return []byte(`{"sheets":[]}`), nil })
	if err == nil {
		defer release3()
		if s3 == s {
			t.Fatal("old key still maps to the moved session")
		}
	}
	// a session that dies under its new key is forgotten under that key
	if err := s.SetCell(ctx, "A1", "CRASH"); !errors.Is(err, ErrLost) {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	m.mu.Lock()
	_, stale := m.open["v2"]
	m.mu.Unlock()
	if stale {
		t.Fatal("a dead session stays filed under its new key")
	}
}

func TestCodeNamesErrorsWithoutTheEnginesText(t *testing.T) {
	for err, want := range map[error]string{nil: "", ErrInvalid: "invalid", ErrPolicy: "policy", fmt.Errorf("%w: secret path /x", ErrRefused): "refused", ErrLost: "lost", ErrSnapshotBusy: "snapshot_busy", errors.New("x"): "failed"} {
		if got := Code(err); got != want {
			t.Errorf("%v: %q want %q", err, got, want)
		}
	}
}

func TestDependentsListsCellsAcrossSheets(t *testing.T) {
	ctx := context.Background()
	m := newMgr(t, nil)
	s, release, _ := m.Acquire(ctx, "doc1", doc(""))
	defer release()
	refs, cut, err := s.Dependents(ctx, "A1")
	if err != nil || cut || len(refs) != 2 || refs[1].Sheet != "Other" || refs[1].Cell != "C1" {
		t.Fatalf("%+v %v %v", refs, cut, err)
	}
	if _, _, err := s.Dependents(ctx, "not a cell"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func stats(t *testing.T, s *Session) (string, int) {
	t.Helper()
	raw, err := s.call(context.Background(), "execute_command", map[string]any{"command": "fake.stats", "params": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Mode    string
		Recalcs int
	}
	if json.Unmarshal(raw, &out) != nil {
		t.Fatalf("%s", raw)
	}
	return out.Mode, out.Recalcs
}

func scattered(n int) []WriteOp {
	ops := make([]WriteOp, n)
	for i := range ops {
		ops[i] = WriteOp{Cell: fmt.Sprintf("Data!D%d", 2+i*7), Input: strconv.Itoa(100 + i)}
	}
	return ops
}

// One by one the engine recalculates after every write; a batch recalculates once.
func TestApplyRecalculatesOnceForManyScatteredCells(t *testing.T) {
	ctx := context.Background()
	m := newMgr(t, nil)
	s, release, _ := m.Acquire(ctx, "doc1", doc(""))
	defer release()
	for _, op := range scattered(5) {
		if err := s.SetCell(ctx, op.Cell, op.Input); err != nil {
			t.Fatal(err)
		}
	}
	if mode, n := stats(t, s); mode != "automatic" || n != 5 {
		t.Fatalf("one by one: %s %d", mode, n)
	}
	applied, err := s.Apply(ctx, append(scattered(64), WriteOp{Range: "Data!E2:E3", Values: [][]any{{1}, {2}}}))
	if err != nil || applied != 65 {
		t.Fatalf("apply: %d %v", applied, err)
	}
	if mode, n := stats(t, s); mode != "automatic" || n != 6 {
		t.Fatalf("a batch of 65 writes cost %d recalculations after the 5 single ones (mode %s), want exactly 1 more", n-5, mode)
	}
}

func TestApplyRefusesBeforeTouchingTheEngineAndKeepsTheModeOnFailure(t *testing.T) {
	ctx := context.Background()
	m := newMgr(t, nil)
	s, release, _ := m.Acquire(ctx, "doc1", doc(""))
	defer release()
	for name, ops := range map[string][]WriteOp{
		"empty":         nil,
		"formula":       {{Cell: "A1", Input: "1"}, {Cell: "A2", Input: "=1+1"}},
		"bad cell":      {{Cell: "nope", Input: "1"}},
		"both shapes":   {{Cell: "A1", Input: "1", Range: "A1:A2", Values: [][]any{{1}}}},
		"too many ops":  make([]WriteOp, MaxBatchOps+1),
		"object value":  {{Range: "A1:A2", Values: [][]any{{map[string]any{"x": 1}}}}},
		"control chars": {{Cell: "A1", Input: "a\x00b"}},
	} {
		if _, err := s.Apply(ctx, ops); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if mode, n := stats(t, s); mode != "automatic" || n != 0 {
		t.Fatalf("a refused batch touched the engine: %s %d", mode, n)
	}
	// the engine refuses the third write: the first two are reported and the session is DISCARDED
	// (no recalculation is spent restoring a copy nobody will save)
	applied, err := s.Apply(ctx, []WriteOp{{Cell: "A1", Input: "1"}, {Cell: "A2", Input: "2"}, {Cell: "A3", Input: "FAIL"}, {Cell: "A4", Input: "4"}})
	if !errors.Is(err, ErrRefused) || applied != 2 {
		t.Fatalf("partial failure: %d %v", applied, err)
	}
	if s.Alive() {
		t.Fatal("a session with a half-applied batch reports itself alive")
	}
	for name, call := range map[string]func() error{
		"snapshot": func() error { _, e := s.Snapshot(ctx); return e },
		"read":     func() error { _, e := s.ReadRange(ctx, "A1:B1"); return e },
		"set":      func() error { return s.SetCell(ctx, "A1", "1") },
		"apply":    func() error { _, e := s.Apply(ctx, scattered(1)); return e },
		"sheets":   func() error { _, e := s.Sheets(ctx); return e },
	} {
		if err := call(); !errors.Is(err, ErrDiscarded) {
			t.Errorf("%s on a discarded session: %v", name, err)
		}
	}
	if Code(ErrDiscarded) != "discarded" {
		t.Fatal("error code")
	}
	s2, release2, err := m.Acquire(ctx, "doc1", doc(""))
	if err != nil || s2 == s {
		t.Fatalf("a discarded session was handed out again: %v", err)
	}
	release2()
}

func TestApplyRestoresTheExactAutomaticVariantAndRecalculatesOnce(t *testing.T) {
	ctx := context.Background()
	m := newMgr(t, nil)
	s, release, _ := m.Acquire(ctx, "doc1", doc(""))
	defer release()
	if _, err := s.call(ctx, "execute_command", map[string]any{"command": "fake.except", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if applied, err := s.Apply(ctx, scattered(12)); err != nil || applied != 12 {
		t.Fatalf("%d %v", applied, err)
	}
	if mode, n := stats(t, s); mode != "automaticExceptTables" || n != 1 {
		t.Fatalf("mode %q with %d recalculations (want automaticExceptTables, one calculateNow)", mode, n)
	}
}

func TestApplyFailingTheFinalRestoreDiscardsTheSessionToo(t *testing.T) {
	ctx := context.Background()
	m := newMgr(t, nil)
	s, release, _ := m.Acquire(ctx, "doc1", doc(""))
	defer release()
	if _, err := s.call(ctx, "execute_command", map[string]any{"command": "fake.failrestore", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	applied, err := s.Apply(ctx, scattered(3))
	if err == nil || applied != 3 {
		t.Fatalf("%d %v", applied, err)
	}
	if s.Alive() {
		t.Fatal("a session stuck in manual calculation reports itself alive")
	}
	if _, err := s.Snapshot(ctx); !errors.Is(err, ErrDiscarded) {
		t.Fatalf("snapshot after a failed restore: %v", err)
	}
}

func TestApplyLeavesAManualWorkbookManualAndRecalculatesOnce(t *testing.T) {
	ctx := context.Background()
	m := newMgr(t, nil)
	s, release, _ := m.Acquire(ctx, "doc1", doc(""))
	defer release()
	if _, err := s.call(ctx, "execute_command", map[string]any{"command": "manual.start", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if applied, err := s.Apply(ctx, scattered(10)); err != nil || applied != 10 {
		t.Fatalf("%d %v", applied, err)
	}
	if mode, n := stats(t, s); mode != "manual" || n != 1 {
		t.Fatalf("manual workbook: mode %s, %d recalculations (want manual, 1)", mode, n)
	}
}

func fullRecalcs(t *testing.T, s *Session) int {
	t.Helper()
	raw, err := s.call(context.Background(), "execute_command", map[string]any{"command": "fake.stats", "params": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Full int }
	if json.Unmarshal(raw, &out) != nil {
		t.Fatalf("%s", raw)
	}
	return out.Full
}

// One write in an automatic workbook goes straight to the engine, which recalculates only what
// depends on it; switching to manual and back would recalculate the whole workbook (on the
// client's 170 000 formula workbook 28.6 s instead of 0.01 s).
func TestApplyOfOneWriteInAnAutomaticWorkbookRecalculatesOnlyItsDependents(t *testing.T) {
	ctx := context.Background()
	m := newMgr(t, nil)
	s, release, _ := m.Acquire(ctx, "doc1", doc(""))
	defer release()
	for _, ops := range [][]WriteOp{{{Cell: "A1", Input: "3"}}, {{Range: "Data!E2:E3", Values: [][]any{{1}, {2}}}}} {
		if applied, err := s.Apply(ctx, ops); err != nil || applied != 1 {
			t.Fatalf("%d %v", applied, err)
		}
	}
	if mode, n := stats(t, s); mode != "automatic" || n != 2 || fullRecalcs(t, s) != 0 {
		t.Fatalf("mode %s, %d recalculations, %d of the whole workbook (want automatic, one per write, none of the whole workbook)", mode, n, fullRecalcs(t, s))
	}
}

// A Sheet kept as its xlsx file opens in the engine as that file, and its snapshot is the engine's
// fidelity-checked workbook: a save that would lose parts is a *FidelityLossError, never bytes,
// and the session stays open with the change.
func TestWorkbookSessionSavesTheFileWithTheFidelityCheck(t *testing.T) {
	m := newMgr(t, nil)
	ctx := context.Background()
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for name, body := range map[string]string{
		"[Content_Types].xml": `<Types><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/></Types>`,
		"xl/workbook.xml":     "<workbook/>",
	} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	original := zb.Bytes()
	s, release, err := m.Acquire(ctx, "wb1", func() ([]byte, error) { return original, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if s.Format() != "xlsx" {
		t.Fatalf("format %q", s.Format())
	}
	if err = s.SetCell(ctx, "A1", "21"); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(ctx)
	if err != nil || string(snap) != "PK\x03\x04 mcp --in "+craftengine.GuestRoot+"/doc.xlsx A1=21" {
		t.Fatalf("snapshot %q %v", snap, err)
	}
	if _, err = s.call(ctx, "execute_command", map[string]any{"command": "fake.loss"}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Snapshot(ctx)
	var loss *FidelityLossError
	if !errors.As(err, &loss) || len(loss.Parts) != 1 || loss.Parts[0] != (FidelityPart{"pivot_fields", 2}) {
		t.Fatalf("loss %v", err)
	}
	if !s.Alive() {
		t.Fatal("a refused save closed the session")
	}
}
