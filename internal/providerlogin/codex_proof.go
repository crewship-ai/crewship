package providerlogin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// IdentityVerifier is configured only by trusted host code.
type IdentityVerifier interface {
	Verify(context.Context, string, string, string) (CodexIdentity, error)
}

// CodexProofStore records signed identity separately from legacy decoded login
// labels. No inference adapter may treat a proof as model entitlement or SIWC.
type CodexProofStore struct {
	db       *sql.DB
	verifier IdentityVerifier
}

func NewCodexProofStore(db *sql.DB, verifier IdentityVerifier) *CodexProofStore {
	if verifier == nil {
		verifier = NewCodexIdentityVerifier()
	}
	return &CodexProofStore{db: db, verifier: verifier}
}

type proofMaterial struct{ workspace, access, refresh, id, account string }
type proofQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadProofMaterial(ctx context.Context, q proofQuery, credential string) (proofMaterial, error) {
	var m proofMaterial
	err := q.QueryRowContext(ctx, `SELECT c.workspace_id,c.encrypted_value,
 COALESCE((SELECT encrypted_value FROM credential_fields WHERE credential_id=c.id AND key='refresh_token'),''),
 COALESCE((SELECT encrypted_value FROM credential_fields WHERE credential_id=c.id AND key='id_token'),''),
 COALESCE((SELECT value FROM credential_fields WHERE credential_id=c.id AND key='account_id'),'')
 FROM credentials c WHERE c.id=? AND c.type='PROVIDER_LOGIN' AND c.provider='OPENAI' AND c.status='ACTIVE' AND c.deleted_at IS NULL`, credential).Scan(&m.workspace, &m.access, &m.refresh, &m.id, &m.account)
	if err != nil || m.access == "" || m.refresh == "" || m.id == "" || !identityString(m.account, 256) {
		return proofMaterial{}, ErrUnverifiedCodexIdentity
	}
	return m, nil
}
func proofHash(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}
func verifyMaterial(ctx context.Context, v IdentityVerifier, m proofMaterial) (CodexIdentity, error) {
	id, e := encryption.Decrypt(m.id)
	if e != nil {
		return CodexIdentity{}, ErrUnverifiedCodexIdentity
	}
	access, e := encryption.Decrypt(m.access)
	if e != nil {
		return CodexIdentity{}, ErrUnverifiedCodexIdentity
	}
	return v.Verify(ctx, id, access, m.account)
}

// Enroll verifies the exact current encrypted generation. Callers must authorize
// the operator and workspace before invoking this host-only method. It neither
// imports credentials nor enrolls an OAuth application or enables inference.
func (s *CodexProofStore) Enroll(ctx context.Context, workspace, credential string) error {
	m, e := loadProofMaterial(ctx, s.db, credential)
	if e != nil || m.workspace != workspace {
		return ErrUnverifiedCodexIdentity
	}
	identity, e := verifyMaterial(ctx, s.verifier, m)
	if e != nil {
		return ErrUnverifiedCodexIdentity
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	// This conditional write is the first transaction operation. Generation
	// cannot be verified in a reader snapshot and then promoted across rotation.
	result, e := tx.ExecContext(ctx, `INSERT INTO codex_login_proofs
 (credential_id,workspace_id,generation,issuer,client_id,subject,account_id,user_id,plan,access_cipher_hash,refresh_cipher_hash,id_cipher_hash,access_expires_at,id_expires_at,verified_at)
 SELECT id,workspace_id,1,?,?,?,?,?,?,?,?,?,?,?,? FROM credentials c
 WHERE id=? AND workspace_id=? AND encrypted_value=? AND type='PROVIDER_LOGIN' AND provider='OPENAI' AND status='ACTIVE' AND deleted_at IS NULL
 AND EXISTS(SELECT 1 FROM credential_fields WHERE credential_id=c.id AND key='refresh_token' AND encrypted_value=?)
 AND EXISTS(SELECT 1 FROM credential_fields WHERE credential_id=c.id AND key='id_token' AND encrypted_value=?)
 AND EXISTS(SELECT 1 FROM credential_fields WHERE credential_id=c.id AND key='account_id' AND value=?)
 ON CONFLICT(credential_id) DO UPDATE SET generation=codex_login_proofs.generation+1,
 enabled=1,workspace_id=excluded.workspace_id,issuer=excluded.issuer,client_id=excluded.client_id,subject=excluded.subject,account_id=excluded.account_id,user_id=excluded.user_id,plan=excluded.plan,
 access_cipher_hash=excluded.access_cipher_hash,refresh_cipher_hash=excluded.refresh_cipher_hash,id_cipher_hash=excluded.id_cipher_hash,
 access_expires_at=excluded.access_expires_at,id_expires_at=excluded.id_expires_at,verified_at=excluded.verified_at`,
		identity.Issuer, identity.ClientID, identity.Subject, identity.AccountID, identity.UserID, identity.Plan,
		proofHash(m.access), proofHash(m.refresh), proofHash(m.id), identity.AccessExpires.UTC().Format(time.RFC3339), identity.IDExpires.UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339Nano),
		credential, workspace, m.access, m.refresh, m.id, m.account)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil || n != 1 {
		return ErrUnverifiedCodexIdentity
	}
	return tx.Commit()
}

type storedProof struct {
	enabled                        bool
	generation                     int64
	identity                       CodexIdentity
	access, refresh, id, workspace string
}

func loadStoredProof(ctx context.Context, q proofQuery, credential string) (storedProof, error) {
	var p storedProof
	e := q.QueryRowContext(ctx, `SELECT generation,enabled,workspace_id,issuer,client_id,subject,account_id,user_id,plan,access_cipher_hash,refresh_cipher_hash,id_cipher_hash FROM codex_login_proofs WHERE credential_id=?`, credential).Scan(&p.generation, &p.enabled, &p.workspace, &p.identity.Issuer, &p.identity.ClientID, &p.identity.Subject, &p.identity.AccountID, &p.identity.UserID, &p.identity.Plan, &p.access, &p.refresh, &p.id)
	return p, e
}
func sameIdentity(a, b CodexIdentity) bool {
	return a.Issuer == b.Issuer && a.ClientID == b.ClientID && a.Subject == b.Subject && a.AccountID == b.AccountID && a.UserID == b.UserID && a.Plan == b.Plan
}

// CodexRotationProof is private validated state, bound to one old generation.
// Its fields cannot be supplied by a worker or serialized through an API.
type CodexRotationProof struct {
	credential                                     string
	before                                         storedProof
	after                                          CodexIdentity
	accessTokenHash, refreshTokenHash, idTokenHash string
}

// PrepareRotation performs remote key verification before acquiring a writer
// transaction. Legacy unenrolled logins return nil and keep their old behavior.
func (s *CodexProofStore) PrepareRotation(ctx context.Context, credential, expectedRefreshCipher string, result RefreshResult) (*CodexRotationProof, error) {
	p, e := loadStoredProof(ctx, s.db, credential)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	deny := func() (*CodexRotationProof, error) {
		_, disableErr := s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE codex_login_proofs SET enabled=0,generation=generation+1 WHERE credential_id=? AND generation=? AND refresh_cipher_hash=?`, credential, p.generation, proofHash(expectedRefreshCipher))
		if disableErr != nil {
			return nil, disableErr
		}
		return nil, ErrUnverifiedCodexIdentity
	}
	m, e := loadProofMaterial(ctx, s.db, credential)
	if e != nil || !p.enabled || p.workspace != m.workspace || p.access != proofHash(m.access) || p.refresh != proofHash(m.refresh) || p.id != proofHash(m.id) || m.refresh != expectedRefreshCipher || p.identity.AccountID != m.account {
		return deny()
	}
	identity, e := s.verifier.Verify(ctx, result.IDToken, result.AccessToken, m.account)
	if e != nil || !sameIdentity(p.identity, identity) || result.RefreshToken == "" {
		return deny()
	}
	return &CodexRotationProof{credential: credential, before: p, after: identity, accessTokenHash: proofHash(result.AccessToken), refreshTokenHash: proofHash(result.RefreshToken), idTokenHash: proofHash(result.IDToken)}, nil
}

// CommitRotation runs after all new ciphertext fields have been written in the
// existing refresh transaction, before commit. Concurrent enrollment/deletion
// cannot silently discard or manufacture a proof during an unverified refresh.
func (s *CodexProofStore) CommitRotation(ctx context.Context, tx *sql.Tx, credential string, proof *CodexRotationProof) error {
	current, e := loadStoredProof(ctx, tx, credential)
	if proof == nil {
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		return ErrUnverifiedCodexIdentity
	}
	if e != nil || proof.credential != credential || current != proof.before {
		return ErrUnverifiedCodexIdentity
	}
	m, e := loadProofMaterial(ctx, tx, credential)
	if e != nil || m.workspace != current.workspace || m.account != proof.after.AccountID {
		return ErrUnverifiedCodexIdentity
	}
	access, decryptErr := encryption.Decrypt(m.access)
	refresh, refreshErr := encryption.Decrypt(m.refresh)
	id, idErr := encryption.Decrypt(m.id)
	if decryptErr != nil || refreshErr != nil || idErr != nil || proofHash(access) != proof.accessTokenHash || proofHash(refresh) != proof.refreshTokenHash || proofHash(id) != proof.idTokenHash {
		return ErrUnverifiedCodexIdentity
	}
	result, e := tx.ExecContext(ctx, `UPDATE codex_login_proofs SET generation=generation+1,access_cipher_hash=?,refresh_cipher_hash=?,id_cipher_hash=?,access_expires_at=?,id_expires_at=?,verified_at=? WHERE credential_id=? AND generation=?`, proofHash(m.access), proofHash(m.refresh), proofHash(m.id), proof.after.AccessExpires.UTC().Format(time.RFC3339), proof.after.IDExpires.UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339Nano), credential, current.generation)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil || n != 1 {
		return ErrUnverifiedCodexIdentity
	}
	return nil
}

// Current returns an identity only while the exact verified encrypted
// generation and both signed expiries remain current. Grant authorization and
// provider/model entitlement must be checked independently by the authority.
func (s *CodexProofStore) Current(ctx context.Context, workspace, credential string) (CodexIdentity, error) {
	p, e := loadStoredProof(ctx, s.db, credential)
	if e != nil || !p.enabled || p.workspace != workspace {
		return CodexIdentity{}, ErrUnverifiedCodexIdentity
	}
	m, e := loadProofMaterial(ctx, s.db, credential)
	if e != nil || m.workspace != workspace || p.access != proofHash(m.access) || p.refresh != proofHash(m.refresh) || p.id != proofHash(m.id) || p.identity.AccountID != m.account {
		return CodexIdentity{}, ErrUnverifiedCodexIdentity
	}
	var accessExpiry, idExpiry string
	e = s.db.QueryRowContext(ctx, `SELECT access_expires_at,id_expires_at FROM codex_login_proofs WHERE credential_id=? AND generation=?`, credential, p.generation).Scan(&accessExpiry, &idExpiry)
	a, ae := time.Parse(time.RFC3339, accessExpiry)
	i, ie := time.Parse(time.RFC3339, idExpiry)
	if e != nil || ae != nil || ie != nil || !a.After(time.Now()) || !i.After(time.Now()) {
		return CodexIdentity{}, ErrUnverifiedCodexIdentity
	}
	p.identity.AccessExpires = a
	p.identity.IDExpires = i
	return p.identity, nil
}

// AccessExpiry is the signed expiry to persist with an enrolled rotation.
func (p *CodexRotationProof) AccessExpiry() time.Time {
	if p == nil {
		return time.Time{}
	}
	return p.after.AccessExpires
}
