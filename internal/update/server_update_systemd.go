package update

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
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

// UnitEnvPort returns the CREWSHIP_PORT the unit runs with, read from systemd's
// resolved environment for the unit (`systemctl show -p Environment`), or 0 if
// it can't be determined. This is how `self-update --systemd` learns the port
// the server actually listens on even when `sudo` scrubbed CREWSHIP_PORT from
// its own environment.
func (s *SystemdService) UnitEnvPort(ctx context.Context) int {
	var out []byte
	var err error
	if s.run != nil {
		// Test seam: the injected runner can't return stdout, so a mocked
		// SystemdService reports "unknown" (0). Real lookups go through exec.
		return 0
	}
	out, err = exec.CommandContext(ctx, "systemctl", "show", s.Unit, "--property=Environment").Output()
	if err != nil {
		return 0
	}
	return parseCrewshipPort(string(out))
}

// parseCrewshipPort extracts CREWSHIP_PORT from the `Environment=` line printed
// by `systemctl show -p Environment` (space-separated KEY=VALUE pairs on a
// single "Environment=..." line). Returns 0 when absent or unparseable.
func parseCrewshipPort(showOutput string) int {
	for _, line := range strings.Split(showOutput, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "Environment=")
		if !ok {
			continue
		}
		for _, kv := range strings.Fields(rest) {
			if v, ok := strings.CutPrefix(kv, "CREWSHIP_PORT="); ok {
				if p, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && p > 0 {
					return p
				}
			}
		}
	}
	return 0
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
