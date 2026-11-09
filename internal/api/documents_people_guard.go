package api

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// The people endpoint's in-process limits (#5155 review round 4). The @-mention
// picker asks on every keystroke, so each person gets a token bucket per
// workspace, and an administrator's audited role-only lookups are recorded
// once per Document and credential within a window instead of once per
// keystroke. Both maps are bounded: a full rate map refuses new people until
// idle buckets expire, and a full audit map stops deduplicating, so the
// failure mode is always "audit again", never "audit less".
const (
	documentPeopleRate        = rate.Limit(2) // lookups per second, sustained
	documentPeopleBurst       = 30
	documentPeopleIdle        = 2 * time.Minute
	documentPeopleAuditWindow = time.Hour
	documentPeopleMaxEntries  = 10000
)

type documentPeopleGuard struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[string]*documentPeopleBucket
	audited map[string]time.Time
}

type documentPeopleBucket struct {
	limiter *rate.Limiter
	used    time.Time
}

// documentPeople is the handler's guard, created on first use so a test
// handler owns its own buckets.
func (h *PageHandler) documentPeople() *documentPeopleGuard {
	h.peopleOnce.Do(func() {
		h.peopleGuard = &documentPeopleGuard{now: time.Now, buckets: map[string]*documentPeopleBucket{}, audited: map[string]time.Time{}}
	})
	return h.peopleGuard
}

// take spends one lookup for user in workspace; a positive wait is a refusal
// that spends nothing.
func (g *documentPeopleGuard) take(workspace, user string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	key := workspace + "\x00" + user
	bucket := g.buckets[key]
	if bucket == nil {
		if len(g.buckets) >= documentPeopleMaxEntries {
			for k, b := range g.buckets {
				if now.Sub(b.used) >= documentPeopleIdle {
					delete(g.buckets, k)
				}
			}
		}
		if len(g.buckets) >= documentPeopleMaxEntries {
			return documentPeopleIdle
		}
		bucket = &documentPeopleBucket{limiter: rate.NewLimiter(documentPeopleRate, documentPeopleBurst)}
		g.buckets[key] = bucket
	}
	bucket.used = now
	reservation := bucket.limiter.ReserveN(now, 1)
	if delay := reservation.DelayFrom(now); delay > 0 {
		reservation.CancelAt(now)
		return delay
	}
	return 0
}

func (g *documentPeopleGuard) auditedRecently(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	at, ok := g.audited[key]
	return ok && g.now().Sub(at) < documentPeopleAuditWindow
}

func (g *documentPeopleGuard) rememberAudit(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if len(g.audited) >= documentPeopleMaxEntries {
		for k, at := range g.audited {
			if now.Sub(at) >= documentPeopleAuditWindow {
				delete(g.audited, k)
			}
		}
	}
	if len(g.audited) < documentPeopleMaxEntries {
		g.audited[key] = now
	}
}
