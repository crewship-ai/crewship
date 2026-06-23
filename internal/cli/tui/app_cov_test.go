package tui

// Coverage-focused tests for the `crewship tui` dashboard. Everything
// runs against httptest servers — no real network, no TTY required.
// The Bubble Tea programs created here use an already-cancelled context
// so Program.Send never blocks (Send selects on the program ctx).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestTruncate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello world", 5, "hell…"},
		{"hello", 1, "h"},
		{"hello", 0, ""},
		{"hello", -3, ""},
		{"", 4, ""},
	}
	for _, c := range cases {
		if got := truncate(c.s, c.max); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.s, c.max, got, c.want)
		}
	}
}

func TestToStr(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"abc", "abc"},
		{42, "42"},
		{3.5, "3.5"},
		{true, "true"},
	}
	for _, c := range cases {
		if got := toStr(c.in); got != c.want {
			t.Errorf("toStr(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNewModelAndInit(t *testing.T) {
	t.Parallel()
	c := cli.NewClient("http://example.invalid", "tok", "")
	m := newModel(c, "http://example.invalid")
	if !m.loading {
		t.Error("newModel: loading = false, want true")
	}
	if m.client != c || m.server != "http://example.invalid" {
		t.Error("newModel did not retain client/server")
	}
	if cmd := m.Init(); cmd == nil {
		t.Error("Init() = nil, want batched fetch+tick command")
	}
}

func TestTickEvery(t *testing.T) {
	t.Parallel()
	cmd := tickEvery(time.Millisecond)
	if cmd == nil {
		t.Fatal("tickEvery returned nil cmd")
	}
	msg := cmd()
	if _, ok := msg.(tickMsg); !ok {
		t.Fatalf("tick cmd produced %T, want tickMsg", msg)
	}
}

// newRunsServer fakes the two REST endpoints the dashboard polls.
func newRunsServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/runs", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("status"); got != "RUNNING" {
			t.Errorf("runs query status = %q, want RUNNING", got)
		}
		fmt.Fprint(w, `{"data":[{"id":"run_1","agent_slug":"viktor","started_at":"2026-06-12T10:00:00Z"}]}`)
	})
	mux.HandleFunc("/api/v1/approvals", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("status"); got != "pending" {
			t.Errorf("approvals query status = %q, want pending", got)
		}
		fmt.Fprint(w, `{"data":[{"id":"appr_1","title":"deploy to prod"}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchRuns_Success(t *testing.T) {
	t.Parallel()
	srv := newRunsServer(t)
	c := cli.NewClient(srv.URL, "tok", "")

	msg := fetchRuns(c)()
	runs, ok := msg.(runsMsg)
	if !ok {
		t.Fatalf("got %T (%v), want runsMsg", msg, msg)
	}
	if len(runs) != 1 || runs[0]["id"] != "run_1" || runs[0]["agent_slug"] != "viktor" {
		t.Fatalf("runs = %+v, want one run_1/viktor entry", runs)
	}
}

func TestFetchRuns_Errors(t *testing.T) {
	t.Parallel()

	t.Run("connection refused", func(t *testing.T) {
		t.Parallel()
		dead := httptest.NewServer(http.NotFoundHandler())
		dead.Close() // guaranteed refused port
		c := cli.NewClient(dead.URL, "tok", "")
		msg := fetchRuns(c)()
		e, ok := msg.(errMsg)
		if !ok || !strings.HasPrefix(string(e), "runs: ") {
			t.Fatalf("got %T %v, want errMsg with runs: prefix", msg, msg)
		}
	})

	t.Run("api error status", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		c := cli.NewClient(srv.URL, "tok", "")
		msg := fetchRuns(c)()
		e, ok := msg.(errMsg)
		if !ok || !strings.Contains(string(e), "boom") {
			t.Fatalf("got %T %v, want errMsg mentioning boom", msg, msg)
		}
	})

	t.Run("decode error", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "not json")
		}))
		t.Cleanup(srv.Close)
		c := cli.NewClient(srv.URL, "tok", "")
		msg := fetchRuns(c)()
		e, ok := msg.(errMsg)
		if !ok || !strings.Contains(string(e), "runs decode") {
			t.Fatalf("got %T %v, want errMsg with runs decode", msg, msg)
		}
	})
}

func TestFetchApprovals_SuccessAndFallbacks(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		srv := newRunsServer(t)
		c := cli.NewClient(srv.URL, "tok", "")
		msg := fetchApprovals(c)()
		appr, ok := msg.(approvalsMsg)
		if !ok {
			t.Fatalf("got %T, want approvalsMsg", msg)
		}
		if len(appr) != 1 || appr[0]["title"] != "deploy to prod" {
			t.Fatalf("approvals = %+v, want deploy to prod entry", appr)
		}
	})

	// Approvals errors degrade to an empty list, never an errMsg —
	// the panel is best-effort.
	t.Run("connection refused -> nil", func(t *testing.T) {
		t.Parallel()
		dead := httptest.NewServer(http.NotFoundHandler())
		dead.Close()
		c := cli.NewClient(dead.URL, "tok", "")
		msg := fetchApprovals(c)()
		if appr, ok := msg.(approvalsMsg); !ok || appr != nil {
			t.Fatalf("got %T %v, want approvalsMsg(nil)", msg, msg)
		}
	})

	t.Run("http error -> nil", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "x", http.StatusForbidden)
		}))
		t.Cleanup(srv.Close)
		c := cli.NewClient(srv.URL, "tok", "")
		if appr, ok := fetchApprovals(c)().(approvalsMsg); !ok || appr != nil {
			t.Fatal("want approvalsMsg(nil) on HTTP error")
		}
	})

	t.Run("bad json -> nil", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "][")
		}))
		t.Cleanup(srv.Close)
		c := cli.NewClient(srv.URL, "tok", "")
		if appr, ok := fetchApprovals(c)().(approvalsMsg); !ok || appr != nil {
			t.Fatal("want approvalsMsg(nil) on decode error")
		}
	})
}

func TestUpdate_Messages(t *testing.T) {
	t.Parallel()
	c := cli.NewClient("http://example.invalid", "tok", "")

	t.Run("window size", func(t *testing.T) {
		t.Parallel()
		m := newModel(c, "s")
		_, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		if m.width != 120 || m.height != 40 {
			t.Errorf("size = %dx%d, want 120x40", m.width, m.height)
		}
		if cmd != nil {
			t.Error("window size should not produce a command")
		}
	})

	t.Run("quit keys", func(t *testing.T) {
		t.Parallel()
		for _, key := range []tea.KeyMsg{
			{Type: tea.KeyRunes, Runes: []rune{'q'}},
			{Type: tea.KeyCtrlC},
		} {
			m := newModel(c, "s")
			_, cmd := m.Update(key)
			if cmd == nil {
				t.Fatalf("key %s: no command, want tea.Quit", key)
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatalf("key %s: cmd msg %T, want tea.QuitMsg", key, cmd())
			}
		}
	})

	t.Run("refresh key", func(t *testing.T) {
		t.Parallel()
		m := newModel(c, "s")
		m.loading = false
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
		if !m.loading {
			t.Error("r: loading = false, want true")
		}
		if cmd == nil {
			t.Error("r: cmd = nil, want refresh batch")
		}
	})

	t.Run("tab cycles focus", func(t *testing.T) {
		t.Parallel()
		m := newModel(c, "s")
		tab := tea.KeyMsg{Type: tea.KeyTab}
		want := []panelFocus{focusApprovals, focusJournal, focusRuns}
		for i, w := range want {
			m.Update(tab)
			if m.focus != w {
				t.Fatalf("tab #%d: focus = %v, want %v", i+1, m.focus, w)
			}
		}
	})

	t.Run("tick reschedules", func(t *testing.T) {
		t.Parallel()
		m := newModel(c, "s")
		_, cmd := m.Update(tickMsg(time.Now()))
		if cmd == nil {
			t.Error("tick: cmd = nil, want fetch+tick batch")
		}
	})

	t.Run("runs and approvals data", func(t *testing.T) {
		t.Parallel()
		m := newModel(c, "s")
		m.Update(runsMsg{{"id": "run_9"}})
		if m.loading {
			t.Error("runsMsg should clear loading")
		}
		if len(m.runs) != 1 || m.runs[0]["id"] != "run_9" {
			t.Errorf("runs = %+v", m.runs)
		}
		m.Update(approvalsMsg{{"id": "appr_9"}})
		if len(m.approvals) != 1 || m.approvals[0]["id"] != "appr_9" {
			t.Errorf("approvals = %+v", m.approvals)
		}
	})

	t.Run("journal append caps at 200", func(t *testing.T) {
		t.Parallel()
		m := newModel(c, "s")
		for i := 0; i < 205; i++ {
			m.Update(journalAppendMsg(journalLine{Ts: "12:00:00", Type: "ev", Payload: fmt.Sprintf("p%d", i)}))
		}
		if len(m.journal) != 200 {
			t.Fatalf("journal len = %d, want 200", len(m.journal))
		}
		if m.journal[len(m.journal)-1].Payload != "p204" {
			t.Errorf("last payload = %q, want p204 (newest kept)", m.journal[len(m.journal)-1].Payload)
		}
		if m.journal[0].Payload != "p5" {
			t.Errorf("first payload = %q, want p5 (oldest dropped)", m.journal[0].Payload)
		}
	})

	t.Run("error message", func(t *testing.T) {
		t.Parallel()
		m := newModel(c, "s")
		m.Update(errMsg("runs: kaput"))
		if m.err != "runs: kaput" {
			t.Errorf("err = %q", m.err)
		}
	})
}

func TestView(t *testing.T) {
	t.Parallel()
	c := cli.NewClient("http://example.invalid", "tok", "")

	t.Run("zero width placeholder", func(t *testing.T) {
		t.Parallel()
		m := newModel(c, "s")
		if got := m.View(); got != "loading…" {
			t.Errorf("View() = %q, want loading placeholder", got)
		}
	})

	t.Run("empty state", func(t *testing.T) {
		t.Parallel()
		m := newModel(c, "s")
		m.width, m.height = 100, 40
		out := m.View()
		for _, want := range []string{
			"Crewship", "0 running", "0 approvals pending",
			"(no runs in flight)", "(none)", "(connecting to journal stream…)",
			"[q]uit",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("View() missing %q", want)
			}
		}
	})

	t.Run("populated panes and error footer", func(t *testing.T) {
		t.Parallel()
		m := newModel(c, "s")
		m.width, m.height = 120, 40
		for i := 0; i < 9; i++ { // 9 runs — only 8 rendered
			m.runs = append(m.runs, map[string]any{
				"id":         fmt.Sprintf("run_%d", i),
				"agent_slug": fmt.Sprintf("agent%d", i),
				"started_at": "2026-06-12T10:00",
			})
		}
		m.approvals = []map[string]any{{"id": "appr_1", "title": "deploy"}}
		for i := 0; i < 14; i++ { // 14 journal lines — only last 12 shown
			m.journal = append(m.journal, journalLine{Ts: "12:00:00", Type: "ev", Payload: fmt.Sprintf("pl%d", i)})
		}
		m.err = "journal stream: kaput"
		m.focus = focusJournal

		out := m.View()
		if !strings.Contains(out, "9 running") || !strings.Contains(out, "1 approvals pending") {
			t.Error("header counts missing")
		}
		if !strings.Contains(out, "agent0") || !strings.Contains(out, "agent7") {
			t.Error("rendered runs missing")
		}
		if strings.Contains(out, "agent8") {
			t.Error("run #9 rendered, want cap at 8")
		}
		if !strings.Contains(out, "appr_1") || !strings.Contains(out, "deploy") {
			t.Error("approval entry missing")
		}
		if !strings.Contains(out, "pl13") || !strings.Contains(out, "pl2") {
			t.Error("recent journal lines missing")
		}
		if strings.Contains(out, "pl1\n") || strings.Contains(out, "pl0") {
			t.Error("journal older than 12 lines rendered")
		}
		if !strings.Contains(out, "kaput") {
			t.Error("error footer missing")
		}
	})

	t.Run("tiny terminal clamps", func(t *testing.T) {
		t.Parallel()
		m := newModel(c, "s")
		m.width, m.height = 20, 10
		if out := m.View(); out == "" {
			t.Error("View() empty on tiny terminal")
		}
	})
}

// newSilentProgram builds a Program whose context is already cancelled:
// Send() then returns immediately instead of blocking on the (never
// started) event loop, which is exactly what runJournalPump needs.
func newSilentProgram(c *cli.Client) *tea.Program {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return tea.NewProgram(newModel(c, "s"), tea.WithContext(ctx))
}

// spyModel is a minimal tea.Model that copies every message the pump
// Sends into a channel so tests can assert on delivery deterministically.
type spyModel struct {
	msgs chan tea.Msg
}

func (s spyModel) Init() tea.Cmd { return nil }

func (s spyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case journalAppendMsg, errMsg:
		select {
		case s.msgs <- msg:
		default:
		}
	}
	return s, nil
}

func (s spyModel) View() string { return "" }

// startSpyProgram runs a headless Bubble Tea program around a spyModel.
// The returned stop func quits the program and waits for Run to return.
func startSpyProgram(t *testing.T) (*tea.Program, chan tea.Msg, func()) {
	t.Helper()
	msgs := make(chan tea.Msg, 16)
	p := tea.NewProgram(spyModel{msgs: msgs},
		tea.WithInput(strings.NewReader("")),
		tea.WithoutRenderer(),
	)
	ranDone := make(chan error, 1)
	go func() {
		_, err := p.Run()
		ranDone <- err
	}()
	stop := func() {
		p.Quit()
		select {
		case <-ranDone:
		case <-time.After(5 * time.Second):
			t.Error("tea program did not stop")
		}
	}
	return p, msgs, stop
}

func TestRunJournalPump_PreCancelled(t *testing.T) {
	t.Parallel()
	c := cli.NewClient("http://example.invalid", "tok", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		runJournalPump(ctx, newSilentProgram(c), c)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pump did not exit for cancelled context")
	}
}

func TestRunJournalPump_StreamsJournalLines(t *testing.T) {
	t.Parallel()
	var (
		mu      sync.Mutex
		paths   []string
		accepts []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		accepts = append(accepts, r.Header.Get("Accept"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		// One empty-data event (skipped by the pump) and one real line,
		// then hold the stream open until the client goes away so the
		// pump does not enter its reconnect loop.
		fmt.Fprint(w, "event: heartbeat\n\nevent: keeper.decision\ndata: allow\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	c := cli.NewClient(srv.URL, "tok", "")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	p, msgs, stop := startSpyProgram(t)
	done := make(chan struct{})
	go func() {
		runJournalPump(ctx, p, c)
		close(done)
	}()

	// The data event must arrive as a journalAppendMsg; the empty-data
	// heartbeat must NOT produce a message.
	select {
	case msg := <-msgs:
		jl, ok := msg.(journalAppendMsg)
		if !ok {
			t.Fatalf("got %T (%v), want journalAppendMsg", msg, msg)
		}
		if jl.Type != "keeper.decision" || jl.Payload != "allow" {
			t.Fatalf("journal line = %+v, want keeper.decision/allow", jl)
		}
		if jl.Ts == "" {
			t.Error("journal line missing timestamp")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pump never delivered the journal line")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pump did not exit after cancel")
	}
	stop()

	mu.Lock()
	defer mu.Unlock()
	if len(paths) == 0 || paths[0] != "/api/v1/journal/stream" {
		t.Fatalf("paths = %v, want first /api/v1/journal/stream", paths)
	}
	if accepts[0] != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", accepts[0])
	}
}

func TestRunJournalPump_HandshakeErrorSurfaced(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	c := cli.NewClient(srv.URL, "tok", "")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	p, msgs, stop := startSpyProgram(t)
	done := make(chan struct{})
	go func() {
		runJournalPump(ctx, p, c)
		close(done)
	}()

	select {
	case msg := <-msgs:
		e, ok := msg.(errMsg)
		if !ok {
			t.Fatalf("got %T (%v), want errMsg", msg, msg)
		}
		if !strings.HasPrefix(string(e), "journal stream: ") || !strings.Contains(string(e), "503") {
			t.Fatalf("errMsg = %q, want journal stream prefix with status 503", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pump never surfaced the handshake error")
	}

	cancel() // pump is now in its backoff select — cancel must end it
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pump did not exit after cancel on error path")
	}
	stop()
}

func TestRun_CancelledContextReturns(t *testing.T) {
	t.Parallel()
	c := cli.NewClient("http://example.invalid", "tok", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, c, "http://example.invalid") }()
	select {
	case err := <-errCh:
		// Depending on the environment Run fails either because the
		// context is already dead (ErrProgramKilled) or because the test
		// runner has no TTY to attach to. Both prove Run wired the model
		// into a real program and surfaced its error; it must never
		// return nil here.
		if err == nil {
			t.Fatal("Run with cancelled ctx returned nil, want error")
		}
		if !errors.Is(err, tea.ErrProgramKilled) && !strings.Contains(err.Error(), "TTY") {
			t.Fatalf("Run err = %v, want ErrProgramKilled or TTY failure", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return for cancelled context")
	}
}
