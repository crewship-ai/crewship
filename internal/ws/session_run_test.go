package ws

import (
	"encoding/json"
	"testing"
	"time"
)

// drainFrames reads everything an observer has queued right now. The session
// run publishes on the CALLER's goroutine (Hub.dispatch, not the Hub.Run
// broadcast queue), so by the time Emit returns the frame is already in the
// observer's buffer — no sentinel dance needed.
func drainFrames(t *testing.T, o *Observer) []ServerMessage {
	t.Helper()
	var out []ServerMessage
	for {
		select {
		case data, ok := <-o.Frames():
			if !ok {
				return out
			}
			var msg ServerMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("unmarshal observed frame: %v", err)
			}
			out = append(out, msg)
		case <-time.After(50 * time.Millisecond):
			return out
		}
	}
}

// The whole point of #1823: a run publishes on session:{chatId} without a
// WebSocket send_message anywhere in the picture. An HTTP observer — the shape
// `crewship chat stream` attaches with — must see run_begin, the run's events,
// and seq numbers that make the frames resumable.
func TestBeginSessionRun_PublishesSeqdFramesToObservers(t *testing.T) {
	hub := newRunningHub(t)
	obs := hub.AddObserver("session:c1", "u1", 16)
	defer hub.RemoveObserver("session:c1", obs)

	run := hub.BeginSessionRun("c1")
	run.Emit(ChatEvent{Type: "text", Content: "hello"})
	run.Emit(ChatEvent{Type: "done"})
	run.End()

	frames := drainFrames(t, obs)
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3 (run_begin, text, done): %+v", len(frames), frames)
	}
	if frames[0].Type != "run_begin" {
		t.Errorf("first frame = %q, want run_begin", frames[0].Type)
	}
	for i, f := range frames {
		if f.Seq != int64(i+1) {
			t.Errorf("frame %d seq = %d, want %d — frames outside the seq'd sequence are not resumable", i, f.Seq, i+1)
		}
		if f.Channel != "session:c1" {
			t.Errorf("frame %d channel = %q, want session:c1", i, f.Channel)
		}
	}
	if frames[1].Type != "chat_event" {
		t.Errorf("second frame = %q, want chat_event", frames[1].Type)
	}
}

// Replay is what a caller that attaches mid-run gets. A run published outside
// the WebSocket must fill the same buffer, and must report itself active until
// it ends — that is what stops `chat stream` closing with no_active_run.
func TestBeginSessionRun_IsReplayableAndReportsActive(t *testing.T) {
	hub := newRunningHub(t)

	run := hub.BeginSessionRun("c2")
	run.Emit(ChatEvent{Type: "text", Content: "mid-run"})

	replay := hub.ReplaySession("session:c2", 0)
	if !replay.Found {
		t.Fatal("no replay buffer for a run that is generating")
	}
	if !replay.Active {
		t.Error("replay reports the run inactive while it is still generating")
	}
	if len(replay.Frames) != 2 {
		t.Errorf("replay carried %d frames, want 2 (run_begin + text)", len(replay.Frames))
	}

	run.End()
	if hub.ReplaySession("session:c2", 0).Active {
		t.Error("replay still reports active after End")
	}
}

// End is called from a defer on a path that can also be reached by an explicit
// finish. A double End must not decrement the refcount twice: the buffer is
// shared with any concurrent run on the same chat, and an underflow would
// declare that run over while it is still generating.
func TestSessionRun_EndIsIdempotent(t *testing.T) {
	hub := newRunningHub(t)

	first := hub.BeginSessionRun("c3")
	second := hub.BeginSessionRun("c3")

	first.End()
	first.End()

	if !hub.ReplaySession("session:c3", 0).Active {
		t.Fatal("a double End on one run ended the OTHER run still generating on the same chat")
	}
	second.End()
	if hub.ReplaySession("session:c3", 0).Active {
		t.Error("channel still active after every run ended")
	}
}

// A run with no chat has no session channel. Recording one anyway would open a
// buffer on `session:` that nobody can ever subscribe to — a slow leak keyed by
// the empty string.
func TestBeginSessionRun_EmptyChatIDRecordsNothing(t *testing.T) {
	hub := newRunningHub(t)

	run := hub.BeginSessionRun("")
	run.Emit(ChatEvent{Type: "text", Content: "nowhere"})
	run.End()

	if hub.ReplaySession("session:", 0).Found {
		t.Error("a chat-less run opened a buffer on the empty session channel")
	}
}
