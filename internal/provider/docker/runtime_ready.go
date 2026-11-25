package docker

import (
	"context"
	"errors"
	"os"
	"time"
)

// ErrMandatoryBindsUnreachable is what RuntimeReady reports while the daemon
// refuses the sidecar or entrypoint bind source. It names no path: readiness
// is served without a login. The full reason is logged.
var ErrMandatoryBindsUnreachable = errors.New("the container daemon cannot see the runtime files every crew container needs")

const (
	// A reachable result is trusted for this long; a confirmed rejection is
	// asked again sooner so the endpoint turns green by itself after a fix.
	bindVerifiedTTL = 30 * time.Second
	bindRejectedTTL = 10 * time.Second
	// One probe may not eat the readiness budget (3 s in the server).
	bindProbeBudget = 2 * time.Second
)

type bindVerdict struct {
	rejected bool
	at       time.Time
}

func (v *bindVerdict) fresh(now time.Time) bool {
	ttl := bindVerifiedTTL
	if v.rejected {
		ttl = bindRejectedTTL
	}
	return now.Sub(v.at) < ttl
}

func (p *Provider) serverInContainer() bool {
	if p.inContainerFn != nil {
		return p.inContainerFn()
	}
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return false
}

// verifyMandatoryBinds runs one probe and records what it proved. An
// inconclusive answer never creates a rejection; it keeps the last confirmed
// one, because only a successful probe can disprove it.
func (p *Provider) verifyMandatoryBinds(ctx context.Context) *bindVerdict {
	outcome, err := p.probeMandatoryBinds(ctx)
	if ctx.Err() != nil {
		return p.bindVerdict.Load()
	}
	previous := p.bindVerdict.Load()
	next := &bindVerdict{at: time.Now()}
	switch outcome {
	case bindProbeRejected:
		next.rejected = true
		if previous == nil || !previous.rejected {
			p.logger.Error("no crew can start on this container runtime: a mandatory bind source is unreachable from the daemon (#1706)",
				"runtime", p.detected.Runtime,
				"socket", p.detected.Socket,
				"error", err.Error(),
			)
		}
	case bindProbeReachable:
		if previous != nil && previous.rejected {
			p.logger.Info("the daemon can see the mandatory bind sources again")
		}
	default:
		if err != nil {
			p.logger.Warn("mandatory bind probe inconclusive; keeping the last confirmed state", "error", err)
		}
		next.rejected = previous != nil && previous.rejected
	}
	p.bindVerdict.Store(next)
	return next
}

// RuntimeReady reports whether crew containers can start. It is a state, not
// a boot-time event: a confirmed rejection is verified again (cached for a few
// seconds) and clears when the daemon accepts the binds. It answers only for
// a server running inside a container on an explicitly chosen docker provider,
// the one topology where the server and the daemon see different filesystems;
// native, auto-selected, rootless and remote setups are never made unready.
func (p *Provider) RuntimeReady(ctx context.Context) error {
	if !p.cfg.ReportRuntimeReadiness || !p.serverInContainer() {
		return nil
	}
	if v := p.bindVerdict.Load(); v != nil && v.fresh(time.Now()) {
		return verdictError(v)
	}
	if !p.bindProbeMu.TryLock() {
		return verdictError(p.bindVerdict.Load())
	}
	defer p.bindProbeMu.Unlock()
	if v := p.bindVerdict.Load(); v != nil && v.fresh(time.Now()) {
		return verdictError(v)
	}
	probeCtx, cancel := context.WithTimeout(ctx, bindProbeBudget)
	defer cancel()
	return verdictError(p.verifyMandatoryBinds(probeCtx))
}

func verdictError(v *bindVerdict) error {
	if v != nil && v.rejected {
		return ErrMandatoryBindsUnreachable
	}
	return nil
}
