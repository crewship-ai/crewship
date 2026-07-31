package api

// One mechanism for the admin routes that spend a model call (#1575).
//
// Two admin surfaces call a model because an operator pressed something: the
// aux probe (`POST /admin/keeper/aux/{slot}/probe`, delegated whole to the
// judge handler) and the manual Reviews trigger
// (`POST /admin/keeper/review/{slot}/run`). Both are gated by roleManage, which
// bounds WHO may press them and says nothing about how often — and "how often"
// is the half that shows up on an invoice.
//
// The probe grew a bucket when it shipped; the run route did not, and inherited
// only the general authed-mutation limiter. Rather than give it a second,
// differently-shaped brake, this file holds the one both use. What varies
// between them is only the numbers:
//
//	judge probe   6/minute, burst = the configured value
//	review run   60/hour,   burst = one pass over the four evaluators
//
// Instance-wide, not per IP: the thing being rationed is the daemon's spend,
// not a client's share of it. The rate is re-read from the ratelimitcfg
// registry on every call, so an operator override applies without a restart.

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/ratelimitcfg"
	"golang.org/x/time/rate"
)

// spendLimiter is an instance-wide token bucket over a ratelimitcfg key.
type spendLimiter struct {
	// key is the registry entry holding "how many per window".
	key string
	// per is the window that number is expressed in (a minute, an hour).
	per time.Duration
	// burst is the fixed bucket depth. Zero means "use the configured value",
	// which is the judge probe's historical shape (6/min, burst 6). A non-zero
	// burst decouples "how big a press can be" from "how much may be spent
	// over time" — the review trigger needs a burst of four (one pass over the
	// evaluators) with a sustained rate far below four per minute.
	burst int

	mu  sync.Mutex
	lim *rate.Limiter
}

func newSpendLimiter(key string, per time.Duration, burst int) *spendLimiter {
	return &spendLimiter{key: key, per: per, burst: burst}
}

// take consumes one unit. It returns (true, 0) when the caller may spend, and
// (false, wait) when it may not — wait being how long until the bucket would
// have accepted this request, so the refusal can name a time instead of saying
// "later". Nothing is consumed on refusal: the reservation is cancelled, so a
// rejected caller does not push the queue further out for the next one.
func (s *spendLimiter) take() (bool, time.Duration) {
	n := ratelimitcfg.Int(s.key)
	if n < 1 {
		// Defensive: the registry bounds forbid it, but a limiter reading zero
		// is a route that is off rather than limited.
		n = 1
	}
	burst := s.burst
	if burst < 1 {
		burst = n
	}
	limit := rate.Limit(float64(n) / s.per.Seconds())

	s.mu.Lock()
	if s.lim == nil {
		s.lim = rate.NewLimiter(limit, burst)
	} else {
		s.lim.SetLimit(limit)
		s.lim.SetBurst(burst)
	}
	lim := s.lim
	s.mu.Unlock()

	now := time.Now()
	res := lim.ReserveN(now, 1)
	if !res.OK() {
		// Burst smaller than the request — unreachable with n>=1, but a
		// silently-allowed spend is the wrong way to be wrong.
		return false, s.per
	}
	if wait := res.DelayFrom(now); wait > 0 {
		res.CancelAt(now)
		return false, wait
	}
	return true, 0
}

// replyRateLimited writes the 429 an operator can act on: what tripped, why the
// cap is there, and when the next attempt will be accepted. reason is a
// complete sentence (or two); the retry clause is appended.
//
// The Retry-After header carries the same number for anything that is not a
// person — the CLI prints the server's message verbatim, a script should back
// off on the header rather than parse prose.
func replyRateLimited(w http.ResponseWriter, reason string, wait time.Duration) {
	secs := int(math.Ceil(wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	replyError(w, http.StatusTooManyRequests, reason+" Try again in "+retryPhrase(secs)+".")
}

// retryPhrase spells a wait the way somebody reading an error would say it.
// Rounds up: telling a person to retry in 59s and refusing them again is worse
// than a second of slack.
func retryPhrase(secs int) string {
	if secs < 60 {
		return strconv.Itoa(secs) + "s"
	}
	mins := (secs + 59) / 60
	if mins == 1 {
		return "a minute"
	}
	return strconv.Itoa(mins) + " minutes"
}
