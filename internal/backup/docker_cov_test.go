package backup

// Coverage tests for docker.go — MobyDockerOps against a fake Docker
// daemon served over httptest (no real daemon needed), plus the pure
// helpers WithPaused / shouldExclude / RepackTar.

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// fakeDaemon is a minimal Docker Engine API stand-in. Each endpoint's
// behaviour is configurable per test; requests are recorded so tests
// can assert on what the client actually sent.
type fakeDaemon struct {
	mu sync.Mutex

	// pauseStatus / pauseMsg control POST /containers/{id}/pause.
	pauseStatus int
	pauseMsg    string
	// unpauseStatus / unpauseMsg control POST /containers/{id}/unpause.
	unpauseStatus int
	unpauseMsg    string
	// inspectStatus / inspectMsg control GET /containers/{id}/json.
	inspectStatus int
	inspectMsg    string
	// archiveGetStatus + archiveGetBody control GET /containers/{id}/archive.
	archiveGetStatus int
	archiveGetBody   []byte
	// archivePutStatus controls PUT /containers/{id}/archive; received
	// body is recorded in archivePutBody.
	archivePutStatus int
	archivePutBody   []byte
	// execCreateStatus controls POST /containers/{id}/exec. The decoded
	// exec request is recorded.
	execCreateStatus int
	execUser         string
	execCmd          []string
	// execStartHijack: when false, /exec/{id}/start responds with
	// execStartStatus instead of upgrading — exercising the attach
	// error path.
	execStartHijack bool
	execStartStatus int
	// execStdout is written (stdcopy-framed) after stdin is drained.
	execStdout []byte
	// execReadStdin: drain the hijacked conn before responding (true
	// for tar-pipe flows; false for plain Exec).
	execReadStdin bool
	execStdin     []byte
	// execInspectStatus + execExitCode control GET /exec/{id}/json.
	execInspectStatus int
	execExitCode      int
}

func newFakeDaemon() *fakeDaemon {
	return &fakeDaemon{
		pauseStatus:       http.StatusNoContent,
		unpauseStatus:     http.StatusNoContent,
		inspectStatus:     http.StatusOK,
		archiveGetStatus:  http.StatusOK,
		archivePutStatus:  http.StatusOK,
		execCreateStatus:  http.StatusCreated,
		execStartHijack:   true,
		execStartStatus:   http.StatusInternalServerError,
		execInspectStatus: http.StatusOK,
	}
}

var versionPrefixRe = regexp.MustCompile(`^/v[0-9.]+`)

func writeDaemonErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

func (d *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := versionPrefixRe.ReplaceAllString(r.URL.Path, "")
	switch {
	case strings.HasSuffix(path, "/pause"):
		if d.pauseStatus >= 400 {
			writeDaemonErr(w, d.pauseStatus, d.pauseMsg)
			return
		}
		w.WriteHeader(d.pauseStatus)
	case strings.HasSuffix(path, "/unpause"):
		if d.unpauseStatus >= 400 {
			writeDaemonErr(w, d.unpauseStatus, d.unpauseMsg)
			return
		}
		w.WriteHeader(d.unpauseStatus)
	case strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/json"):
		if d.inspectStatus >= 400 {
			writeDaemonErr(w, d.inspectStatus, d.inspectMsg)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Id":"abc123","Name":"/c1","State":{"Running":true}}`))
	case strings.HasSuffix(path, "/archive") && r.Method == http.MethodGet:
		if d.archiveGetStatus >= 400 {
			writeDaemonErr(w, d.archiveGetStatus, "Could not find the file")
			return
		}
		stat, _ := json.Marshal(map[string]any{"name": "f", "size": len(d.archiveGetBody)})
		w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(stat))
		w.Header().Set("Content-Type", "application/x-tar")
		_, _ = w.Write(d.archiveGetBody)
	case strings.HasSuffix(path, "/archive") && r.Method == http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		d.mu.Lock()
		d.archivePutBody = body
		d.mu.Unlock()
		if d.archivePutStatus >= 400 {
			writeDaemonErr(w, d.archivePutStatus, "rootfs is marked read-only")
			return
		}
		w.WriteHeader(d.archivePutStatus)
	case strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/exec"):
		var req struct {
			User string   `json:"User"`
			Cmd  []string `json:"Cmd"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		d.mu.Lock()
		d.execUser = req.User
		d.execCmd = req.Cmd
		d.mu.Unlock()
		if d.execCreateStatus >= 400 {
			writeDaemonErr(w, d.execCreateStatus, "exec create refused")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(d.execCreateStatus)
		_, _ = w.Write([]byte(`{"Id":"exec123"}`))
	case strings.HasPrefix(path, "/exec/") && strings.HasSuffix(path, "/start"):
		if !d.execStartHijack {
			writeDaemonErr(w, d.execStartStatus, "exec start refused")
			return
		}
		_, _ = io.ReadAll(r.Body)
		hj, ok := w.(http.Hijacker)
		if !ok {
			writeDaemonErr(w, http.StatusInternalServerError, "no hijack")
			return
		}
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = bufrw.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
		_ = bufrw.Flush()
		if d.execReadStdin {
			// Drain stdin until the client half-closes its write side.
			got, _ := io.ReadAll(bufrw.Reader)
			d.mu.Lock()
			d.execStdin = got
			d.mu.Unlock()
		}
		sw := stdcopy.NewStdWriter(conn, stdcopy.Stdout)
		if len(d.execStdout) > 0 {
			_, _ = sw.Write(d.execStdout)
		}
	case strings.HasPrefix(path, "/exec/") && strings.HasSuffix(path, "/json"):
		if d.execInspectStatus >= 400 {
			writeDaemonErr(w, d.execInspectStatus, "exec inspect refused")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"ID":"exec123","Running":false,"ExitCode":%d}`, d.execExitCode)
	default:
		writeDaemonErr(w, http.StatusNotFound, "page not found: "+path)
	}
}

// newMobyOps spins up the fake daemon and returns a MobyDockerOps
// whose client talks to it.
func newMobyOps(t *testing.T, d *fakeDaemon) *MobyDockerOps {
	t.Helper()
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	cli, err := client.NewClientWithOpts(
		client.WithHost("tcp://"+srv.Listener.Addr().String()),
		client.WithVersion("1.47"),
	)
	if err != nil {
		t.Fatalf("new docker client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return &MobyDockerOps{Client: cli}
}

func TestMobyDockerOps_PauseUnpause(t *testing.T) {
	ctx := context.Background()

	t.Run("pause ok", func(t *testing.T) {
		ops := newMobyOps(t, newFakeDaemon())
		if err := ops.Pause(ctx, "c1"); err != nil {
			t.Fatalf("pause: %v", err)
		}
	})
	t.Run("pause already paused is success", func(t *testing.T) {
		d := newFakeDaemon()
		d.pauseStatus = http.StatusConflict
		d.pauseMsg = "Container c1 is already paused"
		ops := newMobyOps(t, d)
		if err := ops.Pause(ctx, "c1"); err != nil {
			t.Fatalf("expected already-paused to be treated as success, got %v", err)
		}
	})
	t.Run("pause other error wrapped", func(t *testing.T) {
		d := newFakeDaemon()
		d.pauseStatus = http.StatusInternalServerError
		d.pauseMsg = "daemon on fire"
		ops := newMobyOps(t, d)
		err := ops.Pause(ctx, "c1")
		if err == nil || !strings.Contains(err.Error(), "docker pause c1") {
			t.Fatalf("expected wrapped pause error, got %v", err)
		}
	})
	t.Run("unpause ok", func(t *testing.T) {
		ops := newMobyOps(t, newFakeDaemon())
		if err := ops.Unpause(ctx, "c1"); err != nil {
			t.Fatalf("unpause: %v", err)
		}
	})
	t.Run("unpause not paused is success", func(t *testing.T) {
		d := newFakeDaemon()
		d.unpauseStatus = http.StatusConflict
		d.unpauseMsg = "Container c1 is not paused"
		ops := newMobyOps(t, d)
		if err := ops.Unpause(ctx, "c1"); err != nil {
			t.Fatalf("expected not-paused to be treated as success, got %v", err)
		}
	})
	t.Run("unpause other error wrapped", func(t *testing.T) {
		d := newFakeDaemon()
		d.unpauseStatus = http.StatusInternalServerError
		d.unpauseMsg = "daemon on fire"
		ops := newMobyOps(t, d)
		err := ops.Unpause(ctx, "c1")
		if err == nil || !strings.Contains(err.Error(), "docker unpause c1") {
			t.Fatalf("expected wrapped unpause error, got %v", err)
		}
	})
}

func TestMobyDockerOps_CopyFrom(t *testing.T) {
	ctx := context.Background()

	t.Run("streams body", func(t *testing.T) {
		d := newFakeDaemon()
		d.archiveGetBody = []byte("tar-bytes-here")
		ops := newMobyOps(t, d)
		rc, err := ops.CopyFrom(ctx, "c1", "/workspace")
		if err != nil {
			t.Fatalf("CopyFrom: %v", err)
		}
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != "tar-bytes-here" {
			t.Errorf("body = %q, want tar-bytes-here", got)
		}
	})
	t.Run("error wrapped with container and path", func(t *testing.T) {
		d := newFakeDaemon()
		d.archiveGetStatus = http.StatusNotFound
		ops := newMobyOps(t, d)
		_, err := ops.CopyFrom(ctx, "c1", "/missing")
		if err == nil || !strings.Contains(err.Error(), "docker cp from c1:/missing") {
			t.Fatalf("expected wrapped CopyFrom error, got %v", err)
		}
	})
}

func TestMobyDockerOps_ContainerExists(t *testing.T) {
	ctx := context.Background()

	t.Run("exists", func(t *testing.T) {
		ops := newMobyOps(t, newFakeDaemon())
		ok, err := ops.ContainerExists(ctx, "c1")
		if err != nil || !ok {
			t.Fatalf("ContainerExists = (%v, %v), want (true, nil)", ok, err)
		}
	})
	t.Run("no such container resolves false nil", func(t *testing.T) {
		d := newFakeDaemon()
		d.inspectStatus = http.StatusNotFound
		d.inspectMsg = "No such container: c1"
		ops := newMobyOps(t, d)
		ok, err := ops.ContainerExists(ctx, "c1")
		if err != nil || ok {
			t.Fatalf("ContainerExists = (%v, %v), want (false, nil)", ok, err)
		}
	})
	t.Run("other error bubbles", func(t *testing.T) {
		d := newFakeDaemon()
		d.inspectStatus = http.StatusInternalServerError
		d.inspectMsg = "permission denied talking to daemon"
		ops := newMobyOps(t, d)
		ok, err := ops.ContainerExists(ctx, "c1")
		if ok || err == nil || !strings.Contains(err.Error(), "docker inspect c1") {
			t.Fatalf("expected wrapped inspect error, got (%v, %v)", ok, err)
		}
	})
}

func TestMobyDockerOps_CopyTo(t *testing.T) {
	ctx := context.Background()

	t.Run("ok and body forwarded", func(t *testing.T) {
		d := newFakeDaemon()
		ops := newMobyOps(t, d)
		if err := ops.CopyTo(ctx, "c1", "/workspace", strings.NewReader("tar-payload")); err != nil {
			t.Fatalf("CopyTo: %v", err)
		}
		d.mu.Lock()
		body := string(d.archivePutBody)
		d.mu.Unlock()
		if body != "tar-payload" {
			t.Errorf("daemon received %q, want tar-payload", body)
		}
	})
	t.Run("error wrapped", func(t *testing.T) {
		d := newFakeDaemon()
		d.archivePutStatus = http.StatusInternalServerError
		ops := newMobyOps(t, d)
		err := ops.CopyTo(ctx, "c1", "/home", strings.NewReader("x"))
		if err == nil || !strings.Contains(err.Error(), "docker cp to c1:/home") {
			t.Fatalf("expected wrapped CopyTo error, got %v", err)
		}
	})
}

func TestMobyDockerOps_CopyToVolumeAndSystem(t *testing.T) {
	ctx := context.Background()

	t.Run("volume streams stdin as agent user", func(t *testing.T) {
		d := newFakeDaemon()
		d.execReadStdin = true
		ops := newMobyOps(t, d)
		if err := ops.CopyToVolume(ctx, "c1", "/home/agent", strings.NewReader("inner-tar")); err != nil {
			t.Fatalf("CopyToVolume: %v", err)
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if string(d.execStdin) != "inner-tar" {
			t.Errorf("exec stdin = %q, want inner-tar", d.execStdin)
		}
		if d.execUser != "1001:1001" {
			t.Errorf("exec user = %q, want 1001:1001", d.execUser)
		}
		wantCmd := []string{"tar", "-x", "--overwrite", "--no-same-owner", "--no-same-permissions", "--touch", "-f", "-", "-C", "/home/agent"}
		if fmt.Sprint(d.execCmd) != fmt.Sprint(wantCmd) {
			t.Errorf("exec cmd = %v, want %v", d.execCmd, wantCmd)
		}
	})
	t.Run("system runs as root", func(t *testing.T) {
		d := newFakeDaemon()
		d.execReadStdin = true
		ops := newMobyOps(t, d)
		if err := ops.CopyToSystem(ctx, "c1", "/var/lib", strings.NewReader("root-tar")); err != nil {
			t.Fatalf("CopyToSystem: %v", err)
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.execUser != "0:0" {
			t.Errorf("exec user = %q, want 0:0", d.execUser)
		}
	})
	t.Run("exec create error", func(t *testing.T) {
		d := newFakeDaemon()
		d.execCreateStatus = http.StatusInternalServerError
		ops := newMobyOps(t, d)
		err := ops.CopyToVolume(ctx, "c1", "/home/agent", strings.NewReader("x"))
		if err == nil || !strings.Contains(err.Error(), "exec-tar create c1:/home/agent") {
			t.Fatalf("expected exec-tar create error, got %v", err)
		}
	})
	t.Run("attach error", func(t *testing.T) {
		d := newFakeDaemon()
		d.execStartHijack = false
		ops := newMobyOps(t, d)
		err := ops.CopyToVolume(ctx, "c1", "/home/agent", strings.NewReader("x"))
		if err == nil || !strings.Contains(err.Error(), "exec-tar attach c1:/home/agent") {
			t.Fatalf("expected exec-tar attach error, got %v", err)
		}
	})
	t.Run("stdin pump error closes conn and surfaces", func(t *testing.T) {
		d := newFakeDaemon()
		d.execReadStdin = true
		ops := newMobyOps(t, d)
		boom := errors.New("source stream torn")
		err := ops.CopyToVolume(ctx, "c1", "/home/agent", io.MultiReader(strings.NewReader("partial"), errReaderCov{boom}))
		if err == nil || !strings.Contains(err.Error(), "exec-tar stdin c1:/home/agent") {
			t.Fatalf("expected exec-tar stdin error, got %v", err)
		}
	})
	t.Run("non-zero exit includes output", func(t *testing.T) {
		d := newFakeDaemon()
		d.execReadStdin = true
		d.execExitCode = 2
		d.execStdout = []byte("tar: Permission denied\n")
		ops := newMobyOps(t, d)
		err := ops.CopyToVolume(ctx, "c1", "/home/agent", strings.NewReader("x"))
		if err == nil || !strings.Contains(err.Error(), "exited 2") || !strings.Contains(err.Error(), "Permission denied") {
			t.Fatalf("expected exit-2 error with output, got %v", err)
		}
	})
	t.Run("exec inspect error", func(t *testing.T) {
		d := newFakeDaemon()
		d.execReadStdin = true
		d.execInspectStatus = http.StatusInternalServerError
		ops := newMobyOps(t, d)
		err := ops.CopyToVolume(ctx, "c1", "/home/agent", strings.NewReader("x"))
		if err == nil || !strings.Contains(err.Error(), "exec-tar inspect c1:/home/agent") {
			t.Fatalf("expected exec-tar inspect error, got %v", err)
		}
	})
}

func TestMobyDockerOps_Exec(t *testing.T) {
	ctx := context.Background()

	t.Run("runs as root and returns output plus exit code", func(t *testing.T) {
		d := newFakeDaemon()
		d.execStdout = []byte("hello from container")
		d.execExitCode = 7
		ops := newMobyOps(t, d)
		code, out, err := ops.Exec(ctx, "c1", []string{"sh", "-c", "exit 7"})
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if code != 7 {
			t.Errorf("exit code = %d, want 7", code)
		}
		if string(out) != "hello from container" {
			t.Errorf("output = %q", out)
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.execUser != "0:0" {
			t.Errorf("exec user = %q, want 0:0", d.execUser)
		}
		if fmt.Sprint(d.execCmd) != fmt.Sprint([]string{"sh", "-c", "exit 7"}) {
			t.Errorf("cmd = %v", d.execCmd)
		}
	})
	t.Run("create error", func(t *testing.T) {
		d := newFakeDaemon()
		d.execCreateStatus = http.StatusInternalServerError
		ops := newMobyOps(t, d)
		code, _, err := ops.Exec(ctx, "c1", []string{"true"})
		if code != -1 || err == nil || !strings.Contains(err.Error(), "exec create c1") {
			t.Fatalf("expected exec create error, got (%d, %v)", code, err)
		}
	})
	t.Run("attach error", func(t *testing.T) {
		d := newFakeDaemon()
		d.execStartHijack = false
		ops := newMobyOps(t, d)
		code, _, err := ops.Exec(ctx, "c1", []string{"true"})
		if code != -1 || err == nil || !strings.Contains(err.Error(), "exec attach c1") {
			t.Fatalf("expected exec attach error, got (%d, %v)", code, err)
		}
	})
	t.Run("inspect error keeps collected output", func(t *testing.T) {
		d := newFakeDaemon()
		d.execStdout = []byte("partial logs")
		d.execInspectStatus = http.StatusInternalServerError
		ops := newMobyOps(t, d)
		code, out, err := ops.Exec(ctx, "c1", []string{"true"})
		if code != -1 || err == nil || !strings.Contains(err.Error(), "exec inspect c1") {
			t.Fatalf("expected exec inspect error, got (%d, %v)", code, err)
		}
		if string(out) != "partial logs" {
			t.Errorf("output = %q, want partial logs", out)
		}
	})
}

// errReaderCov fails with its error on the first Read.
type errReaderCov struct{ err error }

func (e errReaderCov) Read([]byte) (int, error) { return 0, e.err }

// pausableOps is a minimal DockerOps for WithPaused tests.
type pausableOps struct {
	pauseErr   error
	unpauseErr error
	pauses     int
	unpauses   int
}

func (p *pausableOps) Pause(context.Context, string) error   { p.pauses++; return p.pauseErr }
func (p *pausableOps) Unpause(context.Context, string) error { p.unpauses++; return p.unpauseErr }
func (p *pausableOps) CopyFrom(context.Context, string, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (p *pausableOps) CopyTo(context.Context, string, string, io.Reader) error       { return nil }
func (p *pausableOps) CopyToVolume(context.Context, string, string, io.Reader) error { return nil }
func (p *pausableOps) CopyToSystem(context.Context, string, string, io.Reader) error { return nil }
func (p *pausableOps) ContainerExists(context.Context, string) (bool, error)         { return true, nil }
func (p *pausableOps) Exec(context.Context, string, []string) (int, []byte, error) {
	return 0, nil, nil
}

func TestWithPaused_Branches(t *testing.T) {
	ctx := context.Background()

	t.Run("pause error short-circuits", func(t *testing.T) {
		ops := &pausableOps{pauseErr: errors.New("nope")}
		err := WithPaused(ctx, ops, "c1", func() error {
			t.Fatal("fn must not run when pause fails")
			return nil
		})
		if err == nil || !strings.Contains(err.Error(), "nope") {
			t.Fatalf("expected pause error, got %v", err)
		}
		if ops.unpauses != 0 {
			t.Errorf("unpause must not run after failed pause")
		}
	})
	t.Run("fn error wins over unpause error", func(t *testing.T) {
		ops := &pausableOps{unpauseErr: errors.New("unpause broken")}
		fnErr := errors.New("fn failed")
		err := WithPaused(ctx, ops, "c1", func() error { return fnErr })
		if !errors.Is(err, fnErr) {
			t.Fatalf("expected fn error to win, got %v", err)
		}
	})
	t.Run("unpause failure after success surfaces ErrPauseUnpauseLost", func(t *testing.T) {
		ops := &pausableOps{unpauseErr: errors.New("daemon gone")}
		err := WithPaused(ctx, ops, "c1", func() error { return nil })
		if !errors.Is(err, ErrPauseUnpauseLost) {
			t.Fatalf("expected ErrPauseUnpauseLost, got %v", err)
		}
	})
	t.Run("happy path", func(t *testing.T) {
		ops := &pausableOps{}
		ran := false
		if err := WithPaused(ctx, ops, "c1", func() error { ran = true; return nil }); err != nil {
			t.Fatalf("WithPaused: %v", err)
		}
		if !ran || ops.pauses != 1 || ops.unpauses != 1 {
			t.Errorf("ran=%v pauses=%d unpauses=%d", ran, ops.pauses, ops.unpauses)
		}
	})
}

func TestShouldExclude_Patterns(t *testing.T) {
	cases := []struct {
		path     string
		patterns []string
		want     bool
	}{
		{"node_modules", []string{"node_modules/"}, true},                // exact dir, no trailing slash on path
		{"node_modules/react/index.js", []string{"node_modules/"}, true}, // descendant
		{"src/node_modules/x.js", []string{"node_modules/"}, true},       // nested anywhere
		{"a/b/node_modules", []string{"node_modules/"}, false},           // trailing dir without slash nested — not matched by "/pat" contains
		{".cache/mise/trusted", []string{".cache/"}, true},
		{"src/main.go", []string{"node_modules/", ".cache/"}, false},
		{"dump.rdb", []string{"dpkg/"}, false},
		{"exact", []string{"exact"}, true},        // bare pattern, exact match
		{"deep/exact", []string{"exact"}, true},   // bare pattern as component
		{"exactly-not", []string{"exact"}, false}, // prefix only — no match
		{"workspace/file.txt", nil, false},        // empty patterns
		{"a/__pycache__/mod.pyc", volumeExclusions, true},
		{"redis/dump.rdb", varLibExclusions, false},
		{"dpkg/status", varLibExclusions, true},
	}
	for _, tc := range cases {
		if got := shouldExclude(tc.path, tc.patterns); got != tc.want {
			t.Errorf("shouldExclude(%q, %v) = %v, want %v", tc.path, tc.patterns, got, tc.want)
		}
	}
}

// buildDockerStyleTar emits a tar the way Docker CopyFromContainer
// does: a leading wrapper dir entry followed by entries under it.
func buildDockerStyleTar(t *testing.T, wrapper string, entries map[string]string, links map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if wrapper != "" {
		if err := tw.WriteHeader(&tar.Header{Name: wrapper + "/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: time.Unix(0, 0)}); err != nil {
			t.Fatalf("wrapper header: %v", err)
		}
	}
	for name, content := range entries {
		full := name
		if wrapper != "" {
			full = wrapper + "/" + name
		}
		if err := tw.WriteHeader(&tar.Header{Name: full, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content)), ModTime: time.Unix(0, 0)}); err != nil {
			t.Fatalf("header %s: %v", full, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("body %s: %v", full, err)
		}
	}
	for name, target := range links {
		full := name
		if wrapper != "" {
			full = wrapper + "/" + name
		}
		if err := tw.WriteHeader(&tar.Header{Name: full, Typeflag: tar.TypeLink, Linkname: target, Mode: 0o644, ModTime: time.Unix(0, 0)}); err != nil {
			t.Fatalf("link header %s: %v", full, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}

// readTarZst decodes a tar.zst stream into name → (linkname, body).
func readTarZst(t *testing.T, data []byte) map[string][2]string {
	t.Helper()
	tr, err := NewTarZstReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("NewTarZstReader: %v", err)
	}
	defer tr.Close()
	out := map[string][2]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		body, _ := io.ReadAll(tr)
		out[hdr.Name] = [2]string{hdr.Linkname, string(body)}
	}
	return out
}

func TestRepackTar_StripsWrapperAndExcludes(t *testing.T) {
	src := buildDockerStyleTar(t, "agent",
		map[string]string{
			"notes.txt":                    "hello",
			".cache/mise/tool":             "cached",
			"project/node_modules/pkg/i.j": "dep",
		},
		map[string]string{
			"hardlink.txt": "agent/notes.txt",
		},
	)
	var sink bytes.Buffer
	dst, err := NewTarZstWriter(&sink)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	total, err := RepackTar(bytes.NewReader(src), dst, "volumes/alpha/home")
	if err != nil {
		t.Fatalf("RepackTar: %v", err)
	}
	if err := dst.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if total != int64(len("hello")) {
		t.Errorf("total bytes = %d, want %d (only the non-excluded regular file)", total, len("hello"))
	}
	entries := readTarZst(t, sink.Bytes())
	if got := entries["volumes/alpha/home/notes.txt"][1]; got != "hello" {
		t.Errorf("notes.txt body = %q", got)
	}
	if _, ok := entries["volumes/alpha/home/.cache/mise/tool"]; ok {
		t.Errorf(".cache content must be excluded by volumeExclusions")
	}
	if _, ok := entries["volumes/alpha/home/project/node_modules/pkg/i.j"]; ok {
		t.Errorf("node_modules content must be excluded")
	}
	// Hardlink target had the wrapper stripped.
	if link := entries["volumes/alpha/home/hardlink.txt"][0]; link != "notes.txt" {
		t.Errorf("hardlink target = %q, want notes.txt (wrapper stripped)", link)
	}
	// The wrapper dir entry itself must not appear.
	for name := range entries {
		if strings.Contains(name, "agent/") {
			t.Errorf("wrapper dir leaked into output: %s", name)
		}
	}
}

func TestRepackTarWithExcludes_NoWrapperDotLayout(t *testing.T) {
	// "./file" layout: no wrapper detected, names kept under prefix.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "./hello.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2, ModTime: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()

	var sink bytes.Buffer
	dst, err := NewTarZstWriter(&sink)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RepackTarWithExcludes(bytes.NewReader(buf.Bytes()), dst, "workspace/w1", nil); err != nil {
		t.Fatalf("RepackTarWithExcludes: %v", err)
	}
	_ = dst.Close()
	entries := readTarZst(t, sink.Bytes())
	if got := entries["workspace/w1/hello.txt"][1]; got != "hi" {
		t.Errorf("entries = %v", entries)
	}
}

func TestRepackTarWithExcludes_CorruptInputErrors(t *testing.T) {
	var sink bytes.Buffer
	dst, err := NewTarZstWriter(&sink)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	_, err = RepackTarWithExcludes(strings.NewReader("this is not a tar stream at all, but it is long enough to look like one"), dst, "p", nil)
	if err == nil || !strings.Contains(err.Error(), "repack tar") {
		t.Fatalf("expected repack tar error, got %v", err)
	}
}
