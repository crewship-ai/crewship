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

// #5009: offset and limit count the messages a reader can get. Counting file
// rows for one and visible messages for the other repeats or skips a page.
func TestReadPagesCountOnlyVisibleMessages(t *testing.T) {
	ordinary := func(id string) Message {
		return Message{ID: id, AgentID: "agentA", Role: RoleUser, Content: "ordinary " + id}
	}
	marked := func(id string) Message {
		return Message{ID: id, AgentID: "agentA", Role: RoleUser, Content: markedInstruction, NativeCraftInstruction: true}
	}
	for name, rows := range map[string][]Message{
		"marked first":   {marked("m1"), ordinary("a"), ordinary("b"), ordinary("c")},
		"marked between": {ordinary("a"), marked("m1"), ordinary("b"), marked("m2"), marked("m3"), ordinary("c")},
		"marked around":  {marked("m1"), ordinary("a"), marked("m2"), ordinary("b"), ordinary("c"), marked("m3")},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSearchStore(t)
			for _, m := range rows {
				if err := s.Append(ctx, "sess", m); err != nil {
					t.Fatal(err)
				}
			}
			for size, want := range map[int][]string{1: {"a", "b", "c"}, 2: {"a,b", "c"}} {
				var pages []string
				for offset := 0; ; offset += size {
					page, err := s.Read(ctx, "sess", offset, size)
					if err != nil {
						t.Fatal(err)
					}
					if len(page) == 0 {
						break
					}
					var got []string
					for _, m := range page {
						got = append(got, m.ID)
					}
					pages = append(pages, strings.Join(got, ","))
					if len(pages) > len(rows) {
						t.Fatalf("size %d never ended: %q", size, pages)
					}
				}
				if strings.Join(pages, "|") != strings.Join(want, "|") {
					t.Errorf("size %d pages = %q, want %q", size, pages, want)
				}
			}
			tail, err := s.ReadTail(ctx, "sess", 2)
			if err != nil || len(tail) != 2 || tail[0].ID != "b" || tail[1].ID != "c" {
				t.Errorf("ReadTail(2) = %+v, %v", tail, err)
			}
		})
	}
}
