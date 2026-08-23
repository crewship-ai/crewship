package api

// Delegated authorship: who a routine or a page ends up BELONGING to when an
// agent authors it for someone else.
//
// Every other agent-authored write in Crewship attributes to the crew that
// made the call, and the sidecar injects that crew id from its IPC config so
// the agent cannot lie about it (see internal/sidecar/pipelines.go's trust
// model note). That rule is right for every crew but one.
//
// The Crewship Guide (`_crewship-setup`, kind='setup') exists only to build
// things FOR crews the person is creating during onboarding. Under the
// caller-owns rule, everything it built landed on itself, and author_crew_id
// is not a label — three separate subsystems read it:
//
//   - internal/pipeline/egress_gate.go asks CheckHTTPStep about the AUTHOR
//     crew, so a routine written to poll seznam.cz was checked against the
//     Guide's allowlist, and unblocking it meant widening the Guide;
//   - internal/pipeline/executor.go runs agent steps in the author crew's
//     container, so the work ran as the Guide rather than as the crew that
//     was supposed to do it;
//   - the Guide's own autonomy_level is 'full' (it has to be — it creates
//     Pages), so every routine a person built during onboarding took up
//     permanent residence in the most privileged crew in the workspace.
//
// Pages had a fourth version of the same problem: a panel names its producer
// as `agent/<slug>`, and the only agent slugs the Guide could see were its
// own. Pages built at onboarding pointed their panels at the Guide.
//
// So a setup crew may name a target crew, and in exchange it may own nothing.
// Both halves matter: without the second, the first is merely an option the
// model can forget to take.
//
// The exception is deliberately narrow. It is not "an agent may name a crew"
// — an ordinary crew doing that is still the cross-crew escalation the
// original gate was built to stop, and still 403s here. It is "a
// server-created setup crew, which has no user-authored system prompt and
// exists for exactly this purpose, may name a NON-setup crew inside the
// workspace its own token is already bound to".

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

// delegatedCrewNotAllowedMsg is what an ordinary crew gets for trying.
const delegatedCrewNotAllowedMsg = "target_crew_slug is only accepted from the onboarding setup crew; " +
	"a routine or page is owned by the crew that authors it"

// setupCrewMustDelegateMsg is the error the Guide gets when it tries to keep
// something for itself.
//
// Phrased as an instruction rather than a refusal on purpose: this lands in
// the model's tool-call result, and an error that says what to do instead is
// one the model can act on without another human turn. Naming the field and
// the ordering is the whole repair.
const setupCrewMustDelegateMsg = "the onboarding setup crew cannot own routines or pages. " +
	"Propose the crew first and wait for the person to create it, then set target_crew_slug " +
	"to that crew's slug so the work belongs to them"

// resolveDelegatedAuthorCrew re-points an agent-authored write from the crew
// that called it to the crew it is authoring on behalf of.
//
// authorCrewID must already carry the CALLER's crew — that is, the caller
// must have run assertBoundCrewWorkspaceDB first, which is what proves the
// id against the token binding. This function only ever moves it to another
// crew in the same workspace, and only for a setup caller.
//
// Returns false having already written the response.
func resolveDelegatedAuthorCrew(
	w http.ResponseWriter,
	r *http.Request,
	db *sql.DB,
	logger *slog.Logger,
	workspaceID, targetSlug string,
	authorCrewID *string,
) bool {
	if authorCrewID == nil || db == nil {
		return true
	}
	callerIsSetup, err := crewIsSetupKind(r, db, *authorCrewID)
	if err != nil {
		if logger != nil {
			logger.Error("resolve delegated author crew: caller kind", "crew_id", *authorCrewID, "error", err)
		}
		replyError(w, http.StatusInternalServerError, "Failed to resolve authoring crew")
		return false
	}

	if targetSlug == "" {
		// Fail closed. A setup crew that names no target is the exact case
		// that produced the orphans, and letting it through "just this once"
		// is how it would keep happening every time the prompt drifted.
		if callerIsSetup {
			replyError(w, http.StatusUnprocessableEntity, setupCrewMustDelegateMsg)
			return false
		}
		return true
	}

	if !callerIsSetup {
		if logger != nil {
			logger.Warn("delegated crew authorship refused (caller is not a setup crew)",
				"path", r.URL.Path, "caller_crew", *authorCrewID, "target_slug", targetSlug)
		}
		replyError(w, http.StatusForbidden, delegatedCrewNotAllowedMsg)
		return false
	}

	var targetID, targetKind string
	// Scoped to workspaceID, which assertInternalTokenWorkspace has already
	// pinned to the token's binding — so this cannot reach a foreign tenant's
	// crew even though it resolves by slug, which is only unique per
	// workspace.
	err = db.QueryRowContext(r.Context(),
		`SELECT id, COALESCE(kind, '') FROM crews
		  WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`,
		workspaceID, targetSlug).Scan(&targetID, &targetKind)
	if errors.Is(err, sql.ErrNoRows) {
		// The ordering rule, enforced by physics rather than by prompt: you
		// cannot name a crew that does not exist yet, so "crew first" is not
		// something the model has to remember.
		//
		// The available slugs come back with the refusal because the likely
		// mistake is not "no crew exists" but "the crew exists under a slug
		// the model guessed wrong". A crew's slug is DERIVED server-side from
		// the name — the Guide proposes "Hlídač dostupnosti" and never sees
		// what that became — so a bare "no such crew" would send it looking
		// for a missing crew that is sitting right there. With the list, the
		// next call is correct instead of being the same call again.
		replyError(w, http.StatusUnprocessableEntity,
			"no crew with slug "+targetSlug+" in this workspace. "+
				availableCrewSlugsHint(r, db, logger, workspaceID))
		return false
	}
	if err != nil {
		if logger != nil {
			logger.Error("resolve delegated author crew: target lookup",
				"workspace_id", workspaceID, "slug", targetSlug, "error", err)
		}
		replyError(w, http.StatusInternalServerError, "Failed to resolve target crew")
		return false
	}
	if targetKind == setupCrewKindSetup {
		// Naming the Guide explicitly is the same orphan by a longer route.
		replyError(w, http.StatusUnprocessableEntity, setupCrewMustDelegateMsg)
		return false
	}

	*authorCrewID = targetID
	return true
}

// availableCrewSlugsHint names the crews the caller could have meant, or says
// plainly that there are none yet.
//
// Best-effort: this runs while composing an error, so a failure here must
// degrade to a vaguer message rather than replace a clear 422 with a 500.
func availableCrewSlugsHint(r *http.Request, db *sql.DB, logger *slog.Logger, workspaceID string) string {
	rows, err := db.QueryContext(r.Context(),
		`SELECT slug FROM crews
		  WHERE workspace_id = ? AND deleted_at IS NULL AND COALESCE(kind, '') != ?
		  ORDER BY created_at
		  LIMIT 20`, workspaceID, setupCrewKindSetup)
	if err != nil {
		if logger != nil {
			logger.Warn("list crew slugs for delegation hint", "workspace_id", workspaceID, "error", err)
		}
		return "Create the crew before building work for it."
	}
	defer rows.Close()

	var slugs []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err == nil && s != "" {
			slugs = append(slugs, s)
		}
	}
	if err := rows.Err(); err != nil || len(slugs) == 0 {
		// No crews yet is the honest, common case during onboarding: propose
		// one and wait for the person to accept it.
		return "This workspace has no crews yet — propose one and wait for the person to create it first."
	}
	return "Crews in this workspace: " + strings.Join(slugs, ", ") + "."
}

// crewIsSetupKind reports whether a crew row is the workspace's built-in
// setup crew. A missing row is not a setup crew: the callers have already
// proven the id against the token binding, and a lookup miss here must not
// be readable as "therefore privileged".
func crewIsSetupKind(r *http.Request, db *sql.DB, crewID string) (bool, error) {
	if crewID == "" {
		return false, nil
	}
	var kind string
	err := db.QueryRowContext(r.Context(),
		`SELECT COALESCE(kind, '') FROM crews WHERE id = ? AND deleted_at IS NULL`, crewID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return kind == setupCrewKindSetup, nil
}
