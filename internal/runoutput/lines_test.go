package runoutput

import (
	"errors"
	"reflect"
	"testing"
)

func TestRetainedLinesReassembleStreamsAndReplayStableIdentity(t *testing.T) {
	records := []Record{
		{Sequence: 1, Kind: "accepted"},
		{Sequence: 2, Kind: "output", Stream: "stdout", Data: []byte("{\"text\":\"\xc5")},
		{Sequence: 3, Kind: "output", Stream: "stderr", Data: []byte("diagnostic\npartial error")},
		{Sequence: 4, Kind: "output", Stream: "stdout", Data: []byte("\xbe\"}\nnext\nlast")},
		{Sequence: 5, Kind: "exit", ExitCode: 0, Reason: "exited"},
	}
	var first []Line
	for attempt := 0; attempt < 2; attempt++ {
		var got []Line
		decoder, err := NewLineDecoder(1024, func(line Line) error { got = append(got, line); return nil })
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if err = decoder.Push(record); err != nil {
				t.Fatal(err)
			}
		}
		if !decoder.Complete() {
			t.Fatal("terminal not recognized")
		}
		if attempt == 0 {
			first = got
		} else if !reflect.DeepEqual(first, got) {
			t.Fatal("reconnect changed event identities")
		}
	}
	if len(first) != 5 || string(first[1].Data) != "{\"text\":\"ž\"}" || first[1].Sequence != 4 || first[2].Index != 1 || first[3].Stream != "stdout" || first[4].Stream != "stderr" {
		t.Fatalf("lines=%+v", first)
	}
}

func TestRetainedLinesDoNotFlushIncompletePrefix(t *testing.T) {
	count := 0
	decoder, _ := NewLineDecoder(64, func(Line) error { count++; return nil })
	if err := decoder.Push(Record{Sequence: 1, Kind: "accepted"}); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Push(Record{Sequence: 2, Kind: "output", Stream: "stdout", Data: []byte("half a secret")}); err != nil {
		t.Fatal(err)
	}
	if count != 0 || decoder.Complete() {
		t.Fatal("incomplete output published or acknowledged as complete")
	}
}

func TestRetainedLinesFailClosedOnGapLimitAndCallbackError(t *testing.T) {
	for _, scenario := range []string{"gap", "limit", "callback"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			sentinel := errors.New("sink unavailable")
			decoder, _ := NewLineDecoder(4, func(Line) error { calls++; return sentinel })
			if err := decoder.Push(Record{Sequence: 1, Kind: "accepted"}); err != nil {
				t.Fatal(err)
			}
			record := Record{Sequence: 2, Kind: "output", Stream: "stdout", Data: []byte("line\n")}
			if scenario == "gap" {
				record.Sequence = 3
			}
			if scenario == "limit" {
				record.Data = []byte("12345")
			}
			err := decoder.Push(record)
			if err == nil {
				t.Fatal("invalid input/sink accepted")
			}
			if scenario == "callback" && !errors.Is(err, sentinel) {
				t.Fatal("sink error lost")
			}
			before := calls
			if err = decoder.Push(Record{Sequence: 3, Kind: "exit", Reason: "exited"}); err == nil || calls != before || decoder.Complete() {
				t.Fatal("failed replay continued")
			}
		})
	}
}
