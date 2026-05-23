package telemetry

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"

	"github.com/grafana/pyroscope-go"
)

// StartPyroscopePush starts the pyroscope-go push profiler. When url
// is empty the call is a no-op — this is the production default so a
// binary without a Pyroscope endpoint configured stays fully runnable.
//
// When configured, the profiler ships CPU + heap + goroutines + alloc
// profiles every 10 s to the Pyroscope server. Each sample carries
// service.name=crewship + a host-level tag (slot, hostname) so the
// Grafana flame-graph view can be filtered per slot.
//
// Returned stop closure stops the upload + flushes any in-flight
// sample; defer it next to the OTel telemetry shutdown.
//
// Env contract:
//
//	CREWSHIP_PYROSCOPE_URL       e.g. http://localhost:4040
//	CREWSHIP_PYROSCOPE_TAG_SLOT  e.g. dev1   (optional, defaults to hostname)
//
// Distinction from net/http/pprof: StartPProfServer (pprof.go) opens
// a pull endpoint a remote tool can sample on-demand. This pushes
// the same profiles upstream on a schedule, no inbound socket needed.
func StartPyroscopePush(url string, logger *slog.Logger) (stop func(), err error) {
	if url == "" {
		return func() {}, nil
	}
	if logger == nil {
		logger = slog.Default()
	}

	// Enable mutex + block profiling so the flame graph can attribute
	// "wait on a lock" time, not just "running CPU". Cheap on idle
	// workloads (1/N sample rate), invisible noise on busy ones.
	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(5)

	hostname, _ := os.Hostname()
	slot := os.Getenv("CREWSHIP_PYROSCOPE_TAG_SLOT")
	if slot == "" {
		slot = hostname
	}

	prof, err := pyroscope.Start(pyroscope.Config{
		ApplicationName: "crewship",
		ServerAddress:   url,
		Logger:          pyroscopeLogger{l: logger},
		Tags: map[string]string{
			"slot":     slot,
			"hostname": hostname,
		},
		ProfileTypes: []pyroscope.ProfileType{
			pyroscope.ProfileCPU,
			pyroscope.ProfileAllocObjects,
			pyroscope.ProfileAllocSpace,
			pyroscope.ProfileInuseObjects,
			pyroscope.ProfileInuseSpace,
			pyroscope.ProfileGoroutines,
			pyroscope.ProfileMutexCount,
			pyroscope.ProfileMutexDuration,
			pyroscope.ProfileBlockCount,
			pyroscope.ProfileBlockDuration,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("pyroscope start: %w", err)
	}

	logger.Info("pyroscope push profiler started", "url", url, "slot", slot)

	return func() {
		_ = prof.Stop()
	}, nil
}

// pyroscopeLogger bridges Crewship's slog to the pyroscope-go logger
// interface so debug/info/error from the push client land in the same
// structured log stream as the rest of the binary.
type pyroscopeLogger struct{ l *slog.Logger }

func (p pyroscopeLogger) Infof(format string, args ...any)  { p.l.Info(fmt.Sprintf(format, args...)) }
func (p pyroscopeLogger) Debugf(format string, args ...any) { p.l.Debug(fmt.Sprintf(format, args...)) }
func (p pyroscopeLogger) Errorf(format string, args ...any) { p.l.Error(fmt.Sprintf(format, args...)) }
