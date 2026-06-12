package apple

import (
	"bufio"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// waitExecDone polls ExecInspect until the exec finishes, returning the exit code.
func waitExecDone(t *testing.T, p *Provider, execID string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		running, code, err := p.ExecInspect(context.Background(), execID)
		if err != nil {
			t.Fatalf("ExecInspect: %v", err)
		}
		if !running {
			return code
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for exec to finish")
	return 0
}

// closeConnWriteHalf closes only the stdin pipe of an interactive exec conn,
// letting the process see EOF and exit naturally (Conn.Close would kill it).
func closeConnWriteHalf(t *testing.T, conn io.ReadWriteCloser) {
	t.Helper()
	prwc, ok := conn.(*pipeReadWriteCloser)
	if !ok {
		t.Fatalf("conn is %T, want *pipeReadWriteCloser", conn)
	}
	if err := prwc.Writer.(*io.PipeWriter).Close(); err != nil {
		t.Fatalf("close write half: %v", err)
	}
}

func TestContainerStatsUnsupported(t *testing.T) {
	p := newTestProvider(Config{})
	_, err := p.ContainerStats(context.Background(), "cid")
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("err = %v, want not supported", err)
	}
}

func TestExecResizeNoop(t *testing.T) {
	p := newTestProvider(Config{})
	if err := p.ExecResize(context.Background(), "any", 24, 80); err != nil {
		t.Fatalf("ExecResize: %v", err)
	}
}

func TestExecSuccess(t *testing.T) {
	fake := installFakeContainer(t, `
case "$1" in
  exec) echo 'hello-out'; exit 0;;
esac
exit 0`)
	p := newTestProvider(Config{})

	res, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "cid-1",
		Cmd:         []string{"echo", "hi"},
		Env:         []string{"FOO=bar"},
		WorkingDir:  "/w",
		User:        "1001",
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.HasPrefix(res.ExecID, "apple-exec-") {
		t.Errorf("ExecID = %q, want apple-exec- prefix", res.ExecID)
	}

	out, err := io.ReadAll(res.Reader)
	if err != nil {
		t.Fatalf("read exec output: %v", err)
	}
	if strings.TrimSpace(string(out)) != "hello-out" {
		t.Errorf("output = %q, want hello-out", out)
	}

	if code := waitExecDone(t, p, res.ExecID); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	want := "exec --env FOO=bar --workdir /w --user 1001 cid-1 echo hi"
	if !fake.hasCall(t, want) {
		t.Errorf("expected CLI call %q, got %v", want, fake.calls(t))
	}
}

func TestExecNonZeroExitCode(t *testing.T) {
	installFakeContainer(t, `
case "$1" in
  exec) exit 3;;
esac
exit 0`)
	p := newTestProvider(Config{})

	res, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "cid-1",
		Cmd:         []string{"false"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if _, err := io.ReadAll(res.Reader); err != nil {
		t.Fatalf("read: %v", err)
	}
	if code := waitExecDone(t, p, res.ExecID); code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}

func TestExecStartError(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no container binary -> Start fails
	p := newTestProvider(Config{})

	_, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "cid-1",
		Cmd:         []string{"echo"},
	})
	if err == nil || !strings.Contains(err.Error(), "exec start") {
		t.Fatalf("err = %v, want exec start error", err)
	}
}

func TestExecInteractiveEcho(t *testing.T) {
	fake := installFakeContainer(t, `
case "$1" in
  exec)
    read line
    echo "pong:$line"
    exit 0;;
esac
exit 0`)
	p := newTestProvider(Config{})

	res, err := p.ExecInteractive(context.Background(), provider.InteractiveExecConfig{
		ContainerID: "cid-2",
		Cmd:         []string{"sh"},
		Env:         []string{"A=b"},
		WorkingDir:  "/ws",
		User:        "1001:1001",
	})
	if err != nil {
		t.Fatalf("ExecInteractive: %v", err)
	}
	if !strings.HasPrefix(res.ExecID, "apple-exec-") {
		t.Errorf("ExecID = %q, want apple-exec- prefix", res.ExecID)
	}

	// The script blocks on `read` until we send a line, so the exec must
	// report as running here.
	running, code, err := p.ExecInspect(context.Background(), res.ExecID)
	if err != nil {
		t.Fatalf("ExecInspect: %v", err)
	}
	if !running || code != 0 {
		t.Errorf("inspect = (running=%v, code=%d), want running with code 0", running, code)
	}

	if _, err := io.WriteString(res.Conn, "ping\n"); err != nil {
		t.Fatalf("write to conn: %v", err)
	}
	rd := bufio.NewReader(res.Conn)
	reply, err := rd.ReadString('\n')
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if strings.TrimSpace(reply) != "pong:ping" {
		t.Errorf("reply = %q, want pong:ping", reply)
	}

	// cmd.Wait only returns once the stdin copy goroutine sees EOF, so close
	// the write half (without killing the process) before checking the exit.
	closeConnWriteHalf(t, res.Conn)

	if code := waitExecDone(t, p, res.ExecID); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if _, err := rd.ReadString('\n'); err != io.EOF {
		t.Errorf("post-exit read err = %v, want io.EOF", err)
	}
	if err := res.Conn.Close(); err != nil {
		t.Fatalf("conn close: %v", err)
	}

	want := "exec --tty --env A=b --workdir /ws --user 1001:1001 cid-2 sh"
	if !fake.hasCall(t, want) {
		t.Errorf("expected CLI call %q, got %v", want, fake.calls(t))
	}
}

func TestExecInteractiveExitCode(t *testing.T) {
	installFakeContainer(t, `
case "$1" in
  exec) exit 5;;
esac
exit 0`)
	p := newTestProvider(Config{})

	res, err := p.ExecInteractive(context.Background(), provider.InteractiveExecConfig{
		ContainerID: "cid-3",
		Cmd:         []string{"sh"},
	})
	if err != nil {
		t.Fatalf("ExecInteractive: %v", err)
	}

	// Unblock the stdin copy goroutine so cmd.Wait can return; the process
	// exits on its own with code 5 (no input needed).
	closeConnWriteHalf(t, res.Conn)

	if code := waitExecDone(t, p, res.ExecID); code != 5 {
		t.Errorf("exit code = %d, want 5", code)
	}
	// After the exec finished, the output side is closed -> EOF.
	buf := make([]byte, 16)
	if _, err := res.Conn.Read(buf); err != io.EOF {
		t.Errorf("conn read err = %v, want io.EOF", err)
	}
	if err := res.Conn.Close(); err != nil {
		t.Fatalf("conn close: %v", err)
	}
}

func TestExecInteractiveStartError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	p := newTestProvider(Config{})

	_, err := p.ExecInteractive(context.Background(), provider.InteractiveExecConfig{
		ContainerID: "cid-4",
		Cmd:         []string{"sh"},
	})
	if err == nil || !strings.Contains(err.Error(), "exec interactive start") {
		t.Fatalf("err = %v, want exec interactive start error", err)
	}
}
