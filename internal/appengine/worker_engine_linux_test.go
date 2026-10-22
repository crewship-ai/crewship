//go:build linux

package appengine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestMain lets the test binary act as the engine sandbox launcher, exactly as
// the worker binary does in its image.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == EngineSandboxFlag {
		err := ExecEngineSandbox(os.Args[2:])
		fmt.Fprintln(os.Stderr, "engine sandbox:", err)
		os.Exit(126)
	}
	os.Exit(m.Run())
}

// fakeCLI writes a shell script that stands in for the pinned CLI. The
// runner always invokes `run --in SRC --out DST --quiet` for an import.
func fakeCLI(t *testing.T, body string) WorkerEngine {
	t.Helper()
	if err := PrepareEngineSandbox(); err != nil {
		t.Skipf("engine sandbox unavailable on this kernel: %v", err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-gridcraft")
	script := "#!/bin/sh\nset -u\nsrc=\"$3\"\ndst=\"$5\"\n" + body + "\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(script))
	return WorkerEngine{Binary: binary, Scratch: t.TempDir(), ReadOnly: []string{dir}, digest: hex.EncodeToString(sum[:])}
}

// sleepMarker returns a unique sleep duration so test processes can be found
// in /proc without matching unrelated host processes.
func sleepMarker(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	marker := fmt.Sprintf("30.%d", 1000000+int(b[0])<<16|int(b[1])<<8|int(b[2]))
	t.Cleanup(func() {
		for _, pid := range processesWithArg(marker) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return marker
}

func processesWithArg(arg string) []int {
	entries, _ := os.ReadDir("/proc")
	var pids []int
	for _, entry := range entries {
		var pid int
		if _, err := fmt.Sscanf(entry.Name(), "%d", &pid); err != nil {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		stat, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if i := strings.LastIndexByte(string(stat), ')'); i > 0 && strings.HasPrefix(string(stat[i+1:]), " Z") {
			continue
		}
		for _, field := range strings.Split(string(cmdline), "\x00") {
			if field == arg {
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

func requireGone(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		pids := processesWithArg(marker)
		if len(pids) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("engine descendant survived the candidate: pids %v", pids)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func blankWorkbook(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/blank-workbook.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func importBlank(t *testing.T, ctx context.Context, engine WorkerEngine) (EngineResult, error) {
	raw := blankWorkbook(t)
	return engine.Run(ctx, raw, Command{Kind: "import", Snapshot: raw})
}

// detachScript double-forks a child that leaves the engine's session and
// process group, and waits until it has done so before reporting success.
func detachScript(marker string) string {
	return `( setsid sh -c 'echo > "$1"; exec sleep ` + marker + `' sh "$TMPDIR/escaped" </dev/null >/dev/null 2>&1 & )
while [ ! -e "$TMPDIR/escaped" ]; do sleep 0.01; done
cp "$src" "$dst"`
}

func TestWorkerEngineDetachedChildFailsClosedAndIsKilled(t *testing.T) {
	marker := sleepMarker(t)
	// Double fork plus setsid: the child leaves the engine's process group and
	// session and is reparented when its parent exits; the CLI reports success.
	engine := fakeCLI(t, detachScript(marker))
	var escapes atomic.Int32
	engine.Escaped = func() { escapes.Add(1) }
	_, err := importBlank(t, t.Context(), engine)
	if !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("escaped engine descendant must fail closed, got %v", err)
	}
	if escapes.Load() != 1 {
		t.Fatalf("worker exit hook called %d times, want 1", escapes.Load())
	}
	requireGone(t, marker)
	if files, _ := filepath.Glob(filepath.Join(engine.Scratch, "workbook-*")); len(files) != 0 {
		t.Fatalf("scratch survived: %v", files)
	}
}

func TestWorkerHandlerQuarantinesAfterEngineEscape(t *testing.T) {
	marker := sleepMarker(t)
	engine := fakeCLI(t, detachScript(marker))
	var escapes atomic.Int32
	engine.Escaped = func() { escapes.Add(1) }
	secret := strings.Repeat("q", 64)
	handler, err := NewWorkerHandler(secret, engine, 1)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"snapshot":` + string(blankWorkbook(t)) + `,"command":{"kind":"import"}}`
	request := func(method, path, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set(workerServiceHeader, secret)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := request(http.MethodPost, "/v1/candidate", body); code != http.StatusServiceUnavailable {
		t.Fatalf("escaping candidate status %d, want 503", code)
	}
	if code := request(http.MethodGet, "/v1/health", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("worker must stay unavailable after an escape, health %d", code)
	}
	if escapes.Load() != 1 {
		t.Fatalf("worker exit hook called %d times, want 1", escapes.Load())
	}
	requireGone(t, marker)
}

func TestWorkerEngineTimeoutKillsWholeProcessGroup(t *testing.T) {
	marker := sleepMarker(t)
	engine := fakeCLI(t, `sleep `+marker+` & sleep `+marker)
	var escapes atomic.Int32
	engine.Escaped = func() { escapes.Add(1) }
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := importBlank(t, ctx, engine)
	if err == nil {
		t.Fatal("timed out engine must fail")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timeout cleanup took %v", elapsed)
	}
	requireGone(t, marker)
	if escapes.Load() != 0 {
		t.Fatalf("in-group processes are not an escape, hook called %d times", escapes.Load())
	}
}

func TestWorkerEngineSuccessReapsInGroupLeftovers(t *testing.T) {
	marker := sleepMarker(t)
	engine := fakeCLI(t, `sleep `+marker+` </dev/null >/dev/null 2>&1 & cp "$src" "$dst"`)
	var escapes atomic.Int32
	engine.Escaped = func() { escapes.Add(1) }
	result, err := importBlank(t, t.Context(), engine)
	if err != nil || len(result.Result) == 0 {
		t.Fatalf("result=%d bytes err=%v", len(result.Result), err)
	}
	requireGone(t, marker)
	if escapes.Load() != 0 {
		t.Fatalf("in-group leftovers are killed, not an escape; hook called %d times", escapes.Load())
	}
}

func TestWorkerEngineCannotReachSecretSocketOrOtherCandidates(t *testing.T) {
	private := t.TempDir()
	secret := filepath.Join(private, "secrets", "service-token")
	socketDir := filepath.Join(private, "worker")
	for _, dir := range []string{filepath.Dir(secret), socketDir} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(secret, []byte(strings.Repeat("k", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	placeholder := filepath.Join(socketDir, "service.sock")
	if err := os.WriteFile(placeholder, nil, 0600); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`
if cat %[1]q >/dev/null 2>&1; then exit 71; fi
if ls %[2]q >/dev/null 2>&1; then exit 72; fi
if cat "$src" >/dev/null 2>&1 && ls "$(dirname "$(dirname "$src")")" >/dev/null 2>&1; then exit 73; fi
if cat /proc/$PPID/environ >/dev/null 2>&1; then exit 74; fi
rm -f %[3]q 2>/dev/null
touch %[2]q/listener 2>/dev/null
echo tampered | tee "$(dirname "$(dirname "$src")")/workbook-other/candidate.json" >/dev/null 2>&1
cp "$src" "$dst"`, secret, socketDir, placeholder)
	engine := fakeCLI(t, body)
	other := filepath.Join(engine.Scratch, "workbook-other")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	otherCandidate := filepath.Join(other, "candidate.json")
	if err := os.WriteFile(otherCandidate, []byte(`{"private":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := importBlank(t, t.Context(), engine); err != nil {
		t.Fatalf("sandboxed engine reached worker-private state or could not use its own scratch: %v", err)
	}
	if _, err := os.Stat(placeholder); err != nil {
		t.Fatalf("engine removed the service socket: %v", err)
	}
	if _, err := os.Stat(filepath.Join(socketDir, "listener")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("engine created a file in the socket directory: %v", err)
	}
	if got, _ := os.ReadFile(otherCandidate); string(got) != `{"private":true}` {
		t.Fatalf("engine tampered with another candidate: %q", got)
	}
}
