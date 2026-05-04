package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/recipes"
)

// RecipeHandler powers the 1-click recipe install flow on the
// dashboard empty state and the marketplace empty state. The recipes
// themselves are baked into the binary (internal/recipes) for MVP —
// see CONNECTIONS.md §6 for the rationale.

type RecipeHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewRecipeHandler(db *sql.DB, logger *slog.Logger) *RecipeHandler {
	return &RecipeHandler{db: db, logger: logger}
}

// List returns the curated recipe set.
//
// GET /api/v1/recipes
func (h *RecipeHandler) List(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, recipes.All())
}

// Get returns a single recipe by slug. Distinct from List so the
// install Sheet can fetch fresh detail without paginating the whole
// catalogue.
//
// GET /api/v1/recipes/{slug}
func (h *RecipeHandler) Get(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	rec := recipes.FindBySlug(slug)
	if rec == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Recipe not found"})
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// installRecipeRequest carries the credential values the user typed
// into the install wizard. Keys are credential env var names from
// the recipe's Credentials list.
type installRecipeRequest struct {
	// CredentialValues maps env_var_name -> raw secret. Missing
	// entries are treated as "user already has this credential in
	// the workspace" — the install flow looks up by env_var_name
	// and reuses the existing record.
	CredentialValues map[string]string `json:"credential_values"`

	// AccountLabel maps env_var_name -> human label for the
	// credential. Optional but encouraged (CONNECTIONS.md §3.3 multi-
	// account model promotes account_label to required in Add
	// Credential wizard, but for recipe install we accept
	// auto-generated labels too).
	AccountLabels map[string]string `json:"account_labels"`
}

// installRecipeResponse summarises what was created. The FE redirects
// to /crews?crew=<slug> after install — including the new IDs lets
// us link directly without a follow-up fetch.
type installRecipeResponse struct {
	CrewID            string   `json:"crew_id"`
	CrewSlug          string   `json:"crew_slug"`
	CredentialsAdded  []string `json:"credentials_added"`
	CredentialsReused []string `json:"credentials_reused"`
	MCPServersAdded   []string `json:"mcp_servers_added"`
}

// previewRecipeResponse describes what install WOULD do without
// committing — backs the dry-run preview step in the install Sheet.
type previewRecipeResponse struct {
	Recipe              *recipes.Recipe `json:"recipe"`
	NeededCredentials   []string        `json:"needed_credentials"`
	ExistingCredentials map[string]bool `json:"existing_credentials"`
	CrewSlugAvailable   bool            `json:"crew_slug_available"`
	ResolvedCrewSlug    string          `json:"resolved_crew_slug"`
}

// Preview is a dry run: tells the FE which credentials the user
// already has in the workspace (so the install Sheet can skip the
// "Paste your X" step for those), and what crew_slug the install
// will resolve to (suffixes -2 / -3 if the recipe's slug is taken).
//
// GET /api/v1/recipes/{slug}/preview
func (h *RecipeHandler) Preview(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	rec := recipes.FindBySlug(slug)
	if rec == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Recipe not found"})
		return
	}

	// Look up existing credentials by env_var_name (= credentials.name).
	existing := map[string]bool{}
	needed := []string{}
	for _, c := range rec.Credentials {
		var have int
		_ = h.db.QueryRowContext(r.Context(), `
			SELECT COUNT(*) FROM credentials
			WHERE workspace_id = ? AND name = ? AND deleted_at IS NULL`,
			workspaceID, c.EnvVarName).Scan(&have)
		if have > 0 {
			existing[c.EnvVarName] = true
		} else {
			needed = append(needed, c.EnvVarName)
		}
	}

	resolvedSlug, available, err := resolveCrewSlug(r.Context(), h.db, workspaceID, rec.CrewSlug)
	if err != nil {
		h.logger.Error("resolve crew slug", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, previewRecipeResponse{
		Recipe:              rec,
		NeededCredentials:   needed,
		ExistingCredentials: existing,
		CrewSlugAvailable:   available,
		ResolvedCrewSlug:    resolvedSlug,
	})
}

// Install commits the whole recipe atomically — credentials, crew,
// and MCP servers are all created in one transaction, so a failure
// at any step rolls back everything (no orphaned crew with missing
// credentials etc.).
//
// POST /api/v1/recipes/{slug}/install
func (h *RecipeHandler) Install(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	user := UserFromContext(r.Context())
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	slug := r.PathValue("slug")
	rec := recipes.FindBySlug(slug)
	if rec == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Recipe not found"})
		return
	}

	var req installRecipeRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	// Validate: every credential the user doesn't already have must
	// be supplied. Reusing existing credentials is the supported
	// path for "I've already connected GitHub once".
	credByName := map[string]string{}
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, name FROM credentials
		WHERE workspace_id = ? AND deleted_at IS NULL`, workspaceID)
	if err != nil {
		h.logger.Error("preload credentials", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err == nil {
			credByName[name] = id
		}
	}
	rows.Close()

	missing := []string{}
	for _, c := range rec.Credentials {
		if _, have := credByName[c.EnvVarName]; have {
			continue
		}
		v, ok := req.CredentialValues[c.EnvVarName]
		if !ok || strings.TrimSpace(v) == "" {
			missing = append(missing, c.EnvVarName)
		}
	}
	if len(missing) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":               "Missing credential values",
			"missing_credentials": missing,
		})
		return
	}

	resolvedSlug, _, err := resolveCrewSlug(r.Context(), h.db, workspaceID, rec.CrewSlug)
	if err != nil {
		h.logger.Error("resolve crew slug", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin install tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			h.logger.Warn("install tx rollback", "error", rbErr)
		}
	}()

	now := time.Now().UTC().Format(time.RFC3339)
	resp := installRecipeResponse{
		CrewSlug:          resolvedSlug,
		CredentialsAdded:  []string{},
		CredentialsReused: []string{},
		MCPServersAdded:   []string{},
	}

	// 1. Credentials — create new, capture IDs of reused.
	credIDByEnvVar := map[string]string{}
	for _, c := range rec.Credentials {
		if existingID, have := credByName[c.EnvVarName]; have {
			credIDByEnvVar[c.EnvVarName] = existingID
			resp.CredentialsReused = append(resp.CredentialsReused, c.EnvVarName)
			continue
		}
		raw := strings.TrimSpace(req.CredentialValues[c.EnvVarName])
		enc, err := encryption.Encrypt(raw)
		if err != nil {
			h.logger.Error("encrypt recipe credential", "error", err, "env_var", c.EnvVarName)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to encrypt credential"})
			return
		}
		credID := generateCUID()
		label := req.AccountLabels[c.EnvVarName]
		if label == "" {
			label = c.Label
		}
		if _, err := tx.ExecContext(r.Context(), `
			INSERT INTO credentials (id, workspace_id, name, encrypted_value, scope, type, provider,
				account_label, status, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'WORKSPACE', ?, ?, ?, 'ACTIVE', ?, ?, ?)`,
			credID, workspaceID, c.EnvVarName, enc, c.Type, c.Provider, label,
			user.ID, now, now); err != nil {
			h.logger.Error("insert recipe credential", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		credIDByEnvVar[c.EnvVarName] = credID
		resp.CredentialsAdded = append(resp.CredentialsAdded, c.EnvVarName)
	}

	// 2. Crew — slug already resolved with collision suffix above.
	crewID := generateCUID()
	if _, err := tx.ExecContext(r.Context(), `
		INSERT INTO crews (id, workspace_id, name, slug, icon, color, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		crewID, workspaceID, rec.Name, resolvedSlug, rec.Icon, rec.Color, now, now); err != nil {
		h.logger.Error("insert recipe crew", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	resp.CrewID = crewID

	// 3. MCP servers — env_json maps the recipe's EnvMapping into
	//    actual credential references. The integration handlers
	//    elsewhere consume env_json as a map[string]string of
	//    "ENV_NAME" -> "$credential_name" for the agent runtime.
	for _, srv := range rec.MCPServers {
		envMap := map[string]string{}
		for envName, credEnvVar := range srv.EnvMapping {
			// Recipe declares credEnvVar; integration code resolves
			// the credential lookup at agent run time. We store the
			// env_var_name (= credentials.name) so the integration
			// can re-resolve even if credential IDs change.
			envMap[envName] = credEnvVar
		}
		envJSON, _ := json.Marshal(envMap)
		argsJSON, _ := json.Marshal(srv.Args)

		serverID := generateCUID()
		if _, err := tx.ExecContext(r.Context(), `
			INSERT INTO crew_mcp_servers (id, crew_id, name, display_name, transport,
				command, args_json, endpoint, env_json, icon, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			serverID, crewID, srv.Name, srv.DisplayName, srv.Transport,
			nullableString(srv.Command), nullableJSON(argsJSON, "[]"),
			nullableString(srv.Endpoint), nullableJSON(envJSON, "{}"),
			nullableString(srv.Icon), now, now); err != nil {
			h.logger.Error("insert recipe mcp server", "error", err, "name", srv.Name)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		resp.MCPServersAdded = append(resp.MCPServersAdded, srv.Name)
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit install tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusCreated, resp)
}

// resolveCrewSlug suffixes -2/-3/... to the recipe's preferred slug
// if it's already taken in the workspace, returning the resolved
// slug plus a flag indicating whether the original was free.
func resolveCrewSlug(ctx context.Context, db *sql.DB, workspaceID, base string) (string, bool, error) {
	taken := func(s string) (bool, error) {
		var n int
		err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`,
			workspaceID, s).Scan(&n)
		if err != nil {
			return false, err
		}
		return n > 0, nil
	}
	t, err := taken(base)
	if err != nil {
		return "", false, err
	}
	if !t {
		return base, true, nil
	}
	// Try -2 .. -100; in practice nobody installs the same recipe
	// 100 times, but the cap protects against runaway loops.
	for i := 2; i < 100; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		t, err := taken(candidate)
		if err != nil {
			return "", false, err
		}
		if !t {
			return candidate, false, nil
		}
	}
	return "", false, errors.New("could not allocate crew slug after 100 tries")
}

// nullableString returns a sql.Null-ish string: empty input → empty
// string (NOT NULL constraint compliant), non-empty preserved as-is.
// Existing crew_mcp_servers columns are mostly TEXT NOT NULL with
// empty defaults, so an empty string is the canonical zero value.
func nullableString(s string) string {
	return s
}

// nullableJSON returns the JSON-encoded value or the fallback if the
// raw bytes are empty (e.g. nil slice / map yields "null" not "[]" /
// "{}"). Centralised so the env_json / args_json columns get the
// right shape for downstream code that expects "[]" or "{}" sentinels.
func nullableJSON(raw []byte, fallback string) string {
	s := string(raw)
	if s == "" || s == "null" {
		return fallback
	}
	return s
}
