package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/runoutput"
)

// ReadRetainedRunOutput attaches only a reader to an admitted run. The caller
// supplies authorized durable state and must scrub/deduplicate all callbacks.
// Each attempt starts at sequence 1 to reconstruct framing and adapter state.
// Successfully delivered prefixes may repeat after any error. No terminal
// callback is released before transport EOF and verified helper exit success.
func (o *Orchestrator) ReadRetainedRunOutput(ctx context.Context, run RunState, consume func(runoutput.Record) error) (runoutput.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return runoutput.Snapshot{}, err
	}
	if consume == nil {
		return runoutput.Snapshot{}, errors.New("retained output consumer missing")
	}
	dir, err := o.retainedRunDirectory(run)
	if err != nil {
		return runoutput.Snapshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := o.container.Exec(ctx, provider.ExecConfig{ContainerID: run.ContainerID, User: "1001:1001", Cmd: []string{"/usr/local/bin/crewship-sidecar", "run-read", "--dir", dir, "--after", "0", "--timeout", "25s"}})
	if err != nil {
		return runoutput.Snapshot{}, err
	}
	if res == nil || res.Reader == nil {
		return runoutput.Snapshot{}, errors.New("retained output reader unavailable")
	}
	defer res.Reader.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = res.Reader.Close() })
	defer stopClose()
	limited := &io.LimitedReader{R: res.Reader, N: runoutput.MaxLimit + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	var sequence uint64
	var terminal *runoutput.Record
	for scanner.Scan() {
		if err = ctx.Err(); err != nil {
			return runoutput.Snapshot{}, err
		}
		var record runoutput.Record
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&record); err != nil {
			return runoutput.Snapshot{}, errors.New("invalid retained output record")
		}
		var extra any
		if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return runoutput.Snapshot{}, errors.New("trailing retained output record")
		}
		if terminal != nil || record.Sequence != sequence+1 || (sequence == 0 && record.Kind != "accepted") {
			return runoutput.Snapshot{}, errors.New("invalid retained output sequence")
		}
		sequence = record.Sequence
		switch record.Kind {
		case "accepted":
			if sequence != 1 || len(record.Data) != 0 || record.Stream != "" || record.Reason != "" || record.ExitCode != 0 {
				return runoutput.Snapshot{}, errors.New("invalid retained acceptance")
			}
		case "output":
			if (record.Stream != "stdout" && record.Stream != "stderr") || len(record.Data) == 0 || len(record.Data) > 16*1024 || record.Reason != "" || record.ExitCode != 0 {
				return runoutput.Snapshot{}, errors.New("invalid retained output payload")
			}
		case "exit":
			snapshot := runoutput.Snapshot{Version: 1, Sequence: sequence, Complete: true, Result: &runoutput.Result{ExitCode: record.ExitCode, Reason: record.Reason}}
			if len(record.Data) != 0 || record.Stream != "" {
				return runoutput.Snapshot{}, errors.New("invalid retained terminal payload")
			}
			if err = snapshot.Validate(); err != nil {
				return runoutput.Snapshot{}, err
			}
			terminal = &record
			continue
		default:
			return runoutput.Snapshot{}, errors.New("unknown retained output record")
		}
		if err = consume(record); err != nil {
			return runoutput.Snapshot{}, err
		}
	}
	if scanner.Err() != nil || limited.N <= 0 {
		return runoutput.Snapshot{}, errors.New("retained output transport incomplete or oversized")
	}
	if err = ctx.Err(); err != nil {
		return runoutput.Snapshot{}, err
	}
	code, err := provider.WaitExecExit(ctx, o.container, res.ExecID, 2*time.Second)
	if err != nil {
		return runoutput.Snapshot{}, err
	}
	if code != 0 || terminal == nil {
		return runoutput.Snapshot{}, errors.New("retained output transport did not confirm completion")
	}
	if err = ctx.Err(); err != nil {
		return runoutput.Snapshot{}, err
	}
	if err = consume(*terminal); err != nil {
		return runoutput.Snapshot{}, err
	}
	return runoutput.Snapshot{Version: 1, Sequence: sequence, Complete: true, Result: &runoutput.Result{ExitCode: terminal.ExitCode, Reason: terminal.Reason}}, nil
}
