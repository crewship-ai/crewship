package runoutput

import (
	"context"
	"errors"
)

// Snapshot describes the validated committed prefix, not process liveness.
// Complete=false may mean a live capture, a failed supervisor, or a claim
// interrupted before completion. It never authorizes relaunching the command.
type Snapshot struct {
	Version  int     `json:"version"`
	Sequence uint64  `json:"sequence"`
	Complete bool    `json:"complete"`
	Result   *Result `json:"result,omitempty"`
}

func (s Snapshot) Validate() error {
	if s.Version != 1 || s.Sequence == 0 || s.Complete != (s.Result != nil) {
		return errors.New("invalid retained result envelope")
	}
	if s.Result != nil {
		if s.Sequence < 2 || s.Result.ExitCode < -1 || s.Result.ExitCode > 255 || len(s.Result.Reason) > 512 {
			return errors.New("invalid retained terminal result")
		}
		switch s.Result.Reason {
		case "exited":
			if s.Result.ExitCode < 0 {
				return errors.New("invalid normal exit code")
			}
		case "signal":
			if s.Result.ExitCode != -1 {
				return errors.New("invalid signal exit code")
			}
		case "start_error", "execution_cancelled", "output_limit", "output_error", "output_incomplete":
		default:
			return errors.New("unknown retained terminal reason")
		}
	}
	return nil
}

// Inspect reads a bounded committed log without exposing command output. A
// malformed or unreadable store returns an error, never a successful result.
func Inspect(ctx context.Context, dir string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	var result *Result
	sequence, complete, err := ReadCommitted(dir, 0, func(record Record) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if record.Kind == "exit" {
			result = &Result{ExitCode: record.ExitCode, Reason: record.Reason}
		}
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{Version: 1, Sequence: sequence, Complete: complete, Result: result}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}
