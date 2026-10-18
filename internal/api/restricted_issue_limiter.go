package api

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/crewship-ai/crewship/internal/ratelimitcfg"
)

const sharedSearchAccounts = 4096
const sharedSearchTotalAudiences = 32768
const sharedSearchAudiences = 257 // 256 valid project rights plus the all-project sentinel.
const sharedSearchWorkspaceAccounts = 256
const sharedSearchWorkspaceAudiences = 2048
const sharedSearchIdle = 5 * time.Minute

type sharedSearchKey [32]byte
type sharedSearchAudience [33]byte // explicit kind byte plus a fixed-width identity digest.
type sharedSearchBucket struct {
	limiter *rate.Limiter
	used    time.Time
	key     sharedSearchAudience
	account *sharedSearchAccount
	idle    *list.Element
}
type sharedSearchWorkspace struct {
	key                 sharedSearchKey
	accounts, audiences int
}
type sharedSearchAccount struct {
	aggregate *rate.Limiter
	audiences map[sharedSearchAudience]*sharedSearchBucket
	used      time.Time
	key       sharedSearchKey
	workspace *sharedSearchWorkspace
	idle      *list.Element
	highWater int
}
type sharedSearchLimiter struct {
	mu                                          sync.Mutex
	accounts                                    map[sharedSearchKey]*sharedSearchAccount
	workspaces                                  map[sharedSearchKey]*sharedSearchWorkspace
	accountIdle, audienceIdle                   list.List
	now                                         func() time.Time
	maxAccounts, maxAudiences                   int
	maxTotal, total                             int
	maxWorkspaceAccounts, maxWorkspaceAudiences int
}

func newSharedSearchLimiter() *sharedSearchLimiter {
	return &sharedSearchLimiter{accounts: make(map[sharedSearchKey]*sharedSearchAccount), workspaces: make(map[sharedSearchKey]*sharedSearchWorkspace), now: time.Now, maxAccounts: sharedSearchAccounts, maxAudiences: sharedSearchAudiences, maxTotal: sharedSearchTotalAudiences, maxWorkspaceAccounts: sharedSearchWorkspaceAccounts, maxWorkspaceAudiences: sharedSearchWorkspaceAudiences}
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
		s.removeAccount(key)
		account = nil
	}
	workspaceKey := sharedSearchIdentity("", workspace)
	owner := s.workspaces[workspaceKey]
	if account == nil {
		if s.accountCapacityFull(owner) {
			s.expire(now, idle)
			owner = s.workspaces[workspaceKey]
		}
		if s.accountCapacityFull(owner) {
			return false, time.Minute
		}
		if owner == nil {
			owner = &sharedSearchWorkspace{key: workspaceKey}
			s.workspaces[workspaceKey] = owner
		}
		account = &sharedSearchAccount{aggregate: rate.NewLimiter(searchRate(ratelimitcfg.KeySharedSearchAccount), 15), audiences: make(map[sharedSearchAudience]*sharedSearchBucket), key: key, workspace: owner}
		s.accounts[key] = account
		owner.accounts++
		account.idle = s.accountIdle.PushBack(account)
	}
	account.used = now
	s.accountIdle.MoveToBack(account.idle)
	audienceKey := sharedSearchAudienceKey(project)
	bucket := account.audiences[audienceKey]
	if bucket != nil && now.Sub(bucket.used) >= idle {
		s.removeAudience(bucket)
		bucket = nil
	}
	if bucket == nil {
		if s.audienceCapacityFull(account) {
			s.expire(now, idle)
		}
		if s.audienceCapacityFull(account) {
			return false, time.Minute
		}
		bucket = &sharedSearchBucket{limiter: rate.NewLimiter(searchRate(ratelimitcfg.KeySharedSearchProject), 5), key: audienceKey, account: account}
		account.audiences[audienceKey] = bucket
		account.highWater = max(account.highWater, len(account.audiences))
		bucket.idle = s.audienceIdle.PushBack(bucket)
		s.total++
		account.workspace.audiences++
	}
	bucket.used = now
	s.audienceIdle.MoveToBack(bucket.idle)
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

func (s *sharedSearchLimiter) accountCapacityFull(workspace *sharedSearchWorkspace) bool {
	return len(s.accounts) >= s.maxAccounts || s.total >= s.maxTotal || workspace != nil && (workspace.accounts >= s.maxWorkspaceAccounts || workspace.audiences >= s.maxWorkspaceAudiences)
}
func (s *sharedSearchLimiter) audienceCapacityFull(account *sharedSearchAccount) bool {
	return len(account.audiences) >= s.maxAudiences || s.total >= s.maxTotal || account.workspace.audiences >= s.maxWorkspaceAudiences
}

// Each live account/audience has exactly one idle-list node. Touches move nodes
// rather than append history; monotonically ordered uses let capacity cleanup
// stop at the first active entry, without scanning any remaining active maps.
func (s *sharedSearchLimiter) expire(now time.Time, idle time.Duration) {
	for node := s.accountIdle.Front(); node != nil; node = s.accountIdle.Front() {
		account := node.Value.(*sharedSearchAccount)
		if now.Sub(account.used) < idle {
			break
		}
		s.removeAccount(account.key)
	}
	for node := s.audienceIdle.Front(); node != nil; node = s.audienceIdle.Front() {
		bucket := node.Value.(*sharedSearchBucket)
		if now.Sub(bucket.used) < idle {
			break
		}
		s.removeAudience(bucket)
	}
}

func (s *sharedSearchLimiter) removeAudience(bucket *sharedSearchBucket) {
	account := bucket.account
	delete(account.audiences, bucket.key)
	s.audienceIdle.Remove(bucket.idle)
	s.total--
	account.workspace.audiences--
	compactSharedSearchAudiences(account)
}

// Shrink only after real expiry halves the high-water mark. The copied live
// entries are amortized over deletions; a full, active request never copies a
// map. This also bounds retained Go map storage after repeated audience churn.
func compactSharedSearchAudiences(account *sharedSearchAccount) {
	if len(account.audiences)*2 > account.highWater {
		return
	}
	next := make(map[sharedSearchAudience]*sharedSearchBucket, len(account.audiences))
	for key, bucket := range account.audiences {
		next[key] = bucket
	}
	account.audiences = next
	account.highWater = len(next)
}
func (s *sharedSearchLimiter) removeAccount(key sharedSearchKey) {
	account := s.accounts[key]
	for _, bucket := range account.audiences {
		s.audienceIdle.Remove(bucket.idle)
	}
	s.total -= len(account.audiences)
	account.workspace.audiences -= len(account.audiences)
	account.workspace.accounts--
	if account.workspace.accounts == 0 {
		delete(s.workspaces, account.workspace.key)
	}
	s.accountIdle.Remove(account.idle)
	delete(s.accounts, key)
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
