package logging

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// levelController owns the process-wide runtime log level. The slog handler
// built by New reads its LevelVar (a slog.Leveler), so SetLevel changes
// verbosity on the LIVE logger without a restart — the whole point of an
// operator debug toggle: flip to debug on a misbehaving instance, catch the
// repro, flip back, all without losing state to a restart.
//
// An override can carry a TTL so a forgotten debug switch auto-reverts to the
// configured baseline instead of firehosing the logs indefinitely (which, on
// a busy instance, is its own disk-fill risk).
type levelController struct {
	mu       sync.Mutex
	lv       *slog.LevelVar
	baseline slog.Level // the configured level to revert to
	timer    *time.Timer
	expires  time.Time
	// gen fences stale TTL callbacks. time.Timer.Stop() does NOT stop an
	// already-fired callback goroutine that's blocked on c.mu — so a revert
	// timer whose SetLevel has been superseded could otherwise acquire the
	// lock late and clobber the newer override back to baseline. Each
	// set/reset bumps gen; a callback captures its gen and no-ops if it no
	// longer matches.
	gen uint64
}

// ctrl is the single process-wide controller. There is exactly one root
// logger per process (slog.SetDefault), so a package-level owner is the
// simplest correct home — the admin handler calls SetLevel directly with no
// dependency plumbing.
var ctrl = newLevelController()

func newLevelController() *levelController {
	c := &levelController{lv: new(slog.LevelVar), baseline: slog.LevelInfo}
	c.lv.Set(slog.LevelInfo)
	return c
}

// setBaseline records the configured level and applies it when no runtime
// override is active. New calls this for each logger it builds, so the last
// configured level (the real server logger, after the bootstrap one) wins as
// the revert target.
func (c *levelController) setBaseline(l slog.Level) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseline = l
	if c.timer == nil { // no active override → follow the config
		c.lv.Set(l)
	}
}

// set overrides the live level, cancelling any prior override. A positive ttl
// that differs from the baseline schedules an automatic revert. Returns the
// level that was active before this call.
func (c *levelController) set(l slog.Level, ttl time.Duration) slog.Level {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev := c.lv.Level()
	c.lv.Set(l)
	c.stopTimerLocked()
	c.gen++
	if ttl > 0 && l != c.baseline {
		gen := c.gen
		c.expires = time.Now().Add(ttl)
		c.timer = time.AfterFunc(ttl, func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.gen != gen {
				return // superseded by a newer set/reset while this timer fired
			}
			c.lv.Set(c.baseline)
			c.stopTimerLocked()
		})
	}
	return prev
}

// reset drops any override and returns to the baseline immediately.
func (c *levelController) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lv.Set(c.baseline)
	c.gen++
	c.stopTimerLocked()
}

func (c *levelController) stopTimerLocked() {
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.expires = time.Time{}
}

func (c *levelController) state() (current, baseline slog.Level, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lv.Level(), c.baseline, c.expires
}

// SetLevel overrides the process log level at runtime. level is one of
// debug|info|warn|error. A positive ttl auto-reverts to the configured
// baseline after it elapses (0 = until the next explicit change). Returns the
// previously-active level string.
func SetLevel(level string, ttl time.Duration) (previous string, err error) {
	l, ok := parseLevelStrict(level)
	if !ok {
		return "", fmt.Errorf("unknown log level %q (want debug|info|warn|error)", level)
	}
	return levelString(ctrl.set(l, ttl)), nil
}

// ResetLevel drops any runtime override and returns to the configured level.
func ResetLevel() { ctrl.reset() }

// LevelState reports the live level, the configured baseline it reverts to,
// and the override expiry (zero when no timed override is active).
func LevelState() (current, baseline string, expiresAt time.Time) {
	cur, base, exp := ctrl.state()
	return levelString(cur), levelString(base), exp
}

// parseLevelStrict is parseLevel without the default-to-info fallback: an
// unknown string returns ok=false so the API can reject it with a 400 rather
// than silently applying info.
func parseLevelStrict(s string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error", "fatal":
		return slog.LevelError, true
	default:
		return slog.LevelInfo, false
	}
}

// levelString renders a slog.Level as the canonical lowercase name.
func levelString(l slog.Level) string {
	switch {
	case l <= slog.LevelDebug:
		return "debug"
	case l < slog.LevelWarn:
		return "info"
	case l < slog.LevelError:
		return "warn"
	default:
		return "error"
	}
}
