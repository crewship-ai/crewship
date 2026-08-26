package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
)

// errTemplateNotFound is returned by deployCrewTemplate when the slug doesn't exist.
var errTemplateNotFound = errors.New("template not found")

// errCrewSlugConflict is returned by deployCrewTemplate when the crew slug is already taken.
var errCrewSlugConflict = errors.New("crew slug already exists")

// crewTemplateBySlugScope is the tail every single-row crew-template lookup
// shares. It exists as one constant because the tie-break in it is a product
// rule, not a query detail, and four call sites have to agree on it:
// crew_templates.go (Get, deployCrewTemplate), agents_hire.go
// (lookupCrewTemplate) and onboarding.go (the crew-name default).
//
// Until #1796 the schema made the tie-break unnecessary: crew_templates.slug
// carried a global UNIQUE, so `slug = ? AND (is_builtin = 1 OR workspace_id =
// ?)` could match at most one row and QueryRow's "first row wins" was never
// exercised. Scoping that constraint to the workspace is what makes two
// matches possible — a builtin and a workspace template of the same slug — and
// without an ORDER BY, which one a caller gets is up to whatever order SQLite
// happens to produce. Nondeterministic, and silently so.
//
// The rule: THE MORE SPECIFIC ROW WINS. A workspace template shadows the
// builtin of the same slug, for that workspace only. `workspace_id IS NULL`
// evaluates to 0 for the workspace-owned row and 1 for the builtin, so
// ordering by it ascending puts the override first. This is what lets an
// operator customise a shipped template in place instead of renaming it —
// renaming would break every reference to the old slug.
//
// The `is_builtin = 1 AND workspace_id IS NULL` half is one condition, not
// two spellings of the same thing. is_builtin is a plain INTEGER DEFAULT 0
// with no CHECK tying it to workspace_id, so a WORKSPACE-OWNED row carrying
// is_builtin = 1 is expressible — and under the pre-#1796 predicate
// (`is_builtin = 1 OR workspace_id = ?`) it matched for every tenant, and
// sorted at 0, ahead of the genuine builtin and tied with the caller's own
// override. The global UNIQUE is what used to keep that unreachable: a
// foreign row could not share a slug with a builtin. Splitting it into the
// two partial indexes is what makes the collision expressible, so the
// predicate is written to match those indexes exactly — the builtin namespace
// is `workspace_id IS NULL`, everything else belongs to exactly one tenant.
// Covered by crew_templates_foreign_builtin_test.go.
//
// Takes two placeholders, in order: slug, workspace id.
const crewTemplateBySlugScope = `
	WHERE slug = ? AND ((is_builtin = 1 AND workspace_id IS NULL) OR workspace_id = ?)
	ORDER BY (workspace_id IS NULL)
	LIMIT 1`

type deployCrewResult struct {
	CrewID     string   `json:"crew_id"`
	CrewName   string   `json:"crew_name"`
	CrewSlug   string   `json:"crew_slug"`
	AgentCount int      `json:"agent_count"`
	AgentIDs   []string `json:"agent_ids"`
}

// deployCrewTemplate is a package-level helper shared by CrewTemplateHandler and
// (deprecated) Captain tool executors. CrewTemplateHandler is the primary caller;
// the Captain executor is retained for backward compat only (Captain deprecated 2026-04-16).
// crewSlugInput may be empty — if so, it is derived from crewName via slugify.
//
// Pass a journal.Emitter (callers' h.journal — already defaulted to noopEmitter
// when nothing is wired up) so the template/Captain auto-assign trail lands in
// the canonical event stream alongside server logs.
// deployOverrides carries explicit user choices that replace a template's
// per-agent defaults. The zero value deploys the template verbatim, which is
// what every caller that has nothing to override passes.
type deployOverrides struct {
	// LLMModel replaces CrewTemplateAgent.LLMModel — but only on agents whose
	// llm_provider matches Provider. Writing a Gemini model id onto a
	// CLAUDE_CODE agent breaks it outright, which is strictly worse than the
	// template's own default, so a mismatch falls back rather than applying.
	LLMModel string
	// Provider is the user's chosen provider, already normalised by
	// resolveLLMProvider. Empty disables the override entirely.
	Provider string
}

// modelFor returns the model an agent should be created with: the override
// when it applies, otherwise the template's own pin.
func (o deployOverrides) modelFor(agentProvider, templateModel string) string {
	m := strings.TrimSpace(o.LLMModel)
	if m == "" || strings.TrimSpace(o.Provider) == "" {
		return templateModel
	}
	if !strings.EqualFold(strings.TrimSpace(o.Provider), strings.TrimSpace(agentProvider)) {
		return templateModel
	}
	return m
}

func deployCrewTemplate(ctx context.Context, db *sql.DB, logger *slog.Logger, j journal.Emitter, wsID, templateSlug, crewName, crewSlugInput string, overrides deployOverrides) (*deployCrewResult, error) {
	crewSlug := crewSlugInput
	if crewSlug == "" {
		crewSlug = slugify(crewName)
	} else {
		crewSlug = slugify(crewSlug)
	}
	if crewSlug == "" {
		return nil, fmt.Errorf("%w: crew_slug must contain only lowercase letters, numbers, and hyphens", errCrewSlugConflict)
	}

	var agentsJSON string
	var icon, color *string
	err := db.QueryRowContext(ctx, `
		SELECT agents_json, icon, color FROM crew_templates`+
		crewTemplateBySlugScope, templateSlug, wsID).Scan(&agentsJSON, &icon, &color)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errTemplateNotFound
		}
		return nil, fmt.Errorf("load template: %w", err)
	}

	var agents []database.CrewTemplateAgent
	if err := json.Unmarshal([]byte(agentsJSON), &agents); err != nil {
		return nil, fmt.Errorf("parse template agents: %w", err)
	}

	var existing int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM crews WHERE slug = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewSlug, wsID).Scan(&existing); err != nil {
		return nil, fmt.Errorf("check slug uniqueness: %w", err)
	}
	if existing > 0 {
		return nil, fmt.Errorf("%w: '%s'", errCrewSlugConflict, crewSlug)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	crewID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	if _, err = tx.ExecContext(ctx, `
		INSERT INTO crews (id, workspace_id, name, slug, icon, color, network_mode, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		crewID, wsID, crewName, crewSlug, icon, color, database.DefaultCrewNetworkMode, now, now); err != nil {
		return nil, fmt.Errorf("create crew: %w", err)
	}

	var agentIDs []string
	for _, a := range agents {
		agentID := generateCUID()
		agentIDs = append(agentIDs, agentID)
		// #1072/#1029: store the webhook secret encrypted at rest (fail-open
		// without a key). This creation path never reveals the secret — it's
		// obtained via the show-once rotate endpoint — so a plain encrypt is fine.
		storedSecret, _, encErr := encryption.EncryptAtRest(generateWebhookSecret())
		if encErr != nil {
			return nil, fmt.Errorf("encrypt webhook secret for %s: %w", a.Name, encErr)
		}
		webhookSecret := storedSecret
		agentSlug := a.Slug + "-" + crewSlug

		if _, err = tx.ExecContext(ctx, `
			INSERT INTO agents (id, workspace_id, crew_id, name, slug, role_title, agent_role,
				cli_adapter, llm_provider, llm_model, tool_profile, system_prompt_legacy,
				timeout_seconds, memory_enabled, webhook_secret, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			agentID, wsID, crewID, a.Name, agentSlug, a.RoleTitle, a.AgentRole,
			a.CLIAdapter, a.LLMProvider, overrides.modelFor(a.LLMProvider, a.LLMModel), a.ToolProfile, a.SystemPrompt,
			1800, true, webhookSecret, now, now); err != nil {
			return nil, fmt.Errorf("create agent %s: %w", a.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	// Auto-assign workspace AI credentials to all new agents (best-effort, after commit).
	for _, agentID := range agentIDs {
		autoAssignCredentials(ctx, db, logger, j, wsID, agentID, now)
	}

	return &deployCrewResult{
		CrewID:     crewID,
		CrewName:   crewName,
		CrewSlug:   crewSlug,
		AgentCount: len(agentIDs),
		AgentIDs:   agentIDs,
	}, nil
}

// autoAssignCredentials assigns all workspace-scoped AI credentials (API_KEY, AI_CLI_TOKEN)
// from Anthropic to the given agent. Best-effort: failures land both in the server log
// (logger.Warn — operators tail this for live debugging) AND in the canonical journal
// stream as `credential.auto_assign_failed` / `credential.auto_assign_empty` entries
// (so the workspace timeline shows why a freshly-deployed agent "runs but says nothing").
// Callers can still finish the parent operation and surface 201.
//
// j is required and called without nil-checks per project convention — pass
// noopEmitter{} in tests when journaling is irrelevant. Pass nil logger only in tests.
func autoAssignCredentials(ctx context.Context, db *sql.DB, logger *slog.Logger, j journal.Emitter, wsID, agentID, now string) {
	emitFailure := func(reason, credID string, err error) {
		payload := map[string]any{
			"workspace_id": wsID,
			"agent_id":     agentID,
			"reason":       reason,
		}
		if credID != "" {
			payload["credential_id"] = credID
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		if _, emitErr := j.Emit(ctx, journal.Entry{
			WorkspaceID: wsID,
			AgentID:     agentID,
			Type:        journal.EntryCredentialAutoAssignFailed,
			Severity:    journal.SeverityWarn,
			ActorType:   journal.ActorSystem,
			Summary:     fmt.Sprintf("auto-assign credential failed (%s)", reason),
			Payload:     payload,
		}); emitErr != nil && logger != nil {
			logger.Warn("autoAssignCredentials: journal emit failed",
				"workspace_id", wsID, "agent_id", agentID, "error", emitErr)
		}
	}

	// Match the credential to the agent's OWN provider, read from the row
	// that was just written.
	//
	// This used to be hardcoded to ANTHROPIC, and the failure was silent
	// and total: a workspace set up with Gemini, Codex, Cursor or Factory
	// stored its credential under that provider, this query matched
	// nothing, and every agent in the crew came up with zero credentials.
	// No error surfaced anywhere the user could see it — the crew simply
	// did not work. The onboarding wizard reaches this path for all four
	// builtin templates, so it was one adapter choice away on the most
	// common flow in the product.
	//
	// An agent whose provider is unset falls back to ANTHROPIC, which is
	// the same default resolveLLMProvider applies on the write side; the
	// two have to agree or a credential stored under the default would
	// stop matching the agent that shares it.
	var agentProvider string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(llm_provider, '') FROM agents WHERE id = ?`, agentID,
	).Scan(&agentProvider); err != nil {
		if logger != nil {
			logger.Warn("autoAssignCredentials: agent provider lookup failed",
				"workspace_id", wsID, "agent_id", agentID, "error", err)
		}
		emitFailure("provider_lookup", "", err)
		return
	}
	agentProvider = strings.ToUpper(strings.TrimSpace(agentProvider))
	if agentProvider == "" {
		agentProvider = "ANTHROPIC"
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, name FROM credentials
		WHERE workspace_id = ? AND type IN ('API_KEY', 'AI_CLI_TOKEN')
		  AND provider = ? AND deleted_at IS NULL AND status = 'ACTIVE'
		ORDER BY created_at ASC`, wsID, agentProvider)
	if err != nil {
		if logger != nil {
			logger.Warn("autoAssignCredentials: list query failed",
				"workspace_id", wsID, "agent_id", agentID, "error", err)
		}
		emitFailure("list_query", "", err)
		return
	}
	defer rows.Close()
	// credentialsFound flips on the first successful Scan, so it stays
	// false ONLY when the workspace has zero matching credential rows.
	// We deliberately don't track "assigned count" for the empty-event
	// gate: if rows existed but every insert failed, those are already
	// covered by per-row credential.auto_assign_failed entries — firing
	// the "empty" entry there too would misreport "no credentials"
	// when really there were credentials we just couldn't link.
	credentialsFound := false
	for rows.Next() {
		var credID, credName string
		if err := rows.Scan(&credID, &credName); err != nil {
			if logger != nil {
				logger.Warn("autoAssignCredentials: scan failed",
					"workspace_id", wsID, "agent_id", agentID, "error", err)
			}
			emitFailure("scan", "", err)
			continue
		}
		credentialsFound = true
		if _, err := db.ExecContext(ctx, `
			INSERT OR IGNORE INTO agent_credentials (agent_id, credential_id, env_var_name, created_at)
			VALUES (?, ?, ?, ?)`, agentID, credID, credName, now); err != nil {
			if logger != nil {
				logger.Warn("autoAssignCredentials: insert failed",
					"workspace_id", wsID, "agent_id", agentID,
					"credential_id", credID, "error", err)
			}
			emitFailure("insert", credID, err)
			continue
		}
	}
	// Surface late cursor failures (network blip, conn drop mid-iteration)
	// — without this, partial assignments could be reported as success.
	if err := rows.Err(); err != nil {
		if logger != nil {
			logger.Warn("autoAssignCredentials: row iteration error",
				"workspace_id", wsID, "agent_id", agentID, "error", err)
		}
		emitFailure("row_iteration", "", err)
	}
	if !credentialsFound {
		// Most common cause of "agent created but chat returns empty":
		// no Anthropic creds in the workspace yet. Log so operators
		// know to add a credential, and surface in the journal so it
		// shows up alongside the agent.created entry in the timeline.
		if logger != nil {
			logger.Warn("autoAssignCredentials: no Anthropic credentials available — agent will need manual assignment to chat",
				"workspace_id", wsID, "agent_id", agentID)
		}
		if _, err := j.Emit(ctx, journal.Entry{
			WorkspaceID: wsID,
			AgentID:     agentID,
			Type:        journal.EntryCredentialAutoAssignEmpty,
			Severity:    journal.SeverityWarn,
			ActorType:   journal.ActorSystem,
			Summary:     "no Anthropic credentials available — agent will need manual assignment",
			Payload: map[string]any{
				"workspace_id": wsID,
				"agent_id":     agentID,
			},
		}); err != nil && logger != nil {
			logger.Warn("autoAssignCredentials: journal emit failed",
				"workspace_id", wsID, "agent_id", agentID, "error", err)
		}
	}
}

// CrewTemplateHandler provides endpoints for listing and applying crew templates.
type CrewTemplateHandler struct {
	db      *sql.DB
	logger  *slog.Logger
	journal journal.Emitter
}

// NewCrewTemplateHandler creates a CrewTemplateHandler with the given database and logger.
// Journal emitter defaults to noopEmitter — call SetJournal to wire up the real one.
func NewCrewTemplateHandler(db *sql.DB, logger *slog.Logger) *CrewTemplateHandler {
	return &CrewTemplateHandler{db: db, logger: logger, journal: noopEmitter{}}
}

// SetJournal attaches the canonical event-stream emitter; credential auto-assign
// failures from this handler land in `journal_entries` so the workspace timeline
// reflects them. Defaults to noopEmitter when not called (e.g. tests).
func (h *CrewTemplateHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

type crewTemplateResponse struct {
	ID          string                       `json:"id"`
	Name        string                       `json:"name"`
	Slug        string                       `json:"slug"`
	Description *string                      `json:"description"`
	Icon        *string                      `json:"icon"`
	Color       *string                      `json:"color"`
	Category    string                       `json:"category"`
	Agents      []database.CrewTemplateAgent `json:"agents"`
	IsBuiltin   bool                         `json:"is_builtin"`
	CreatedAt   string                       `json:"created_at"`
}

// List handles GET /api/v1/crew-templates
func (h *CrewTemplateHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	seedCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := database.SeedBuiltinCrewTemplates(seedCtx, h.db, h.logger); err != nil {
		h.logger.Warn("seed crew templates", "error", err)
	}

	// The NOT EXISTS clause is the list-shaped half of the shadowing rule
	// documented on crewTemplateBySlugScope: a builtin whose slug this
	// workspace has overridden is dropped from the catalogue, so each slug
	// appears exactly once and it is the workspace's version. Without it the
	// list would show the same slug twice — and the second copy would be the
	// one Get and Deploy refuse to return, since those resolve to the
	// override. Only rows the caller's own workspace owns shadow anything;
	// another tenant's template is not visible here and cannot hide a builtin.
	//
	// The visibility predicate is the list-shaped spelling of
	// crewTemplateBySlugScope and has to stay identical to it: a row owned by
	// ANOTHER workspace that carries is_builtin = 1 is expressible (see the
	// note there), and `is_builtin = 1 OR workspace_id = ?` admits it. The
	// NOT EXISTS below would not save us either — it only drops builtins with
	// workspace_id IS NULL — so the foreign row would come back as a second
	// copy of a slug the caller has already overridden.
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT ct.id, ct.name, ct.slug, ct.description, ct.icon, ct.color,
		       ct.category, ct.agents_json, ct.is_builtin, ct.created_at
		FROM crew_templates ct
		WHERE ((ct.is_builtin = 1 AND ct.workspace_id IS NULL) OR ct.workspace_id = ?)
		  AND NOT (ct.workspace_id IS NULL AND EXISTS (
		        SELECT 1 FROM crew_templates ov
		        WHERE ov.slug = ct.slug AND ov.workspace_id = ?))
		ORDER BY ct.is_builtin DESC, ct.category ASC, ct.name ASC`, wsID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "list crew templates", err)
		return
	}
	defer rows.Close()

	var result []crewTemplateResponse
	for rows.Next() {
		var t crewTemplateResponse
		var agentsJSON string
		if err := rows.Scan(&t.ID, &t.Name, &t.Slug, &t.Description, &t.Icon, &t.Color,
			&t.Category, &agentsJSON, &t.IsBuiltin, &t.CreatedAt); err != nil {
			internalError(w, r, h.logger, "scan crew template", err)
			return
		}
		if err := json.Unmarshal([]byte(agentsJSON), &t.Agents); err != nil {
			h.logger.Warn("parse agents_json", "slug", t.Slug, "error", err)
			t.Agents = []database.CrewTemplateAgent{}
		}
		result = append(result, t)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "iterate crew templates", err)
		return
	}
	if result == nil {
		result = []crewTemplateResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// Get handles GET /api/v1/crew-templates/{slug}
func (h *CrewTemplateHandler) Get(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")

	var t crewTemplateResponse
	var agentsJSON string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, name, slug, description, icon, color, category, agents_json, is_builtin, created_at
		FROM crew_templates`+crewTemplateBySlugScope, slug, wsID).Scan(
		&t.ID, &t.Name, &t.Slug, &t.Description, &t.Icon, &t.Color,
		&t.Category, &agentsJSON, &t.IsBuiltin, &t.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Template not found")
			return
		}
		internalError(w, r, h.logger, "get crew template", err)
		return
	}
	if err := json.Unmarshal([]byte(agentsJSON), &t.Agents); err != nil {
		t.Agents = []database.CrewTemplateAgent{}
	}
	writeJSON(w, http.StatusOK, t)
}

// Deploy handles POST /api/v1/crew-templates/{slug}/deploy
// Creates a crew + all agents from the template in a single transaction.
func (h *CrewTemplateHandler) Deploy(w http.ResponseWriter, r *http.Request) {
	// Deploy creates a full crew + agents (it bypasses the gated POST /crews),
	// so it must gate role, not just membership. MANAGER+ (create).
	if !requireRole(w, r, "create") {
		return
	}
	slug := r.PathValue("slug")
	wsID := WorkspaceIDFromContext(r.Context())

	var body struct {
		CrewName string `json:"crew_name"`
		CrewSlug string `json:"crew_slug"`
	}
	if err := readJSON(r, &body); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	body.CrewName = strings.TrimSpace(body.CrewName)
	if body.CrewName == "" {
		writeProblem(w, r, http.StatusBadRequest, "crew_name is required")
		return
	}

	result, err := deployCrewTemplate(r.Context(), h.db, h.logger, h.journal, wsID, slug, body.CrewName, body.CrewSlug, deployOverrides{})
	if err != nil {
		if errors.Is(err, errTemplateNotFound) {
			writeProblem(w, r, http.StatusNotFound, "Template not found")
			return
		}
		if errors.Is(err, errCrewSlugConflict) {
			writeProblem(w, r, http.StatusConflict, err.Error())
			return
		}
		h.logger.Error("deploy crew template", "template", slug, "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Failed to deploy template")
		return
	}

	h.logger.Info("crew template deployed",
		"template", slug,
		"crew_id", result.CrewID,
		"crew_name", result.CrewName,
		"agents", result.AgentCount,
	)

	writeJSON(w, http.StatusCreated, result)
}

// slugLatinFolds are the letters Unicode decomposition cannot help with.
//
// NFD splits "á" into "a" + a combining accent, so dropping combining marks
// recovers the base letter. But "ł", "ø", "đ" and "ß" are atomic code points
// with no decomposition at all — NFD leaves them exactly as they were, and the
// ASCII filter then deletes them. They need naming.
var slugLatinFolds = map[rune]string{
	'ß': "ss", 'æ': "ae", 'œ': "oe",
	'ø': "o", 'ł': "l", 'đ': "d", 'ð': "d", 'þ': "th", 'ħ': "h", 'ı': "i",
}

// slugify turns a display name into a url-safe slug.
//
// RUNE-aware, and it folds diacritics rather than discarding them. It used to
// iterate []byte and keep only ASCII, which deleted every multi-byte character
// outright: "Hlídač dostupnosti" came out as "hlda-dostupnosti", and a name
// carrying no ASCII at all — "Дозорный" — came out empty, which the crew
// endpoint reported as "crew slug already exists: crew_slug must contain only
// lowercase letters, numbers, and hyphens". Both halves of that message were
// wrong, and the person's name was fine; the slugifier was not.
//
// It also collapsed distinct names together, since "Hlídač" and "Hldac" mapped
// to the same output — a slug collision produced by the slugifier itself.
//
// The empty-input contract is unchanged: a name with nothing nameable in it
// still returns "", because callers rely on that to reject it. What no longer
// returns "" is a name that IS nameable, merely not in a Latin script — those
// fall back to a stable digest so the crew remains addressable. The display
// name, which is the thing a person actually reads, is untouched either way.
func slugify(name string) string {
	trimmed := strings.TrimSpace(name)
	s := strings.ToLower(trimmed)

	// Did the name contain anything that is a letter or a digit in ANY script?
	// This is what separates "Дозорный" — a real name written in a script this
	// cannot transliterate — from "!!!", which names nothing at all. Only the
	// former earns the digest fallback; the latter must keep returning "" so
	// callers go on rejecting it.
	hadAlnum := false

	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			hadAlnum = true
		}
		switch {
		case unicode.Is(unicode.Mn, r):
			// A combining mark left over from NFD — the accent itself, now
			// that its base letter has been emitted.
			continue
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		default:
			if fold, ok := slugLatinFolds[r]; ok {
				b.WriteString(fold)
			}
			// Anything else (Cyrillic, CJK, punctuation) is dropped here and
			// recovered by the digest fallback below if nothing survived.
		}
	}

	result := b.String()
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	result = strings.Trim(result, "-")
	if result != "" || !hadAlnum {
		return result
	}
	// A real name in a script this cannot transliterate. Returning "" would
	// tell the caller the name was unusable, which is not true — so give it a
	// stable, legal handle derived from the name itself. Same name, same slug,
	// every time; different names never collide.
	sum := sha256.Sum256([]byte(trimmed))
	return "crew-" + hex.EncodeToString(sum[:4])
}

func generateWebhookSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return fmt.Sprintf("%x", b)
}
