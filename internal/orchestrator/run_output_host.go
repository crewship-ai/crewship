package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/runoutput"
)

// ErrHostCaptureUnconfirmed means only the controller observer was lost. The
// protected helper may still execute; callers must retain occupancy and must
// never promote it to a terminal failure, cancellation or retry permission.
var ErrHostCaptureUnconfirmed = errors.New("protected host capture requires reattachment")

type hostCaptureProvider interface {
	PrepareHostCapture(context.Context, provider.ExecConfig, string, []string) (string, json.RawMessage, error)
	RestoreHostCapture(runoutput.HostInput, string) (runoutput.HostExecutor, io.Closer, error)
}

func (o *Orchestrator) startHostExec(ctx context.Context, req AgentRunRequest, run RunState, cfg provider.ExecConfig, secrets []string) (*provider.ExecResult, error) {
	capability, ok := o.container.(hostCaptureProvider)
	if !ok {
		return nil, errors.New("container provider does not support protected host capture")
	}
	if run.Output == nil || run.Output.Version != 2 || !ValidRunID(run.ID) {
		return nil, errors.New("invalid protected host run")
	}
	directory := filepath.Dir(run.Output.Directory)
	if err := os.MkdirAll(req.HostOutputDir, 0700); err != nil {
		return nil, err
	}
	endpoint, authority, err := capability.PrepareHostCapture(ctx, cfg, directory, []string{"/usr/local/bin/crewship-sidecar", "run-stop-process"})
	if err != nil {
		return nil, err
	}
	o.mu.RLock()
	capacityBytes, capacityRuns := o.hostOutputCapacityBytes, o.hostOutputCapacityRuns
	o.mu.RUnlock()
	input := runoutput.HostInput{CapacityBytes: capacityBytes, CapacityRuns: capacityRuns, Directory: directory, Deadline: run.Output.Deadline, MaxBytes: runoutput.DefaultLimit, Endpoint: endpoint, Authority: authority, Secrets: secrets,
		Command: runoutput.HostCommand{ContainerID: cfg.ContainerID, User: cfg.User, Args: cfg.Cmd, Env: cfg.Env, WorkingDir: cfg.WorkingDir}}
	if cfg.Stdin != nil {
		input.Command.Stdin, err = io.ReadAll(io.LimitReader(cfg.Stdin, (8<<20)+1))
		if err != nil || len(input.Command.Stdin) > 8<<20 {
			return nil, errors.New("host capture stdin exceeds limit")
		}
	}
	atomic, ok := o.state.(provider.AtomicStateProvider)
	if !ok {
		return nil, errors.New("protected host capture requires atomic state")
	}
	if err = atomic.Update(ctx, "agent_runs", run.ID, func(raw []byte) ([]byte, error) {
		var current RunState
		if len(raw) == 0 || json.Unmarshal(raw, &current) != nil || current.Status != "running" || !SameRecoveredIdentity(current, run) {
			return nil, errors.New("host capture run identity changed before creation")
		}
		current.Output.Authority = authority
		current.Output.Endpoint = endpoint
		return json.Marshal(current)
	}); err != nil {
		return nil, err
	}
	run.Output.Authority = authority
	run.Output.Endpoint = endpoint
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	pid, err := runoutput.StartHost(ctx, input, []string{executable, "--internal-run-capture"}, []string{})
	if err != nil {
		if pid != 0 {
			return nil, fmt.Errorf("%w: private helper handoff failed", ErrHostCaptureUnconfirmed)
		}
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var identity runoutput.HostIdentity
	for {
		identity, err = runoutput.ReadHostIdentity(directory)
		if err == nil {
			break
		}
		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf("%w: provider identity unavailable", ErrHostCaptureUnconfirmed)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if identity.ContainerID != req.ContainerID {
		return nil, fmt.Errorf("%w: provider identity mismatched", ErrHostCaptureUnconfirmed)
	}
	if err = atomic.Update(ctx, "agent_runs", run.ID, func(raw []byte) ([]byte, error) {
		var current RunState
		if len(raw) == 0 || json.Unmarshal(raw, &current) != nil || current.Status != "running" || current.Output == nil || current.Output.Version != 2 || current.ContainerID != req.ContainerID || !SameRecoveredIdentity(current, run) {
			return nil, errors.New("host capture run changed after creation")
		}
		current.ExecID = identity.ExecID
		return json.Marshal(current)
	}); err != nil {
		return nil, fmt.Errorf("%w: provider identity persistence failed", ErrHostCaptureUnconfirmed)
	}
	reader, writer := io.Pipe()
	go func() {
		decoder, decodeErr := runoutput.NewLineDecoder(16*1024*1024, func(line runoutput.Line) error {
			// stderr remains available through retained raw output. It must
			// never impersonate a structured CLI result in the live parser.
			if line.Stream != "stdout" {
				return nil
			}
			_, err := writer.Write(append(line.Data, '\n'))
			return err
		})
		if decodeErr == nil {
			decodeErr = runoutput.Follow(ctx, run.Output.Directory, 0, decoder.Push)
		}
		_ = writer.CloseWithError(decodeErr)
	}()
	return &provider.ExecResult{ExecID: identity.ExecID, Reader: reader}, nil
}

func hostRunDirectory(run RunState) (string, error) {
	if run.Output == nil || run.Output.Version != 2 || !ValidRunID(run.ID) || !filepath.IsAbs(run.Output.Directory) || filepath.Clean(run.Output.Directory) != run.Output.Directory || filepath.Base(run.Output.Directory) != "output" || filepath.Base(filepath.Dir(run.Output.Directory)) != run.ID {
		return "", errors.New("invalid protected host location")
	}
	return filepath.Dir(run.Output.Directory), nil
}

func (o *Orchestrator) hostRunObserver(run RunState) (runoutput.HostExecutor, io.Closer, error) {
	directory, err := o.validatedHostRunDirectory(run)
	if err != nil {
		return nil, nil, err
	}
	identity, err := runoutput.ReadHostIdentity(directory)
	if err != nil {
		return nil, nil, err
	}
	if identity.ContainerID != run.ContainerID || !identity.Deadline.Equal(run.Output.Deadline) || (run.ExecID != "" && identity.ExecID != run.ExecID) {
		return nil, nil, errors.New("protected host provider identity changed")
	}
	capability, ok := o.container.(hostCaptureProvider)
	if !ok {
		return nil, nil, errors.New("provider cannot inspect protected host execution")
	}
	return capability.RestoreHostCapture(runoutput.HostInput{Directory: directory, Endpoint: run.Output.Endpoint, Authority: run.Output.Authority, Command: runoutput.HostCommand{ContainerID: run.ContainerID, User: "1001:1001"}}, identity.ExecID)
}

// hostRunAt resolves the durable launch mode before legacy tmux probing. A
// failed state read is uncertainty, not permission to claim legacy absence.
func (o *Orchestrator) hostRunAt(ctx context.Context, location RunLocation) (RunState, bool, error) {
	if o.state == nil {
		return RunState{}, false, nil
	}
	raw, err := o.state.Get(ctx, "agent_runs", location.RunID)
	if err != nil {
		return RunState{}, false, err
	}
	if len(raw) == 0 {
		return RunState{}, false, nil
	}
	var run RunState
	if err = json.Unmarshal(raw, &run); err != nil {
		return run, false, err
	}
	if run.Output == nil || run.Output.Version != 2 {
		return run, false, nil
	}
	if run.ID != location.RunID || run.ContainerID != location.ContainerID || run.AgentSlug != location.AgentSlug {
		return run, true, errors.New("protected host runtime location mismatched")
	}
	return run, true, nil
}

func (o *Orchestrator) probeHostRun(ctx context.Context, run RunState, stop bool) (bool, error) {
	directory, err := o.validatedHostRunDirectory(run)
	if err != nil {
		return false, err
	}
	if snapshot, err := runoutput.Inspect(ctx, filepath.Join(directory, "output")); err == nil && snapshot.Complete {
		if snapshot.Result.Reason == "exited" {
			return stop, nil
		}
		if absent, err := o.containerRuntimeAbsent(ctx, run.ContainerID); err == nil && absent {
			return stop, nil
		}
		if stop {
			return false, errors.New("forced leader termination does not prove descendant absence")
		}
		return true, nil
	}
	observer, closer, err := o.hostRunObserver(run)
	if err != nil {
		return false, err
	}
	defer closer.Close()
	identity, err := runoutput.ReadHostIdentity(directory)
	if err != nil {
		return false, err
	}
	if stop {
		if err = observer.Stop(ctx, identity.ExecID); err != nil {
			return false, err
		}
	}
	running, _, err := observer.Inspect(ctx, identity.ExecID)
	if err != nil {
		return false, err
	}
	if stop {
		if !running {
			return false, errors.New("leader exit does not prove descendant absence")
		}
		return false, nil
	}
	// Process exit before terminal capture does not imply the output/result is
	// recovered; keep occupancy until protected terminal evidence is durable.
	return true, nil
}

// SetHostOutputRoot selects protected transport only through server-owned
// configuration. Request fields cannot change this root or escape its run ID.
func (o *Orchestrator) SetHostOutputRoot(root string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.hostOutputRoot = root
}

// SetHostOutputCapacity selects an aggregate protected spool budget. Zero
// values use runoutput defaults; invalid limits fail admission before launch.
func (o *Orchestrator) SetHostOutputCapacity(bytes int64, runs int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.hostOutputCapacityBytes, o.hostOutputCapacityRuns = bytes, runs
}

func (o *Orchestrator) configureHostOutput(req *AgentRunRequest) error {
	o.mu.RLock()
	root := o.hostOutputRoot
	o.mu.RUnlock()
	if root == "" {
		return nil
	}
	capable, ok := o.container.(interface{ SupportsHostCapture() bool })
	if runtime.GOOS != "linux" || !ok || !capable.SupportsHostCapture() {
		return nil
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("invalid configured protected host output root")
	}
	req.HostOutputDir = root
	return nil
}

// Qualify only after execution admission: even the bounded trusted helper
// probe consumes a container exec. Unsupported existing runtimes retain the
// legacy transport; operational inspection errors refuse this launch.
func (o *Orchestrator) qualifyHostOutput(ctx context.Context, req *AgentRunRequest) error {
	if req.HostOutputDir == "" {
		return nil
	}
	qualifier, ok := o.container.(interface {
		QualifyHostCapture(context.Context, string) (bool, error)
	})
	if !ok {
		req.HostOutputDir = ""
		return nil
	}
	qualified, err := qualifier.QualifyHostCapture(ctx, req.ContainerID)
	if err != nil {
		return fmt.Errorf("qualify protected host capture: %w", err)
	}
	if !qualified {
		req.HostOutputDir = ""
	}
	return nil
}

func (o *Orchestrator) validatedHostRunDirectory(run RunState) (string, error) {
	directory, err := hostRunDirectory(run)
	if err != nil {
		return "", err
	}
	o.mu.RLock()
	root := o.hostOutputRoot
	o.mu.RUnlock()
	if root == "" || directory != filepath.Join(root, run.ID) {
		return "", errors.New("retained host output is outside configured root")
	}
	identity, err := runoutput.ReadHostIdentity(directory)
	if err != nil {
		return "", err
	}
	if identity.ContainerID != run.ContainerID || !identity.Deadline.Equal(run.Output.Deadline) || (run.ExecID != "" && identity.ExecID != run.ExecID) {
		return "", errors.New("retained host provider identity mismatched")
	}
	return directory, nil
}

func (o *Orchestrator) ReplayRetainedRun(ctx context.Context, run RunState, project func(runoutput.Line) error, complete func(runoutput.Snapshot) error) (runoutput.Snapshot, error) {
	directory, err := o.validatedHostRunDirectory(run)
	if err != nil {
		return runoutput.Snapshot{}, err
	}
	return runoutput.ReplayHost(ctx, directory, 16*1024*1024, project, complete)
}

// AcknowledgeRetainedRunResult is called only after downstream history and the
// work ledger have committed. It never races a locally owned RunAgent or uses
// leader-only forced exit as permission to release runtime occupancy.
func (o *Orchestrator) AcknowledgeRetainedRunResult(ctx context.Context, run RunState, snapshot runoutput.Snapshot, authoritativeStatus ...string) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if !snapshot.Complete {
		return errors.New("cannot acknowledge incomplete retained output")
	}
	actual, err := o.ReadRetainedRunResult(ctx, run)
	if err != nil {
		return err
	}
	if !actual.Complete || actual.Sequence != snapshot.Sequence || actual.Result == nil || *actual.Result != *snapshot.Result {
		return errors.New("retained terminal acknowledgement mismatched source")
	}
	if snapshot.Result.Reason != "exited" {
		absent, err := o.containerRuntimeAbsent(ctx, run.ContainerID)
		if err != nil || !absent {
			return fmt.Errorf("%w: forced process end lacks whole-runtime absence", ErrHostCaptureUnconfirmed)
		}
	}
	status := ""
	if len(authoritativeStatus) > 1 {
		return errors.New("ambiguous retained authoritative status")
	}
	if len(authoritativeStatus) == 1 {
		switch authoritativeStatus[0] {
		case "COMPLETED":
			status = "completed"
		case "FAILED", "TIMEOUT":
			status = "error"
		case "CANCELLED":
			status = "cancelled"
		default:
			return errors.New("invalid retained authoritative status")
		}
	}
	analysis, analysisErr := o.AnalyzeRetainedRun(ctx, run)
	if status == "" || status == "completed" {
		if analysisErr != nil {
			return analysisErr
		}
		if analysis.Unknown {
			return fmt.Errorf("%w: CLI terminal semantics unknown", ErrHostCaptureUnconfirmed)
		}
		if status == "completed" && analysis.Failed {
			return errors.New("authoritative success contradicts retained failure")
		}
	}

	o.runRecoveryMu.Lock()
	defer o.runRecoveryMu.Unlock()
	if _, owned := o.agentRuns.Load(run.ID); owned {
		return fmt.Errorf("%w: local invocation still owns outcome", ErrHostCaptureUnconfirmed)
	}
	atomic, ok := o.state.(provider.AtomicStateProvider)
	if !ok {
		return errors.New("retained outcome acknowledgement requires atomic state")
	}
	return atomic.Update(ctx, "agent_runs", run.ID, func(raw []byte) ([]byte, error) {
		if len(raw) == 0 {
			return nil, errors.New("retained run disappeared before acknowledgement")
		}
		var current RunState
		if err := json.Unmarshal(raw, &current); err != nil {
			return nil, err
		}
		if !SameRecoveredIdentity(current, run) {
			return nil, errors.New("retained run changed before acknowledgement")
		}
		if current.Status != "running" && status == "" {
			return raw, nil
		}
		current.Output.RetainedResult = &snapshot
		current.Status = status
		if current.Status == "" {
			current.Status = "error"
			if snapshot.Result.Reason == "exited" && snapshot.Result.ExitCode == 0 && !analysis.Failed {
				current.Status = "completed"
			}
		}
		current.LastActivity = time.Now()
		return json.Marshal(current)
	})
}

func (o *Orchestrator) CollectRetainedRun(ctx context.Context, run RunState, cutoff time.Time) (bool, error) {
	directory, err := hostRunDirectory(run)
	if err != nil {
		return false, err
	}
	o.mu.RLock()
	root := o.hostOutputRoot
	o.mu.RUnlock()
	if root == "" || directory != filepath.Join(root, run.ID) {
		return false, errors.New("retained collection outside configured root")
	}
	if run.Output.Collected {
		return false, nil
	}
	collected, err := runoutput.CollectHost(ctx, directory, cutoff)
	if err != nil {
		return false, err
	}
	if !collected {
		if _, sourceErr := os.Lstat(filepath.Join(directory, "output")); sourceErr == nil {
			return false, nil
		} else if !errors.Is(sourceErr, os.ErrNotExist) {
			return false, sourceErr
		}
		complete, receiptErr := runoutput.HostCollectionComplete(ctx, directory)
		if receiptErr != nil {
			return false, receiptErr
		}
		if !complete {
			return false, nil
		}
	}

	o.runRecoveryMu.Lock()
	defer o.runRecoveryMu.Unlock()
	atomic, ok := o.state.(provider.AtomicStateProvider)
	if !ok {
		return false, errors.New("retained collection acknowledgement requires atomic state")
	}
	err = atomic.Update(ctx, "agent_runs", run.ID, func(raw []byte) ([]byte, error) {
		if raw == nil {
			return nil, nil
		}
		var current RunState
		if err := json.Unmarshal(raw, &current); err != nil {
			return nil, err
		}
		if !SameRecoveredIdentity(current, run) {
			return nil, errors.New("retained collection identity changed")
		}
		current.Output.Collected = true
		return json.Marshal(current)
	})
	return err == nil, err
}
