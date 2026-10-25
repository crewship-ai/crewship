package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestScriptControlReceiptAndOptionalMetadata(t *testing.T) {
	nonce := strings.Repeat("a", 32)
	dir := scriptControlPrefix + nonce
	frame := []byte("crewship-script-created-v1:" + nonce + ":4321\n")
	for _, tc := range []struct {
		name, dir, nonce string
		frame            []byte
		err              error
		owned            bool
	}{
		{"valid", dir, nonce, frame, nil, true},
		{"unknown start", dir, nonce, nil, io.EOF, false},
		{"partial", dir, nonce, frame[:len(frame)-1], io.EOF, false},
		{"sibling", dir, strings.Repeat("b", 32), frame, nil, false},
		{"trailing slash", dir + "/", nonce, frame, nil, false},
		{"fake parent", "/other/crewship-script-" + nonce, nonce, frame, nil, false},
		{"zero pid", dir, nonce, []byte("crewship-script-created-v1:" + nonce + ":0\n"), nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := parseScriptControlReceipt(tc.dir, tc.nonce, tc.frame, tc.err)
			if r.owned != tc.owned || (err == nil) != tc.owned {
				t.Fatalf("receipt: %#v %v", r, err)
			}
			if !r.owned {
				return
			}
			if !r.acceptMetadata([]byte("crewship-script-cleanup-v1:"+nonce+":-\n")) || r.eligible() {
				t.Fatal("unavailable optional metadata must keep ownership without permitting cleanup")
			}
			if !r.acceptMetadata([]byte("crewship-script-cleanup-v1:"+nonce+":4321:7:11\n")) || !r.eligible() {
				t.Fatal("verified metadata was not bound to receipt")
			}
		})
	}
}

type controlFakeDir struct {
	owner, device, inode string
	files                map[string]string
}
type controlFakeRun struct {
	id, dir, pid, device, inode                                                     string
	userStarted, runClosed, groupLive, stopNeeded, stopTerminal, diagnosticTerminal bool
	exit                                                                            int
}
type controlFakeExec struct {
	kind   string
	run    *controlFakeRun
	code   int
	closed bool
}
type controlLifecycleFake struct {
	orchCovContainer
	mu                                 sync.Mutex
	mode                               string
	next                               int
	objects                            map[string]*controlFakeDir
	execs                              map[string]*controlFakeExec
	runs                               []*controlFakeRun
	guardCalls, removeCalls, stopCalls int
	illegal                            string
	prefixDrained                      chan struct{}
	prefixOnce                         sync.Once
	diagnosticEntered                  chan struct{}
	diagnosticOnce                     sync.Once
	diagnosticGate                     chan struct{}
}

func newControlLifecycleFake(mode string) *controlLifecycleFake {
	return &controlLifecycleFake{mode: mode, objects: map[string]*controlFakeDir{
		"/tmp/unrelated-sentinel": {owner: "sibling", device: "7", inode: "9", files: map[string]string{"keep": "unchanged"}},
	}, execs: map[string]*controlFakeExec{}, prefixDrained: make(chan struct{}), diagnosticEntered: make(chan struct{})}
}

type controlFakeReader struct {
	io.Reader
	close func() error
}

func (r controlFakeReader) Close() error { return r.close() }

type controlFakeBlockingReader struct {
	reader io.Reader
	once   *sync.Once
	ready  chan struct{}
}

func (r controlFakeBlockingReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.ready) })
	return r.reader.Read(p)
}

func (f *controlLifecycleFake) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cfg.User != "1001:1001" {
		f.illegal = "wrong helper UID"
	}
	f.next++
	id := fmt.Sprintf("owned-exec-%d", f.next)
	if len(cfg.Cmd) > 7 && cfg.Cmd[0] == "setsid" {
		dir := cfg.Cmd[6]
		run := &controlFakeRun{id: id, dir: dir, pid: fmt.Sprint(4321 + f.next), device: "7", inode: fmt.Sprint(11 + f.next)}
		f.runs = append(f.runs, run)
		if f.mode == "collision" {
			f.objects[dir] = &controlFakeDir{owner: "sibling", device: "7", inode: "99", files: map[string]string{"keep": "collision preserved"}}
			f.execs[id] = &controlFakeExec{kind: "run", run: run, code: 125}
			return f.reader(id, strings.NewReader("mkdir: already exists\n"), nil), nil
		}
		f.objects[dir] = &controlFakeDir{owner: id, device: run.device, inode: run.inode, files: map[string]string{"pid": run.pid, "stderr": "fixture stderr"}}
		run.userStarted = true
		if f.mode == "exec-unknown" {
			return nil, errors.New("remote start outcome unknown")
		}
		metadata := run.pid + ":" + run.device + ":" + run.inode
		if f.mode == "no-metadata" {
			metadata = "-"
		}
		frames := scriptTestFrames(cfg, run.pid, metadata)
		if f.mode == "nonzero" {
			run.exit = 7
		}
		run.groupLive = f.mode == "live-child" || f.mode == "cancel" || f.mode == "stop-unknown"
		f.execs[id] = &controlFakeExec{kind: "run", run: run, code: run.exit}
		if f.mode == "replacement" {
			f.objects[dir+"-moved"] = f.objects[dir]
			f.objects[dir] = &controlFakeDir{owner: "sibling", device: "7", inode: "100", files: map[string]string{"keep": "replacement preserved"}}
		}
		if f.mode == "cancel" || f.mode == "stop-unknown" {
			pr, pw := io.Pipe()
			// No writer goroutine: closing both pipe endpoints joins the reader.
			blocking := controlFakeBlockingReader{reader: pr, once: &f.prefixOnce, ready: f.prefixDrained}
			return f.reader(id, io.MultiReader(strings.NewReader(frames), blocking), func() error { _ = pw.Close(); return pr.Close() }), nil
		}
		return f.reader(id, strings.NewReader(frames+"payload"), nil), nil
	}
	var kind, dir string
	code := 0
	if len(cfg.Cmd) == 6 && cfg.Cmd[0] == "sh" && cfg.Cmd[2] == scriptStopWrapper {
		kind, dir = "stop", cfg.Cmd[4]
	}
	if len(cfg.Cmd) == 5 && cfg.Cmd[0] == "sh" && cfg.Cmd[2] == scriptDiagnosticWrapper {
		kind, dir = "diagnostic", strings.TrimSuffix(cfg.Cmd[4], "/stderr")
	}
	if len(cfg.Cmd) == 12 && cfg.Cmd[0] == "timeout" && cfg.Cmd[6] == scriptCleanupWrapper {
		kind, dir = "cleanup", cfg.Cmd[8]
	}
	var run *controlFakeRun
	for _, candidate := range f.runs {
		if candidate.dir == dir {
			run = candidate
		}
	}
	if kind == "" || run == nil {
		f.illegal = "unowned/unrecognized helper"
		return nil, errors.New(f.illegal)
	}
	if !run.runClosed {
		f.illegal = "helper before run reader close"
	}
	output := ""
	switch kind {
	case "stop":
		f.stopCalls++
		run.stopNeeded = true
		if cfg.Cmd[5] != run.pid {
			f.illegal = "stop identity differs"
		}
		if f.mode == "stop-unknown" {
			code = 1
		} else {
			run.groupLive = false
		}
	case "diagnostic":
		output = "fixture stderr"
	case "cleanup":
		f.guardCalls++
		if run.stopNeeded && !run.stopTerminal || run.exit != 0 && !run.diagnosticTerminal {
			f.illegal = "cleanup before helper inspect/join"
		}
		object := f.objects[dir]
		if object == nil || object.owner != run.id || object.device != cfg.Cmd[10] || object.inode != cfg.Cmd[11] {
			code = 4
			break
		}
		if run.groupLive {
			code = 5
			break
		}
		f.removeCalls++
		if f.mode == "cleanup-partial" {
			delete(object.files, "stderr")
			code = 1
		} else {
			delete(f.objects, dir)
		}
	}
	f.execs[id] = &controlFakeExec{kind: kind, run: run, code: code}
	return f.reader(id, strings.NewReader(output), nil), nil
}

// Called with fake lock held. Each exec receives its own stream and inspect state.
func (f *controlLifecycleFake) reader(id string, reader io.Reader, extraClose func() error) *provider.ExecResult {
	return &provider.ExecResult{ExecID: id, Reader: controlFakeReader{Reader: reader, close: func() error {
		f.mu.Lock()
		record := f.execs[id]
		record.closed = true
		if record.kind == "run" {
			record.run.runClosed = true
		}
		f.mu.Unlock()
		if extraClose != nil {
			return extraClose()
		}
		return nil
	}}}
}
func (f *controlLifecycleFake) ExecInspect(ctx context.Context, id string) (bool, int, error) {
	f.mu.Lock()
	record := f.execs[id]
	if record == nil {
		f.mu.Unlock()
		return false, -1, errors.New("unknown exec identity")
	}
	if !record.closed {
		f.illegal = "inspect before reader joined"
	}
	if record.kind == "run" && f.mode == "inspect-unknown" {
		f.mu.Unlock()
		return false, -1, errors.New("main inspect unknown")
	}
	gate := f.diagnosticGate
	if record.kind == "diagnostic" && gate != nil {
		f.diagnosticOnce.Do(func() { close(f.diagnosticEntered) })
		f.mu.Unlock()
		select {
		case <-gate:
		case <-ctx.Done():
			return false, -1, ctx.Err()
		}
		f.mu.Lock()
	}
	if record.kind == "stop" {
		record.run.stopTerminal = true
	}
	if record.kind == "diagnostic" {
		record.run.diagnosticTerminal = true
	}
	code := record.code
	f.mu.Unlock()
	return false, code, nil
}
func (f *controlLifecycleFake) counts() (owned, files, guards, removes, stops int, illegal string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, object := range f.objects {
		if object.owner != "sibling" {
			owned++
			files += len(object.files)
		}
	}
	if f.objects["/tmp/unrelated-sentinel"].files["keep"] != "unchanged" {
		f.illegal = "sentinel changed"
	}
	return owned, files, f.guardCalls, f.removeCalls, f.stopCalls, f.illegal
}

func TestRunScriptControlLifecycle(t *testing.T) {
	for _, tc := range []struct {
		mode           string
		owned, removes int
		wantErr        bool
	}{
		{"normal", 0, 1, false}, {"nonzero", 0, 1, false}, {"no-metadata", 1, 0, false},
		{"collision", 0, 0, true}, {"exec-unknown", 1, 0, true}, {"live-child", 1, 0, false},
		{"replacement", 1, 0, false}, {"cleanup-partial", 1, 1, false}, {"inspect-unknown", 1, 0, true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			fake := newControlLifecycleFake(tc.mode)
			res, err := newScriptTestRunner(fake).RunScript(context.Background(), ScriptRunRequest{AuthorCrewID: "crew_1", Interpreter: "sh", Path: "/crew/shared/owned.sh"})
			owned, _, _, removes, stops, illegal := fake.counts()
			if owned != tc.owned || removes != tc.removes || illegal != "" || (err != nil) != tc.wantErr {
				t.Fatalf("resources=%d removes=%d illegal=%s res=%#v err=%v", owned, removes, illegal, res, err)
			}
			if !tc.wantErr && (res.Stdout != "payload" || res.ExitCode != fake.runs[0].exit) {
				t.Fatalf("primary result changed: %#v", res)
			}
			if tc.mode == "nonzero" && res.Stderr != "fixture stderr" {
				t.Fatalf("stderr lost: %#v", res)
			}
			if (tc.mode == "collision" || tc.mode == "exec-unknown" || tc.mode == "live-child") && stops != 0 {
				t.Fatal("new signaling authority")
			}
		})
	}
}

func TestRunScriptControlCancellation(t *testing.T) {
	for _, mode := range []string{"cancel", "stop-unknown"} {
		t.Run(mode, func(t *testing.T) {
			fake := newControlLifecycleFake(mode)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				_, err := newScriptTestRunner(fake).RunScript(ctx, ScriptRunRequest{AuthorCrewID: "crew_1", Interpreter: "sh", Path: "/crew/shared/owned.sh"})
				done <- err
			}()
			<-fake.prefixDrained // Receipt and metadata consumed before cancel.
			cancel()
			err := <-done
			owned, _, guards, removes, stops, illegal := fake.counts()
			if err == nil || stops != 1 || illegal != "" {
				t.Fatalf("cancel coordination: %v / %s", err, illegal)
			}
			if mode == "cancel" && (owned != 0 || removes != 1) {
				t.Fatal("confirmed stop did not reclaim")
			}
			if mode == "stop-unknown" && (owned != 1 || guards != 0 || removes != 0) {
				t.Fatal("unknown stop authorized cleanup")
			}
		})
	}
}

func TestRunScriptControlWaitsForDiagnosticHelper(t *testing.T) {
	fake := newControlLifecycleFake("nonzero")
	fake.diagnosticGate = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := newScriptTestRunner(fake).RunScript(context.Background(), ScriptRunRequest{AuthorCrewID: "crew_1", Interpreter: "sh", Path: "/crew/shared/owned.sh"})
		done <- err
	}()
	<-fake.diagnosticEntered
	_, _, guards, removes, _, illegal := fake.counts()
	close(fake.diagnosticGate)
	err := <-done
	if guards != 0 || removes != 0 || illegal != "" || err != nil {
		t.Fatalf("cleanup raced diagnostics: %d %d %s %v", guards, removes, illegal, err)
	}
	owned, _, _, removes, _, illegal := fake.counts()
	if owned != 0 || removes != 1 || illegal != "" {
		t.Fatal("terminal helper did not permit cleanup")
	}
}

func TestRunScriptControlResourceCounts(t *testing.T) {
	for _, n := range []int{1, 100, 1000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			fake := newControlLifecycleFake("normal")
			runner := newScriptTestRunner(fake)
			for i := 0; i < n; i++ {
				if _, err := runner.RunScript(context.Background(), ScriptRunRequest{AuthorCrewID: "crew_1", Interpreter: "sh", Path: "/crew/shared/owned.sh"}); err != nil {
					t.Fatal(err)
				}
			}
			owned, files, guards, removes, _, illegal := fake.counts()
			if owned != 0 || files != 0 || guards != n || removes != n || illegal != "" {
				t.Fatalf("retained dirs/files=%d/%d helpers=%d/%d illegal=%s", owned, files, guards, removes, illegal)
			}
		})
	}
}
