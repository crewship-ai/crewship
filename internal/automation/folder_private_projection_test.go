package automation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestFolderPrivateEventCannotEscapeThroughAutomation(t *testing.T) {
	for _, kind := range []string{"ordinary-calibration", "private", "historical-unmarked"} {
		t.Run(kind, func(t *testing.T) {
			enqueuer, opener := &recordingEnqueuer{}, &recordingOpener{}
			registry := NewRegistry(nil, enqueuer, Options{})
			registry.SetIssueOpener(opener)
			routine := rule("folder-routine", "ws_1", "page.folder_membership_changed")
			routine.Action.Inputs = map[string]any{"page": "{{ event.payload.page }}", "id": "{{ event.payload.page_id }}"}
			issue := issueRule("folder-issue", "ws_1", "page.folder_membership_changed")
			issue.Action.Issue.Title = "Page {{ event.payload.page }}"
			issue.Action.Issue.Body = "ID {{ event.payload.page_id }}"
			payload := map[string]any{"page": "secret-folder-document", "page_id": "private-page-opaque-id", "op": "moved", "from": "one", "to": "two", "pages_version": int64(2)}
			if kind != "historical-unmarked" {
				payload["private_audience"] = kind == "private"
				payload["private_output_metadata"] = kind == "private"
			}
			if kind == "ordinary-calibration" {
				payload["page"] = "ordinary-page"
				payload["page_id"] = "ordinary-page-id"
			}
			event := journal.Entry{ID: "folder-event", WorkspaceID: "ws_1", Type: "page.folder_membership_changed", ActorType: journal.ActorUser, ActorID: "owner", Summary: "Moved page " + payload["page"].(string), Payload: payload}
			before, _ := json.Marshal(event)
			// A rule matching a canonical private slug must not turn its truth value
			// into a visible issue or pending run even if rendered inputs are scrubbed.
			probe := rule("folder-probe", "ws_1", "page.folder_membership_changed")
			probe.Matcher.PayloadEquals = map[string]any{"page": "secret-folder-document"}
			registry.Load([]Resolved{routine, issue, probe})
			registry.Observer([]journal.Entry{event})
			if n := registry.Flush(t.Context()); n != 2 {
				t.Errorf("private canonical matcher escaped into action count=%d want2", n)
			}
			if enqueuer.n() < 1 || opener.n() != 1 {
				t.Fatalf("real registry action calibration missing: queue%d issue%d", enqueuer.n(), opener.n())
			}
			output := enqueuer.at(0).InputsJSON + opener.at(0).Title + opener.at(0).Body
			if kind == "ordinary-calibration" {
				if !strings.Contains(output, "ordinary-page") || !strings.Contains(output, "ordinary-page-id") {
					t.Error("ordinary action payload lost")
				}
			} else {
				for _, forbidden := range []string{"secret-folder-document", "private-page-opaque-id"} {
					if strings.Contains(output, forbidden) {
						t.Errorf("native automation action disclosed %q", forbidden)
					}
				}
			}
			after, _ := json.Marshal(event)
			if string(before) != string(after) {
				t.Error("observer changed canonical event")
			}
		})
	}
}
