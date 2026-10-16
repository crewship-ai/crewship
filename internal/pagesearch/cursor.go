package pagesearch

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

const MaxCursors = 256
const CursorTTL = 5 * time.Minute

var ErrCursor = errors.New("native search continuation unavailable")

type CursorBinding struct{ ViewerID, CredentialHash, WorkspaceID, QueryHash string }

func (b CursorBinding) valid() bool {
	return identifier.MatchString(b.ViewerID) && identifier.MatchString(b.WorkspaceID) && digest.MatchString(b.CredentialHash) && digest.MatchString(b.QueryHash)
}

type cursorRecord struct {
	binding          CursorBinding
	after            string
	expires, lastUse time.Time
}
type CursorStore struct {
	mu   sync.Mutex
	rows map[string]cursorRecord
	now  func() time.Time
}

func NewCursorStore() *CursorStore {
	return &CursorStore{rows: map[string]cursorRecord{}, now: time.Now}
}

func (q Query) Fingerprint() string {
	terms := append([]string{}, q.terms...)
	sort.Strings(terms)
	sum := sha256.Sum256([]byte("crewship-native-search-query/v1\n" + strings.Join(terms, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (s *CursorStore) Issue(binding CursorBinding, after string) (string, error) {
	if !binding.valid() || !identifier.MatchString(after) {
		return "", ErrCursor
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return "", ErrCursor
	}
	token := base64.RawURLEncoding.EncodeToString(nonce)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.expire(now)
	if _, collision := s.rows[token]; collision {
		return "", ErrCursor
	}
	if len(s.rows) >= MaxCursors {
		var oldest string
		var oldestTime time.Time
		for key, row := range s.rows {
			if oldest == "" || row.lastUse.Before(oldestTime) {
				oldest, oldestTime = key, row.lastUse
			}
		}
		delete(s.rows, oldest)
	}
	s.rows[token] = cursorRecord{binding, after, now.Add(CursorTTL), now}
	return token, nil
}

func (s *CursorStore) Resolve(token string, binding CursorBinding) (string, error) {
	if len(token) != 43 || !binding.valid() {
		return "", ErrCursor
	}
	nonce, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(nonce) != 32 {
		return "", ErrCursor
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.expire(now)
	row, ok := s.rows[token]
	if !ok || row.binding != binding {
		return "", ErrCursor
	}
	row.lastUse = now
	s.rows[token] = row
	return row.after, nil
}
func (s *CursorStore) expire(now time.Time) {
	for token, row := range s.rows {
		if !now.Before(row.expires) {
			delete(s.rows, token)
		}
	}
}
