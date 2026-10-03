package orchestrator

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/runoutput"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

func TestRetainedEventDecoderReplaysSplitSecretsWithoutDuplicateIdentity(t *testing.T) {
	secret := "synthetic-historical-secret"
	records := []runoutput.Record{{Sequence: 1, Kind: "accepted"}}
	for i, part := range []string{"synthetic-historical-", "secret"} {
		raw, _ := json.Marshal(map[string]any{"type": "text", "sessionID": "same", "part": map[string]any{"id": "part-" + string(rune('a'+i)), "text": part}})
		records = append(records, runoutput.Record{Sequence: uint64(i + 2), Kind: "output", Stream: "stdout", Data: append(raw, '\n')})
	}
	records = append(records, runoutput.Record{Sequence: 4, Kind: "exit", Reason: "exited"})
	run := RunState{ID: "replay-run", StartedAt: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), Output: &RunOutputState{Version: 1, Adapter: "OPENCODE"}}
	o := New(nil, newMemState(), opencodeTestLogger())
	var first []RetainedEvent
	for attempt := 0; attempt < 2; attempt++ {
		var events []RetainedEvent
		d, err := o.NewRetainedEventDecoder(run, []string{secret}, func(e RetainedEvent) error { events = append(events, e); return nil })
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if err = d.Push(record); err != nil {
				t.Fatal(err)
			}
		}
		if !d.Complete() {
			t.Fatal("complete source not decoded")
		}
		raw, _ := json.Marshal(events)
		if strings.Contains(string(raw), secret) {
			t.Fatal("split historical credential disclosed")
		}
		if len(events) == 0 {
			t.Fatal("all output lost")
		}
		if attempt == 0 {
			first = events
		} else if !reflect.DeepEqual(first, events) {
			t.Fatal("replay changed event identity or content")
		}
	}
}

func TestRetainedEventDecoderDoesNotFlushOnIncompleteSource(t *testing.T) {
	o := New(nil, newMemState(), opencodeTestLogger())
	run := RunState{ID: "replay-run", StartedAt: time.Now(), Output: &RunOutputState{Version: 1, Adapter: "OPENCODE"}}
	count := 0
	d, err := o.NewRetainedEventDecoder(run, nil, func(RetainedEvent) error { count++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []runoutput.Record{{Sequence: 1, Kind: "accepted"}, {Sequence: 2, Kind: "output", Stream: "stdout", Data: []byte(`{"type":"text"`)}} {
		if err = d.Push(r); err != nil {
			t.Fatal(err)
		}
	}
	if count != 0 || d.Complete() {
		t.Fatal("incomplete output released")
	}
}

func TestRetainedMetadataScrubsNestedValuesAndPreservesNumbers(t *testing.T) {
	s := scrubber.New()
	s.AddSecretValues("synthetic-old-token")
	value, err := scrubRetainedMetadata(s, map[string]any{"input": []any{map[string]any{"token": "synthetic-old-token"}}, "count": int64(9007199254740993)})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(value)
	if strings.Contains(string(raw), "synthetic-old-token") || !strings.Contains(string(raw), "9007199254740993") {
		t.Fatal("metadata leaked or lost precision")
	}
}

func TestRetainedEventDecoderSinkFailurePreventsCompletion(t *testing.T) {
	o := New(nil, newMemState(), opencodeTestLogger())
	run := RunState{ID: "replay-run", StartedAt: time.Now(), Output: &RunOutputState{Version: 1, Adapter: "OPENCODE"}}
	stopped := errors.New("sink failed")
	d, _ := o.NewRetainedEventDecoder(run, nil, func(RetainedEvent) error { return stopped })
	_ = d.Push(runoutput.Record{Sequence: 1, Kind: "accepted"})
	_ = d.Push(runoutput.Record{Sequence: 2, Kind: "output", Stream: "stderr", Data: []byte("diagnostic")})
	if err := d.Push(runoutput.Record{Sequence: 3, Kind: "exit", Reason: "exited"}); !errors.Is(err, stopped) || d.Complete() {
		t.Fatal("failed sink acknowledged terminal output")
	}
}
