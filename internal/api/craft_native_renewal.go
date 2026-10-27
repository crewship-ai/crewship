package api

import (
	"math"
	"net/http"
	"time"
)

// A renewal requires a separately host-authenticated, strictly newer credential
// horizon for the SAME original identity/epoch. Replaying the old JWT is a
// no-op; an exhausted BOOTTIME admission is never recoverable by this method.
func (m *craftNativeHTTP) renewAdmission(r *http.Request) (bool, error) {
	entry, err := m.bound(r)
	if err != nil {
		return false, err
	}
	fresh, err := craftNativeHuman(r.Context())
	if err != nil {
		return false, err
	}
	oldExpiry, _ := entry.source.Expiry()
	freshExpiry, _ := fresh.Expiry()
	if oldExpiry.IsZero() || !freshExpiry.IsZero() && !freshExpiry.After(oldExpiry) {
		return false, nil
	}
	tx, err := m.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = m.checkEntry(r.Context(), tx, entry, false); err != nil {
		return false, err
	}
	proof, err := m.pageAuthority(r.Context(), tx, fresh, entry.scope, false)
	if err != nil {
		return false, err
	}
	if proof.application != entry.application || proof.workspaceEpoch != entry.workspaceEpoch || proof.memberID != entry.memberID || proof.memberRevision != entry.memberRevision {
		return false, errCraftNativeAccess
	}
	boot, start, err := m.clock.Now()
	if err != nil || boot != entry.boot || start < 0 || start >= entry.deadline {
		return false, errCraftNativeAccess
	}
	remaining := 24 * time.Hour
	if !proof.expiry.IsZero() && time.Until(proof.expiry) < remaining {
		remaining = time.Until(proof.expiry)
	}
	if remaining <= 0 || start > time.Duration(math.MaxInt64)-remaining {
		return false, errCraftNativeAccess
	}
	next := *entry
	next.source = fresh
	next.deadline = start + remaining
	if next.deadline <= entry.deadline {
		return false, nil
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.sessionsByID[entry.id]
	boot, now, err := m.clock.Now()
	if current != entry || err != nil || boot != entry.boot || now < start || now >= entry.deadline || now >= next.deadline || r.Context().Err() != nil {
		return false, errCraftNativeAccess
	}
	m.sessionsByID[next.id] = &next
	m.sessionsByHash[next.tokenHash] = &next
	return true, nil
}
