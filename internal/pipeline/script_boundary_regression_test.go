package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// This file deliberately uses only runner/provider APIs present on base9cf.
// It can be overlaid alone onto base: RED must be a retained-resource assertion,
// never a missing-symbol compile failure. Fake-created objects are independent
// from receipts, command-looking names, and the stdout the runner receives.
type boundaryObject struct {
	owner, device, inode string
	files                map[string]string
}
type boundaryExec struct {
	kind             string
	closed, terminal bool
	code             int
}
type scriptBoundaryFake struct {
	orchCovContainer
	mu                          sync.Mutex
	mode, dir, nonce            string
	objects                     map[string]*boundaryObject
	execs                       map[string]*boundaryExec
	next, stops, removes        int
	illegal                     string
	inspectEntered, inspectGate chan struct{}
	inspectOnce                 sync.Once
}

func newScriptBoundaryFake(mode string) *scriptBoundaryFake {
	return &scriptBoundaryFake{mode: mode, objects: map[string]*boundaryObject{"/tmp/sibling-sentinel": {owner: "sibling", device: "7", inode: "9", files: map[string]string{"keep": "untouched"}}}, execs: map[string]*boundaryExec{}, inspectEntered: make(chan struct{}), inspectGate: make(chan struct{})}
}

var errBoundaryTransport = errors.New("one-shot metadata transport failure")

type boundaryOneShot struct {
	data []byte
	err  error
}

func (r *boundaryOneShot) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		err := r.err
		r.err = nil
		return n, err
	}
	return n, nil
}

type boundaryReader struct {
	io.Reader
	close func() error
}

func (r boundaryReader) Close() error { return r.close() }
func (f *scriptBoundaryFake) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cfg.User != "1001:1001" {
		f.illegal = "UID changed"
	}
	f.next++
	id := fmt.Sprintf("boundary-%d", f.next)
	kind := ""
	code := 0
	var reader io.Reader = strings.NewReader("")
	if len(cfg.Cmd) > 6 && cfg.Cmd[0] == "setsid" {
		kind = "main"
		f.dir = cfg.Cmd[6]
		// Old wrapper receives argv immediately after dir; new wrapper has a nonce.
		framed := len(cfg.Cmd) > 7 && len(cfg.Cmd[7]) == 32 && strings.Contains(cfg.Cmd[4], "crewship-script-created-v1")
		if framed {
			f.nonce = cfg.Cmd[7]
		}
		if f.mode == "collision" {
			f.objects[f.dir] = &boundaryObject{owner: "sibling", device: "7", inode: "99", files: map[string]string{"keep": "untouched"}}
			reader = strings.NewReader("mkdir: exists\n")
			code = 125
		} else {
			f.objects[f.dir] = &boundaryObject{owner: "invocation", device: "7", inode: "11", files: map[string]string{"pid": "4321", "stderr": ""}}
			payload := "payload"
			if framed {
				first := "crewship-script-created-v1:" + f.nonce + ":4321\n"
				metadata := "crewship-script-cleanup-v1:" + f.nonce + ":4321:7:11\n"
				switch f.mode {
				case "metadata-split", "metadata-stop-unknown":
					reader = io.MultiReader(strings.NewReader(first), &boundaryOneShot{data: []byte(metadata[:len(metadata)-4]), err: errBoundaryTransport})
				case "metadata-buffered":
					reader = &boundaryOneShot{data: []byte(first + metadata[:len(metadata)-4]), err: errBoundaryTransport}
				default:
					reader = strings.NewReader(first + metadata + payload)
				}
			} else {
				reader = strings.NewReader(payload)
			}
		}
	} else if len(cfg.Cmd) == 6 && cfg.Cmd[0] == "sh" && strings.Contains(cfg.Cmd[2], "/bin/kill -KILL --") {
		kind = "stop"
		f.stops++
		if cfg.Cmd[4] != f.dir || cfg.Cmd[5] != "4321" {
			f.illegal = "stop identity changed"
		}
		if f.mode == "metadata-stop-unknown" {
			code = 1
		}
	} else if len(cfg.Cmd) == 12 && cfg.Cmd[0] == "timeout" && strings.Contains(cfg.Cmd[6], `exec rm -rf -- "$control"`) {
		kind = "cleanup"
		object := f.objects[cfg.Cmd[8]]
		for _, e := range f.execs {
			if !e.closed || !e.terminal {
				f.illegal = "cleanup before consumer/helper terminal"
			}
		}
		if object == nil || object.owner != "invocation" || cfg.Cmd[8] != f.dir || cfg.Cmd[9] != "4321" || object.device != cfg.Cmd[10] || object.inode != cfg.Cmd[11] {
			code = 4
		} else {
			delete(f.objects, cfg.Cmd[8])
			f.removes++
		}
	} else if len(cfg.Cmd) >= 3 && cfg.Cmd[0] == "sh" && strings.Contains(cfg.Cmd[2], "cat") {
		kind = "diagnostic"
	} else {
		f.illegal = "unexpected helper"
		return nil, errors.New(f.illegal)
	}
	f.execs[id] = &boundaryExec{kind: kind, code: code}
	return &provider.ExecResult{ExecID: id, Reader: boundaryReader{Reader: reader, close: func() error { f.mu.Lock(); defer f.mu.Unlock(); f.execs[id].closed = true; return nil }}}, nil
}
func (f *scriptBoundaryFake) ExecInspect(ctx context.Context, id string) (bool, int, error) {
	f.mu.Lock()
	record := f.execs[id]
	if record == nil {
		f.mu.Unlock()
		return false, -1, errors.New("unknown exec")
	}
	kind := record.kind
	if !record.closed {
		f.illegal = "inspect before reader close"
	}
	if kind == "main" && f.mode == "inspect-cancel" {
		f.inspectOnce.Do(func() { close(f.inspectEntered) })
		f.mu.Unlock()
		select {
		case <-f.inspectGate:
		case <-ctx.Done():
		}
		if err := ctx.Err(); err != nil {
			return false, -1, err
		}
		f.mu.Lock()
	}
	record.terminal = true
	code := record.code
	f.mu.Unlock()
	return false, code, nil
}
func (f *scriptBoundaryFake) state() (int, int, int, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owned := 0
	for _, o := range f.objects {
		if o.owner == "invocation" {
			owned++
		}
	}
	if f.objects["/tmp/sibling-sentinel"].files["keep"] != "untouched" {
		f.illegal = "sibling modified"
	}
	return owned, f.removes, f.stops, f.illegal
}
func boundaryRequest() ScriptRunRequest {
	return ScriptRunRequest{AuthorCrewID: "crew_1", Interpreter: "sh", Path: "/crew/shared/fixture.sh"}
}

func TestG11StandaloneLeakReclamation(t *testing.T) {
	fake := newScriptBoundaryFake("normal")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := newScriptTestRunner(fake).RunScript(ctx, boundaryRequest())
	if err != nil || res.Stdout != "payload" || res.ExitCode != 0 {
		t.Fatalf("primary result changed: %#v %v", res, err)
	}
	owned, removes, stops, illegal := fake.state()
	if owned != 0 || removes != 1 || stops != 0 || illegal != "" {
		t.Fatalf("completed invocation retained controls: owned=%d removes=%d stops=%d illegal=%s", owned, removes, stops, illegal)
	}
}
func TestG11MetadataTransportErrorNeverSucceeds(t *testing.T) {
	for _, mode := range []string{"metadata-split", "metadata-buffered", "metadata-stop-unknown"} {
		t.Run(mode, func(t *testing.T) {
			fake := newScriptBoundaryFake(mode)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			res, err := newScriptTestRunner(fake).RunScript(ctx, boundaryRequest())
			owned, removes, stops, illegal := fake.state()
			if err == nil || res.ExitCode != -1 || stops != 1 || removes != 0 || owned != 1 || illegal != "" {
				t.Fatalf("transport error became success/unauthorized cleanup: %#v %v owned=%d removes=%d stops=%d illegal=%s", res, err, owned, removes, stops, illegal)
			}
		})
	}
}
func TestG11CancellationDuringPrimaryInspect(t *testing.T) {
	fake := newScriptBoundaryFake("inspect-cancel")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type result struct {
		res ScriptRunResult
		err error
	}
	done := make(chan result, 1)
	joined := false
	var release sync.Once
	defer func() {
		cancel()
		release.Do(func() { close(fake.inspectGate) })
		if !joined {
			select {
			case <-done:
			case <-time.After(6 * time.Second):
				t.Error("runner failed to join during fixture cleanup")
			}
		}
	}()
	go func() {
		res, err := newScriptTestRunner(fake).RunScript(ctx, boundaryRequest())
		done <- result{res, err}
	}()
	select {
	case <-fake.inspectEntered:
	case <-ctx.Done():
		t.Fatal("primary inspect never entered")
	}
	cancel()
	release.Do(func() { close(fake.inspectGate) })
	// Join the runner even on the failure path; its finalization is separately bounded.
	var got result
	select {
	case got = <-done:
		joined = true
	case <-time.After(6 * time.Second):
		t.Fatal("runner did not join after cancellation")
	}
	owned, removes, stops, illegal := fake.state()
	if got.err == nil || got.res.ExitCode != -1 || stops != 1 || removes != 0 || owned != 1 || illegal != "" {
		t.Fatalf("inspect cancellation became success: %#v %v owned=%d removes=%d stops=%d illegal=%s", got.res, got.err, owned, removes, stops, illegal)
	}
}

func TestG11StandaloneCollisionHasNoAuthority(t *testing.T) {
	fake := newScriptBoundaryFake("collision")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := newScriptTestRunner(fake).RunScript(ctx, boundaryRequest())
	owned, removes, stops, illegal := fake.state()
	if err == nil || res.ExitCode != -1 || owned != 0 || removes != 0 || stops != 0 || illegal != "" {
		t.Fatalf("collision authorized operation: %#v %v %d/%d/%d %s", res, err, owned, removes, stops, illegal)
	}
	if fake.objects[fake.dir].files["keep"] != "untouched" {
		t.Fatal("collision object changed")
	}
}
