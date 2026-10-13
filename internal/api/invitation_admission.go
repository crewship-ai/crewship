package api

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

var errInvitationConflict = errors.New("invitation membership or claim conflict")

func (h *AuthHandler) checkInvitationAdmission(ctx context.Context, tx *sql.Tx, workspace string) error {
	if h.license == nil {
		return nil
	}
	return h.license.CheckMemberLimit(ctx, tx, workspace)
}

// Tagged stored hashes cannot be used as bearer proof through legacy lookup.
func invitationTokenHash(raw string) string { return "sha256:v1:" + hashResetToken(raw) }
func validInvitationProof(raw string) bool {
	if raw == "" || len(raw) > 8192 || strings.HasPrefix(raw, "sha256:") {
		return false
	}
	if strings.HasPrefix(raw, "wi1_") {
		if len(raw) != 68 {
			return false
		}
		for _, c := range raw[4:] {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return false
			}
		}
	}
	if strings.HasPrefix(raw, "wi") && !strings.HasPrefix(raw, "wi1_") {
		return false
	}
	return true
}
