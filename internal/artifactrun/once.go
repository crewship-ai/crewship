package artifactrun

import (
	"sync"
	"time"
)

// onceLimit bounds Once: past it, entries older than MaxTTL are dropped, and
// if that is not enough the set starts over. Forgetting only means a
// duplicate journal entry; it never admits anything.
const onceLimit = 4096

// Once remembers keys (a run, a schedule fire) for at most MaxTTL so a
// process journals one event per key even before the asynchronous journal
// row lands. The zero value is ready to use.
type Once struct {
	mu   sync.Mutex
	seen map[string]time.Time
	now  func() time.Time // nil: time.Now
}

// First reports whether key is new, and remembers it.
func (o *Once) First(key string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	if o.now != nil {
		now = o.now()
	}
	if o.seen == nil {
		o.seen = map[string]time.Time{}
	}
	if at, ok := o.seen[key]; ok && now.Sub(at) < MaxTTL {
		return false
	}
	if len(o.seen) >= onceLimit {
		for k, at := range o.seen {
			if now.Sub(at) >= MaxTTL {
				delete(o.seen, k)
			}
		}
		if len(o.seen) >= onceLimit {
			o.seen = map[string]time.Time{}
		}
	}
	o.seen[key] = now
	return true
}

// Forget drops key, for a run that ended.
func (o *Once) Forget(key string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.seen, key)
}

// Len is the number of remembered keys.
func (o *Once) Len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.seen)
}
