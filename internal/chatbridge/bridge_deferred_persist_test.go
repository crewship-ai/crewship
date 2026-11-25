package chatbridge

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/ws"
)

// A send that arrives while the crew's image still has to be built is queued
// on the build and run by the server afterwards. The sender's composer shows
// it as sent at once, so the server must save and acknowledge the human turn
// at once too — exactly one time across the deferral, any re-deferral and the
// resumed run (#5559).
func TestDeferredSendIsPersistedOnceBeforeTheBuild(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		// redefers: the first resume finds the image unusable again and is
		// deferred to another build before the second resume runs.
		redefers   bool
		enqueueErr error
		wantSaved  int
		wantAcks   int
		wantResume bool
	}{
		{name: "queued then resumed", wantSaved: 1, wantAcks: 1, wantResume: true},
		{name: "queued, deferred again, resumed", redefers: true, wantSaved: 1, wantAcks: 1, wantResume: true},
		// The build never started: nothing will run the message, so it is
		// not recorded and the sender gets the error instead.
		{name: "build could not be queued", enqueueErr: errors.New("rate limited"), wantSaved: 0, wantAcks: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			info := &ChatInfo{
				HumanAuthority: &chataudience.Receipt{UserID: "user-1", ChatID: "sess-1", MemberID: "m", MemberRevision: 1},
				AgentID:        "a1",
				AgentSlug:      "alice",
				CrewID:         "crew-1",
				CrewSlug:       "engineering",
				WorkspaceID:    "ws-1",
				CLIAdapter:     "CLAUDE_CODE",
				ToolProfile:    "CODING",
				TimeoutSecs:    30,
				Visibility:     "private",
				CachedImage:    "", // first build still running
			}
			b := testBridgeWithContainer(t, &mockResolver{info: info}, &failContainer{})
			enq := &stubEnqueuer{resRunning: true, returnError: tc.enqueueErr}
			b.SetProvisioningEnqueuer(enq)

			acks := 0
			stream := func(e ws.ChatEvent) {
				if e.Type == "user_message" {
					acks++
				}
			}
			saved := func() int {
				t.Helper()
				msgs, err := b.convStore.Read(context.Background(), "sess-1", 0, 0)
				if err != nil {
					t.Fatalf("read conversation: %v", err)
				}
				n := 0
				for _, m := range msgs {
					if m.Content == "files from d1" {
						n++
					}
				}
				return n
			}

			err := b.HandleChatMessage(context.Background(), "user-1", "sess-1", "files from d1", stream)
			if tc.enqueueErr != nil {
				if err == nil || errors.Is(err, ws.ErrCrewProvisioning) {
					t.Fatalf("enqueue failure must surface as an error, got %v", err)
				}
			} else if !errors.Is(err, ws.ErrCrewProvisioning) || !enq.attachCalled {
				t.Fatalf("send was not deferred to the build: err=%v attached=%v", err, enq.attachCalled)
			}
			// Saved before the build finishes: a reload now must show it.
			if got := saved(); got != tc.wantSaved {
				t.Fatalf("saved while building = %d, want %d", got, tc.wantSaved)
			}
			if !tc.wantResume {
				if acks != tc.wantAcks {
					t.Fatalf("acks = %d, want %d", acks, tc.wantAcks)
				}
				return
			}

			resume := func() error {
				pending := enq.attachMsg
				pending.Opts.HumanResume = true
				enq.attachCalled = false
				return b.HandleChatMessage(context.Background(), pending.UserID, pending.ChatID, pending.Content, stream, pending.Opts)
			}
			if tc.redefers {
				// The completed build's image is still unusable: deferred again.
				if err := resume(); !errors.Is(err, ws.ErrCrewProvisioning) || !enq.attachCalled {
					t.Fatalf("resume was not deferred again: err=%v attached=%v", err, enq.attachCalled)
				}
			}
			info.CachedImage = "crewship-cache:ready"
			info.CachedRequirements = &devcontainer.AggregatedRequirements{
				AdapterBinaries:      []string{"claude"},
				AdapterReleasePolicy: devcontainer.AdapterReleasePolicy([]string{"claude"}),
			}
			// The resumed run reaches the runtime (the fake container refuses
			// to start); the human turn must not be saved a second time.
			if err := resume(); err == nil || errors.Is(err, ws.ErrCrewProvisioning) {
				t.Fatalf("resume did not reach the runtime: %v", err)
			}
			if got := saved(); got != tc.wantSaved {
				t.Fatalf("saved after resume = %d, want %d", got, tc.wantSaved)
			}
			if acks != tc.wantAcks {
				t.Fatalf("acks = %d, want %d", acks, tc.wantAcks)
			}
		})
	}
}

type deltaResolver struct {
	mockResolver
	total atomic.Int32
}

func (d *deltaResolver) IncrementMessageCount(_ context.Context, _ string, delta int) error {
	d.total.Add(int32(delta))
	return nil
}

// A turn saved at deferral is counted when it is saved, so the count matches
// the transcript even if the turn never runs, and the resumed run does not
// count it a second time (#5571).
func TestDeferredSendIsCountedOnceAtSave(t *testing.T) {
	t.Parallel()
	info := &ChatInfo{
		HumanAuthority: &chataudience.Receipt{UserID: "user-1", ChatID: "sess-1", MemberID: "m", MemberRevision: 1},
		AgentID:        "a1", AgentSlug: "alice", CrewID: "crew-1", CrewSlug: "engineering",
		WorkspaceID: "ws-1", CLIAdapter: "CLAUDE_CODE", ToolProfile: "CODING", TimeoutSecs: 30, Visibility: "private",
	}
	res := &deltaResolver{mockResolver: mockResolver{info: info}}
	b := testBridgeWithContainer(t, res, &failContainer{})
	enq := &stubEnqueuer{resRunning: true}
	b.SetProvisioningEnqueuer(enq)
	err := b.HandleChatMessage(context.Background(), "user-1", "sess-1", "files from d1", func(ws.ChatEvent) {})
	if !errors.Is(err, ws.ErrCrewProvisioning) {
		t.Fatalf("send was not deferred: %v", err)
	}
	if got := res.total.Load(); got != 1 {
		t.Fatalf("count after a saved, never-run turn = %d, want 1", got)
	}
	info.CachedImage = "crewship-cache:ready"
	info.CachedRequirements = &devcontainer.AggregatedRequirements{
		AdapterBinaries:      []string{"claude"},
		AdapterReleasePolicy: devcontainer.AdapterReleasePolicy([]string{"claude"}),
	}
	pending := enq.attachMsg
	pending.Opts.HumanResume = true
	_ = b.HandleChatMessage(context.Background(), pending.UserID, pending.ChatID, pending.Content, func(ws.ChatEvent) {}, pending.Opts)
	if got := res.total.Load(); got != 1 {
		t.Fatalf("count after the resumed run = %d, want the user turn counted once", got)
	}
}

// A failure turn written after a turn that was counted at save adds only
// itself, so a run that ends right after the save is never counted twice.
func TestInBandFailureTurnDoesNotRecountASavedUserTurn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		userCounted  bool
		wantIncrease int32
	}{{"user turn counted at save", true, 1}, {"user turn not yet counted", false, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			info := &ChatInfo{AgentID: "a1", WorkspaceID: "ws-1"}
			res := &deltaResolver{mockResolver: mockResolver{info: info}}
			b := testBridgeWithContainer(t, res, &failContainer{})
			b.persistInBandFailureTurn(context.Background(), "sess-1", info, "", nil, "cancelled", tc.userCounted)
			if got := res.total.Load(); got != tc.wantIncrease {
				t.Fatalf("count increase = %d, want %d", got, tc.wantIncrease)
			}
		})
	}
}

// The reason a saved message never ran is written once per message, however
// often the failure is reported.
func TestRecordNotRunWritesOneTurnPerMessage(t *testing.T) {
	t.Parallel()
	info := &ChatInfo{AgentID: "a1", WorkspaceID: "ws-1"}
	res := &deltaResolver{mockResolver: mockResolver{info: info}}
	b := testBridgeWithContainer(t, res, &failContainer{})
	for i := 0; i < 3; i++ {
		b.RecordNotRun(context.Background(), "sess-1", "trig-1", "build failed")
	}
	b.RecordNotRun(context.Background(), "sess-1", "trig-2", "build failed")
	msgs, err := b.convStore.Read(context.Background(), "sess-1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].ID != "notrun-trig-1" || msgs[1].ID != "notrun-trig-2" {
		t.Fatalf("transcript = %+v, want one turn per message", msgs)
	}
	if got := res.total.Load(); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
}

// A queued message that waited behind a long run is checked against the
// sender's rights again when it starts: if the membership changed meanwhile it
// does not run (#5571).
func TestQueuedSendIsRefusedWhenAuthorityChangedMeanwhile(t *testing.T) {
	t.Parallel()
	queued := &chataudience.Receipt{UserID: "user-1", ChatID: "sess-1", MemberID: "m", MemberRevision: 1}
	info := &ChatInfo{
		HumanAuthority: &chataudience.Receipt{UserID: "user-1", ChatID: "sess-1", MemberID: "m", MemberRevision: 2},
		AgentID:        "a1", AgentSlug: "alice", CrewID: "crew-1", CrewSlug: "engineering",
		WorkspaceID: "ws-1", CLIAdapter: "CLAUDE_CODE", ToolProfile: "CODING", TimeoutSecs: 30, Visibility: "private",
		CachedImage: "crewship-cache:ready",
	}
	b := testBridgeWithContainer(t, &mockResolver{info: info}, &failContainer{})
	err := b.HandleChatMessage(context.Background(), "user-1", "sess-1", "second", func(ws.ChatEvent) {},
		ws.ChatMessageOption{HumanResume: true, HumanAuthority: queued, UserMessagePersisted: true, OriginalTriggerMessageID: "t2"})
	if err == nil || !strings.Contains(err.Error(), "queued human authority changed") {
		t.Fatalf("err = %v, want the authority-changed refusal", err)
	}
}
