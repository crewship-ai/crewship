package api

import (
	"context"
	"database/sql"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/work"
)

func TestCollaborationAgentTaskRequestOnlyNarrowsAccess(t *testing.T) {
	for _, tc := range []struct {
		name, body, access string
		valid              bool
	}{
		{"original", `{"chat_id":"chat-original","message_id":"message-original"}`, "", true},
		{"read", `{"chat_id":"chat-original","message_id":"message-original","access":"read"}`, "read", true},
		{"explicit-write", `{"chat_id":"chat-original","message_id":"message-original","access":"write"}`, "", false},
		{"task-alias", `{"chat_id":"chat-original","message_id":"message-original","access":"task"}`, "", false},
		{"empty", `{"chat_id":"chat-original","message_id":"message-original","access":""}`, "", false},
		{"null", `{"chat_id":"chat-original","message_id":"message-original","access":null}`, "", false},
		{"boolean", `{"chat_id":"chat-original","message_id":"message-original","access":true}`, "", false},
		{"array", `{"chat_id":"chat-original","message_id":"message-original","access":["read"]}`, "", false},
		{"extra", `{"chat_id":"chat-original","message_id":"message-original","access":"read","role":"reader"}`, "", false},
		{"case", `{"chat_id":"chat-original","message_id":"message-original","Access":"read"}`, "", false},
		{"duplicate", `{"chat_id":"chat-original","message_id":"message-original","access":"read","access":"read"}`, "", false},
		{"missing-message", `{"chat_id":"chat-original","access":"read"}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, err := parseCollaborationAgentTaskRequest([]byte(tc.body))
			if (err == nil) != tc.valid {
				t.Fatalf("closed body validity=%v want%v", err == nil, tc.valid)
			}
			if tc.valid && (in.Access != tc.access || in.ChatID != "chat-original" || in.MessageID != "message-original") {
				t.Fatal("request DATA changed")
			}
		})
	}
}

// SQL/current original mandate proof only: synthetic runtime identity, no native
// execution or actual paid model. Existing writer sealing positives stay mandatory.
func TestCollaborationAgentAdmissionPreservesAttachedOriginalIntent(t *testing.T) {
	for _, mode := range []string{"task", "read"} {
		t.Run(mode, func(t *testing.T) {
			grant := "read"
			if mode == "task" {
				grant = "write"
			}
			x := newCollaborationAgentTestFixture(t, grant)
			var pending *collaborationAgentPending
			x.tx(t, func(tx *sql.Tx) {
				var receipt work.Receipt
				var err error
				pending, receipt, err = x.store.StartTask(x.human, tx, x.key, mode, x.f.a.MessageID, x.f.receipt)
				if err != nil {
					t.Fatal(err)
				}
				x.workID = receipt.WorkID
			})
			handle := x.attach(t, pending)
			stamp, err := collaborationHumanLifecycleStamp(x.fence)
			if err != nil {
				t.Fatal(err)
			}
			humans := &collaborationHumanRegistry{lifecycle: stamp, bindingID: "read-task-binding", runtime: &mooring.Runtime{}, pages: x.f.pages, fence: x.fence, clock: x.clock, scope: x.key}
			registry, err := newCollaborationSubmissionRegistry(humans)
			if err != nil {
				t.Fatal(err)
			}
			binding, err := humans.storeBinding()
			if err != nil {
				t.Fatal(err)
			}
			request := x.request()
			err = registry.run(request.Context(), binding, func(ctx context.Context) error {
				p, e := registry.admitAgent(ctx, request, x.store, handle)
				if e != nil {
					return e
				}
				if p.captured.intent != mode {
					t.Fatal("attached original mode narrowed/upgraded unexpectedly")
				}
				again, e := registry.admitAgent(ctx, request, x.store, handle)
				if e != nil || again != p {
					t.Fatal("same original admission rebound", e)
				}
				changes := []mooring.BatchChange{submissionTestChange(t, x.key, "read-task-seal", x.workID, p.id)}
				e = registry.sealAgent(ctx, request, p, changes)
				if mode == "read" {
					if e == nil {
						t.Fatal("read participant sealed writer original")
					}
					return nil
				}
				return e
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCollaborationAgentTaskRequestPartialPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, extra    string
		valid, partial bool
	}{
		{"default", "", true, false},
		{"atomic", `,"allow_partial":false`, true, false},
		{"partial", `,"allow_partial":true`, true, true},
		{"read", `,"access":"read","allow_partial":false`, true, false},
		{"reader-partial", `,"access":"read","allow_partial":true`, false, false},
		{"null", `,"allow_partial":null`, false, false},
		{"string", `,"allow_partial":"true"`, false, false},
		{"duplicate", `,"allow_partial":false,"allow_partial":true`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, err := parseCollaborationAgentTaskRequest([]byte(`{"chat_id":"original-chat","message_id":"original-message"` + tc.extra + `}`))
			if (err == nil) != tc.valid || err == nil && in.AllowPartial != tc.partial {
				t.Fatalf("partial policy parsed as %v/%v; want valid=%v partial=%v", in.AllowPartial, err, tc.valid, tc.partial)
			}
		})
	}
}

func TestCollaborationAgentOriginalPartialPolicyCannotBeChanged(t *testing.T) {
	x := newCollaborationAgentTestFixture(t, "write")
	var pending *collaborationAgentPending
	x.tx(t, func(tx *sql.Tx) {
		var receipt work.Receipt
		var err error
		pending, receipt, err = x.store.StartTask(x.human, tx, x.key, "task", x.f.a.MessageID, x.f.receipt, true)
		if err != nil {
			t.Fatal(err)
		}
		x.workID = receipt.WorkID
	})
	handle := x.attach(t, pending)
	x.tx(t, func(tx *sql.Tx) {
		captured, err := x.store.Check(x.request(), tx, handle, x.key, "task")
		if err != nil || !captured.allowPartial {
			t.Fatal("original declared policy missing", err)
		}
		decision, err := x.store.DecideCaptured(x.request().Context(), tx, captured, x.key, "task")
		if err != nil || decision.kind != collaborationAgentAllowed {
			t.Fatal("original partial policy denied", err)
		}
		captured.allowPartial = false
		if _, err := x.store.DecideCaptured(x.request().Context(), tx, captured, x.key, "task"); err == nil {
			t.Fatal("changed captured policy accepted")
		}
	})
}
