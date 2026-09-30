package restricteddispatch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

type providerBinding struct {
	Credential, Grant, Revision, Model string
	MaxOutputTokens                    int64
}

type providerSnapshot struct {
	Credential, Grant, Ciphertext, Model string
	GrantExpiry, KeyExpiry               sql.NullString
}

type providerQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

var providerModel = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,95}$`)

// PrepareResponses pins one explicit agent API-key grant before building any
// prompt. It does not use crew/workspace credentials, provider-login refresh or
// Keeper-mediated credentials. Multiple eligible keys are an ambiguity, not a
// credential pool to try until one works. maxOutputTokens is trusted server
// configuration; this API must not be populated from client task metadata.
func (a Authority) PrepareResponses(ctx context.Context, user, workspace, agent, chat, parent string, rights []access.Right, maxOutputTokens int64, build BuildCommand) (string, access.Attempt, error) {
	if build == nil || maxOutputTokens < 1 || maxOutputTokens > 32768 {
		return "", access.Attempt{}, access.ErrDenied
	}
	return a.Prepare(ctx, user, workspace, agent, chat, parent, rights, func(ctx context.Context, attempt access.Attempt) ([]string, error) {
		if err := a.pinProvider(ctx, attempt, maxOutputTokens); err != nil {
			return nil, err
		}
		return build(ctx, attempt)
	})
}

func (a Authority) pinProvider(ctx context.Context, attempt access.Attempt, limit int64) error {
	tx, err := a.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	snapshot, err := providerCurrent(ctx, tx, attempt, "")
	if err != nil {
		return err
	}
	binding := providerBinding{snapshot.Credential, snapshot.Grant, snapshot.revision(), snapshot.Model, limit}
	if attempt.Parent != "" {
		p, err := loadProvider(ctx, tx, attempt.Parent)
		if err != nil || p.Credential != binding.Credential || p.Revision != binding.Revision || p.Model != binding.Model || p.MaxOutputTokens < limit {
			return access.ErrDenied
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO restricted_provider_bindings(attempt_id,credential_id,grant_id,credential_revision,model,max_output_tokens) VALUES(?,?,?,?,?,?)`, attempt.ID, binding.Credential, binding.Grant, binding.Revision, binding.Model, binding.MaxOutputTokens)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func loadProvider(ctx context.Context, q providerQuery, attemptID string) (providerBinding, error) {
	var b providerBinding
	err := q.QueryRowContext(ctx, `SELECT credential_id,grant_id,credential_revision,model,max_output_tokens FROM restricted_provider_bindings WHERE attempt_id=?`, attemptID).Scan(&b.Credential, &b.Grant, &b.Revision, &b.Model, &b.MaxOutputTokens)
	return b, err
}

func providerCurrent(ctx context.Context, q providerQuery, attempt access.Attempt, grantID string) (providerSnapshot, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.id,ac.id,c.encrypted_value,a.llm_model,ac.expires_at,c.token_expires_at
        FROM agents a JOIN agent_credentials ac ON ac.agent_id=a.id
        JOIN credentials c ON c.id=ac.credential_id AND c.workspace_id=a.workspace_id
        WHERE a.id=? AND a.workspace_id=? AND a.deleted_at IS NULL AND a.llm_provider='OPENAI'
          AND c.deleted_at IS NULL AND c.status='ACTIVE' AND c.provider='OPENAI' AND c.type='API_KEY'
          AND c.security_level IN (1,2) AND ac.env_var_name='OPENAI_API_KEY'
          AND (?='' OR ac.id=?) LIMIT 2`, attempt.Agent, attempt.Workspace, grantID, grantID)
	if err != nil {
		return providerSnapshot{}, err
	}
	defer rows.Close()
	var snapshot providerSnapshot
	count := 0
	for rows.Next() {
		count++
		if err := rows.Scan(&snapshot.Credential, &snapshot.Grant, &snapshot.Ciphertext, &snapshot.Model, &snapshot.GrantExpiry, &snapshot.KeyExpiry); err != nil {
			return providerSnapshot{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return providerSnapshot{}, err
	}
	if count != 1 || !providerModel.MatchString(snapshot.Model) || snapshot.Ciphertext == "" || len(snapshot.Ciphertext) > 128<<10 {
		return providerSnapshot{}, access.ErrDenied
	}
	if _, err := snapshot.deadline(time.Now().Add(5 * time.Minute)); err != nil {
		return providerSnapshot{}, err
	}
	return snapshot, nil
}

func (s providerSnapshot) revision() string {
	sum := sha256.Sum256([]byte(s.Credential + "\x00" + s.Ciphertext))
	return hex.EncodeToString(sum[:])
}

func (s providerSnapshot) deadline(ceiling time.Time) (time.Time, error) {
	for _, raw := range []sql.NullString{s.GrantExpiry, s.KeyExpiry} {
		if !raw.Valid {
			continue
		}
		expires, err := time.Parse(time.RFC3339Nano, raw.String)
		if err != nil || !expires.After(time.Now()) {
			return time.Time{}, access.ErrDenied
		}
		if expires.Before(ceiling) {
			ceiling = expires
		}
	}
	return ceiling, nil
}

func (a Authority) checkedProvider(ctx context.Context, attempt access.Attempt, b providerBinding) (providerSnapshot, error) {
	s, err := providerCurrent(ctx, a.Store.DB, attempt, b.Grant)
	if err != nil {
		return providerSnapshot{}, err
	}
	if s.Credential != b.Credential || s.revision() != b.Revision || s.Model != b.Model || b.MaxOutputTokens < 1 || b.MaxOutputTokens > 32768 {
		return providerSnapshot{}, access.ErrDenied
	}
	return s, nil
}

func (a Authority) attachProvider(ctx context.Context, attempt access.Attempt, plan *restrictedruntime.Plan) error {
	b, err := loadProvider(ctx, a.Store.DB, attempt.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // existing offline preparation
	}
	if err != nil {
		return err
	}
	s, err := a.checkedProvider(ctx, attempt, b)
	if err != nil {
		return err
	}
	plan.Expires, err = s.deadline(plan.Expires)
	if err != nil {
		return err
	}
	plan.Profile = "brokered-http-v2"
	plan.Network = &restrictedruntime.NetworkPlan{
		Version: 2, Audience: attempt.ID,
		Grants: []restrictedruntime.HTTPGrant{{ID: "responses", Revision: b.Revision, Method: "POST", URL: "https://api.openai.com/v1/responses", CredentialID: b.Credential,
			ResponseMode: "sse", Responses: &restrictedruntime.ResponsesPolicy{Model: b.Model, MaxOutputTokens: b.MaxOutputTokens}, MaxRequest: 1 << 20, MaxResponse: 1 << 20, TimeoutMillis: 300000}},
		// Account denotes the pinned key identity, not a caller-supplied vendor
		// account label. This adapter never forwards account-selection headers.
		Credentials: []restrictedruntime.BrokerCredential{{ID: b.Credential, Revision: b.Revision, Provider: "openai", Account: b.Credential, Delivery: "broker-bearer-v1"}},
	}
	return nil
}

// BrokerSecret returns material only to the host relay for the exact pinned
// credential. Secrets() continues to return no agent-visible credentials.
func (a Authority) BrokerSecret(ctx context.Context, handle, credentialID string) (restrictedruntime.BoundSecret, error) {
	attempt, err := a.Store.Resolve(ctx, handle)
	if err != nil {
		return restrictedruntime.BoundSecret{}, err
	}
	b, err := loadProvider(ctx, a.Store.DB, attempt.ID)
	if err != nil || b.Credential != credentialID {
		return restrictedruntime.BoundSecret{}, access.ErrDenied
	}
	s, err := a.checkedProvider(ctx, attempt, b)
	if err != nil {
		return restrictedruntime.BoundSecret{}, err
	}
	value, err := encryption.Decrypt(s.Ciphertext)
	if err != nil || value == "" {
		return restrictedruntime.BoundSecret{}, access.ErrDenied
	}
	// Credential/grant mutations atomically revoke the attempt. Recheck after
	// decryption so a concurrent mutation cannot release an old snapshot.
	if _, err := a.Resolve(ctx, handle); err != nil {
		return restrictedruntime.BoundSecret{}, err
	}
	expires, err := s.deadline(time.Now().Add(5 * time.Minute))
	if err != nil {
		return restrictedruntime.BoundSecret{}, err
	}
	return restrictedruntime.BoundSecret{ID: b.Credential, Revision: b.Revision, Provider: "openai", Account: b.Credential, Value: value, Expires: expires}, nil
}

var _ restrictedruntime.BrokerAuthority = Authority{}
