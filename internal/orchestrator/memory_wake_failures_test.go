package orchestrator

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// #1637: three ways the wake path could tell the model something the rest
// of the prompt contradicts, or hand it less memory than it had before the
// #1628 backwards scan existed.
//
//  1. A failed `ls` was recognised by the English text of two specific
//     diagnostics. Every other failure shape — a permission-denied listing
//     on a glibc image, a translated diagnostic on a non-C-locale
//     container — was parsed as a one-entry listing, which suppressed the
//     `cat` of today's log entirely.
//  2. The [MEMORY GAP] block said "the daily log below is from that day"
//     based on whether the READ succeeded, not on whether the section was
//     actually emitted, so a tight budget produced a prompt that promised
//     a log it had just dropped.
//  3. A day inside the 30-day window whose log could not be read was
//     reported as "older than the 30-day boot window".

// lsProbeContainer answers one exec with a fixed merged-stream body and a
// fixed ExecInspect outcome, so a test can pin how listContainerDir decides
// "this listing is usable" independently of the text on the stream.
type lsProbeContainer struct {
	*mockContainer
	output     string
	exitCode   int
	running    bool
	inspectErr error
}

func newLSProbeContainer(output string, exitCode int) *lsProbeContainer {
	c := &lsProbeContainer{mockContainer: &mockContainer{}, output: output, exitCode: exitCode}
	c.mockContainer.execFn = func(_ provider.ExecConfig) (*provider.ExecResult, error) {
		return &provider.ExecResult{
			ExecID: "ls-probe",
			Reader: io.NopCloser(strings.NewReader(c.output)),
		}, nil
	}
	return c
}

func (c *lsProbeContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return c.running, c.exitCode, c.inspectErr
}

// TestListContainerDir_ExitStatusDecidesFailure: the exit status, not the
// wording of the diagnostic, decides whether a listing is trustworthy.
// Both container providers merge stderr into the output stream, so the
// bytes alone cannot distinguish a listing from an error — and the set of
// error wordings is open (coreutils vs busybox vs a translated libc).
func TestListContainerDir_ExitStatusDecidesFailure(t *testing.T) {
	const dir = "/crew/agents/wake-agent/.memory/daily"

	tests := []struct {
		name       string
		output     string
		exitCode   int
		running    bool
		inspectErr error
		wantErr    bool
		wantNames  []string
	}{
		{
			// The regression that motivated #1637: a traversable but
			// unreadable daily dir on debian/ubuntu/mcr images.
			name:     "glibc permission denied",
			output:   "ls: cannot open directory '" + dir + "': Permission denied",
			exitCode: 2,
			wantErr:  true,
		},
		{
			name:     "busybox missing directory",
			output:   "ls: " + dir + ": No such file or directory",
			exitCode: 1,
			wantErr:  true,
		},
		{
			name:     "coreutils missing directory",
			output:   "ls: cannot access '" + dir + "': No such file or directory",
			exitCode: 2,
			wantErr:  true,
		},
		{
			// containerEnv may set LANG; the diagnostic then shares no
			// prefix with any English form. Nothing but the exit status
			// can catch this one.
			name:     "translated diagnostic",
			output:   "ls: nelze otevřít adresář '" + dir + "': Přístup odepřen",
			exitCode: 2,
			wantErr:  true,
		},
		{
			name:       "inspect failed — outcome unknown",
			output:     "2026-07-31.md",
			inspectErr: fmt.Errorf("daemon gone"),
			wantErr:    true,
		},
		{
			name:    "still running after stream EOF",
			output:  "2026-07-31.md",
			running: true,
			wantErr: true,
		},
		{
			// An empty stream is indistinguishable from a provider that
			// swallowed the output, so it degrades to the fixed-path
			// fallback rather than claiming "this directory is empty".
			name:    "empty stream",
			output:  "",
			wantErr: true,
		},
		{
			name:      "healthy listing",
			output:    "2026-07-30.md\n2026-07-31.md\n",
			wantNames: []string{"2026-07-30.md", "2026-07-31.md"},
		},
		{
			// The inverse of the bug: a real entry whose name happens to
			// read like a diagnostic must NOT make a successful listing
			// look like a failure.
			name:      "entry named like a diagnostic",
			output:    "ls: cannot access\n2026-07-31.md\n",
			wantNames: []string{"ls: cannot access", "2026-07-31.md"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			mc := newLSProbeContainer(tc.output, tc.exitCode)
			mc.running = tc.running
			mc.inspectErr = tc.inspectErr

			o := New(mc, newMemState(), slog.Default())
			names, err := o.listContainerDir(context.Background(), "c1", dir)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected a listing failure, got names %v", names)
				}
				if names != nil {
					t.Errorf("a failed listing must return no names, got %v", names)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(names, "|") != strings.Join(tc.wantNames, "|") {
				t.Errorf("names = %v, want %v", names, tc.wantNames)
			}
		})
	}
}

// TestBuildMemoryContext_UnreadableDailyDirStillReadsToday is defect 1
// end-to-end: an x-only daily directory on a glibc base. The listing is
// unusable, so the window must degrade to the pre-#1628 fixed pair —
// `cat daily/<today>.md` still succeeds on an x-only dir, and that is the
// memory the agent had before the backwards scan existed. Parsing the
// diagnostic as a one-entry listing instead means no `cat` is issued at
// all and today's own notes vanish from the wake prompt.
func TestBuildMemoryContext_UnreadableDailyDirStillReadsToday(t *testing.T) {
	const agentDaily = "/crew/agents/wake-agent/.memory/daily"
	today := time.Now().UTC().Format("2006-01-02")

	mc := newWakeMemoryContainer(map[string]string{
		"/crew/agents/wake-agent/.memory/AGENT.md": "# Agent\nI am the wake canary.",
		agentDaily + "/" + today + ".md":           "Shipped the parser rewrite this morning.",
	})
	mc.failList(agentDaily, "ls: cannot open directory '"+agentDaily+"': Permission denied", 2)

	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		AgentSlug:     "wake-agent",
		AgentID:       "a1",
		ContainerID:   "c1",
		WorkspaceID:   "ws1",
		MemoryEnabled: true,
	}

	out := o.buildMemoryContext(context.Background(), req, 0)

	todayPath := agentDaily + "/" + today + ".md"
	if !containsPath(mc.catPaths(), todayPath) {
		t.Errorf("an unreadable listing suppressed the read of today's own log %s (cat paths: %v)",
			todayPath, mc.catPaths())
	}
	if !strings.Contains(out, "Shipped the parser rewrite this morning") {
		t.Errorf("today's notes missing from the prompt — the listing failure made memory worse than before the scan:\n%s", out)
	}
	// A failed listing is not evidence of an idle stretch, and today's log
	// is right there: no gap claim.
	if strings.Contains(out, "[END MEMORY GAP]") {
		t.Errorf("no gap should be claimed when today's log was read:\n%s", out)
	}
}

// TestBuildMemoryContext_GapNeverPromisesADroppedDailyLog is defect 2:
// the read succeeded but assembleSections dropped the section for budget,
// so the block must not say "the daily log below is from that day" — and
// must hand over the memory.read key the agent now needs.
func TestBuildMemoryContext_GapNeverPromisesADroppedDailyLog(t *testing.T) {
	const agentDaily = "/crew/agents/wake-agent/.memory/daily"
	now := time.Now().UTC()
	lastDate := now.AddDate(0, 0, -9).Format("2006-01-02")
	const priorNote = "PRIOR-DAY-NOTES-MARKER"

	mc := newWakeMemoryContainer(map[string]string{
		// Large enough to consume the whole content budget on its own,
		// which is exactly how a real agent with big pins + AGENT.md
		// starves the second-to-last section.
		"/crew/agents/wake-agent/.memory/AGENT.md": "# Agent\n" + strings.Repeat("evergreen fact.\n", 400),
		agentDaily + "/" + lastDate + ".md":        priorNote,
	})

	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		AgentSlug:     "wake-agent",
		AgentID:       "a1",
		ContainerID:   "c1",
		WorkspaceID:   "ws1",
		MemoryEnabled: true,
	}

	out := o.buildMemoryContext(context.Background(), req, 1000)

	// Control: the premise of the test is that the section really was
	// dropped. If it fits, the rest asserts nothing.
	if strings.Contains(out, priorNote) {
		t.Fatalf("control failed: the prior daily section fit the budget, so nothing was dropped:\n%s", out)
	}
	if !containsPath(mc.catPaths(), agentDaily+"/"+lastDate+".md") {
		t.Fatalf("control failed: the prior day was never read (cat paths: %v)", mc.catPaths())
	}
	if !strings.Contains(out, "[END MEMORY GAP]") {
		t.Fatalf("expected a gap block after 9 idle days:\n%s", out)
	}
	if strings.Contains(out, "The daily log below is from that day") {
		t.Errorf("the gap block promises a log the budget dropped:\n%s", out)
	}
	if !strings.Contains(out, "memory.read tier=daily key="+lastDate) {
		t.Errorf("a gap whose notes are not below must hand the agent the exact key to read:\n%s", out)
	}
	// It is in the window — the budget dropped it. Saying otherwise sends
	// the model looking for a reason that does not exist.
	if strings.Contains(out, "older than the") {
		t.Errorf("an in-window day was blamed on the lookback window:\n%s", out)
	}
}

// TestBuildMemoryContext_InWindowUnreadableDayIsNotBlamedOnTheWindow is
// defect 3: the listing proves the day has a log and it is inside the
// window, but the read came back empty (unreadable file, or the shared
// read deadline expired mid-scan). The block must say that, not "older
// than the 30-day boot window" about a day three days old.
func TestBuildMemoryContext_InWindowUnreadableDayIsNotBlamedOnTheWindow(t *testing.T) {
	const agentDaily = "/crew/agents/wake-agent/.memory/daily"
	now := time.Now().UTC()
	lastDate := now.AddDate(0, 0, -3).Format("2006-01-02")
	priorPath := agentDaily + "/" + lastDate + ".md"

	mc := newWakeMemoryContainer(map[string]string{
		"/crew/agents/wake-agent/.memory/AGENT.md": "# Agent\nI am the wake canary.",
		priorPath: "notes the agent cannot reach",
	})
	// Listed (so the day is known to hold notes) but not readable. The
	// busybox wording is deliberate: it shares no prefix with the GNU
	// `cat: <path>: …` shape the reader used to match on, so only the exit
	// status can stop the diagnostic being returned as the day's notes.
	mc.failRead(priorPath, "cat: can't open '"+priorPath+"': Permission denied", 1)

	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		AgentSlug:     "wake-agent",
		AgentID:       "a1",
		ContainerID:   "c1",
		WorkspaceID:   "ws1",
		MemoryEnabled: true,
	}

	out := o.buildMemoryContext(context.Background(), req, 0)

	if !strings.Contains(out, "[END MEMORY GAP]") {
		t.Fatalf("expected a gap block for a 3-day-old last active day:\n%s", out)
	}
	if !strings.Contains(out, "3 days ago") {
		t.Errorf("gap should state the real age:\n%s", out)
	}
	if strings.Contains(out, "older than the") {
		t.Errorf("a day 3 days old was reported as outside the 30-day window:\n%s", out)
	}
	if strings.Contains(out, "The daily log below is from that day") {
		t.Errorf("the gap block promises a log that could not be read:\n%s", out)
	}
	if !strings.Contains(out, "memory.read tier=daily key="+lastDate) {
		t.Errorf("an unreadable in-window day must still hand over the recovery key:\n%s", out)
	}
	// The failed read must not leak the diagnostic into the prompt as if
	// it were the day's notes.
	if strings.Contains(out, "Permission denied") {
		t.Errorf("cat's diagnostic was injected as memory content:\n%s", out)
	}
}

// TestBuildMemoryContext_OutOfWindowDayKeepsItsOwnExplanation is the
// control for the two tests above: the "older than the boot window"
// wording must still appear for a day that really is older, otherwise the
// fix would have collapsed three distinct situations into one message.
func TestBuildMemoryContext_OutOfWindowDayKeepsItsOwnExplanation(t *testing.T) {
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -120).Format("2006-01-02")

	mc := newWakeMemoryContainer(map[string]string{
		"/crew/agents/wake-agent/.memory/AGENT.md":             "# Agent\nI am the wake canary.",
		"/crew/agents/wake-agent/.memory/daily/" + old + ".md": "ancient notes",
	})

	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		AgentSlug:     "wake-agent",
		AgentID:       "a1",
		ContainerID:   "c1",
		WorkspaceID:   "ws1",
		MemoryEnabled: true,
	}

	out := o.buildMemoryContext(context.Background(), req, 0)

	if !strings.Contains(out, fmt.Sprintf("older than the %d-day boot window", dailyLogLookbackDays)) {
		t.Errorf("an out-of-window day must still be explained as out of window:\n%s", out)
	}
	if !strings.Contains(out, "memory.read tier=daily key="+old) {
		t.Errorf("out-of-window gap should hand the agent the exact key to read:\n%s", out)
	}
	if containsPath(mc.catPaths(), "/crew/agents/wake-agent/.memory/daily/"+old+".md") {
		t.Errorf("an out-of-window day must not be read (cat paths: %v)", mc.catPaths())
	}
}
