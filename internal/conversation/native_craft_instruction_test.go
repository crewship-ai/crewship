package conversation

import (
	"strings"
	"testing"
)

const markedInstruction = "Confidential merger instruction"

// #5009: the transcript is the one read that keeps a native Craft instruction.
// Every other read leaves it out, so a new reader is safe by default.
func TestReadsLeaveOutNativeCraftInstructionByDefault(t *testing.T) {
	s := newSearchStore(t)
	for _, m := range []Message{
		{ID: "first", AgentID: "agentA", Role: RoleUser, Content: "ordinary merger question"},
		{ID: "marked", AgentID: "agentA", Role: RoleUser, Content: markedInstruction, NativeCraftInstruction: true},
		{ID: "last", AgentID: "agentA", Role: RoleAssistant, Content: "ordinary merger answer"},
	} {
		if err := s.Append(ctx, "sess", m); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(msgs []Message) string {
		var got []string
		for _, m := range msgs {
			got = append(got, m.ID)
		}
		return strings.Join(got, ",")
	}
	read, err := s.Read(ctx, "sess", 0, 0)
	if err != nil || ids(read) != "first,last" {
		t.Errorf("Read = %q, %v", ids(read), err)
	}
	limited, err := s.Read(ctx, "sess", 0, 2)
	if err != nil || ids(limited) != "first,last" {
		t.Errorf("a marked message used up the Read limit: %q, %v", ids(limited), err)
	}
	tail, err := s.ReadTail(ctx, "sess", 2)
	if err != nil || ids(tail) != "first,last" {
		t.Errorf("a marked message used up the ReadTail window: %q, %v", ids(tail), err)
	}
	shared, err := s.ReadShared(ctx, "sess", 1<<20, 100)
	if err != nil || ids(shared) != "first,last" {
		t.Errorf("ReadShared = %q, %v", ids(shared), err)
	}
	if hits, err := s.Search(ctx, "agentA", "merger", 10); err != nil || len(hits) != 2 {
		t.Errorf("Search = %+v, %v", hits, err)
	}
	transcript, err := s.ReadTranscript(ctx, "sess")
	if err != nil || ids(transcript) != "first,marked,last" {
		t.Errorf("the transcript lost the instruction: %q, %v", ids(transcript), err)
	}
}
