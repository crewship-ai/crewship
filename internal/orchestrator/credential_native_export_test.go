package orchestrator

import (
	"context"
	"fmt"
	"github.com/crewship-ai/crewship/internal/provider"
	"io"
	"log/slog"
	"testing"
	"time"
)

// Test-only seams for the external-package CLI/API/Vault acceptance. These
// symbols do not exist in production builds or expose a new product API.
var NativeCredentialScriptForTest = buildCredFileScript
var NativeCredentialEnvForTest = appendCredentialFileEnv
var NativeSSHFixtureForTest = startNativeSSHFixture

// Exercise the same batch/flush boundary as production run setup. The observer
// checks the argument boundary and confirms the actual exec settled; it never
// writes or transforms credentials itself.
func NativeCredentialWriteForTest(ctx context.Context, ctr provider.ContainerProvider, containerID, slug string, creds []Credential, directory, shared string, keeper bool, logger *slog.Logger) error {
	observed := &nativeCredentialBatchObserver{ContainerProvider: ctr}
	batch := newPreflightBatch(observed, containerID, "1001:1001", logger)
	if err := writeCredentialFiles(ctx, batch, containerID, slug, creds, directory, shared, keeper, logger); err != nil {
		return err
	}
	if err := batch.Flush(ctx); err != nil {
		return err
	}
	if batch.stepFailed(preflightStepCredentials) || observed.execID == "" {
		return fmt.Errorf("credential fixture batch was not confirmed")
	}
	code, err := provider.WaitExecExit(ctx, ctr, observed.execID, time.Second)
	if err != nil || code != 0 {
		return fmt.Errorf("credential fixture batch exit unconfirmed: code=%d error=%v", code, err)
	}
	return nil
}

type nativeCredentialBatchObserver struct {
	provider.ContainerProvider
	execID string
}

func (o *nativeCredentialBatchObserver) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if len(cfg.Cmd) != 1 || cfg.Cmd[0] != "sh" || cfg.Stdin == nil || cfg.User != "1001:1001" {
		return nil, fmt.Errorf("credential fixture bypassed production stdin batch")
	}
	result, err := o.ContainerProvider.Exec(ctx, cfg)
	if err != nil {
		return nil, err
	}
	o.execID = result.ExecID
	reader := result.Reader
	result.Reader = &nativeCredentialBatchReader{ReadCloser: reader, stop: context.AfterFunc(ctx, func() { reader.Close() })}
	return result, nil
}

type nativeCredentialBatchReader struct {
	io.ReadCloser
	stop func() bool
}

func (r *nativeCredentialBatchReader) Close() error { r.stop(); return r.ReadCloser.Close() }

func TestNativeCredentialFixtureUsesProductionBatchStdin(t *testing.T) {
	ctr := &scriptedContainer{output: preflightDoneMarker, exitCode: 0}
	err := NativeCredentialWriteForTest(context.Background(), ctr, "fixture", "fixture", []Credential{{ID: "fixture", Type: "GENERIC_SECRET", EnvVarName: "NATIVE_FIXTURE_FILE", PlainValue: "synthetic-fixture"}}, "/secrets/fixture", "", true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if !ctr.sawStdin {
		t.Fatal("fixture used credential-bearing argv instead of production batch stdin")
	}
}
