package automation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// recordingOpener is the issue-side twin of recordingEnqueuer: it counts
// calls, because "one burst produces ONE issue" is the property the coalescing
// exists to hold and a count is the only way to see it.
type recordingOpener struct {
	mu      sync.Mutex
	calls   []IssueIntent
	decline bool
	err     error
}

func (o *recordingOpener) OpenIssue(_ context.Context, in IssueIntent) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return false, o.err
	}
	o.calls = append(o.calls, in)
	return !o.decline, nil
}

func (o *recordingOpener) n() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.calls)
}

func (o *recordingOpener) at(i int) IssueIntent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls[i]
}

func issueRule(id, workspace, eventType string) Resolved {
	return Resolved{
		Automation: Automation{
			ID:          id,
			WorkspaceID: workspace,
			Name:        id,
			Enabled:     true,
			EventType:   eventType,
			ActionKind:  ActionKindIssue,
			Action: Action{Issue: &IssueAction{
				CrewSlug:  "devops",
				Title:     "panel {{ event.payload.panel }} needs a look",
				Body:      "seq {{ event.payload.seq }}",
				DedupeKey: "page:ops/sluzby#wake_1",
				Context:   map[string]string{"page_id": "pg_1", "panel": "sluzby"},
			}},
			DebounceSeconds: 10,
			MaxPerHour:      60,
		},
	}
}

func pageEntry(workspace string, extra map[string]any) journal.Entry {
	payload := map[string]any{"page": "ops", "page_id": "pg_1", "panel": "sluzby", "seq": 7}
	for k, v := range extra {
		payload[k] = v
	}
	return journal.Entry{
		WorkspaceID: workspace,
		Type:        journal.EntryPagePanelUpdated,
		CrewID:      "crew_ops",
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorSystem,
		Summary:     "pushed ops/sluzby",
		Payload:     payload,
	}
}

func TestIssueRuleOpensOneIssueAndRendersTheEvent(t *testing.T) {
	opener := &recordingOpener{}
	reg := NewRegistry(nil, nil, Options{})
	reg.SetIssueOpener(opener)
	reg.Load([]Resolved{issueRule("a_issue", "ws_1", string(journal.EntryPagePanelUpdated))})

	reg.Observer([]journal.Entry{pageEntry("ws_1", nil)})
	if n := reg.Flush(context.Background()); n != 1 {
		t.Fatalf("Flush opened %d issues, want 1", n)
	}
	if opener.n() != 1 {
		t.Fatalf("opener called %d times, want 1", opener.n())
	}
	got := opener.at(0)
	switch {
	case got.CrewSlug != "devops":
		t.Errorf("CrewSlug = %q, want devops", got.CrewSlug)
	case got.Title != "panel sluzby needs a look":
		t.Errorf("Title = %q; {{ event.payload.* }} must render with the same renderer routine inputs use", got.Title)
	case !strings.Contains(got.Body, "7"):
		t.Errorf("Body = %q, want the event's seq rendered into it", got.Body)
	case got.DedupeKey != "page:ops/sluzby#wake_1":
		t.Errorf("DedupeKey = %q, want it carried through verbatim", got.DedupeKey)
	case got.Context["page_id"] != "pg_1":
		t.Errorf("Context = %v, want the opener's own detail carried through", got.Context)
	}
}

// A storm inside one debounce window is ONE issue. This is the same guarantee
// the routine path has, and the reason wake gates compile to automations rows
// instead of to a second observer that would have had to reimplement it.
func TestIssueRuleCoalescesABurstIntoOneIssue(t *testing.T) {
	opener := &recordingOpener{}
	reg := NewRegistry(nil, nil, Options{})
	reg.SetIssueOpener(opener)
	reg.Load([]Resolved{issueRule("a_issue", "ws_1", string(journal.EntryPagePanelUpdated))})

	entries := make([]journal.Entry, 0, 200)
	for i := 0; i < 200; i++ {
		entries = append(entries, pageEntry("ws_1", nil))
	}
	reg.Observer(entries)

	if got := reg.PendingIntents(); got != 1 {
		t.Fatalf("200 matching entries coalesced into %d intents, want 1", got)
	}
	if n := reg.Flush(context.Background()); n != 1 {
		t.Fatalf("Flush opened %d issues for one burst, want 1", n)
	}
	if got := opener.at(0).Coalesced; got != 200 {
		t.Errorf("Coalesced = %d, want 200 — the issue has to be able to say why it fired once", got)
	}
}

// Two rules on the same event are two subjects: one crew's gate must not
// swallow another's.
func TestIssueRulesDoNotCoalesceAcrossAutomations(t *testing.T) {
	opener := &recordingOpener{}
	reg := NewRegistry(nil, nil, Options{})
	reg.SetIssueOpener(opener)
	first := issueRule("a_one", "ws_1", string(journal.EntryPagePanelUpdated))
	second := issueRule("a_two", "ws_1", string(journal.EntryPagePanelUpdated))
	second.Action.Issue.CrewSlug = "ops"
	reg.Load([]Resolved{first, second})

	reg.Observer([]journal.Entry{pageEntry("ws_1", nil)})
	if n := reg.Flush(context.Background()); n != 2 {
		t.Fatalf("Flush opened %d issues, want one per rule", n)
	}
}

func TestIssueRuleDeclinedByTheOpenerIsNotCountedAsFired(t *testing.T) {
	opener := &recordingOpener{decline: true}
	reg := NewRegistry(nil, nil, Options{})
	reg.SetIssueOpener(opener)
	reg.Load([]Resolved{issueRule("a_issue", "ws_1", string(journal.EntryPagePanelUpdated))})

	reg.Observer([]journal.Entry{pageEntry("ws_1", nil)})
	if n := reg.Flush(context.Background()); n != 0 {
		t.Fatalf("Flush counted %d issues; an opener that declined opened nothing", n)
	}
	if opener.n() != 1 {
		t.Fatalf("opener called %d times, want 1 — declining is the opener's decision to make", opener.n())
	}
}

func TestIssueRuleWithNoOpenerWiredDoesNotPanic(t *testing.T) {
	reg := NewRegistry(nil, nil, Options{})
	reg.Load([]Resolved{issueRule("a_issue", "ws_1", string(journal.EntryPagePanelUpdated))})
	reg.Observer([]journal.Entry{pageEntry("ws_1", nil)})
	if n := reg.Flush(context.Background()); n != 0 {
		t.Fatalf("Flush = %d with no opener wired, want 0", n)
	}
}

func TestIssueRuleSpendsTheHourlyBudget(t *testing.T) {
	opener := &recordingOpener{}
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	reg := NewRegistry(nil, nil, Options{Now: func() time.Time { return now }})
	reg.SetIssueOpener(opener)
	r := issueRule("a_issue", "ws_1", string(journal.EntryPagePanelUpdated))
	r.MaxPerHour = 2
	r.DebounceSeconds = 0
	reg.Load([]Resolved{r})

	for i := 0; i < 5; i++ {
		reg.Observer([]journal.Entry{pageEntry("ws_1", nil)})
		reg.Flush(context.Background())
		now = now.Add(time.Second)
	}
	if opener.n() > 2 {
		t.Fatalf("opener called %d times against max_per_hour=2; the burst brake must apply to issues too", opener.n())
	}
}

func TestIssueActionValidation(t *testing.T) {
	tests := []struct {
		name    string
		action  Action
		wantErr string
	}{
		{
			name:   "a complete issue action",
			action: Action{Issue: &IssueAction{CrewSlug: "ops", Title: "look at this"}},
		},
		{
			name:    "no issue config at all",
			action:  Action{},
			wantErr: "action.issue required",
		},
		{
			name:    "no crew",
			action:  Action{Issue: &IssueAction{Title: "look at this"}},
			wantErr: "crew_slug required",
		},
		{
			name:    "no title",
			action:  Action{Issue: &IssueAction{CrewSlug: "ops"}},
			wantErr: "title required",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := Automation{
				WorkspaceID: "ws_1", Name: "n", EventType: "page.panel.updated",
				ActionKind: ActionKindIssue, Action: tc.action,
				DebounceSeconds: 10, MaxPerHour: 60,
			}
			err := a.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestUnknownActionKindStillRefused(t *testing.T) {
	a := Automation{
		WorkspaceID: "ws_1", Name: "n", EventType: "x", ActionKind: "notify",
		DebounceSeconds: 10, MaxPerHour: 60,
	}
	if err := a.Validate(); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("Validate = %v, want 'notify' refused at write time", err)
	}
}

func TestIssueOpenerErrorIsNotCountedAsFired(t *testing.T) {
	opener := &recordingOpener{err: errors.New("crew has no LEAD agent")}
	reg := NewRegistry(nil, nil, Options{})
	reg.SetIssueOpener(opener)
	reg.Load([]Resolved{issueRule("a_issue", "ws_1", string(journal.EntryPagePanelUpdated))})
	reg.Observer([]journal.Entry{pageEntry("ws_1", nil)})
	if n := reg.Flush(context.Background()); n != 0 {
		t.Fatalf("Flush = %d, want 0 when the opener failed", n)
	}
}
