package api

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// The native publication binding is separate from human session/lease routing.
type craftNativeContextKey struct{}

// authorizeNative is the single binding for the native manifest publication
// callback. Only a successfully bound HTTP request may install this context.
func (m *craftNativeHTTP) authorizeNative(ctx context.Context, tx *sql.Tx, scope mooring.EditLeaseScope, intent string) (mooring.EditLeasePrincipal, error) {
	entry, _ := ctx.Value(craftNativeContextKey{}).(*craftNativeSession)
	if entry == nil || entry.scope != scope {
		return mooring.EditLeasePrincipal{}, errCraftNativeAccess
	}
	m.mu.Lock()
	current := m.sessionsByID[entry.id]
	m.mu.Unlock()
	if current == nil || current.tokenHash != entry.tokenHash || current.scope != entry.scope {
		return mooring.EditLeasePrincipal{}, errCraftNativeAccess
	}
	entry = current
	if intent != "read" && intent != "write" {
		return mooring.EditLeasePrincipal{}, errCraftNativeAccess
	}
	if _, err := m.checkEntry(ctx, tx, entry, intent == "write"); err != nil {
		return mooring.EditLeasePrincipal{}, err
	}
	return craftNativePrincipal(entry), nil
}

// boundNativeRequest is reused by native data handlers; transport code cannot
// install arbitrary actor/session fields as an authenticated publication proof.
func (m *craftNativeHTTP) boundNativeRequest(r *http.Request) (*http.Request, error) {
	entry, err := m.bound(r)
	if err != nil {
		return nil, err
	}
	return r.WithContext(context.WithValue(r.Context(), craftNativeContextKey{}, entry)), nil
}
