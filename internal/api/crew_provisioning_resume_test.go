package api

// Server-owned resume of a chat message chatbridge.Bridge.HandleChatMessage
// deferred while a crew's devcontainer built (AttachPendingMessage /
// resumeMessage / resumePending / failPending in crew_provisioning_jobs.go).
//
// These tests drive the REAL ProvisioningHandler.{runProvisioning,
// markJobFailed, AttachPendingMessage} machinery against a real ws.Hub +
// ws.Observer, with only chatbridge.Bridge itself replaced by a fake
// ws.ChatHandler. That is deliberate: the test the original bug shipped
// behind (components/features/onboarding/__tests__/onboarding-setup-chat.
// test.tsx:383-443) passed by stubbing useProvisioningStatus, useWorkspace
// and RealtimeProvider — it proved the CLIENT wiring given a working feed and
// never exercised the server-side race at all. Nothing here can be satisfied
// by a client-side stub: every case below asserts on frames that actually
// travelled through the hub's session-channel fan-out, or on calls that
// actually reached the fake chat handler.
import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/ws"
)

// fakeChatResumer stands in for chatbridge.Bridge in these tests: the real
// Bridge needs an orchestrator, a container provider and a conversation
// store wired up, none of which this package's job-lifecycle tests otherwise
// touch. It implements exactly the ws.ChatHandler contract resumeMessage
// calls through h.chatResumer.
type fakeChatResumer struct {
	mu     sync.Mutex
	calls  []fakeResumeCall
	called chan fakeResumeCall // signals each call so tests don't sleep/poll
	err    error               // returned from every HandleChatMessage call
	stream []ws.ChatEvent      // events emitted via streamFn before returning err
}

type fakeResumeCall struct {
	userID, chatID, content string
}

func newFakeChatResumer() *fakeChatResumer {
	return &fakeChatResumer{called: make(chan fakeResumeCall, 8)}
}

func (f *fakeChatResumer) HandleChatMessage(_ context.Context, userID, chatID, content string, streamFn func(ws.ChatEvent), _ ...ws.ChatMessageOption) error {
	for _, e := range f.stream {
		streamFn(e)
	}
	call := fakeResumeCall{userID, chatID, content}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	f.called <- call
	return f.err
}

func (f *fakeChatResumer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// resumeTestRig builds a ProvisioningHandler wired to a real ws.Hub (so
// BeginSessionRun/observers actually work) plus a seeded crew and a working
// fake Docker client, mirroring covProvRig but adding the hub wiring those
// tests don't need.
func resumeTestRig(t *testing.T, devcontainerCfg string) (h *ProvisioningHandler, wsID, crewID string, hub *ws.Hub) {
	t.Helper()
	logger := newTestLogger()
	hub = ws.NewHub(logger, nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
	h = newTestProvisioningHandler(t)
	h.wsHub = hub
	h.provisioner = devcontainer.NewProvisioner(&covCommitClient{}, nil, nil, logger)
	userID := seedTestUser(t, h.db)
	wsID = seedTestWorkspace(t, h.db, userID)
	crewID = seedCrewRow(t, h.db, "crew-resume", wsID, "Resume", "resume-crew")
	if devcontainerCfg != "" {
		if _, err := h.db.Exec(`UPDATE crews SET devcontainer_config = ? WHERE id = ?`, devcontainerCfg, crewID); err != nil {
			t.Fatalf("set devcontainer_config: %v", err)
		}
	}
	return h, wsID, crewID, hub
}

// rawFrame decodes just enough of a ws.ServerMessage frame to route on Type
// before decoding Payload into whatever shape that type carries.
type rawFrame struct {
	Type    string          `json:"type"`
	Channel string          `json:"channel"`
	Payload json.RawMessage `json:"payload"`
}

// nextChatEvent reads frames off o until it finds a "chat_event" (skipping
// the leading "run_begin" every session run opens with) and decodes its
// payload. Fails the test if none arrives — resumeMessage runs on its own
// goroutine, so tests synchronize on its actual output instead of sleeping.
func nextChatEvent(t *testing.T, o *ws.Observer) ws.ChatEvent {
	t.Helper()
	for {
		select {
		case raw, ok := <-o.Frames():
			if !ok {
				t.Fatal("observer closed before a chat_event frame arrived")
			}
			var f rawFrame
			if err := json.Unmarshal(raw, &f); err != nil {
				t.Fatalf("unmarshal frame: %v", err)
			}
			if f.Type != "chat_event" {
				continue
			}
			var ev ws.ChatEvent
			if err := json.Unmarshal(f.Payload, &ev); err != nil {
				t.Fatalf("unmarshal chat event: %v", err)
			}
			return ev
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for a chat_event frame")
			return ws.ChatEvent{}
		}
	}
}

// expectNoMoreFrames fails the test if another frame arrives on o within a
// short window. Used to pin "silent" contracts (e.g. ws.ErrAgentBusy) where
// emitting anything at all would mean a duplicate/leaked frame.
func expectNoMoreFrames(t *testing.T, o *ws.Observer) {
	t.Helper()
	select {
	case raw, ok := <-o.Frames():
		if ok {
			t.Fatalf("expected no further frames, got: %s", raw)
		}
	case <-time.After(200 * time.Millisecond):
		// no frame arrived — expected.
	}
}

func TestResumeDeferredChatMessage(t *testing.T) {
	// No features/postCreate/containerEnv/mise: the provisioner's skip path
	// completes without touching Docker (see TestRunProvisioning_
	// SkipPath_CompletesAndPersists), which is all these tests need — they
	// are about what happens to a message ATTACHED to the job, not about the
	// build pipeline itself.
	const skipCfg = `{"image":"ubuntu:22.04"}`
	// {not-json can never parse, so runProvisioning always fails fast via
	// markJobFailed — used by the failure-path cases below.
	const parseErrorCfg = `{not-json`

	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			// The bug this whole mechanism exists to close: a deferred
			// message got NO further action once the build finished,
			// because completion was only a fire-and-forget broadcast on a
			// channel the client might not be subscribed to yet. This proves
			// the job's own completion goroutine calls back into the chat
			// handler directly — no client involvement possible.
			name: "success: job completion resumes the attached message exactly once",
			run: func(t *testing.T) {
				h, wsID, crewID, hub := resumeTestRig(t, "")
				fake := newFakeChatResumer()
				fake.stream = []ws.ChatEvent{{Type: "text", Content: "hello from the resumed run"}}
				h.chatResumer = fake

				obs := hub.AddObserver("session:chat-1", "user-1", 8)
				defer hub.RemoveObserver("session:chat-1", obs)

				job := covJob(crewID)
				h.jobs[crewID] = job
				if ok := h.AttachPendingMessage(crewID, chatbridge.PendingChatMessage{
					UserID: "user-1", ChatID: "chat-1", Content: "hi there",
				}); !ok {
					t.Fatal("AttachPendingMessage returned false for a freshly-tracked job")
				}

				h.runProvisioning(crewID, wsID, skipCfg, "", "", job)
				if job.Status != "completed" {
					t.Fatalf("job status = %q, want completed (err=%q)", job.Status, job.Error)
				}

				select {
				case call := <-fake.called:
					if call.userID != "user-1" || call.chatID != "chat-1" || call.content != "hi there" {
						t.Errorf("resumed call = %+v, want {user-1 chat-1 hi there}", call)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("timed out waiting for the deferred message to resume")
				}

				ev := nextChatEvent(t, obs)
				if ev.Type != "text" || ev.Content != "hello from the resumed run" {
					t.Errorf("streamed event = %+v, want the fake's text event", ev)
				}

				if got := fake.callCount(); got != 1 {
					t.Errorf("HandleChatMessage called %d times, want exactly 1", got)
				}
			},
		},
		{
			// Symptom prevented: a user who hits "resend" (or the UI's old
			// auto-resend firing) WHILE the build is still running used to
			// have no attach-side protection — appending both sends would
			// queue two entries and double-run the message once the build
			// finished. Coalescing on ChatID means only the latest content
			// survives to run, exactly once.
			name: "coalescing: a second deferred send on the same chat replaces, not queues",
			run: func(t *testing.T) {
				h, wsID, crewID, hub := resumeTestRig(t, "")
				fake := newFakeChatResumer()
				h.chatResumer = fake

				obs := hub.AddObserver("session:chat-1", "user-1", 8)
				defer hub.RemoveObserver("session:chat-1", obs)

				job := covJob(crewID)
				h.jobs[crewID] = job
				h.AttachPendingMessage(crewID, chatbridge.PendingChatMessage{UserID: "user-1", ChatID: "chat-1", Content: "first"})
				h.AttachPendingMessage(crewID, chatbridge.PendingChatMessage{UserID: "user-1", ChatID: "chat-1", Content: "second (resend)"})

				h.runProvisioning(crewID, wsID, skipCfg, "", "", job)

				var call fakeResumeCall
				select {
				case call = <-fake.called:
				case <-time.After(3 * time.Second):
					t.Fatal("timed out waiting for the deferred message to resume")
				}
				if call.content != "second (resend)" {
					t.Errorf("resumed content = %q, want the latest send", call.content)
				}
				// Give any (incorrect) second resume a moment to show up before
				// asserting the count — the goroutine that would produce it, if
				// coalescing were broken, races this exact assertion.
				time.Sleep(100 * time.Millisecond)
				if got := fake.callCount(); got != 1 {
					t.Errorf("HandleChatMessage called %d times, want exactly 1 (no duplicate run)", got)
				}
			},
		},
		{
			// Symptom prevented: a message attached in the instant AFTER the
			// job already went terminal (the completion goroutine's drain
			// already ran and found nothing) must not be silently lost just
			// because "nobody will ever drain this map again" — it has to be
			// resumed immediately instead.
			name: "late attach after success resumes immediately, not lost",
			run: func(t *testing.T) {
				h, _, crewID, hub := resumeTestRig(t, "")
				fake := newFakeChatResumer()
				h.chatResumer = fake

				now := time.Now()
				h.jobs[crewID] = &ProvisionJob{
					CrewID: crewID, Status: "completed", CompletedAt: &now,
					CachedImage: "img:1", ConfigHash: "hash1",
				}

				obs := hub.AddObserver("session:chat-late", "user-1", 8)
				defer hub.RemoveObserver("session:chat-late", obs)

				if ok := h.AttachPendingMessage(crewID, chatbridge.PendingChatMessage{
					UserID: "user-1", ChatID: "chat-late", Content: "arrived after completion",
				}); !ok {
					t.Fatal("AttachPendingMessage returned false for a tracked (terminal) job")
				}

				select {
				case call := <-fake.called:
					if call.content != "arrived after completion" {
						t.Errorf("resumed content = %q", call.content)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("late attach after success was never resumed")
				}
			},
		},
		{
			// The task's explicit requirement: a FAILED build must surface a
			// real error to the user, not silence and not a false success —
			// and the agent must never actually run against a crew whose
			// devcontainer build just failed.
			name: "failure: job failure surfaces a real error and never runs the agent",
			run: func(t *testing.T) {
				h, wsID, crewID, hub := resumeTestRig(t, "")
				fake := newFakeChatResumer()
				h.chatResumer = fake

				obs := hub.AddObserver("session:chat-1", "user-1", 8)
				defer hub.RemoveObserver("session:chat-1", obs)

				job := covJob(crewID)
				h.jobs[crewID] = job
				h.AttachPendingMessage(crewID, chatbridge.PendingChatMessage{UserID: "user-1", ChatID: "chat-1", Content: "hi there"})

				h.runProvisioning(crewID, wsID, parseErrorCfg, "", "", job)
				if job.Status != "failed" {
					t.Fatalf("job status = %q, want failed", job.Status)
				}

				errEv := nextChatEvent(t, obs)
				if errEv.Type != "error" {
					t.Fatalf("first streamed event type = %q, want error", errEv.Type)
				}
				if errEv.Content == "" {
					t.Error("error event carries no content — silence is exactly what this must not do")
				}
				doneEv := nextChatEvent(t, obs)
				if doneEv.Type != "done" {
					t.Errorf("second streamed event type = %q, want done (so the UI leaves the generating state)", doneEv.Type)
				}

				time.Sleep(100 * time.Millisecond) // let a wrongly-fired resume land, if any
				if got := fake.callCount(); got != 0 {
					t.Errorf("HandleChatMessage called %d times on a FAILED build, want 0 — a failure must not run the agent", got)
				}
			},
		},
		{
			// Mirrors the success late-attach case for the failure side: a
			// message attached after the job already recorded its failure
			// must still get the real error, not silence.
			name: "late attach after failure surfaces the error immediately",
			run: func(t *testing.T) {
				h, _, crewID, hub := resumeTestRig(t, "")
				fake := newFakeChatResumer()
				h.chatResumer = fake

				now := time.Now()
				h.jobs[crewID] = &ProvisionJob{
					CrewID: crewID, Status: "failed", CompletedAt: &now,
					Error: "buildkit: disk quota exceeded",
				}

				obs := hub.AddObserver("session:chat-late", "user-1", 8)
				defer hub.RemoveObserver("session:chat-late", obs)

				h.AttachPendingMessage(crewID, chatbridge.PendingChatMessage{UserID: "user-1", ChatID: "chat-late", Content: "hi"})

				errEv := nextChatEvent(t, obs)
				if errEv.Type != "error" {
					t.Fatalf("event type = %q, want error", errEv.Type)
				}
				if got := errEv.Content; got == "" {
					t.Error("error event carries no content")
				}
				time.Sleep(100 * time.Millisecond)
				if got := fake.callCount(); got != 0 {
					t.Errorf("HandleChatMessage called %d times on a failed job, want 0", got)
				}
			},
		},
		{
			// At-most-once against a CONCURRENT manual run: if a manual
			// resend won the chat's run-exclusivity slot (tryMarkRunStart)
			// right as this job completed, chatResumer.HandleChatMessage
			// returns ws.ErrAgentBusy and MUST stream nothing further — the
			// winning run is the one that finishes and settles the UI. Any
			// frame emitted here would be a second, contradictory verdict on
			// the same turn.
			name: "concurrent manual run wins the slot: the resume stays silent, not a duplicate",
			run: func(t *testing.T) {
				h, wsID, crewID, hub := resumeTestRig(t, "")
				fake := newFakeChatResumer()
				fake.err = ws.ErrAgentBusy
				h.chatResumer = fake

				obs := hub.AddObserver("session:chat-1", "user-1", 8)
				defer hub.RemoveObserver("session:chat-1", obs)

				job := covJob(crewID)
				h.jobs[crewID] = job
				h.AttachPendingMessage(crewID, chatbridge.PendingChatMessage{UserID: "user-1", ChatID: "chat-1", Content: "hi there"})

				h.runProvisioning(crewID, wsID, skipCfg, "", "", job)

				select {
				case <-fake.called:
				case <-time.After(3 * time.Second):
					t.Fatal("timed out waiting for the resume attempt")
				}

				// Drain the run_begin frame BeginSessionRun always opens
				// with, then assert nothing else ever arrives.
				select {
				case <-obs.Frames():
				case <-time.After(2 * time.Second):
					t.Fatal("never even got the run_begin frame")
				}
				expectNoMoreFrames(t, obs)
			},
		},
		{
			name: "AttachPendingMessage on an untracked crew reports false, not a panic",
			run: func(t *testing.T) {
				h, _, _, _ := resumeTestRig(t, "")
				if ok := h.AttachPendingMessage("no-such-crew", chatbridge.PendingChatMessage{ChatID: "c", UserID: "u", Content: "x"}); ok {
					t.Error("expected false for a crew with no tracked job")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, tc.run)
	}
}
