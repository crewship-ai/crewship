package update

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// SystemdService is the production ServiceManager: it drives a systemd unit via
// `systemctl`. The command runner is injectable so the wiring stays testable
// without a live init system.
type SystemdService struct {
	// Unit is the systemd unit name (e.g. "crewship" or "crewship.service").
	Unit string
	// run executes a systemctl subcommand; nil uses the real systemctl.
	run func(ctx context.Context, args ...string) error
}

// NewSystemdService returns a SystemdService for the given unit, erroring if
// systemctl isn't on PATH (so `--server` fails fast on a non-systemd host with
// an actionable message rather than a cryptic exec error mid-upgrade).
func NewSystemdService(unit string) (*SystemdService, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil, fmt.Errorf("systemctl not found on PATH: --server manages a systemd service, " +
			"which this host doesn't appear to run. Upgrade the binary directly with 'crewship self-update', " +
			"then restart your service however it's managed")
	}
	return &SystemdService{Unit: unit}, nil
}

func (s *SystemdService) systemctl(ctx context.Context, args ...string) error {
	if s.run != nil {
		return s.run(ctx, args...)
	}
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *SystemdService) Stop(ctx context.Context) error {
	return s.systemctl(ctx, "stop", s.Unit)
}

func (s *SystemdService) Start(ctx context.Context) error {
	return s.systemctl(ctx, "start", s.Unit)
}

// HTTPHealthChecker returns a HealthChecker that polls url until it answers
// 2xx, giving up after timeout. interval is the gap between probes. Used to
// confirm the freshly started server is actually serving before declaring the
// upgrade a success.
func HTTPHealthChecker(url string, timeout, interval time.Duration) HealthChecker {
	if interval <= 0 {
		interval = time.Second
	}
	return func(ctx context.Context) error {
		deadline := time.Now().Add(timeout)
		var lastErr error
		for {
			reqCtx, cancel := context.WithTimeout(ctx, interval)
			req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
			if err != nil {
				cancel()
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					cancel()
					return nil
				}
				lastErr = fmt.Errorf("health endpoint %s returned HTTP %d", url, resp.StatusCode)
			} else {
				lastErr = err
			}
			cancel()

			if time.Now().After(deadline) {
				if lastErr == nil {
					lastErr = fmt.Errorf("timed out")
				}
				return fmt.Errorf("server not healthy within %s: %w", timeout, lastErr)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interval):
			}
		}
	}
}
