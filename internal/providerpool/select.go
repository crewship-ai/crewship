// Package providerpool implements provider-account pool policy. It never
// decrypts credentials, refreshes a login, or retries an agent's work. Selection
// belongs at the run boundary, after refresh, inside the caller's transaction
// that records the selected account and advances its sequence.
package providerpool

import (
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/providerlogin"
)

var (
	ErrUnavailable = errors.New("no provider account is available")
	ErrCrossOwner  = errors.New("pooling accounts from different owners requires explicit consent")
	ErrInvalid     = errors.New("invalid provider account pool")
)

// Policy applies to every member, including unavailable members. Revocation or
// expiry must not silently turn a mixed-owner pool into an approved one.
type Policy struct {
	Provider        string
	Mode            string
	AllowCrossOwner bool
}

// Candidate is metadata only. ExpiresAt and CooldownUntil are zero when
// unknown/absent, not an assertion of unlimited quota. Blocked means a known
// condition requiring intervention (e.g. invalid grant or exhausted billing),
// not a temporary rate limit. LastSelected is a monotonically increasing pool
// sequence, not wall-clock time; simultaneous run starts need distinct turns.
type Candidate struct {
	ID            string
	OwnerID       string
	Provider      string
	Mode          string
	Priority      int
	LastSelected  int64
	Active        bool
	Blocked       bool
	ExpiresAt     time.Time
	CooldownUntil time.Time
}

// Select picks the least recently used eligible account in the lowest-numbered
// priority layer. ID breaks ties so database iteration order never decides
// whose account pays. It does not mutate or retain the caller's slice. In
// particular, all accounts cooling down is an error, never a fallback to the
// first account. A successful choice does not prove the upstream will accept
// it: a quota shared by several API keys may still reject the next request.
func Select(policy Policy, candidates []Candidate, now time.Time) (Candidate, error) {
	if now.IsZero() || !providerlogin.IsProvider(policy.Provider) || !providerlogin.ValidMode(policy.Mode) {
		return Candidate{}, ErrInvalid
	}
	provider := providerlogin.Canonical(policy.Provider)
	seen := make(map[string]bool, len(candidates))
	owner := ""
	var selected Candidate
	for _, c := range candidates {
		if c.ID == "" || c.OwnerID == "" || seen[c.ID] || c.LastSelected < 0 ||
			providerlogin.Canonical(c.Provider) != provider || c.Mode != policy.Mode {
			return Candidate{}, ErrInvalid
		}
		seen[c.ID] = true
		if owner != "" && owner != c.OwnerID && !policy.AllowCrossOwner {
			return Candidate{}, ErrCrossOwner
		}
		owner = c.OwnerID
		if !c.Active || c.Blocked || now.Before(c.CooldownUntil) ||
			(!c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt)) {
			continue
		}
		if selected.ID == "" || c.Priority < selected.Priority ||
			(c.Priority == selected.Priority && (c.LastSelected < selected.LastSelected ||
				(c.LastSelected == selected.LastSelected && c.ID < selected.ID))) {
			selected = c
		}
	}
	if selected.ID == "" {
		return Candidate{}, ErrUnavailable
	}
	return selected, nil
}
