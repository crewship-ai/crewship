package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"runtime"
	"sync"

	"github.com/grafana/pyroscope-go"
)

// blockProfileRateNS is the default ns-resolution sampling rate for the
// goroutine block profiler when pyroscope push is active. SetBlockProfile-
// Rate semantics: "average one sample per N nanoseconds spent blocked"
// — small N samples (almost) every block event, large N samples rarely.
// 10 ms balances signal (real lock-contention shows up) against the
// per-sample overhead of recording every sub-microsecond goroutine park.
const blockProfileRateNS = 10_000_000

// RedactURL strips any userinfo from a URL string so it's safe to put
// in logs. Falls back to "<unparseable url>" if the input isn't a valid
// URL — never returns the raw string with credentials intact.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<unparseable url>"
	}
	u.User = nil
	return u.String()
}

// StartPyroscopePush starts the pyroscope-go push profiler. When pushURL
// is empty the call is a no-op — this is the production default so a
// binary without a push endpoint configured stays fully runnable.
//
// When configured, the profiler ships CPU + heap + goroutines + alloc
// profiles every 10 s to the push server. Each sample carries
// service.name=crewship + a host-level tag (slot, hostname) so the
// flame-graph view can be filtered per slot.
//
// ctx drives lifecycle: when it's cancelled the profiler is stopped.
// The returned stop closure does the same thing and is idempotent — it's
// safe to call either, both, or neither.
//
// Env contract:
//
//	CREWSHIP_PYROSCOPE_URL       e.g. http://localhost:4040
//	CREWSHIP_PYROSCOPE_TAG_SLOT  e.g. dev1   (optional, defaults to hostname)
//
// Distinction from net/http/pprof: StartPProfServer (pprof.go) opens
// a pull endpoint a remote tool can sample on-demand. This pushes
// the same profiles upstream on a schedule, no inbound socket needed.
func StartPyroscopePush(ctx context.Context, pushURL string, logger *slog.Logger) (stop func(), err error) {
	if pushURL == "" {
		return func() {}, nil
	}
	if logger == nil {
		logger = slog.Default()
	}

	// Enable mutex + block profiling so the flame graph can attribute
	// "wait on a lock" time, not just "running CPU". The block-profile
	// rate is in nanoseconds — see blockProfileRateNS for the rationale.
	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(blockProfileRateNS)

	hostname, _ := os.Hostname()
	slot := os.Getenv("CREWSHIP_PYROSCOPE_TAG_SLOT")
	if slot == "" {
		slot = hostname
	}

	prof, err := pyroscope.Start(pyroscope.Config{
		ApplicationName: "crewship",
		ServerAddress:   pushURL,
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

	logger.Info("pyroscope push profiler started", "url", RedactURL(pushURL), "slot", slot)

	var once sync.Once
	stopFn := func() {
		once.Do(func() {
			_ = prof.Stop()
		})
	}

	go func() {
		<-ctx.Done()
		stopFn()
	}()

	return stopFn, nil
}

// pyroscopeLogger bridges Crewship's slog to the pyroscope-go logger
// interface so debug/info/error from the push client land in the same
// structured log stream as the rest of the binary.
type pyroscopeLogger struct{ l *slog.Logger }

func (p pyroscopeLogger) Infof(format string, args ...any)  { p.l.Info(fmt.Sprintf(format, args...)) }
func (p pyroscopeLogger) Debugf(format string, args ...any) { p.l.Debug(fmt.Sprintf(format, args...)) }
func (p pyroscopeLogger) Errorf(format string, args ...any) { p.l.Error(fmt.Sprintf(format, args...)) }
