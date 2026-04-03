package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// --- Registry API response types (defensive parsing) ---

type registryEnvVar struct {
	Name       string `json:"name"`
	IsRequired bool   `json:"is_required"`
	IsSecret   bool   `json:"is_secret"`
}

type registryPackage struct {
	RegistryName string           `json:"registry_name"`
	Name         string           `json:"name"`
	Version      string           `json:"version"`
	Runtime      string           `json:"runtime"`
	EnvVars      []registryEnvVar `json:"environment_variables"`
}

type registryRemote struct {
	TransportType string           `json:"transport_type"`
	URL           string           `json:"url"`
	Headers       []registryEnvVar `json:"headers"`
}

type registryServerEntry struct {
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name"`
	Description string            `json:"description"`
	Homepage    string            `json:"homepage"`
	SourceURL   string            `json:"source_url"`
	Icon        string            `json:"icon"`
	Category    string            `json:"category"`
	IsVerified  bool              `json:"is_verified"`
	Packages    []registryPackage `json:"packages"`
	Remotes     []registryRemote  `json:"remotes"`
}

// --- Sync function ---

const mcpRegistryURL = "https://registry.modelcontextprotocol.io/v0/servers"

// SyncMCPRegistry fetches the official MCP registry and upserts all servers
// into the local mcp_registry_servers table.
func SyncMCPRegistry(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mcpRegistryURL, nil)
	if err != nil {
		return fmt.Errorf("create registry request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch registry: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20)) // 50MB limit
	if err != nil {
		return fmt.Errorf("read registry response: %w", err)
	}

	var entries []registryServerEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return fmt.Errorf("parse registry response: %w", err)
	}

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sync transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO mcp_registry_servers
			(id, name, display_name, description, icon, transport,
			 homepage_url, source_url, package_name, package_registry,
			 command, endpoint, auth_type, env_vars_json, category,
			 is_verified, synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare upsert statement: %w", err)
	}
	defer stmt.Close()

	count := 0
	for _, entry := range entries {
		if entry.Name == "" {
			continue
		}

		displayName := entry.DisplayName
		if displayName == "" {
			displayName = entry.Name
		}

		transport := "stdio"
		packageName := ""
		packageRegistry := ""
		command := ""
		endpoint := ""
		authType := ""
		var envVars []registryEnvVar

		// Prefer packages (stdio) if available, otherwise use remotes
		if len(entry.Packages) > 0 {
			pkg := entry.Packages[0]
			transport = "stdio"
			packageName = pkg.Name
			packageRegistry = pkg.RegistryName
			if pkg.Runtime != "" {
				command = pkg.Runtime
			}
			envVars = pkg.EnvVars
		} else if len(entry.Remotes) > 0 {
			remote := entry.Remotes[0]
			transport = remote.TransportType
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

		// Use name as the stable ID
		id := entry.Name

		verified := 0
		if entry.IsVerified {
			verified = 1
		}

		if _, err := stmt.ExecContext(ctx,
			id, entry.Name, displayName, entry.Description,
			entry.Icon, transport, entry.Homepage, entry.SourceURL,
			packageName, packageRegistry, command, endpoint,
			authType, string(envVarsJSON), entry.Category,
			verified, now,
		); err != nil {
			logger.Warn("skip registry entry", "name", entry.Name, "error", err)
			continue
		}
		count++
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sync transaction: %w", err)
	}

	logger.Info("MCP registry sync complete", "servers_synced", count, "total_entries", len(entries))
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

		if err := SyncMCPRegistry(ctx, db, logger); err != nil {
			logger.Error("initial MCP registry sync failed", "error", err)
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
	IsVerified      bool   `json:"is_verified"`
	SyncedAt        string `json:"synced_at"`
}

func scanRegistryRow(rows *sql.Rows) (mcpRegistryServerRow, error) {
	var s mcpRegistryServerRow
	var isVerified int
	err := rows.Scan(
		&s.ID, &s.Name, &s.DisplayName, &s.Description, &s.Icon,
		&s.Transport, &s.HomepageURL, &s.SourceURL,
		&s.PackageName, &s.PackageRegistry, &s.Command,
		&s.Endpoint, &s.AuthType, &s.EnvVarsJSON,
		&s.Category, &isVerified, &s.SyncedAt,
	)
	s.IsVerified = isVerified != 0
	return s, err
}

const registrySelectCols = `id, name, display_name, description, icon, transport,
	homepage_url, source_url, package_name, package_registry, command,
	endpoint, auth_type, env_vars_json, category, is_verified, synced_at`

// List handles GET /api/v1/mcp-registry — returns paginated list.
func (h *MCPRegistryHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r)

	rows, err := h.db.QueryContext(r.Context(),
		fmt.Sprintf(`SELECT %s FROM mcp_registry_servers ORDER BY name ASC LIMIT ? OFFSET ?`, registrySelectCols),
		limit, offset)
	if err != nil {
		h.logger.Error("list MCP registry", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	defer rows.Close()

	servers := make([]mcpRegistryServerRow, 0)
	for rows.Next() {
		s, err := scanRegistryRow(rows)
		if err != nil {
			h.logger.Error("scan registry row", "error", err)
			continue
		}
		servers = append(servers, s)
	}

	var total int
	h.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM mcp_registry_servers").Scan(&total)

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

	limit, offset := parsePagination(r)
	pattern := "%" + q + "%"

	rows, err := h.db.QueryContext(r.Context(),
		fmt.Sprintf(`SELECT %s FROM mcp_registry_servers
			WHERE name LIKE ? OR description LIKE ? OR category LIKE ? OR display_name LIKE ?
			ORDER BY
				CASE WHEN name LIKE ? THEN 0 ELSE 1 END,
				name ASC
			LIMIT ? OFFSET ?`, registrySelectCols),
		pattern, pattern, pattern, pattern,
		pattern,
		limit, offset)
	if err != nil {
		h.logger.Error("search MCP registry", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	defer rows.Close()

	servers := make([]mcpRegistryServerRow, 0)
	for rows.Next() {
		s, err := scanRegistryRow(rows)
		if err != nil {
			h.logger.Error("scan registry row", "error", err)
			continue
		}
		servers = append(servers, s)
	}

	var total int
	h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM mcp_registry_servers
		 WHERE name LIKE ? OR description LIKE ? OR category LIKE ? OR display_name LIKE ?`,
		pattern, pattern, pattern, pattern).Scan(&total)

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
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	const syncCooldown = int64(3600) // 1 hour in seconds
	now := time.Now().Unix()
	last := h.lastSync.Load()
	if last > 0 && now-last < syncCooldown {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"error": "Sync was triggered recently, please wait before retrying",
		})
		return
	}
	h.lastSync.Store(now)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
func parsePagination(r *http.Request) (int, int) {
	limit := 50
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}
