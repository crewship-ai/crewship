package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/keepercfg"
	"github.com/crewship-ai/crewship/internal/llm"
)

// Resolving the vault key an evaluator slot spends (#1554).
//
// keeper_aux_settings could name a provider and a model per slot but not a
// credential, so every hosted evaluator dialled with whatever ANTHROPIC_API_KEY
// the server process booted with. This is the pair of seams that closes that:
// one for the BUILD path and one for the WRITE path, deliberately scoped
// differently because the table is instance-global while the vault is
// workspace-scoped.
//
//   - NewAuxCredentialLookup (build): by id alone. The setting is instance-wide,
//     so scoping the resolution to whichever workspace happened to trigger the
//     evaluation would make one setting work in one workspace and silently
//     degrade in every other — which is the exact class of silent failure this
//     issue exists to remove.
//   - newAuxCredentialCheck (write): workspace-scoped. An admin may only bind a
//     credential their own workspace holds, so cross-tenant selection is refused
//     at the only moment a human chooses one.
//
// (#1558 owns the wider question of what an instance-global setting over a
// workspace-scoped vault should mean. This is the narrowest split that is not a
// silent failure in either direction.)

// newAuxCredentialLookup builds the keepercfg.AuxCredentialLookup the evaluator
// builders consume.
//
// Revoke-safety (§4.4, mirroring govModelCredentialLookup): the query requires
// status = 'ACTIVE' AND deleted_at IS NULL. A revoke in this product is a soft
// delete, so the FK's ON DELETE SET NULL never fires and the id survives in the
// aux row — this query is the thing that notices, and its error is what makes
// the caller degrade to the process-env key rather than dial with a stale id.
//
// Only API_KEY is usable. An evaluator's endpoint is ours (api.anthropic.com /
// api.openai.com), so there is nothing for an ENDPOINT_URL credential to do
// here; accepting one would give the operator a slot that saves cleanly and
// then authenticates with a URL.
//
// Exported because the server bootstrap wires it into buildPhase2Evaluators.
func NewAuxCredentialLookup(db *sql.DB) keepercfg.AuxCredentialLookup {
	return func(ctx context.Context, credentialID string) (string, error) {
		if db == nil {
			return "", fmt.Errorf("evaluator credential lookup: no db handle")
		}
		if credentialID == "" {
			return "", fmt.Errorf("evaluator credential lookup: empty credential id")
		}

		var encrypted, credType string
		err := db.QueryRowContext(ctx, `
			SELECT encrypted_value, type FROM credentials
			WHERE id = ? AND status = 'ACTIVE' AND deleted_at IS NULL`,
			credentialID).Scan(&encrypted, &credType)
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("evaluator credential %q is not found, inactive, or revoked", credentialID)
		}
		if err != nil {
			return "", fmt.Errorf("evaluator credential lookup: %w", err)
		}
		if credType != string(CredTypeAPIKey) {
			return "", fmt.Errorf("evaluator credential %q is a %s; an evaluator needs an API_KEY", credentialID, credType)
		}

		dec, err := decryptCredential(encrypted)
		if err != nil {
			// The wrapped error is a crypto failure, not the plaintext — but say
			// only that, so no future change can widen this into a value echo.
			return "", fmt.Errorf("evaluator credential %q could not be decrypted", credentialID)
		}
		if dec == "" || isPendingSentinel(dec) {
			return "", fmt.Errorf("evaluator credential %q has no usable value yet (pending)", credentialID)
		}
		return dec, nil
	}
}

// buildAuxWithCredential builds one aux-slot provider, sourcing its key from the
// named vault credential when there is one.
//
// It is the BOOT-time counterpart of internal/server's auxLiveResolver: the
// run_summary verdict provider is built once and captured into every pipeline
// executor, so it cannot go through the per-request seam. Both have to honour
// the same credential, or the console would offer a key picker on a row that
// quietly keeps spending the process env's key.
//
// Revoke-safety, same contract: an unusable credential DEGRADES to the env key
// with a WARN. It never fails the build on that account — the post-run verdict
// is a feature that must not disappear because a key was rotated.
func buildAuxWithCredential(
	ctx context.Context,
	m llm.AuxModel,
	credentialID string,
	lookup keepercfg.AuxCredentialLookup,
	logger *slog.Logger,
) (llm.Provider, error) {
	var apiKey string
	if lookup != nil && credentialID != "" && m.Provider != keepercfg.ProviderOllama {
		key, err := lookup(ctx, credentialID)
		if err != nil {
			logger.Warn("keeper: evaluator credential is unusable; the slot falls back to the server's own key",
				"provider", m.Provider, "model", m.Model, "error", err)
		} else {
			apiKey = key
		}
	}
	return llm.BuildAuxProviderWithKey(m, "", apiKey)
}

// newAuxCredentialCheck builds the write-time validator the admin handler uses.
// It answers "may this workspace's admin bind this credential to an evaluator",
// and nothing else — it never returns the secret, because the write path has no
// use for one.
//
// An empty id is the documented clear ("go back to the process env"), so it is
// not a lookup and must not be refused as a missing credential.
func newAuxCredentialCheck(db *sql.DB) func(ctx context.Context, workspaceID, credentialID string) error {
	return func(ctx context.Context, workspaceID, credentialID string) error {
		if credentialID == "" {
			return nil
		}
		if db == nil {
			return fmt.Errorf("credentials are not available on this server")
		}
		if workspaceID == "" {
			return fmt.Errorf("a workspace is required to choose a credential")
		}

		var credType string
		err := db.QueryRowContext(ctx, `
			SELECT type FROM credentials
			WHERE id = ? AND workspace_id = ? AND status = 'ACTIVE' AND deleted_at IS NULL`,
			credentialID, workspaceID).Scan(&credType)
		if err == sql.ErrNoRows {
			return fmt.Errorf("no active credential %q in this workspace — evaluators can only spend a key this workspace holds", credentialID)
		}
		if err != nil {
			return fmt.Errorf("look up credential %q: %w", credentialID, err)
		}
		if credType != string(CredTypeAPIKey) {
			return fmt.Errorf("credential %q is a %s — an evaluator needs an API_KEY", credentialID, credType)
		}
		return nil
	}
}
