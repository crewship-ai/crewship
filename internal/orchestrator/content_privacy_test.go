package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStrictContentPrivacyRunDoesNotPublishContent(t *testing.T) {
	const marker = "synthetic-private-page-content"
	stream := `{"type":"stream_event","event":{"type":"delta","delta":{"type":"text_delta","text":"` + marker + `"}}}` + "\n"
	j := &covJournal{}
	o := New(covNewRunContainer(covRunOpts{stream: stream}), newMemState(), covQuietLogger())
	o.SetJournal(j)
	var delivered []AgentEvent
	req := covRunReq().WithStrictContentPrivacy()
	// Later ordinary overrides cannot downgrade the private host choice.
	req.SuppressSessionStream = false
	req.SkipConvHistory = false
	req.MemoryEnabled = true
	pub := &fakeSessionPublisher{}
	o.SetSessionPublisher(pub)
	if err := o.RunAgent(context.Background(), req, func(e AgentEvent) { delivered = append(delivered, e) }); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"chat.agent_response", "exec.output_chunk"} {
		if len(j.byType(kind)) != 0 {
			t.Errorf("private run persisted %s", kind)
		}
	}
	for _, e := range delivered {
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), marker) {
			t.Error("private content reached generic caller handler")
		}
	}
	if len(pub.began()) != 0 {
		t.Error("private run opened generic session recording")
	}
}

func TestStrictContentPrivacyCannotBeChosenOrDowngradedByJSON(t *testing.T) {
	var ordinary AgentRunRequest
	if err := json.Unmarshal([]byte(`{"strictContentPrivacy":true,"StrictContentPrivacy":true}`), &ordinary); err != nil {
		t.Fatal(err)
	}
	if ordinary.strictContentPrivacy {
		t.Fatal("JSON selected host privacy mode")
	}
	private := ordinary.WithStrictContentPrivacy()
	if err := json.Unmarshal([]byte(`{"strictContentPrivacy":false,"SuppressSessionStream":false}`), &private); err != nil {
		t.Fatal(err)
	}
	if !private.strictContentPrivacy {
		t.Fatal("JSON downgraded host privacy mode")
	}
}

type privacyRecaller struct{ calls int }

func (r *privacyRecaller) Recall(context.Context, EpisodicRecallInput) (string, error) {
	r.calls++
	return "", nil
}

func TestStrictContentPrivacyDoesNotSendPromptToEpisodicOrTmux(t *testing.T) {
	o := New(nil, newMemState(), covQuietLogger())
	recaller := &privacyRecaller{}
	o.SetEpisodicRecall(recaller)
	req := covRunReq().WithStrictContentPrivacy()
	if err := o.assembleSystemPrompt(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if recaller.calls != 0 {
		t.Fatal("private instruction sent to episodic backend")
	}
	// A nil provider would panic on the generic tmux availability probe.
	cfg, err := o.buildExecCommand(context.Background(), req, []string{"claude", "--print", "synthetic"}, nil, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(cfg.Cmd, " "), "tmux") {
		t.Fatal("private run wrapped in observable tmux")
	}
	ordinary := covRunReq()
	if err = o.assembleSystemPrompt(context.Background(), &ordinary); err != nil {
		t.Fatal(err)
	}
	if recaller.calls != 1 {
		t.Fatal("ordinary recall changed")
	}
}

func TestStrictContentPrivacySkipsToolObservers(t *testing.T) {
	o := New(nil, nil, covQuietLogger())
	skill := &fakeSkillObs{done: make(chan struct{}, 2)}
	post := &fakePostObs{done: make(chan struct{}, 2)}
	o.SetSkillInvocationObserver(skill)
	o.SetPostToolCallObserver(post)
	o.dispatchToolCallObservers(covRunReq().WithStrictContentPrivacy(), toolCallEvent("Read", map[string]any{"input": "synthetic-private"}))
	for _, ch := range []chan struct{}{skill.done, post.done} {
		select {
		case <-ch:
			t.Fatal("private input reached generic observer")
		case <-time.After(25 * time.Millisecond):
		}
	}
	o.dispatchToolCallObservers(covRunReq(), toolCallEvent("Read", map[string]any{"input": "ordinary"}))
	for _, ch := range []chan struct{}{skill.done, post.done} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("ordinary observer stopped")
		}
	}
}

func TestStrictContentPrivacyErrorsContainNoChildDiagnostic(t *testing.T) {
	for _, opts := range []covRunOpts{
		{stream: "synthetic-private-stderr\n", agentExit: 1},
		{stream: `{"type":"result","is_error":true,"result":"synthetic-private-refusal"}` + "\n"},
	} {
		o := New(covNewRunContainer(opts), newMemState(), covQuietLogger())
		journal := &covJournal{}
		o.SetJournal(journal)
		err := o.RunAgent(context.Background(), covRunReq().WithStrictContentPrivacy(), nil)
		if !errors.Is(err, ErrPrivateRunUnqualified) || strings.Contains(err.Error(), "synthetic-private") {
			t.Fatalf("private diagnostic: %v", err)
		}
		for _, entry := range journal.byType("exec.command") {
			raw, _ := json.Marshal(entry)
			if strings.Contains(string(raw), "synthetic-private") {
				t.Fatal("private terminal diagnostic persisted in exec command journal")
			}
		}
	}
}
