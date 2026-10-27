package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"

	"github.com/crewship-ai/crewship/internal/mooring"
)

type craftNativeStreamKey struct{}

func craftNativeOpaque() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func (m *craftNativeHTTP) leaseAuthority(ctx context.Context, tx *sql.Tx, a mooring.EditLeaseAccess) error {
	m.mu.Lock()
	entry := m.sessionsByID[a.Principal.SessionID]
	m.mu.Unlock()
	if entry == nil || entry.scope != a.Scope || a.Principal.ActorID != "user:"+entry.source.UserID || a.Intent == "revoke" {
		return errCraftNativeAccess
	}
	_, err := m.checkEntry(ctx, tx, entry, a.Intent != "read")
	return err
}
func (m *craftNativeHTTP) checkEntry(ctx context.Context, tx *sql.Tx, entry *craftNativeSession, write bool) (craftNativeProof, error) {
	proof, err := m.pageAuthority(ctx, tx, entry.source, entry.scope, write)
	if err != nil {
		return proof, err
	}
	// No epoch rebinding after a revoke/regain cycle. This first dormant adapter
	// conservatively retires reads for unclassified ACL epoch changes as well.
	if proof.application != entry.application || proof.workspaceEpoch != entry.workspaceEpoch || proof.memberID != entry.memberID || proof.memberRevision != entry.memberRevision {
		return proof, errCraftNativeAccess
	}
	boot, now, err := m.clock.Now()
	if err != nil || boot != entry.boot || now < 0 || now >= entry.deadline {
		return proof, errCraftNativeAccess
	}
	return proof, nil
}
func (m *craftNativeHTTP) bound(r *http.Request) (*craftNativeSession, error) {
	if !m.enabled || m.pages == nil || m.clock == nil {
		return nil, errCraftNativeAccess
	}
	token := r.Header.Get("X-Craft-Session")
	if len(token) != 64 {
		return nil, errCraftNativeAccess
	}
	hash := sha256.Sum256([]byte(token))
	m.mu.Lock()
	entry := m.sessionsByHash[hash]
	m.mu.Unlock()
	if entry == nil {
		return nil, errCraftNativeAccess
	}
	source, err := craftNativeHuman(r.Context())
	if err != nil {
		return nil, err
	}
	original := entry.source
	// A renewed JWT may shorten its accepted horizon, never replace the captured
	// source identity/epoch or extend this admission's original monotonic cap.
	if source.UserID != original.UserID || source.Kind != original.Kind || source.ID != original.ID || source.Binding != original.Binding || source.UserAuthorityRevision != original.UserAuthorityRevision || WorkspaceIDFromContext(r.Context()) != entry.scope.WorkspaceID {
		return nil, errCraftNativeAccess
	}
	tx, err := m.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	rec, err := m.pages.loadPageForRead(ctx, entry.scope.WorkspaceID, r.PathValue("slug"))
	if err != nil || rec.ID != entry.scope.ArtifactID {
		return nil, errCraftNativeAccess
	}
	// An established server stream may use a separately authenticated renewal
	// published for this same connection. Ordinary HTTP still validates its
	// own current JWT horizon; the private stream marker is never a wire input.
	if stream, ok := r.Context().Value(craftNativeStreamKey{}).(string); !ok || stream != entry.id {
		if _, err = validateOriginalHumanCredential(ctx, tx, source, "pages:read"); err != nil {
			return nil, err
		}
	}
	if _, err = m.checkEntry(ctx, tx, entry, false); err != nil {
		return nil, err
	}
	return entry, nil
}
func craftNativePrincipal(entry *craftNativeSession) mooring.EditLeasePrincipal {
	return mooring.EditLeasePrincipal{ActorID: "user:" + entry.source.UserID, SessionID: entry.id}
}
