package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/runoutput"
)

// ReadRetainedRunResult reads only the result of an already admitted run. It
// never launches or resumes a workload and never infers completion from PID
// absence. The caller must load the location from authorized durable state.
func (o *Orchestrator) ReadRetainedRunResult(ctx context.Context, run RunState) (runoutput.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return runoutput.Snapshot{}, err
	}
	dir, err := o.retainedRunDirectory(run)
	if err != nil {
		return runoutput.Snapshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	res, err := o.container.Exec(ctx, provider.ExecConfig{
		ContainerID: run.ContainerID, User: "1001:1001",
		Cmd: []string{"/usr/local/bin/crewship-sidecar", "run-result", "--dir", dir, "--timeout", "2s"},
	})
	if err != nil {
		return runoutput.Snapshot{}, err
	}
	if res == nil || res.Reader == nil {
		return runoutput.Snapshot{}, errors.New("retained result probe returned no reader")
	}
	defer res.Reader.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = res.Reader.Close() })
	defer stopClose()
	data, err := io.ReadAll(io.LimitReader(res.Reader, 4097))
	if err != nil || len(data) > 4096 {
		return runoutput.Snapshot{}, errors.New("retained result probe unreadable or oversized")
	}
	code, err := provider.WaitExecExit(ctx, o.container, res.ExecID, 2*time.Second)
	if err != nil {
		return runoutput.Snapshot{}, err
	}
	if code != 0 {
		return runoutput.Snapshot{}, fmt.Errorf("retained result probe exited %d", code)
	}
	var snapshot runoutput.Snapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&snapshot); err != nil {
		return runoutput.Snapshot{}, errors.New("invalid retained result response")
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return runoutput.Snapshot{}, errors.New("trailing retained result response")
	}
	if err = snapshot.Validate(); err != nil {
		return runoutput.Snapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return runoutput.Snapshot{}, err
	}
	return snapshot, nil
}

// RecordRetainedRunResult preserves completion evidence without publishing a
// successful run or acknowledging output which has not yet been projected.
func (o *Orchestrator) RecordRetainedRunResult(ctx context.Context, key string, expected RunState, snapshot runoutput.Snapshot) (RunState, error) {
	if key != expected.ID || !ValidRunID(expected.ID) {
		return expected, errors.New("retained result key does not match run identity")
	}
	if err := ctx.Err(); err != nil {
		return expected, err
	}
	if err := snapshot.Validate(); err != nil {
		return expected, err
	}
	if !snapshot.Complete {
		return expected, nil
	}
	o.runRecoveryMu.Lock()
	defer o.runRecoveryMu.Unlock()
	if _, owned := o.agentRuns.Load(expected.ID); owned {
		return expected, nil
	}
	raw, err := o.state.Get(ctx, "agent_runs", key)
	if err != nil {
		return expected, err
	}
	var current RunState
	if err = json.Unmarshal(raw, &current); err != nil {
		return expected, err
	}
	if current.Status != "running" || current.Output == nil || !SameRecoveredIdentity(current, expected) {
		return expected, nil
	}
	if prior := current.Output.RetainedResult; prior != nil {
		if err := prior.Validate(); err != nil {
			return expected, err
		}
		if prior.Sequence != snapshot.Sequence || prior.Result == nil || *prior.Result != *snapshot.Result {
			return expected, errors.New("retained terminal result changed")
		}
		return current, nil
	}
	current.Output.RetainedResult = &snapshot
	current.LastActivity = time.Now()
	raw, err = json.Marshal(current)
	if err != nil {
		return expected, err
	}
	if err = o.state.Set(ctx, "agent_runs", key, raw); err != nil {
		return expected, err
	}
	return current, nil
}

func (o *Orchestrator) retainedRunDirectory(run RunState) (string, error) {
	if run.Output == nil || run.Output.Version != 1 || !ValidRunID(run.ID) || run.ContainerID == "" || o.container == nil {
		return "", errors.New("retained run location unavailable")
	}
	dir := run.Output.Directory
	if !path.IsAbs(dir) || path.Clean(dir) != dir || strings.ContainsRune(dir, 0) || path.Base(dir) != "output" || path.Base(path.Dir(dir)) != run.ID {
		return "", errors.New("retained run location does not match identity")
	}
	return dir, nil
}
