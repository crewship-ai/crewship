package orchestrator

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/runoutput"
)

func TestRetainedOutputReaderRequiresHelperSuccessBeforeTerminal(t *testing.T) {
	for _, scenario := range []string{"success", "helper-failed", "missing-terminal", "trailing-record", "gap"} {
		t.Run(scenario, func(t *testing.T) {
			records := []runoutput.Record{{Sequence: 1, Kind: "accepted"}, {Sequence: 2, Kind: "output", Stream: "stdout", Data: []byte("output\n")}}
			if scenario != "missing-terminal" {
				records = append(records, runoutput.Record{Sequence: 3, Kind: "exit", ExitCode: 7, Reason: "exited"})
			}
			if scenario == "trailing-record" {
				records = append(records, runoutput.Record{Sequence: 4, Kind: "output", Stream: "stdout", Data: []byte("bad")})
			}
			if scenario == "gap" {
				records[1].Sequence = 5
			}
			var raw bytes.Buffer
			encoder := json.NewEncoder(&raw)
			for _, r := range records {
				if err := encoder.Encode(r); err != nil {
					t.Fatal(err)
				}
			}
			provider := &retainedResultProvider{response: raw.String()}
			if scenario == "helper-failed" {
				provider.code = 125
			}
			o := &Orchestrator{container: provider}
			run := RunState{ID: "run-1", ContainerID: "container", Output: &RunOutputState{Version: 1, Directory: "/runs/run-1/output"}}
			terminal := 0
			snapshot, err := o.ReadRetainedRunOutput(t.Context(), run, func(r runoutput.Record) error {
				if r.Kind == "exit" {
					terminal++
				}
				return nil
			})
			if scenario == "success" {
				if err != nil || terminal != 1 || snapshot.Result == nil || snapshot.Result.ExitCode != 7 {
					t.Fatalf("result=%+v terminal=%d err=%v", snapshot, terminal, err)
				}
			} else if err == nil || terminal != 0 {
				t.Fatalf("unverified terminal delivered: %d, %v", terminal, err)
			}
			if len(provider.calls) != 1 || provider.calls[0].Cmd[1] != "run-read" {
				t.Fatal("reader launched a workload")
			}
		})
	}
}
