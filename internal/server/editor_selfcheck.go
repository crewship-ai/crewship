package server

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/editororigin"
)

// editorProxyCheckSchedule spaces the same-host editor's self-check while
// no target answers: soon after a target is known, then backing off (the
// proxy may come up after Crewship).
var editorProxyCheckSchedule = []time.Duration{3 * time.Second, 15 * time.Second, time.Minute, 5 * time.Minute, time.Hour}

// editorProxyCheckPassedEvery repeats the check once every target passed,
// so a later proxy change is noticed. editorProxyCheckFailedEvery repeats it
// while a target failed or is disabled, so a fixed proxy (or the end of a
// maintenance window) brings the editor back after
// editororigin.ReenableAfterPasses checks.
var (
	editorProxyCheckPassedEvery = time.Hour
	editorProxyCheckFailedEvery = 5 * time.Minute
)

// editorProxyCheckConfirm is how long the self-check waits before repeating
// a check that found an isolation failure. Only a repeated failure disables
// the editor, so one transient redirect (a maintenance page) does not.
var editorProxyCheckConfirm = 30 * time.Second

// newEditorProxyCheckClient dials the public Studio URL directly, without an
// egress proxy from the environment, whose own answers would be mistaken
// for the deployment's.
func newEditorProxyCheckClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Timeout: 10 * time.Second, Transport: transport}
}

// startEditorProxyCheck verifies, through each public Studio URL browsers
// use, that nothing in front of Crewship redirects or rewrites the same-host
// editor prefix. The targets are the configured Studio origin, or the
// origins learned from signed-in browser requests: until a browser has used
// Studio there is nothing to probe and the state stays pending, never
// passed. A confirmed isolation failure disables the editor on that origin
// until editororigin.ReenableAfterPasses consecutive checks pass; an
// unreachable URL decides nothing, because many deployments cannot reach
// their own public name from inside. `crewship doctor` runs the same check
// from the operator's side.
func startEditorProxyCheck(ctx context.Context, wg *sync.WaitGroup, h *editororigin.Handler, logger *slog.Logger, client *http.Client, schedule []time.Duration) {
	if h == nil || h.Detail().Mode != "same-host" || len(schedule) == 0 {
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		backoff := 0
		delay := schedule[0]
		// Without a target there is nothing to wait for but a learned origin.
		waiting := len(h.CheckTargets()) == 0
		for {
			var timer *time.Timer
			var tick <-chan time.Time
			if !waiting {
				timer = time.NewTimer(delay)
				tick = timer.C
			}
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case <-h.Wake():
				// A newly learned origin is checked soon, not at the next
				// interval. At most a handful of origins are ever learned.
				if timer != nil {
					timer.Stop()
				}
				waiting, delay = false, schedule[0]
				continue
			case <-tick:
			}
			targets := h.CheckTargets()
			if len(targets) == 0 {
				waiting = true
				continue
			}
			answered := false
			for _, origin := range targets {
				c := checkEditorOrigin(ctx, client, origin)
				if ctx.Err() != nil {
					return
				}
				before := originDisabled(h, origin)
				h.RecordCheck(origin, c)
				after := originDisabled(h, origin)
				switch c.Status {
				case editororigin.ProxyCheckUnreachable:
					logger.Warn("gridcraft editor (same host): public URL not reachable for the proxy self-check; run `crewship doctor` from outside", "origin", origin, "detail", c.Warnings)
					continue
				case editororigin.ProxyCheckFailed:
					if c.Isolation {
						logger.Error("gridcraft editor (same host) disabled on this origin: a proxy redirects or rewrites /_editor/", "origin", origin, "cause", c.Cause, "problems", c.Problems)
					} else {
						logger.Warn("gridcraft editor (same host): /_editor/ does not work through the public URL", "origin", origin, "cause", c.Cause, "problems", c.Problems)
					}
				default:
					if before && !after {
						logger.Info("gridcraft editor (same host) re-enabled: the proxy self-check passed repeatedly", "origin", origin)
					}
					if len(c.Warnings) > 0 {
						logger.Warn("gridcraft editor (same host) proxy self-check", "origin", origin, "warnings", c.Warnings)
					}
				}
				answered = true
			}
			switch {
			case !answered:
				backoff = min(backoff+1, len(schedule)-1)
				delay = schedule[backoff]
			case h.Health() == editororigin.HealthOK:
				backoff, delay = 0, editorProxyCheckPassedEvery
			default:
				backoff, delay = 0, editorProxyCheckFailedEvery
			}
		}
	}()
}

// checkEditorOrigin runs CheckProxy; an isolation failure is checked again
// after editorProxyCheckConfirm and only a repeated one stands.
func checkEditorOrigin(ctx context.Context, client *http.Client, origin string) editororigin.ProxyCheck {
	c := editororigin.CheckProxy(ctx, client, origin)
	if !c.Isolation {
		return c
	}
	confirm := time.NewTimer(editorProxyCheckConfirm)
	defer confirm.Stop()
	select {
	case <-ctx.Done():
		return c
	case <-confirm.C:
	}
	if again := editororigin.CheckProxy(ctx, client, origin); again.Status != editororigin.ProxyCheckUnreachable {
		return again
	}
	return c
}

func originDisabled(h *editororigin.Handler, origin string) bool {
	for _, o := range h.Detail().Origins {
		if o.Origin == origin {
			return o.Disabled
		}
	}
	return false
}
