package api

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/crewship-ai/crewship/internal/ratelimitcfg"
)

const sharedSearchAccounts = 4096
const sharedSearchAudiences = 257 // 256 valid project rights plus the all-project sentinel.
const sharedSearchIdle = 5 * time.Minute

type sharedSearchKey [32]byte
type sharedSearchAudience [33]byte // explicit kind byte plus a fixed-width identity digest.
type sharedSearchBucket struct {
	limiter *rate.Limiter
	used    time.Time
}
type sharedSearchAccount struct {
	aggregate *rate.Limiter
	audiences map[sharedSearchAudience]*sharedSearchBucket
	used      time.Time
}
type sharedSearchLimiter struct {
	mu                        sync.Mutex
	accounts                  map[sharedSearchKey]*sharedSearchAccount
	now                       func() time.Time
	maxAccounts, maxAudiences int
}

func newSharedSearchLimiter() *sharedSearchLimiter {
	return &sharedSearchLimiter{accounts: make(map[sharedSearchKey]*sharedSearchAccount), now: time.Now, maxAccounts: sharedSearchAccounts, maxAudiences: sharedSearchAudiences}
}

// take is called only after authentication, input checks and explicit project List.
// A single lock and clock instant make both reservations atomic. Refusals spend
// neither budget. Query text, pagination, IP and credentials never create keys.
func (s *sharedSearchLimiter) take(ctx context.Context, user, workspace, project string) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return false, 0
	}
	now := s.now()
	idle := sharedSearchExpiry(ratelimitcfg.Int(ratelimitcfg.KeySharedSearchAccount), ratelimitcfg.Int(ratelimitcfg.KeySharedSearchProject))
	key := sharedSearchIdentity(user, workspace)
	account := s.accounts[key]
	if account != nil && now.Sub(account.used) >= idle {
		delete(s.accounts, key)
		account = nil
	}
	if account == nil {
		if len(s.accounts) >= s.maxAccounts {
			for k, a := range s.accounts {
				if now.Sub(a.used) >= idle {
					delete(s.accounts, k)
				}
			}
		}
		if len(s.accounts) >= s.maxAccounts {
			return false, time.Minute
		}
		account = &sharedSearchAccount{aggregate: rate.NewLimiter(searchRate(ratelimitcfg.KeySharedSearchAccount), 15), audiences: make(map[sharedSearchAudience]*sharedSearchBucket)}
		s.accounts[key] = account
	}
	account.used = now
	audienceKey := sharedSearchAudienceKey(project)
	bucket := account.audiences[audienceKey]
	if bucket != nil && now.Sub(bucket.used) >= idle {
		delete(account.audiences, audienceKey)
		bucket = nil
	}
	if bucket == nil {
		if len(account.audiences) >= s.maxAudiences {
			for k, b := range account.audiences {
				if now.Sub(b.used) >= idle {
					delete(account.audiences, k)
				}
			}
		}
		if len(account.audiences) >= s.maxAudiences {
			return false, time.Minute
		}
		bucket = &sharedSearchBucket{limiter: rate.NewLimiter(searchRate(ratelimitcfg.KeySharedSearchProject), 5)}
		account.audiences[audienceKey] = bucket
	}
	bucket.used = now
	account.aggregate.SetLimitAt(now, searchRate(ratelimitcfg.KeySharedSearchAccount))
	bucket.limiter.SetLimitAt(now, searchRate(ratelimitcfg.KeySharedSearchProject))
	aggregate := account.aggregate.ReserveN(now, 1)
	audience := bucket.limiter.ReserveN(now, 1)
	wait := max(aggregate.DelayFrom(now), audience.DelayFrom(now))
	if !aggregate.OK() || !audience.OK() || wait > 0 {
		aggregate.CancelAt(now)
		audience.CancelAt(now)
		return false, max(wait, time.Nanosecond)
	}
	return true, 0
}
func searchRate(key string) rate.Limit {
	return rate.Limit(float64(max(1, ratelimitcfg.Int(key))) / 60)
}

// Expiry cannot grant a fresh burst sooner than the slowest configured refill.
func sharedSearchExpiry(account, project int) time.Duration {
	return max(sharedSearchIdle, 15*time.Minute/time.Duration(max(1, account)), 5*time.Minute/time.Duration(max(1, project)))
}

// Length prefixes preserve the tuple boundary even for legacy IDs containing separators.
func sharedSearchIdentity(user, workspace string) sharedSearchKey {
	h := sha256.New()
	var size [8]byte
	for _, id := range []string{user, workspace} {
		binary.BigEndian.PutUint64(size[:], uint64(len(id)))
		h.Write(size[:])
		h.Write([]byte(id))
	}
	var key sharedSearchKey
	h.Sum(key[:0])
	return key
}
func sharedSearchAudienceKey(project string) sharedSearchAudience {
	var key sharedSearchAudience
	if project != "" {
		key[0] = 1
		digest := sha256.Sum256([]byte(project))
		copy(key[1:], digest[:])
	}
	return key
}
