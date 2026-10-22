// crewship-gridcraft-worker serves a fixed private listener inside the offline image.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/documents"
)

var errEngineEscaped = errors.New("engine process outlived its candidate")

// Concurrent candidates per worker. The default fits the documented container
// profile (256 MiB, 32 PIDs); a higher value needs the larger profile listed in
// docker/gridcraft/README.md. The hard bound keeps one environment's worker
// from being configured past what one container can hold.
const (
	capacityEnv           = "CREWSHIP_GRIDCRAFT_WORKER_CAPACITY"
	defaultWorkerCapacity = 2
	maxWorkerCapacity     = 8
)

func workerCapacity(getenv func(string) string) (int, error) {
	value := getenv(capacityEnv)
	if value == "" {
		return defaultWorkerCapacity, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > maxWorkerCapacity {
		return 0, fmt.Errorf("%s must be an integer from 1 to %d", capacityEnv, maxWorkerCapacity)
	}
	return n, nil
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == appengine.EngineSandboxFlag {
		// Internal launcher mode: confine this process, then exec the engine.
		return appengine.ExecEngineSandbox(os.Args[2:])
	}
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		client, err := appengine.NewWorkerClient("/run/worker/service.sock", "/run/secrets/service-token")
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return client.Health(ctx)
	}
	if len(os.Args) != 1 {
		return fmt.Errorf("invalid worker arguments")
	}

	capacity, err := workerCapacity(os.Getenv)
	if err != nil {
		return err
	}
	// Refuse to serve unless every engine execution can be confined and every
	// engine descendant is reparented to this process for the survivor sweep.
	if err = appengine.PrepareEngineSandbox(); err != nil {
		return err
	}
	secret, err := documents.ReadServiceSecret("/run/secrets/service-token")
	if err != nil {
		return err
	}
	escaped := make(chan struct{})
	var escapeOnce sync.Once
	engine := appengine.WorkerEngine{
		Binary:  "/usr/local/bin/gridcraft-cli",
		Scratch: "/scratch",
		Escaped: func() { escapeOnce.Do(func() { close(escaped) }) },
	}
	// Hash the pinned CLI once; candidates then only confirm its identity.
	if err = engine.Verify(); err != nil {
		return err
	}
	handler, err := appengine.NewWorkerHandler(secret, engine, capacity)
	if err != nil {
		return err
	}
	stop, err := documents.ServePrivateUnix("/run/worker/service.sock", handler)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	return serve(ctx, escaped, stop)
}

// serve runs until shutdown or an engine escape. After an escape the handler
// is already quarantined; the worker then exits non-zero so the supervisor
// replaces the container, which ends its PID namespace and every process in it.
func serve(ctx context.Context, escaped <-chan struct{}, stop func(context.Context) error) error {
	select {
	case <-ctx.Done():
		drain, done := context.WithTimeout(context.Background(), 25*time.Second)
		defer done()
		return stop(drain)
	case <-escaped:
		drain, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		_ = stop(drain)
		return errEngineEscaped
	}
}

func main() {
	if err := run(); err != nil {
		if errors.Is(err, errEngineEscaped) {
			fmt.Fprintln(os.Stderr, "workbook worker stopping: engine process outlived its candidate")
		} else {
			fmt.Fprintln(os.Stderr, "workbook worker unavailable")
		}
		os.Exit(1)
	}
}
