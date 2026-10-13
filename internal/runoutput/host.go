package runoutput

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/scrubber"
)

const maxHostInputBytes = 16 << 20
const DefaultHostCapacityBytes int64 = 1024 * 1024 * 1024
const DefaultHostCapacityRuns = 4096
const hostReservationAllowance int64 = 64 * 1024

var ErrHostCapacity = errors.New("protected host output capacity exhausted")

// HostCommand contains the complete admitted execution request. The host helper
// consumes this through a private pipe; it is never placed in workload storage.
type HostCommand struct {
	ContainerID string   `json:"container_id"`
	User        string   `json:"user"`
	Args        []string `json:"args"`
	Env         []string `json:"env"`
	WorkingDir  string   `json:"working_dir"`
	Stdin       []byte   `json:"stdin,omitempty"`
}

type HostInput struct {
	CapacityBytes int64           `json:"capacity_bytes,omitempty"`
	CapacityRuns  int             `json:"capacity_runs,omitempty"`
	Authority     json.RawMessage `json:"authority"`
	Secrets       []string        `json:"secrets,omitempty"`
	Directory     string          `json:"directory"`
	Deadline      time.Time       `json:"deadline"`
	MaxBytes      int64           `json:"max_bytes"`
	Endpoint      string          `json:"endpoint"`
	Command       HostCommand     `json:"command"`
}

// HostExecution exposes separate streams so fragments from stderr cannot
// corrupt a structured stdout line. The execution ID comes from the provider.
type HostExecution struct {
	ID     string
	Stdout io.ReadCloser
	Stderr io.ReadCloser
}

type HostExecutor interface {
	Start(context.Context, HostCommand) (*HostExecution, error)
	Inspect(context.Context, string) (bool, int, error)
	// Stop must target only this execution, never its whole shared container.
	Stop(context.Context, string) error
}

type HostIdentity struct {
	Version     int       `json:"version"`
	ContainerID string    `json:"container_id"`
	ExecID      string    `json:"exec_id"`
	Deadline    time.Time `json:"deadline"`
}

func (input HostInput) validate() error {
	if !filepath.IsAbs(input.Directory) || filepath.Clean(input.Directory) != input.Directory || input.Command.ContainerID == "" || input.Command.User != "1001:1001" || len(input.Command.Args) == 0 || input.Command.Args[0] == "" || !input.Deadline.After(time.Now()) {
		return errors.New("invalid host capture identity, command or deadline")
	}
	if input.MaxBytes < terminalReserve+chunkBytes*2 || input.MaxBytes > MaxLimit {
		return errors.New("invalid host capture limit")
	}
	return nil
}

// ReadHostInput consumes the entire private handoff before execution. An
// interrupted/oversized/ambiguous input refuses creation rather than retrying.
func ReadHostInput(reader io.Reader) (HostInput, error) {
	var input HostInput
	data, err := io.ReadAll(io.LimitReader(reader, maxHostInputBytes+1))
	if err != nil {
		return input, err
	}
	if len(data) > maxHostInputBytes {
		return input, errors.New("host capture input exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&input); err != nil {
		return input, err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return input, errors.New("trailing host capture input")
	}
	return input, input.validate()
}

// StartHost claims the run identity and hands the admitted input to an
// independent helper process. Cancelling the caller only interrupts handoff;
// it never signals the helper or an already created execution. Any uncertain
// handoff leaves the claim intact and must be reconciled, never relaunched.
// helperArgv names the host-native trusted executable and its private mode.
// helperEnv is explicit: the server's unrelated credentials are not inherited.
func StartHost(ctx context.Context, input HostInput, helperArgv, helperEnv []string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := input.validate(); err != nil {
		return 0, err
	}
	if len(helperArgv) == 0 || !filepath.IsAbs(helperArgv[0]) {
		return 0, errors.New("absolute host helper required")
	}
	if err := validateHostDirectory(filepath.Dir(input.Directory)); err != nil {
		return 0, err
	}
	data, err := json.Marshal(input)
	if err != nil || len(data) > maxHostInputBytes {
		return 0, errors.New("host capture input exceeds limit")
	}
	if err = reserveHostCapacity(ctx, input); err != nil {
		return 0, err
	}
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	defer readPipe.Close()
	defer writePipe.Close()
	cmd := exec.Command(helperArgv[0], helperArgv[1:]...)
	cmd.Env = append([]string{}, helperEnv...)
	cmd.Stdin = readPipe
	if err = configureHostHelper(cmd); err != nil {
		return 0, err
	}
	if err = cmd.Start(); err != nil {
		return 0, err
	}
	_ = readPipe.Close()
	pid := cmd.Process.Pid
	// Reap without tying the child's life to the controller. The child receives
	// neither controller stdout/stderr nor a death signal when the controller exits.
	go func() { _ = cmd.Wait() }()
	stop := context.AfterFunc(ctx, func() { _ = writePipe.Close() })
	_, err = writePipe.Write(data)
	stop()
	closeErr := writePipe.Close()
	if err != nil || closeErr != nil {
		return pid, errors.Join(err, closeErr)
	}
	return pid, nil
}

// CaptureHost is the protected host writer. Provider outcome, rather than a
// workload-writable exit file or stream EOF, supplies its terminal evidence.
func CaptureHost(ctx context.Context, input HostInput, executor HostExecutor) (Result, error) {
	result := Result{ExitCode: -1, Reason: "start_error"}
	if err := input.validate(); err != nil {
		return result, err
	}
	if executor == nil {
		return result, errors.New("missing host executor")
	}
	if err := validateHostDirectory(input.Directory); err != nil {
		return result, err
	}
	store, err := Create(filepath.Join(input.Directory, "output"), input.MaxBytes)
	if err != nil {
		return result, err
	}
	defer store.Close()
	runCtx, cancel := context.WithDeadline(ctx, input.Deadline)
	defer cancel()
	execution, err := executor.Start(runCtx, input.Command)
	if err != nil {
		// A provider may have created work before an attach failure. No trusted
		// execution identity is available, so no terminal result can be invented.
		return result, fmt.Errorf("host exec creation unconfirmed: %w", err)
	}
	if execution == nil || execution.ID == "" || execution.Stdout == nil || execution.Stderr == nil {
		return result, errors.New("invalid host execution transport")
	}
	defer execution.Stdout.Close()
	defer execution.Stderr.Close()
	var stopOnce sync.Once
	var stopErr error
	stopExecution := func() error {
		stopOnce.Do(func() {
			stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer stopCancel()
			stopErr = executor.Stop(stopCtx, execution.ID)
		})
		return stopErr
	}
	identity := HostIdentity{Version: 1, ContainerID: input.Command.ContainerID, ExecID: execution.ID, Deadline: input.Deadline}
	if err = writeHostIdentity(input.Directory, identity); err != nil {
		return result, errors.Join(err, stopExecution())
	}
	var mu sync.Mutex
	var outputErr error
	var readers sync.WaitGroup
	readers.Add(2)
	copyStream := func(name string, reader io.Reader) {
		defer readers.Done()
		scrub := scrubber.New()
		writer := hostStreamWriter{store: store, stream: name, scrub: scrubber.NewStreamScrubber(scrub)}
		writer.scrub.AddSecretValues(input.Secrets...)
		_, copyErr := io.Copy(writer, reader)
		if copyErr == nil {
			_, copyErr = store.Write(name, []byte(writer.scrub.Flush()))
		}
		if copyErr != nil {
			mu.Lock()
			if outputErr == nil {
				outputErr = copyErr
			}
			mu.Unlock()
			cancel()
		}
	}
	go copyStream("stdout", execution.Stdout)
	go copyStream("stderr", execution.Stderr)
	drained := make(chan struct{})
	go func() { readers.Wait(); close(drained) }()
	forcedOutputClosure := false
	select {
	case <-drained:
	case <-runCtx.Done():
		forcedOutputClosure = true
		err = stopExecution()
		_ = execution.Stdout.Close()
		_ = execution.Stderr.Close()
		<-drained
	}
	// Cancellation may race with a fully drained stream. It still represents
	// the execution's own deadline/stop, so request stop before inspecting.
	if runCtx.Err() != nil && err == nil {
		err = stopExecution()
	}
	inspectCtx, inspectCancel := context.WithDeadline(context.WithoutCancel(ctx), input.Deadline.Add(10*time.Second))
	defer inspectCancel()
	for {
		running, code, inspectErr := executor.Inspect(inspectCtx, execution.ID)
		if inspectErr != nil {
			return result, errors.Join(err, inspectErr, stopExecution())
		}
		if !running {
			if code < 0 || code > 255 {
				return result, errors.New("invalid provider terminal code")
			}
			result.ExitCode, result.Reason = code, "exited"
			break
		}
		select {
		case <-inspectCtx.Done():
			return result, errors.Join(err, inspectCtx.Err())
		case <-runCtx.Done():
			if err == nil {
				err = stopExecution()
			}
			// Avoid spinning on an already cancelled context while waiting for the
			// provider to confirm that the stop request actually took effect.
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-timer.C:
			case <-inspectCtx.Done():
				timer.Stop()
				return result, errors.Join(err, inspectCtx.Err())
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	mu.Lock()
	captureErr := outputErr
	mu.Unlock()
	stopRequested, stopReadErr := HostStopRequested(input.Directory)
	if stopReadErr != nil {
		return result, stopReadErr
	}
	switch {
	case errors.Is(captureErr, ErrLimit):
		result.Reason = "output_limit"
	case captureErr != nil:
		result.Reason = "output_error"
	case (runCtx.Err() != nil && (forcedOutputClosure || result.ExitCode != 0)) || (stopRequested && result.ExitCode != 0):
		result.Reason = "execution_cancelled"
	}
	if errors.Is(captureErr, ErrLimit) {
		captureErr = nil
	}
	finishErr := store.Finish(result.ExitCode, result.Reason)
	var reservationErr error
	if finishErr == nil {
		reservationErr = shrinkHostReservation(context.WithoutCancel(ctx), input.Directory)
	}
	return result, errors.Join(err, captureErr, finishErr, reservationErr)
}

type hostStreamWriter struct {
	store  *Store
	stream string
	scrub  *scrubber.StreamScrubber
}

func (w hostStreamWriter) Write(data []byte) (int, error) {
	safe := w.scrub.Write(string(data))
	if _, err := w.store.Write(w.stream, []byte(safe)); err != nil {
		return 0, err
	}
	return len(data), nil
}

func writeHostIdentity(dir string, identity HostIdentity) error {
	data, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "identity.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	return errors.Join(writeErr, syncErr, closeErr, syncDir(dir))
}

// ReadHostIdentity loads only provider identity, never secret-bearing execution
// input. It is useful after controller restart for exact-execution stop/probes.
func ReadHostIdentity(dir string) (HostIdentity, error) {
	var identity HostIdentity
	if err := validateHostDirectory(dir); err != nil {
		return identity, err
	}
	f, err := os.Open(filepath.Join(dir, "identity.json"))
	if err != nil {
		return identity, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return identity, err
	}
	if len(data) > 4096 {
		return identity, errors.New("oversized host execution identity")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&identity); err != nil {
		return identity, err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return identity, errors.New("trailing host execution identity")
	}
	if identity.Version != 1 || identity.ContainerID == "" || identity.ExecID == "" || identity.Deadline.IsZero() {
		return identity, errors.New("invalid host execution identity")
	}
	return identity, nil
}

// reserveHostCapacity serializes capacity accounting with the exclusive run
// claim. Reservations survive helper/controller crashes and are released only
// after fully acknowledged collection, never merely because a PID vanished.
func reserveHostCapacity(ctx context.Context, input HostInput) error {
	root := filepath.Dir(input.Directory)
	release, err := lockHostReplay(ctx, root)
	if err != nil {
		return err
	}
	defer release()
	if _, err = os.Lstat(input.Directory); err == nil {
		return ErrClaimed
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	capacity, runs := input.CapacityBytes, input.CapacityRuns
	if capacity == 0 {
		capacity = DefaultHostCapacityBytes
	}
	if runs == 0 {
		runs = DefaultHostCapacityRuns
	}
	if capacity < 1 || runs < 1 {
		return ErrHostCapacity
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var reserved int64
	var count int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		f, readErr := os.Open(filepath.Join(dir, "reservation.json"))
		var data []byte
		if readErr == nil {
			data, readErr = io.ReadAll(io.LimitReader(f, 4097))
			readErr = errors.Join(readErr, f.Close())
		}
		if errors.Is(readErr, os.ErrNotExist) {
			complete, receiptErr := HostCollectionComplete(ctx, dir)
			if receiptErr == nil && complete {
				continue
			}
			return errors.New("host capacity contains an unresolved reservation")
		}
		if readErr != nil {
			return readErr
		}
		var reservation struct {
			Version int   `json:"version"`
			Bytes   int64 `json:"bytes"`
		}
		if len(data) > 4096 || json.Unmarshal(data, &reservation) != nil || reservation.Version != 1 || reservation.Bytes < 1 || reservation.Bytes > MaxLimit+hostReservationAllowance {
			return errors.New("invalid host output reservation")
		}
		if reservation.Bytes > capacity-reserved {
			return ErrHostCapacity
		}
		reserved += reservation.Bytes
		count++
	}
	if count >= runs || input.MaxBytes+hostReservationAllowance > capacity-reserved {
		return ErrHostCapacity
	}
	if err = os.Mkdir(input.Directory, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrClaimed
		}
		return err
	}
	if err = syncDir(root); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(input.Directory, "reservation.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		Version int   `json:"version"`
		Bytes   int64 `json:"bytes"`
	}{1, input.MaxBytes + hostReservationAllowance})
	if err != nil {
		f.Close()
		return err
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	return errors.Join(writeErr, syncErr, closeErr, syncDir(input.Directory))
}

// RequestHostStop records protected stop intent before the provider signals its
// exact process. It is not evidence that descendants or even the leader ended.
func RequestHostStop(dir string) error {
	if err := validateHostDirectory(dir); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "stop-requested"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		_, err = HostStopRequested(dir)
		return err
	}
	if err != nil {
		return err
	}
	_, writeErr := f.Write([]byte("requested\n"))
	syncErr := f.Sync()
	closeErr := f.Close()
	return errors.Join(writeErr, syncErr, closeErr, syncDir(dir))
}
func HostStopRequested(dir string) (bool, error) {
	if err := validateHostDirectory(dir); err != nil {
		return false, err
	}
	f, err := os.Open(filepath.Join(dir, "stop-requested"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65))
	if err != nil {
		return false, err
	}
	if string(data) != "requested\n" {
		return false, errors.New("invalid protected stop intent")
	}
	return true, nil
}

// shrinkHostReservation replaces a live worst-case reservation only after
// validating a fully fsynced terminal source. Crashed/incomplete helpers keep
// their full reservation. The allowance covers immutable identity plus later
// cursor, collection receipts and filesystem metadata.
func shrinkHostReservation(ctx context.Context, dir string) error {
	root := filepath.Dir(dir)
	release, err := lockHostReplay(ctx, root)
	if err != nil {
		return err
	}
	defer release()
	releaseRun, err := lockHostReplay(ctx, dir)
	if err != nil {
		return err
	}
	defer releaseRun()
	if err := cleanupHostTemps(dir); err != nil {
		return err
	}
	reservationPath := filepath.Join(dir, "reservation.json")
	f, err := os.Open(reservationPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} // explicit transport fixtures
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, 4097))
	closeErr := f.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		return err
	}
	var reservation struct {
		Version int   `json:"version"`
		Bytes   int64 `json:"bytes"`
	}
	if len(data) > 4096 || json.Unmarshal(data, &reservation) != nil || reservation.Version != 1 || reservation.Bytes < 1 || reservation.Bytes > MaxLimit+hostReservationAllowance {
		return errors.New("invalid host output reservation")
	}
	snapshot, err := Inspect(ctx, filepath.Join(dir, "output"))
	if err != nil {
		return err
	}
	if !snapshot.Complete {
		return errors.New("cannot shrink incomplete host reservation")
	}
	bytes := hostReservationAllowance
	for _, name := range []string{"events.jsonl", "checkpoint.json"} {
		info, err := os.Lstat(filepath.Join(dir, "output", name))
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("nonregular retained output during capacity settlement")
		}
		bytes += info.Size()
	}
	if bytes >= reservation.Bytes {
		return nil
	}
	reservation.Bytes = bytes
	data, err = json.Marshal(reservation)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".reservation-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, writeErr := temp.Write(data)
	syncErr := temp.Sync()
	closeErr = temp.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err = os.Rename(temp.Name(), reservationPath); err != nil {
		return err
	}
	return syncDir(dir)
}
