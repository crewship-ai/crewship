package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// --- Registry API response types (2025-12-11 schema) ---
//
// The official MCP registry switched to a versioned schema in late 2025: the
// list endpoint now wraps each entry in {server, _meta}, paginates via
// metadata.nextCursor, and renames many fields (display_name → title,
// homepage → websiteUrl, source_url → repository.url, icon → icons[].src,
// transport_type → type, registry_name → registryType, environment_variables
// → environmentVariables, runtime → runtimeHint). Older parsers blow up with
// "cannot unmarshal object into Go value of type []…".

type registryEnvVar struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	IsRequired  bool   `json:"isRequired"`
	IsSecret    bool   `json:"isSecret"`
}

type registryTransport struct {
	Type string `json:"type"`
}

type registryPackage struct {
	RegistryType         string            `json:"registryType"`
	Identifier           string            `json:"identifier"`
	Version              string            `json:"version"`
	RuntimeHint          string            `json:"runtimeHint"`
	Transport            registryTransport `json:"transport"`
	EnvironmentVariables []registryEnvVar  `json:"environmentVariables"`
}

type registryRemote struct {
	Type    string           `json:"type"`
	URL     string           `json:"url"`
	Headers []registryEnvVar `json:"headers"`
}

type registryRepository struct {
	URL    string `json:"url"`
	Source string `json:"source"`
}

type registryIcon struct {
	Src string `json:"src"`
}

type registryServerEntry struct {
	Name        string             `json:"name"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	Version     string             `json:"version"`
	WebsiteURL  string             `json:"websiteUrl"`
	Repository  registryRepository `json:"repository"`
	Icons       []registryIcon     `json:"icons"`
	Packages    []registryPackage  `json:"packages"`
	Remotes     []registryRemote   `json:"remotes"`
}

type registryOfficialMeta struct {
	Status   string `json:"status"`
	IsLatest bool   `json:"isLatest"`
}

type registryEntryMeta struct {
	Official registryOfficialMeta `json:"io.modelcontextprotocol.registry/official"`
}

type registryEntryEnvelope struct {
	Server registryServerEntry `json:"server"`
	Meta   registryEntryMeta   `json:"_meta"`
}

type registryListResponse struct {
	Servers  []registryEntryEnvelope `json:"servers"`
	Metadata struct {
		NextCursor string `json:"nextCursor"`
		Count      int    `json:"count"`
	} `json:"metadata"`
}

// --- Sync function ---

const (
	// Upstream registry caps `limit` at 100 — a 200 request comes back
	// as HTTP 422 `expected number <= 100`. Pre-fix the daemon retried
	// the same too-large request four times then gave up, leaving the
	// MCP catalog permanently empty on every fresh install. Discovered
	// when the verbose-422 logging from this PR finally surfaced the
	// registry's actual rejection text. (Issue #540 root cause.)
	mcpRegistryPageSize = 100
	mcpRegistryMaxPages = 200 // hard ceiling to avoid runaway loops on broken cursors
)

// mcpRegistryURL is a var (not const) so tests can point it at a httptest
// server.
var mcpRegistryURL = "https://registry.modelcontextprotocol.io/v0/servers"

// SyncMCPRegistry fetches the official MCP registry and upserts all servers
// into the local mcp_registry_servers table. Only entries flagged as
// _meta.…/official.isLatest are persisted (the registry now returns every
// version of every server, and we want one row per server).
func SyncMCPRegistry(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	client := &http.Client{Timeout: 60 * time.Second}

	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sync transaction: %w", err)
	}
	defer tx.Rollback()

	// ON CONFLICT(name) DO UPDATE excludes trust_tier and is_featured
	// from the update set — those are admin-curation fields owned
	// locally (CONNECTIONS.md §5.6 trust tiers + DO-NOT-BUILD #4 no
	// faked install counts → featured is our manual signal). A fresh
	// INSERT seeds them with sane defaults via the schema (`'community'`
	// and `0`) but the v68 backfill promotes anything historically
	// flagged is_verified=1 to trust_tier='anthropic'. Future syncs
	// must never silently demote a curated entry.
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO mcp_registry_servers
			(id, name, display_name, description, icon, transport,
			 homepage_url, source_url, package_name, package_registry,
			 command, endpoint, auth_type, env_vars_json, category,
			 is_verified, synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			display_name = excluded.display_name,
			description = excluded.description,
			icon = excluded.icon,
			transport = excluded.transport,
			homepage_url = excluded.homepage_url,
			source_url = excluded.source_url,
			package_name = excluded.package_name,
			package_registry = excluded.package_registry,
			command = excluded.command,
			endpoint = excluded.endpoint,
			auth_type = excluded.auth_type,
			env_vars_json = excluded.env_vars_json,
			category = excluded.category,
			is_verified = excluded.is_verified,
			synced_at = excluded.synced_at
			-- intentionally NOT touched: trust_tier, is_featured
		`)
	if err != nil {
		return fmt.Errorf("prepare upsert statement: %w", err)
	}
	defer stmt.Close()

	cursor := ""
	totalEntries := 0
	count := 0
	for pageNum := 0; pageNum < mcpRegistryMaxPages; pageNum++ {
		pageURL := fmt.Sprintf("%s?limit=%d", mcpRegistryURL, mcpRegistryPageSize)
		if cursor != "" {
			pageURL += "&cursor=" + url.QueryEscape(cursor)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return fmt.Errorf("create registry request: %w", err)
		}
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("fetch registry: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			// Capture a small slice of the response body so the daily-retry
			// log line carries the upstream error message instead of just
			// "HTTP 422" — pre-fix the operator only saw the bare status
			// code and had to curl the registry by hand to find out what
			// the schema mismatch was. (Issue #540.)
			snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return fmt.Errorf("registry returned HTTP %d for %s: %s",
				resp.StatusCode, pageURL, strings.TrimSpace(string(snippet)))
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read registry response: %w", err)
		}

		var listPage registryListResponse
		if err := json.Unmarshal(body, &listPage); err != nil {
			return fmt.Errorf("parse registry response: %w", err)
		}

		for _, envelope := range listPage.Servers {
			totalEntries++
			entry := envelope.Server
			if entry.Name == "" {
				continue
			}
			if !envelope.Meta.Official.IsLatest {
				continue
			}

			displayName := entry.Title
			if displayName == "" {
				displayName = entry.Name
			}

			icon := ""
			if len(entry.Icons) > 0 {
				icon = entry.Icons[0].Src
			}

			transport := "stdio"
			packageName := ""
			packageRegistry := ""
			command := ""
			endpoint := ""
			authType := ""
			var envVars []registryEnvVar

			if len(entry.Packages) > 0 {
				pkg := entry.Packages[0]
				if pkg.Transport.Type != "" {
					transport = pkg.Transport.Type
				}
				packageName = pkg.Identifier
				packageRegistry = pkg.RegistryType
				if pkg.RuntimeHint != "" {
					command = pkg.RuntimeHint
				}
				envVars = pkg.EnvironmentVariables
			} else if len(entry.Remotes) > 0 {
				remote := entry.Remotes[0]
				transport = remote.Type
				if transport == "" {
					transport = "streamable-http"
				}
				endpoint = remote.URL
				envVars = remote.Headers
				if len(remote.Headers) > 0 {
					authType = "header"
				}
			}

			envVarsJSON, err := json.Marshal(envVars)
			if err != nil {
				envVarsJSON = []byte("[]")
			}

			verified := 0
			if envelope.Meta.Official.Status == "active" {
				verified = 1
			}

			if _, err := stmt.ExecContext(ctx,
				entry.Name, entry.Name, displayName, entry.Description,
				icon, transport, entry.WebsiteURL, entry.Repository.URL,
				packageName, packageRegistry, command, endpoint,
				authType, string(envVarsJSON), "",
				verified, now,
			); err != nil {
				logger.Warn("skip registry entry", "name", entry.Name, "error", err)
				continue
			}
			count++
		}

		if listPage.Metadata.NextCursor == "" || listPage.Metadata.NextCursor == cursor {
			break
		}
		cursor = listPage.Metadata.NextCursor
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sync transaction: %w", err)
	}

	logger.Info("MCP registry sync complete", "servers_synced", count, "total_entries", totalEntries)
	return nil
}

// --- Background worker ---

// StartRegistrySyncWorker runs a background goroutine that syncs the MCP
// registry on startup (after a 10s delay) and then every 24 hours.
func StartRegistrySyncWorker(db *sql.DB, logger *slog.Logger, stop <-chan struct{}, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()

		// Derive a context that cancels when stop is closed
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-stop
			cancel()
		}()

		// Initial sync after 10 second delay
		select {
		case <-stop:
			return
		case <-time.After(10 * time.Second):
		}

		// Initial sync may hit a momentarily unreachable registry; retry a
		// few times with exponential backoff so a transient 4xx/5xx at boot
		// doesn't leave the catalog permanently empty until the 24h ticker
		// fires. Stop early if the server is shutting down. This goroutine
		// is already detached from server startup, so the retry total of
		// ~3.75 min does not block boot.
		const maxAttempts = 4
		backoff := 15 * time.Second
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			err := SyncMCPRegistry(ctx, db, logger)
			if err == nil {
				if attempt > 1 {
					logger.Info("initial MCP registry sync succeeded after retry", "attempt", attempt)
				}
				break
			}
			if attempt == maxAttempts {
				logger.Error("initial MCP registry sync failed (giving up; daily ticker will retry)", "error", err, "attempts", attempt)
				break
			}
			logger.Warn("initial MCP registry sync failed; will retry", "error", err, "attempt", attempt, "next_retry_in", backoff)
			select {
			case <-stop:
				return
			case <-time.After(backoff):
			}
			backoff *= 2
		}

		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := SyncMCPRegistry(ctx, db, logger); err != nil {
					logger.Error("MCP registry sync failed", "error", err)
				}
			}
		}
	}()
}

// --- API handler ---

// MCPRegistryHandler serves the local MCP registry cache.
type MCPRegistryHandler struct {
	db       *sql.DB
	logger   *slog.Logger
	lastSync atomic.Int64 // unix timestamp of last manual sync
}

// NewMCPRegistryHandler creates a new MCPRegistryHandler.
func NewMCPRegistryHandler(db *sql.DB, logger *slog.Logger) *MCPRegistryHandler {
	return &MCPRegistryHandler{db: db, logger: logger}
}

type mcpRegistryServerRow struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	DisplayName     string `json:"display_name"`
	Description     string `json:"description"`
	Icon            string `json:"icon"`
	Transport       string `json:"transport"`
	HomepageURL     string `json:"homepage_url"`
	SourceURL       string `json:"source_url"`
	PackageName     string `json:"package_name"`
	PackageRegistry string `json:"package_registry"`
	Command         string `json:"command"`
	Endpoint        string `json:"endpoint"`
	AuthType        string `json:"auth_type"`
	EnvVarsJSON     string `json:"env_vars_json"`
	Category        string `json:"category"`
	// IsVerified is the legacy binary flag kept for back-compat with
	// callers that already render it. Prefer TrustTier for new code —
	// it carries the 3-tier signal (anthropic / crewship / community).
	IsVerified bool   `json:"is_verified"`
	TrustTier  string `json:"trust_tier"`
	IsFeatured bool   `json:"is_featured"`
	SyncedAt   string `json:"synced_at"`
}

func scanRegistryRow(rows *sql.Rows) (mcpRegistryServerRow, error) {
	var s mcpRegistryServerRow
	var isVerified, isFeatured int
	err := rows.Scan(
		&s.ID, &s.Name, &s.DisplayName, &s.Description, &s.Icon,
		&s.Transport, &s.HomepageURL, &s.SourceURL,
		&s.PackageName, &s.PackageRegistry, &s.Command,
		&s.Endpoint, &s.AuthType, &s.EnvVarsJSON,
		&s.Category, &isVerified, &s.TrustTier, &isFeatured, &s.SyncedAt,
	)
	s.IsVerified = isVerified != 0
	s.IsFeatured = isFeatured != 0
	return s, err
}

const registrySelectCols = `id, name, display_name, description, icon, transport,
	homepage_url, source_url, package_name, package_registry, command,
	endpoint, auth_type, env_vars_json, category, is_verified, trust_tier, is_featured, synced_at`

// validTrustTiers gates the ?trust_tier= query param against arbitrary
// strings — without this any user-supplied value would flow into the
// SQL fragment and break the prepared-statement contract.
var validTrustTiers = map[string]struct{}{
	"anthropic": {},
	"crewship":  {},
	"community": {},
}

// registryFilters captures the optional ?trust_tier= and ?featured=
// query params shared by List and Search. Returns the WHERE clause
// fragment (without leading WHERE/AND) and the bind args that match
// its placeholders, so callers can splice them onto whatever fixed
// predicate they already have.
//
// Returns a non-nil error when an unknown filter value is passed
// (e.g. ?trust_tier=verified-by-mom or ?featured=maybe). Silently
// dropping the predicate would broaden the result set unexpectedly,
// which CodeRabbit flagged as a security smell — bad client requests
// must surface as 400, not as oversized 200s.
func parseRegistryFilters(r *http.Request) (clause string, args []any, err error) {
	parts := []string{}
	if t := strings.TrimSpace(r.URL.Query().Get("trust_tier")); t != "" {
		if _, ok := validTrustTiers[t]; !ok {
			return "", nil, fmt.Errorf("invalid trust_tier %q (allowed: anthropic, crewship, community)", t)
		}
		parts = append(parts, "trust_tier = ?")
		args = append(args, t)
	}
	if f := strings.TrimSpace(r.URL.Query().Get("featured")); f != "" {
		switch f {
		case "true", "1":
			parts = append(parts, "is_featured = 1")
		case "false", "0":
			parts = append(parts, "is_featured = 0")
		default:
			return "", nil, fmt.Errorf("invalid featured %q (allowed: true, false, 1, 0)", f)
		}
	}
	if len(parts) == 0 {
		return "", nil, nil
	}
	return strings.Join(parts, " AND "), args, nil
}

// List handles GET /api/v1/mcp-registry — returns paginated list.
// Optional filters: ?trust_tier=anthropic|crewship|community, ?featured=true.
func (h *MCPRegistryHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r, 50, 200)

	whereClause, whereArgs, err := parseRegistryFilters(r)
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	where := ""
	if whereClause != "" {
		where = " WHERE " + whereClause
	}

	// Featured rows surface first, then alphabetical — matches the
	// CONNECTIONS.md §5.3 wireframe (featured row sits above the grid).
	query := fmt.Sprintf(
		`SELECT %s FROM mcp_registry_servers%s ORDER BY is_featured DESC, name ASC LIMIT ? OFFSET ?`,
		registrySelectCols, where)
	args := append(append([]any{}, whereArgs...), limit, offset)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("list MCP registry", "error", err)
		replyError(w, http.StatusInternalServerError, "database error")
		return
	}
	defer rows.Close()

	servers := make([]mcpRegistryServerRow, 0, capacityHint(limit))
	for rows.Next() {
		s, err := scanRegistryRow(rows)
		if err != nil {
			h.logger.Error("scan registry row", "error", err)
			continue
		}
		servers = append(servers, s)
	}

	var total int
	countQuery := "SELECT COUNT(*) FROM mcp_registry_servers" + where
	if err := h.db.QueryRowContext(r.Context(), countQuery, whereArgs...).Scan(&total); err != nil {
		h.logger.Error("count mcp registry servers", "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"servers": servers,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
	})
}

// Search handles GET /api/v1/mcp-registry/search?q=... — full-text search.
func (h *MCPRegistryHandler) Search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		h.List(w, r)
		return
	}

	limit, offset := parsePagination(r, 50, 200)
	pattern := "%" + q + "%"

	// Compose the LIKE predicate with optional trust_tier / featured
	// filters so a search can be narrowed to e.g. only Anthropic-
	// verified servers from the marketplace UI.
	filterClause, filterArgs, ferr := parseRegistryFilters(r)
	if ferr != nil {
		replyError(w, http.StatusBadRequest, ferr.Error())
		return
	}
	likeClause := "(name LIKE ? OR description LIKE ? OR category LIKE ? OR display_name LIKE ?)"
	whereClause := likeClause
	if filterClause != "" {
		whereClause = likeClause + " AND " + filterClause
	}
	likeArgs := []any{pattern, pattern, pattern, pattern}

	queryArgs := append(append([]any{}, likeArgs...), filterArgs...)
	queryArgs = append(queryArgs, pattern, limit, offset)

	rows, err := h.db.QueryContext(r.Context(),
		fmt.Sprintf(`SELECT %s FROM mcp_registry_servers
			WHERE %s
			ORDER BY
				is_featured DESC,
				CASE WHEN name LIKE ? THEN 0 ELSE 1 END,
				name ASC
			LIMIT ? OFFSET ?`, registrySelectCols, whereClause),
		queryArgs...)
	if err != nil {
		h.logger.Error("search MCP registry", "error", err)
		replyError(w, http.StatusInternalServerError, "database error")
		return
	}
	defer rows.Close()

	servers := make([]mcpRegistryServerRow, 0, capacityHint(limit))
	for rows.Next() {
		s, err := scanRegistryRow(rows)
		if err != nil {
			h.logger.Error("scan registry row", "error", err)
			continue
		}
		servers = append(servers, s)
	}

	var total int
	countArgs := append(append([]any{}, likeArgs...), filterArgs...)
	if err := h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT COUNT(*) FROM mcp_registry_servers WHERE %s`, whereClause),
		countArgs...).Scan(&total); err != nil {
		// Mirror the checked count handling in List — surface DB
		// failures rather than silently returning total=0 with a 200
		// response (CodeRabbit caught this).
		h.logger.Error("count search results", "error", err)
		replyError(w, http.StatusInternalServerError, "database error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"servers": servers,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
		"query":   q,
	})
}

// Sync handles POST /api/v1/mcp-registry/sync — triggers manual sync (admin only).
func (h *MCPRegistryHandler) Sync(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	const syncCooldown = int64(3600) // 1 hour in seconds
	now := time.Now().Unix()
	for {
		last := h.lastSync.Load()
		if last > 0 && now-last < syncCooldown {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{
				"error": "Sync was triggered recently, please wait before retrying",
			})
			return
		}
		if h.lastSync.CompareAndSwap(last, now) {
			break
		}
	}

	// Preserve OTel + auth context from the request while shedding its
	// cancellation -- the 202 has already been flushed when this fires.
	parentCtx := context.WithoutCancel(r.Context())
	go func() {
		ctx, cancel := context.WithTimeout(parentCtx, 2*time.Minute)
		defer cancel()
		if err := SyncMCPRegistry(ctx, h.db, h.logger); err != nil {
			h.logger.Error("manual MCP registry sync failed", "error", err)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "sync_started",
		"message": "MCP registry sync has been triggered in the background",
	})
}

// parsePagination extracts limit and offset from query params with defaults.
