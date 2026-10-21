package api

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/ws"
)

func TestChatProposalHintsRequireCurrentWholeSourceVisibility(t *testing.T) {
	for _, hidden := range []string{"crew", "project", "assignee", "invalid-shape", "foreign-workspace", "missing-source"} {
		t.Run(hidden, func(t *testing.T) {
			f, r, c := proposalFixture(t)
			hub := startedTestHub(t)
			hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(r.db))
			observers := map[string]*ws.Observer{}
			for _, channel := range []string{"session:", "chat-hints:"} {
				name := channel + f.chat
				allowed, err := hub.CanSubscribeChannel(t.Context(), f.user, name)
				if err != nil || !allowed {
					t.Fatalf("legitimate subscriber: %v", err)
				}
				observer := hub.AddObserver(name, f.user, 16)
				observers[name] = observer
				defer hub.RemoveObserver(name, observer)
			}
			broadcastProposalInvalidation(t.Context(), r.db, hub, f.workspace, c.Entity.ID)
			for name, observer := range observers {
				frames := entityHintFramesOnChannel(t, hub, observer, name)
				if len(frames) != 1 || frames[0].Type != "chat.entity_refs_changed" {
					t.Fatalf("visible proposal hints: %+v", frames)
				}
			}
			workspace, id := f.workspace, c.Entity.ID
			switch hidden {
			case "crew":
				execOrFatal(t, r.db, `UPDATE chat_issue_proposals SET items=json_set(items,'$[0].crew_id','hidden-crew') WHERE id=?`, id)
			case "project":
				execOrFatal(t, r.db, `UPDATE chat_issue_proposals SET items=json_set(items,'$[1].project_id','hidden-project') WHERE id=?`, id)
			case "assignee":
				execOrFatal(t, r.db, `UPDATE chat_issue_proposals SET items=json_set(items,'$[0].assignee_id','hidden-agent') WHERE id=?`, id)
			case "invalid-shape":
				execOrFatal(t, r.db, `UPDATE chat_issue_proposals SET items='[]' WHERE id=?`, id)
			case "foreign-workspace":
				workspace = "foreign"
			case "missing-source":
				id = "missing"
			}
			broadcastProposalInvalidation(t.Context(), r.db, hub, workspace, id)
			for name, observer := range observers {
				if frames := entityHintFramesOnChannel(t, hub, observer, name); len(frames) != 0 {
					t.Fatalf("inaccessible %s proposal emitted hints: %+v", hidden, frames)
				}
			}
		})
	}
}
