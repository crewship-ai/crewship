package server

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/editororigin"
)

// editorProxyCheckSchedule spaces the same-host editor's self-check: soon
// after start, then backing off while the public URL does not answer (the
// proxy may come up after Crewship). Once a check gets an answer it repeats
// at the last interval, so a later proxy change is noticed.
var editorProxyCheckSchedule = []time.Duration{3 * time.Second, 15 * time.Second, time.Minute, 5 * time.Minute, time.Hour}

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

// startEditorProxyCheck verifies, through the public Studio URL, that nothing
// in front of Crewship redirects or normalises the same-host editor prefix.
// A failure that defeats the path-scoped isolation disables the editor
// (editororigin.Handler.SetProxyCheck); an unreachable URL decides nothing,
// because many deployments cannot reach their own public name from inside.
// `crewship doctor` runs the same check from the operator's side.
func startEditorProxyCheck(ctx context.Context, wg *sync.WaitGroup, h *editororigin.Handler, studio string, logger *slog.Logger, client *http.Client, schedule []time.Duration) {
	if h == nil || h.Status().Mode != "same-host" || len(schedule) == 0 {
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			delay := schedule[min(i, len(schedule)-1)]
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			c := editororigin.CheckProxy(ctx, client, studio)
			if c.Isolation {
				confirm := time.NewTimer(editorProxyCheckConfirm)
				select {
				case <-ctx.Done():
					confirm.Stop()
					return
				case <-confirm.C:
				}
				if again := editororigin.CheckProxy(ctx, client, studio); again.Status != editororigin.ProxyCheckUnreachable {
					c = again
				}
			}
			if ctx.Err() != nil {
				return
			}
			switch c.Status {
			case editororigin.ProxyCheckUnreachable:
				if h.Status().ProxyCheck == editororigin.ProxyCheckPending {
					logger.Warn("gridcraft editor (same host): public URL not reachable for the proxy self-check; run `crewship doctor` from outside", "url", studio, "detail", c.Warnings)
				}
				continue
			case editororigin.ProxyCheckFailed:
				h.SetProxyCheck(c)
				if c.Isolation {
					logger.Error("gridcraft editor (same host) disabled: a proxy redirects or rewrites /_editor/", "problems", c.Problems)
				} else {
					logger.Warn("gridcraft editor (same host): /_editor/ does not reach Crewship through the public URL", "problems", c.Problems)
				}
			default:
				h.SetProxyCheck(c)
				if len(c.Warnings) > 0 {
					logger.Warn("gridcraft editor (same host) proxy self-check", "warnings", c.Warnings)
				}
			}
			if i < len(schedule)-1 {
				i = len(schedule) - 2 // answered: continue at the last interval
			}
		}
	}()
}
