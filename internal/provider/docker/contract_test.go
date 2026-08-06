package docker

// The Docker provider's run of the shared, provider-agnostic contract suite
// (internal/provider/providertest).
//
// Why this exists next to the tests that already cover the same ground. Every
// exec guarantee this package pins — stdin delivery (exec_stdin_test.go), the
// #1158 fail-closed user guard (exec_fail_closed_test.go) — was written HERE,
// in one provider's own directory. The apple provider then shipped an Exec that
// discarded stdin and ignored AllowPrivileged entirely, and no test anywhere
// noticed, because nothing held a second implementation to the first's
// behaviour. The suite is that missing thing: one table, executed against every
// provider. This file is only the wiring — a fake Docker daemon that models the
// real one closely enough for the contracts to mean something.
//
// The fake is deliberately faithful rather than convenient. Notably it reports
// ExitCode 0 for a still-running exec, because that is what dockerd does, and
// Provider.ExecInspect passes the daemon's value straight through
// (docker.go:1443). A contract that fails because of that is reporting a real
// property of this provider, not an artefact of the harness.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/providertest"
)

func TestDockerProvider_ContractSuite(t *testing.T) {
	providertest.RunContractSuite(t, newDockerContractRuntime)
}

// contractContainerUser is the run-as user the fake daemon reports for the
// contract container, so the empty-User resolution path has something real to
// resolve.
const contractContainerUser = "1001:1001"

// contractStdinDeadline bounds how long the fake daemon waits for the client to
// half-close the stdin stream. A provider that streams the bytes but never
// half-closes hangs a real process forever; here it trips this deadline and the
// "process" exits non-zero, which is the convention the suite's EchoStdinCmd
// documents.
const contractStdinDeadline = 5 * time.Second

// contractDaemon is a fake dockerd that understands the four commands the
// contract suite issues.
type contractDaemon struct {
	t *testing.T

	mu              sync.Mutex
	seq             int
	calls           int
	lastUser        string
	lastAttachStdin bool
	execs           map[string]*contractExecState

	unblockOnce sync.Once
	unblockCh   chan struct{}
}

type contractExecState struct {
	cmd         []string
	attachStdin bool
	running     bool
	exitCode    int
}

func newContractDaemon(t *testing.T) *contractDaemon {
	return &contractDaemon{
		t:         t,
		execs:     make(map[string]*contractExecState),
		unblockCh: make(chan struct{}),
	}
}

func (d *contractDaemon) unblock() { d.unblockOnce.Do(func() { close(d.unblockCh) }) }

func (d *contractDaemon) handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	// Container inspect — the source of truth for the empty-User resolution.
	case strings.HasSuffix(path, "/json") && strings.Contains(path, "/containers/") && r.Method == http.MethodGet:
		d.countCall()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Id":     "contract-container",
			"Config": map[string]any{"User": contractContainerUser},
			"State":  map[string]any{"Running": true},
		})

	case strings.HasSuffix(path, "/exec") && r.Method == http.MethodPost:
		d.handleExecCreate(w, r)

	case strings.Contains(path, "/exec/") && strings.HasSuffix(path, "/start"):
		d.handleExecStart(w, r)

	case strings.Contains(path, "/exec/") && strings.HasSuffix(path, "/json") && r.Method == http.MethodGet:
		d.handleExecInspect(w, r)

	default:
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	}
}

func (d *contractDaemon) countCall() {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
}

func (d *contractDaemon) handleExecCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cmd         []string `json:"Cmd"`
		User        string   `json:"User"`
		AttachStdin bool     `json:"AttachStdin"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	d.mu.Lock()
	d.calls++
	d.seq++
	execID := fmt.Sprintf("contract-exec-%d", d.seq)
	d.lastUser = body.User
	d.lastAttachStdin = body.AttachStdin
	d.execs[execID] = &contractExecState{
		cmd:         body.Cmd,
		attachStdin: body.AttachStdin,
		running:     true,
	}
	d.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"Id":%q}`, execID)
}

// handleExecStart runs the fake "process" over the hijacked raw stream: it
// reads whatever stdin the client sends, produces the command's output, records
// its exit status and closes.
func (d *contractDaemon) handleExecStart(w http.ResponseWriter, r *http.Request) {
	execID := execIDFromPath(r.URL.Path)
	d.mu.Lock()
	st := d.execs[execID]
	// Copy the immutable-after-create fields out under the lock rather than
	// reading them off st later: exec inspect runs concurrently with this
	// handler for the blocking command.
	var attachStdin bool
	var cmd []string
	if st != nil {
		attachStdin = st.attachStdin
		cmd = st.cmd
	}
	d.mu.Unlock()
	if st == nil {
		http.Error(w, `{"message":"no such exec"}`, http.StatusNotFound)
		return
	}

	// Drain the start-options body BEFORE hijacking, or the post-upgrade reader
	// sees the leftover request payload as if it were streamed stdin.
	_, _ = io.Copy(io.Discard, r.Body)
	hj, ok := w.(http.Hijacker)
	if !ok {
		d.t.Error("response writer does not support hijacking")
		return
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		d.t.Errorf("hijack: %v", err)
		return
	}
	defer conn.Close()
	_, _ = bufrw.WriteString("HTTP/1.1 101 UPGRADED\r\n" +
		"Content-Type: application/vnd.docker.raw-stream\r\n" +
		"Connection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
	_ = bufrw.Flush()

	var stdin []byte
	sawEOF := true
	if attachStdin {
		_ = conn.SetReadDeadline(time.Now().Add(contractStdinDeadline))
		stdin, err = io.ReadAll(bufrw.Reader)
		if err != nil {
			// The client never half-closed: a real process reading to EOF would
			// still be waiting.
			sawEOF = false
		}
		_ = conn.SetReadDeadline(time.Time{})
	}

	stdout, stderr, code := contractRunCommand(d, cmd, stdin, sawEOF)

	if len(stdout) > 0 {
		_, _ = conn.Write(covStdcopyFrame(1, stdout))
	}
	if len(stderr) > 0 {
		_, _ = conn.Write(covStdcopyFrame(2, stderr))
	}

	d.mu.Lock()
	st.running = false
	st.exitCode = code
	d.mu.Unlock()

	if tc, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = tc.CloseWrite()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.Copy(io.Discard, conn)
	}
}

// contractRunCommand interprets the suite's command vocabulary.
func contractRunCommand(d *contractDaemon, cmd []string, stdin []byte, sawEOF bool) (stdout, stderr string, code int) {
	if len(cmd) == 0 {
		return "", "", 0
	}
	switch {
	case cmd[0] == "echo-stdin":
		if !sawEOF {
			// Harness convention: "stdin never reached EOF" is a non-zero exit.
			return "", "", 97
		}
		return string(stdin), "", 0
	case cmd[0] == "stderr":
		return "", contractStderrText, 0
	case cmd[0] == "block":
		<-d.unblockCh
		return "", "", 0
	case strings.HasPrefix(cmd[0], "exit-"):
		n, _ := strconv.Atoi(strings.TrimPrefix(cmd[0], "exit-"))
		return "", "", n
	}
	return "", "", 0
}

func (d *contractDaemon) handleExecInspect(w http.ResponseWriter, r *http.Request) {
	execID := execIDFromPath(r.URL.Path)
	d.mu.Lock()
	st := d.execs[execID]
	var running bool
	var code int
	if st != nil {
		running = st.running
		// Faithful to dockerd: ExitCode is 0 until the exec finishes. The
		// provider forwards it verbatim, so this is the value real callers get.
		code = st.exitCode
	}
	d.mu.Unlock()

	if st == nil {
		http.Error(w, `{"message":"no such exec"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ID":       execID,
		"Running":  running,
		"ExitCode": code,
	})
}

// execIDFromPath pulls the exec id out of /v1.43/exec/<id>/{start,json}.
func execIDFromPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if p == "exec" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

const contractStderrText = "contract-stderr-marker"

func newDockerContractRuntime(t *testing.T) providertest.Runtime {
	t.Helper()
	d := newContractDaemon(t)
	// The blocked "process" holds a server goroutine; release it whatever the
	// contract does, so the httptest server can shut down.
	t.Cleanup(d.unblock)

	p := newCovProviderTCP(t, Config{}, d.handle)

	return providertest.Runtime{
		Provider:     p,
		ContainerID:  "contract-container",
		SafeUser:     contractContainerUser,
		EchoStdinCmd: []string{"echo-stdin"},
		ExitCmd:      func(code int) []string { return []string{"exit-" + strconv.Itoa(code)} },
		StderrCmd:    []string{"stderr"},
		StderrText:   contractStderrText,
		BlockCmd:     []string{"block"},
		Unblock:      d.unblock,
		AttachedStdin: func() bool {
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.lastAttachStdin
		},
		ExecUser: func() string {
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.lastUser
		},
		RuntimeCalls: func() int {
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.calls
		},
	}
}

// Compile-time proof that the provider under test still satisfies the interface
// the suite is written against.
var _ provider.ContainerProvider = (*Provider)(nil)
