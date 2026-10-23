package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type auditKeyRotateRequest struct {
	ExpectedActiveKeyID string `json:"expected_active_key_id,omitempty"`
	Reason              string `json:"reason"`
}

// auditKeyRotateMu serialises rotations in this process: the keyring file
// and its audit entry are one decision.
var auditKeyRotateMu sync.Mutex

var errAuditKeyringExternal = errors.New("audit keyring is managed outside the data directory")

// appOwnedAuditKeyring returns the data directory whose app-owned keyring the
// server booted with. An operator-managed CREWSHIP_AUDIT_KEYRING_PATH elsewhere
// is read-only to Crewship and is rotated by its owner.
func appOwnedAuditKeyring() (string, error) {
	root, err := database.ResolveDataDirRoot("")
	if err != nil {
		return "", err
	}
	if root, err = filepath.Abs(root); err != nil {
		return "", err
	}
	if path := os.Getenv(journal.SigningKeysPathEnv); path == "" || filepath.Clean(path) != filepath.Join(root, journal.SigningKeysFile) {
		return "", errAuditKeyringExternal
	}
	return root, nil
}

// rotateAuditSigningKey adds a new active signing key to the app-owned keyring.
// Earlier keys stay for verification only; no historical MAC is rewritten.
// The running server signs with its boot snapshot until it restarts.
func (r *Router) rotateAuditSigningKey(w http.ResponseWriter, request *http.Request) {
	var input auditKeyRotateRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(new(any)) != io.EOF {
		replyError(w, 400, "Invalid audit key rotation request")
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if len(input.Reason) < 8 || len(input.Reason) > 512 || len(input.ExpectedActiveKeyID) > 64 {
		replyError(w, 400, "An 8-512 character reason is required")
		return
	}
	if err := auditResealAuthority(request, r.db, "instance", ""); err != nil {
		r.replyResealFailure(w, err)
		return
	}
	dataDir, err := appOwnedAuditKeyring()
	if errors.Is(err, errAuditKeyringExternal) {
		replyError(w, 409, "The audit keyring is managed outside this instance's data directory; rotate it there and restart the server")
		return
	}
	if err != nil {
		replyInternalError(w, r.logger, "resolve audit keyring", err)
		return
	}
	auditKeyRotateMu.Lock()
	defer auditKeyRotateMu.Unlock()
	tx, err := r.db.BeginTx(request.Context(), nil)
	if err != nil {
		replyInternalError(w, r.logger, "rotate audit key", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck
	if err := auditResealAuthority(request, tx, "instance", ""); err != nil {
		r.replyResealFailure(w, err)
		return
	}
	// The entry is signed with the snapshot this request runs under, never
	// with the key it is about to add.
	keys, err := journal.SigningKeysForContext(request.Context())
	if err != nil {
		replyInternalError(w, r.logger, "rotate audit key", err)
		return
	}
	auditID := generateCUID()
	previous, active, pending, err := journal.StageSigningKeyRotation(dataDir, input.ExpectedActiveKeyID, auditID)
	if errors.Is(err, journal.ErrSigningKeyRotationConflict) {
		replyError(w, 409, "The active audit key changed or a rotation is pending; review it before retrying")
		return
	}
	if err != nil {
		replyInternalError(w, r.logger, "rotate audit key", err)
		return
	}
	// Until the audit entry is handed to Commit, nothing decided the rotation:
	// the staged keyring is removed. A Commit error leaves it for the next
	// start, which installs it only if the entry exists.
	submitted := false
	defer func() {
		if !submitted {
			if err := os.Remove(pending); err != nil {
				r.logger.Error("remove staged audit keyring after failed rotation", "error", err)
			}
		}
	}()
	// Key identities are public metadata; key bytes never leave the file.
	metadata, err := json.Marshal(map[string]string{"previous_active_key_id": previous, "active_key_id": active, "reason": input.Reason, "effective": "next_start"})
	if err != nil {
		replyInternalError(w, r.logger, "rotate audit key", err)
		return
	}
	actor := UserFromContext(request.Context()).ID
	if err := appendInstanceAuditTx(journal.WithSigningKeys(request.Context(), keys), tx, journal.InstanceAuditFields{
		ID: auditID, UserID: sql.NullString{String: actor, Valid: true}, Action: journal.SigningKeyRotatedAction,
		EntityType: "audit_keyring", EntityID: sql.NullString{String: active, Valid: true},
		Metadata: sql.NullString{String: string(metadata), Valid: true}, CreatedAt: tsformat.Format(timeNow()),
	}); err != nil {
		replyInternalError(w, r.logger, "audit key rotation", err)
		return
	}
	submitted = true
	if err := tx.Commit(); err != nil {
		replyInternalError(w, r.logger, "commit audit key rotation", err)
		return
	}
	if err := journal.FinishSigningKeyRotation(dataDir, pending); err != nil {
		// Committed: the next start installs the staged keyring.
		r.logger.Error("install rotated audit keyring; the next start completes it", "error", err)
	}
	writeJSON(w, 200, map[string]any{"previous_active_key_id": previous, "active_key_id": active, "effective": "next_start"})
}
