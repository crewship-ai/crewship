// Package tui implements the `crewship tui` real-time dashboard.
//
// The shape is a 3-pane Bubble Tea program:
//
//	┌─────────────── Running ───────────────┐ ┌── Approvals ──┐
//	│ <run id>  agent=…  started=…          │ │ <id>  title   │
//	│ <run id>  agent=…  started=…          │ │ <id>  title   │
//	└───────────────────────────────────────┘ └───────────────┘
//	┌──────────── Journal (live) ────────────────────────────┐
//	│ 12:04:01  agent.run.started  trace=…                   │
//	│ 12:04:03  keeper.decision    allow=…                   │
//	└────────────────────────────────────────────────────────┘
//	[q]uit  [r]efresh  [tab] focus  [/] filter  [enter] open
//
// Live data sources:
//
//   - GET /api/v1/runs?status=RUNNING&limit=20 (refresh on tick)
//   - GET /api/v1/approvals?status=pending (refresh on tick)
//   - GET /api/v1/journal/stream (SSE; lines append as they arrive)
//
// The tick refresher (every 5s) is intentionally coarse — for sub-second
// feel, run the SSE journal as the "live" signal and keep table reads
// cheap. The model uses tea.Msg fan-in: each data source is a separate
// goroutine that emits typed messages back into Update.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Tick interval for run / approvals table refresh.
const tickInterval = 5 * time.Second

// Run boots the TUI program against the given API client and blocks
// until the user quits.
func Run(ctx context.Context, client *cli.Client, server string) error {
	m := newModel(client, server)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	// Start the journal SSE pump in a goroutine that calls p.Send back
	// into the program loop. ctx cancels both the SSE connection and
	// the program at quit time.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go runJournalPump(streamCtx, p, client)
	_, err := p.Run()
	return err
}

// runJournalPump is the SSE consumer. Lives in a single goroutine and
// pushes journalAppendMsg into the program. Reconnect is intentionally
// minimal — one retry-loop with a 2 s backoff; long network outages
// are surfaced as an errMsg and the user can `r` to recover or quit.
func runJournalPump(ctx context.Context, p *tea.Program, c *cli.Client) {
	backoff := 2 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.WithContext(ctx).StreamSSE(ctx, "/api/v1/journal/stream", "", func(ev cli.SSEEvent) error {
			if ev.Data == "" {
				return nil
			}
			line := journalLine{
				Ts:      time.Now().Format("15:04:05"),
				Type:    ev.Event,
				Payload: truncate(ev.Data, 100),
			}
			p.Send(journalAppendMsg(line))
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			p.Send(errMsg("journal stream: " + err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// panelFocus identifies which panel currently captures keystrokes.
type panelFocus int

const (
	focusRuns panelFocus = iota
	focusApprovals
	focusJournal
)

// model is the Bubble Tea Model. It composes 3 view-states plus the
// shared refresh ticker and the SSE-pumped journal lines.
type model struct {
	client *cli.Client
	server string

	runs      []map[string]any
	approvals []map[string]any
	journal   []journalLine

	err     string
	focus   panelFocus
	filter  string
	width   int
	height  int
	loading bool

	// journalSSEStarted ensures we only kick off the SSE pump once;
	// re-subscribing on every tick would leak goroutines.
	journalSSEStarted bool
}

type journalLine struct {
	Ts      string
	Type    string
	TraceID string
	Payload string
}

func newModel(c *cli.Client, server string) *model {
	return &model{
		client:  c,
		server:  server,
		loading: true,
	}
}

// ── Tea messages ──

type tickMsg time.Time
type runsMsg []map[string]any
type approvalsMsg []map[string]any
type journalAppendMsg journalLine
type errMsg string

func tickEvery(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func fetchRuns(c *cli.Client) tea.Cmd {
	return func() tea.Msg {
		var body struct {
			Data []map[string]any `json:"data"`
		}
		resp, err := c.Get("/api/v1/runs?status=RUNNING&limit=20")
		if err != nil {
			return errMsg("runs: " + err.Error())
		}
		if err := cli.CheckError(resp); err != nil {
			return errMsg("runs: " + err.Error())
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return errMsg("runs decode: " + err.Error())
		}
		return runsMsg(body.Data)
	}
}

func fetchApprovals(c *cli.Client) tea.Cmd {
	return func() tea.Msg {
		var body struct {
			Data []map[string]any `json:"data"`
		}
		resp, err := c.Get("/api/v1/approvals?status=pending")
		if err != nil {
			// Endpoint might return bare list — try once.
			return approvalsMsg(nil)
		}
		if err := cli.CheckError(resp); err != nil {
			return approvalsMsg(nil)
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return approvalsMsg(nil)
		}
		return approvalsMsg(body.Data)
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(
		fetchRuns(m.client),
		fetchApprovals(m.client),
		tickEvery(tickInterval),
	)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		return m, nil

	case tea.KeyMsg:
		switch v.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r":
			m.loading = true
			return m, tea.Batch(fetchRuns(m.client), fetchApprovals(m.client))
		case "tab":
			m.focus = (m.focus + 1) % 3
		}

	case tickMsg:
		return m, tea.Batch(
			fetchRuns(m.client),
			fetchApprovals(m.client),
			tickEvery(tickInterval),
		)

	case runsMsg:
		m.runs = v
		m.loading = false

	case approvalsMsg:
		m.approvals = v

	case journalAppendMsg:
		m.journal = append(m.journal, journalLine(v))
		// Cap journal scrollback to keep memory bounded. 200 lines is
		// the visible-ish budget.
		if len(m.journal) > 200 {
			m.journal = m.journal[len(m.journal)-200:]
		}

	case errMsg:
		m.err = string(v)
	}
	return m, nil
}

func (m *model) View() string {
	if m.width == 0 {
		return "loading…"
	}

	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FAFAFA")).
		Background(lipgloss.Color("#7D56F4")).
		Padding(0, 1).
		Width(m.width).
		Render(fmt.Sprintf(" Crewship  ·  %d running  ·  %d approvals pending ", len(m.runs), len(m.approvals)))

	box := func(title, body string, width int, focused bool) string {
		border := lipgloss.RoundedBorder()
		style := lipgloss.NewStyle().
			Border(border).
			BorderForeground(lipgloss.Color("#626262")).
			Padding(0, 1).
			Width(width).
			Height(10)
		if focused {
			style = style.BorderForeground(lipgloss.Color("#7D56F4"))
		}
		return style.Render(lipgloss.NewStyle().Bold(true).Render(title) + "\n" + body)
	}

	leftW := (m.width * 2) / 3
	rightW := m.width - leftW - 4
	if leftW < 30 {
		leftW = 30
	}
	if rightW < 20 {
		rightW = 20
	}

	runsBody := strings.Builder{}
	if len(m.runs) == 0 {
		runsBody.WriteString("(no runs in flight)")
	}
	for i, r := range m.runs {
		if i >= 8 {
			break
		}
		fmt.Fprintf(&runsBody, "%s  agent=%s  started=%s\n",
			truncate(toStr(r["id"]), 14),
			truncate(toStr(r["agent_slug"]), 18),
			truncate(toStr(r["started_at"]), 16))
	}
	apprBody := strings.Builder{}
	if len(m.approvals) == 0 {
		apprBody.WriteString("(none)")
	}
	for i, a := range m.approvals {
		if i >= 8 {
			break
		}
		fmt.Fprintf(&apprBody, "%s\n  %s\n",
			truncate(toStr(a["id"]), 22),
			truncate(toStr(a["title"]), rightW-4))
	}

	top := lipgloss.JoinHorizontal(
		lipgloss.Top,
		box("Running", runsBody.String(), leftW, m.focus == focusRuns),
		" ",
		box("Approvals", apprBody.String(), rightW, m.focus == focusApprovals),
	)

	jBody := strings.Builder{}
	start := 0
	if len(m.journal) > 12 {
		start = len(m.journal) - 12
	}
	for _, e := range m.journal[start:] {
		fmt.Fprintf(&jBody, "%s  %s  %s\n", e.Ts, e.Type, e.Payload)
	}
	if jBody.Len() == 0 {
		jBody.WriteString("(connecting to journal stream…)")
	}
	bottom := box("Journal (live)", jBody.String(), m.width-2, m.focus == focusJournal)

	footer := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#888")).
		Padding(0, 1).
		Render("[q]uit  [r]efresh  [tab] focus")
	if m.err != "" {
		footer = footer + "  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#F87171")).Render(m.err)
	}

	return lipgloss.JoinVertical(lipgloss.Left, header, "", top, bottom, footer)
}

// ── helpers ──

func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

func toStr(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
