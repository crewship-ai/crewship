package docker

// Tests for ExecConfig.Stdin plumbing — the fix for oversized agent prompts.
// A user message larger than Linux MAX_ARG_STRLEN (128 KiB) makes execve fail
// with E2BIG when passed as a positional argv element, so the orchestrator
// delivers it over stdin instead. These tests pin the docker provider's half
// of that contract: when ExecConfig.Stdin is set the exec is created with
// AttachStdin and the reader's bytes are streamed into the hijacked exec
// connection; when it is nil the behaviour is byte-for-byte the historic one
// (no stdin attached).

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// covHijackReadStdin hijacks the connection, completes the docker raw-stream
// upgrade, reads everything the client writes (the streamed stdin) until the
// client half-closes, records it, then writes one stdout frame and closes.
func covHijackReadStdin(t *testing.T, w http.ResponseWriter, r *http.Request, out string, sink *[]byte, mu *sync.Mutex) {
	t.Helper()
	// Drain the request body (the exec-start options JSON) BEFORE hijacking so
	// the post-upgrade reader sees only the streamed stdin, not the leftover
	// request payload.
	_, _ = io.Copy(io.Discard, r.Body)
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Error("response writer does not support hijacking")
		return
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		t.Errorf("hijack: %v", err)
		return
	}
	defer conn.Close()
	_, _ = bufrw.WriteString("HTTP/1.1 101 UPGRADED\r\n" +
		"Content-Type: application/vnd.docker.raw-stream\r\n" +
		"Connection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
	_ = bufrw.Flush()

	// Read the streamed stdin until the client CloseWrite()s (EOF). Bounded by
	// a deadline so a contract break fails the test instead of hanging it.
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	stdin, _ := io.ReadAll(bufrw.Reader)
	mu.Lock()
	*sink = stdin
	mu.Unlock()

	_, _ = conn.Write(covStdcopyFrame(1, out))
}

func TestExec_Stdin_AttachesAndStreams(t *testing.T) {
	t.Parallel()

	const prompt = "this is the oversized prompt delivered via stdin"

	var mu sync.Mutex
	var seenStdin []byte
	var attachStdin bool
	p := newCovProviderTCP(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/containers/cid/exec"):
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			attachStdin = strings.Contains(string(body), "\"AttachStdin\":true")
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"e1"}`))
		case strings.Contains(path, "/exec/e1/start"):
			covHijackReadStdin(t, w, r, "done", &seenStdin, &mu)
		default:
			t.Errorf("unexpected request %s %s", r.Method, path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	res, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "cid",
		Cmd:         []string{"claude", "--print"},
		Stdin:       strings.NewReader(prompt),
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	out, err := io.ReadAll(res.Reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(out) != "done" {
		t.Errorf("stdout = %q, want %q", out, "done")
	}

	mu.Lock()
	defer mu.Unlock()
	if !attachStdin {
		t.Error("exec create must set AttachStdin=true when ExecConfig.Stdin is set")
	}
	if string(seenStdin) != prompt {
		t.Errorf("streamed stdin = %q, want %q", seenStdin, prompt)
	}
}

func TestExec_NilStdin_DoesNotAttachStdin(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var attachStdin bool
	p := newCovProviderTCP(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/containers/cid/exec"):
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			attachStdin = strings.Contains(string(body), "\"AttachStdin\":true")
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"e1"}`))
		case strings.Contains(path, "/exec/e1/start"):
			covHijackUpgrade(t, w, r, covStdcopyFrame(1, "ok"))
		default:
			t.Errorf("unexpected request %s %s", r.Method, path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	res, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "cid",
		Cmd:         []string{"echo", "hi"},
		// Stdin intentionally nil.
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	_, _ = io.ReadAll(res.Reader)

	mu.Lock()
	defer mu.Unlock()
	if attachStdin {
		t.Error("exec create must NOT set AttachStdin when ExecConfig.Stdin is nil")
	}
}
