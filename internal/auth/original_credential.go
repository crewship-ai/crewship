package auth

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// OriginalCredential is provenance for an already authenticated human action,
// not a bearer capability. Only host authentication may populate it. Current
// credential/identity/ACL checks remain mandatory at each protected operation.
// All fields are copied values; no raw bearer or secret is retained.
type OriginalCredential struct {
	UserID                string `json:"user_id"`
	Kind                  string `json:"kind"`
	ID                    string `json:"id"`
	ExpiresAt             string `json:"expires_at,omitempty"`
	Binding               string `json:"binding"`
	UserAuthorityRevision string `json:"user_authority_revision"`
}

func (r OriginalCredential) Valid() bool {
	validID := func(value string) bool {
		return len(value) > 0 && len(value) <= 200 && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
	}
	if !validID(r.UserID) || !validID(r.ID) || (r.Kind != "session" && r.Kind != "cli_token") {
		return false
	}
	revision, err := strconv.ParseInt(r.UserAuthorityRevision, 10, 64)
	if err != nil || revision < 1 || strconv.FormatInt(revision, 10) != r.UserAuthorityRevision {
		return false
	}
	if len(r.Binding) != 71 || !strings.HasPrefix(r.Binding, "sha256:") {
		return false
	}
	for _, digit := range []byte(r.Binding[7:]) {
		if !(digit >= '0' && digit <= '9' || digit >= 'a' && digit <= 'f') {
			return false
		}
	}
	if r.ExpiresAt == "" {
		return r.Kind == "cli_token"
	}
	expiry, err := time.Parse(time.RFC3339Nano, r.ExpiresAt)
	return err == nil && !expiry.IsZero() && expiry.UTC().Format(time.RFC3339Nano) == r.ExpiresAt
}

// Expiry returns the original accepted horizon. Zero is permitted only for an
// explicitly bound CLI credential which originally had no deadline.
func (r OriginalCredential) Expiry() (time.Time, error) {
	if !r.Valid() {
		return time.Time{}, ErrInvalidToken
	}
	if r.ExpiresAt == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, r.ExpiresAt)
}

type originalCredentialKey struct{}

// WithOriginalCredential carries host-owned provenance without inheriting a
// socket's cancellation lifetime. It never refreshes the original expiry/fence.
func WithOriginalCredential(ctx context.Context, r OriginalCredential) context.Context {
	return context.WithValue(ctx, originalCredentialKey{}, r)
}

// OriginalCredentialFromContext returns a copy so a queued message cannot
// share mutable credential state with a caller or another admission.
func OriginalCredentialFromContext(ctx context.Context) *OriginalCredential {
	if ctx == nil {
		return nil
	}
	r, ok := ctx.Value(originalCredentialKey{}).(OriginalCredential)
	if !ok || !r.Valid() {
		return nil
	}
	return &r
}
