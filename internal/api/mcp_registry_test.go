package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// fixture: two-page response in 2025-12-11 schema. page 1 has a stdio
// package entry (isLatest=true), an older version of the same server
// (isLatest=false, must be skipped), and a remote-only entry. page 2 has
// one more remote entry. Cursor returned by page 1 must drive the second
// fetch, and metadata.nextCursor="" on page 2 must end the loop.
func newRegistryFixture(t *testing.T) *httptest.Server {
	t.Helper()
	page1 := registryListResponse{
		Servers: []registryEntryEnvelope{
			{
				Server: registryServerEntry{
					Name:        "example.com/server-stdio",
					Title:       "Server Stdio",
					Description: "Stdio sample",
					Version:     "1.0.0",
					WebsiteURL:  "https://example.com/stdio",
					Repository:  registryRepository{URL: "https://github.com/example/server-stdio", Source: "github"},
					Icons:       []registryIcon{{Src: "https://example.com/icon.png"}},
					Packages: []registryPackage{{
						RegistryType: "npm",
						Identifier:   "@example/server-stdio",
						Version:      "1.0.0",
						RuntimeHint:  "node",
						Transport:    registryTransport{Type: "stdio"},
						EnvironmentVariables: []registryEnvVar{
							{Name: "API_KEY", Description: "key", IsRequired: true, IsSecret: true},
						},
					}},
				},
				Meta: registryEntryMeta{Official: registryOfficialMeta{Status: "active", IsLatest: true}},
			},
			{
				Server: registryServerEntry{
					Name:    "example.com/server-stdio",
					Title:   "Server Stdio",
					Version: "0.9.0",
				},
				Meta: registryEntryMeta{Official: registryOfficialMeta{Status: "active", IsLatest: false}},
			},
			{
				Server: registryServerEntry{
					Name:        "example.com/server-remote",
					Title:       "Server Remote",
					Description: "Remote sample",
					Version:     "2.0.0",
					Remotes: []registryRemote{{
						Type:    "streamable-http",
						URL:     "https://api.example.com/mcp",
						Headers: []registryEnvVar{{Name: "Authorization", IsRequired: true}},
					}},
				},
				Meta: registryEntryMeta{Official: registryOfficialMeta{Status: "active", IsLatest: true}},
			},
		},
	}
	page1.Metadata.NextCursor = "example.com/server-remote:2.0.0"

	page2 := registryListResponse{
		Servers: []registryEntryEnvelope{
			{
				Server: registryServerEntry{
					Name:        "example.com/server-deprecated",
					Title:       "Deprecated",
					Description: "Old",
					Remotes:     []registryRemote{{Type: "sse", URL: "https://old.example.com/mcp"}},
				},
				Meta: registryEntryMeta{Official: registryOfficialMeta{Status: "deprecated", IsLatest: true}},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "" {
			_ = json.NewEncoder(w).Encode(page1)
			return
		}
		_ = json.NewEncoder(w).Encode(page2)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSyncMCPRegistry_NewSchemaAndPagination(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	srv := newRegistryFixture(t)
	prev := mcpRegistryURL
	mcpRegistryURL = srv.URL
	t.Cleanup(func() { mcpRegistryURL = prev })

	if err := SyncMCPRegistry(context.Background(), db, logger); err != nil {
		t.Fatalf("SyncMCPRegistry: %v", err)
	}

	var rowCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM mcp_registry_servers").Scan(&rowCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	// 3 isLatest entries (the duplicate-version one must be skipped).
	if rowCount != 3 {
		t.Fatalf("expected 3 rows, got %d", rowCount)
	}

	// Verify field mapping for the stdio entry.
	var displayName, icon, homepage, sourceURL, transport, pkgName, pkgReg, command, envJSON string
	var verified int
	err := db.QueryRow(`SELECT display_name, icon, homepage_url, source_url, transport,
			package_name, package_registry, command, env_vars_json, is_verified
		FROM mcp_registry_servers WHERE id = ?`, "example.com/server-stdio").Scan(
		&displayName, &icon, &homepage, &sourceURL, &transport,
		&pkgName, &pkgReg, &command, &envJSON, &verified)
	if err != nil {
		t.Fatalf("scan stdio: %v", err)
	}
	if displayName != "Server Stdio" {
		t.Errorf("display_name: got %q", displayName)
	}
	if icon != "https://example.com/icon.png" {
		t.Errorf("icon: got %q", icon)
	}
	if homepage != "https://example.com/stdio" {
		t.Errorf("homepage_url: got %q", homepage)
	}
	if sourceURL != "https://github.com/example/server-stdio" {
		t.Errorf("source_url: got %q", sourceURL)
	}
	if transport != "stdio" {
		t.Errorf("transport: got %q", transport)
	}
	if pkgName != "@example/server-stdio" {
		t.Errorf("package_name: got %q", pkgName)
	}
	if pkgReg != "npm" {
		t.Errorf("package_registry: got %q", pkgReg)
	}
	if command != "node" {
		t.Errorf("command: got %q", command)
	}
	if verified != 1 {
		t.Errorf("is_verified: want 1 for status=active, got %d", verified)
	}
	if !strings.Contains(envJSON, "API_KEY") || !strings.Contains(envJSON, `"isRequired":true`) {
		t.Errorf("env_vars_json missing fields: %s", envJSON)
	}

	// Verify field mapping for the remote entry.
	var rTransport, rEndpoint, rAuth string
	if err := db.QueryRow(`SELECT transport, endpoint, auth_type FROM mcp_registry_servers WHERE id = ?`,
		"example.com/server-remote").Scan(&rTransport, &rEndpoint, &rAuth); err != nil {
		t.Fatalf("scan remote: %v", err)
	}
	if rTransport != "streamable-http" {
		t.Errorf("remote transport: got %q", rTransport)
	}
	if rEndpoint != "https://api.example.com/mcp" {
		t.Errorf("remote endpoint: got %q", rEndpoint)
	}
	if rAuth != "header" {
		t.Errorf("remote auth_type: got %q", rAuth)
	}

	// Pagination reached page 2 (deprecated entry persisted with verified=0).
	var depVerified int
	if err := db.QueryRow(`SELECT is_verified FROM mcp_registry_servers WHERE id = ?`,
		"example.com/server-deprecated").Scan(&depVerified); err != nil {
		t.Fatalf("scan deprecated (page 2 not fetched?): %v", err)
	}
	if depVerified != 0 {
		t.Errorf("deprecated should have is_verified=0, got %d", depVerified)
	}
}

// realUpstreamSample is a verbatim capture of two entries from
// https://registry.modelcontextprotocol.io/v0/servers (April 2026). The
// other fixture round-trips JSON through our own struct tags, so a typo
// like `json:"website_url"` instead of `json:"websiteUrl"` would be
// invisible to it. This test pins the wire format independently — if
// upstream changes a field name, this fails first.
const realUpstreamSample = `{
  "servers": [
    {
      "server": {
        "$schema": "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json",
        "name": "ai.agenttrust/mcp-server",
        "description": "Identity, trust, and A2A orchestration for autonomous AI agents.",
        "title": "AgentTrust",
        "repository": {"url": "https://github.com/agenttrust/mcp-server", "source": "github"},
        "version": "1.1.1",
        "websiteUrl": "https://agenttrust.ai",
        "icons": [{"src": "https://agenttrust.ai/icon.png", "sizes": ["96x96"]}],
        "packages": [{
          "registryType": "npm",
          "identifier": "@agenttrust/mcp-server",
          "version": "1.1.1",
          "transport": {"type": "stdio"},
          "environmentVariables": [
            {"description": "Your AgentTrust API key", "isRequired": true, "isSecret": true, "name": "AGENTTRUST_API_KEY"}
          ]
        }]
      },
      "_meta": {
        "io.modelcontextprotocol.registry/official": {"status": "active", "isLatest": true}
      }
    },
    {
      "server": {
        "name": "ac.tandem/docs-mcp",
        "description": "Remote MCP server for Tandem docs.",
        "repository": {"url": "https://github.com/frumu-ai/tandem", "source": "github"},
        "version": "0.3.0",
        "remotes": [{"type": "streamable-http", "url": "https://tandem.ac/mcp"}]
      },
      "_meta": {
        "io.modelcontextprotocol.registry/official": {"status": "active", "isLatest": true}
      }
    }
  ],
  "metadata": {"nextCursor": "", "count": 2}
}`

func TestSyncMCPRegistry_RealUpstreamSchema(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(realUpstreamSample))
	}))
	t.Cleanup(srv.Close)

	prev := mcpRegistryURL
	mcpRegistryURL = srv.URL
	t.Cleanup(func() { mcpRegistryURL = prev })

	if err := SyncMCPRegistry(context.Background(), db, logger); err != nil {
		t.Fatalf("SyncMCPRegistry: %v", err)
	}

	// Field mapping for the packages-based entry.
	var displayName, icon, homepage, sourceURL, transport, pkgName, pkgReg, command, envJSON string
	err := db.QueryRow(`SELECT display_name, icon, homepage_url, source_url, transport,
			package_name, package_registry, command, env_vars_json
		FROM mcp_registry_servers WHERE id = ?`, "ai.agenttrust/mcp-server").Scan(
		&displayName, &icon, &homepage, &sourceURL, &transport,
		&pkgName, &pkgReg, &command, &envJSON)
	if err != nil {
		t.Fatalf("scan agenttrust: %v", err)
	}
	checks := []struct{ field, got, want string }{
		{"display_name (← title)", displayName, "AgentTrust"},
		{"icon (← icons[0].src)", icon, "https://agenttrust.ai/icon.png"},
		{"homepage_url (← websiteUrl)", homepage, "https://agenttrust.ai"},
		{"source_url (← repository.url)", sourceURL, "https://github.com/agenttrust/mcp-server"},
		{"transport (← packages[0].transport.type)", transport, "stdio"},
		{"package_name (← packages[0].identifier)", pkgName, "@agenttrust/mcp-server"},
		{"package_registry (← packages[0].registryType)", pkgReg, "npm"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.field, c.got, c.want)
		}
	}
	// envVars JSON must contain the upstream camelCase tags — the parser
	// re-emits them via the same struct, so a tag typo on read would also
	// drop the field on write.
	for _, want := range []string{"AGENTTRUST_API_KEY", `"isRequired":true`, `"isSecret":true`} {
		if !strings.Contains(envJSON, want) {
			t.Errorf("env_vars_json missing %q: %s", want, envJSON)
		}
	}

	// Field mapping for the remote-only entry (also asserts that a missing
	// `title` falls back to `name`, that an empty `websiteUrl` lands as ""
	// rather than the zero default of some other field, and that
	// `repository.url` still maps when title/website are absent).
	var rTitle, rHomepage, rSource, rTransport, rEndpoint string
	if err := db.QueryRow(`SELECT display_name, homepage_url, source_url, transport, endpoint
		FROM mcp_registry_servers WHERE id = ?`, "ac.tandem/docs-mcp").Scan(
		&rTitle, &rHomepage, &rSource, &rTransport, &rEndpoint); err != nil {
		t.Fatalf("scan tandem: %v", err)
	}
	if rTitle != "ac.tandem/docs-mcp" {
		t.Errorf("missing title should fall back to name: got %q", rTitle)
	}
	if rHomepage != "" {
		t.Errorf("missing websiteUrl should be empty: got %q", rHomepage)
	}
	if rSource != "https://github.com/frumu-ai/tandem" {
		t.Errorf("repository.url not mapped: got %q", rSource)
	}
	if rTransport != "streamable-http" {
		t.Errorf("remote.type not mapped to transport: got %q", rTransport)
	}
	if rEndpoint != "https://tandem.ac/mcp" {
		t.Errorf("remote.url not mapped to endpoint: got %q", rEndpoint)
	}
}

// TestSyncMCPRegistry_PreservesCuration is the regression guard for the
// most subtle invariant of v67: an admin promoting an entry to
// trust_tier='crewship' or is_featured=1 must NOT lose those flags on
// the next sync cycle. The whole "Verified by Crewship" trust signal
// only works if curation is locally owned (CONNECTIONS.md §5.6 + the
// ON CONFLICT clause in SyncMCPRegistry that explicitly excludes those
// columns).
func TestSyncMCPRegistry_PreservesCuration(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	srv := newRegistryFixture(t)
	prev := mcpRegistryURL
	mcpRegistryURL = srv.URL
	t.Cleanup(func() { mcpRegistryURL = prev })

	// First sync to populate the table.
	if err := SyncMCPRegistry(context.Background(), db, logger); err != nil {
		t.Fatalf("first SyncMCPRegistry: %v", err)
	}

	// Admin curates: promote example.com/server-stdio to crewship-tier
	// and feature it. Both columns are locally owned per v67 design.
	if _, err := db.Exec(`UPDATE mcp_registry_servers
		SET trust_tier = 'crewship', is_featured = 1
		WHERE id = ?`, "example.com/server-stdio"); err != nil {
		t.Fatalf("curate: %v", err)
	}

	// Second sync — upstream still says is_verified=true (active). If the
	// ON CONFLICT clause was wrong, this would clobber trust_tier back to
	// 'community' (the default) and is_featured back to 0.
	if err := SyncMCPRegistry(context.Background(), db, logger); err != nil {
		t.Fatalf("second SyncMCPRegistry: %v", err)
	}

	var trustTier string
	var isFeatured int
	if err := db.QueryRow(`SELECT trust_tier, is_featured FROM mcp_registry_servers WHERE id = ?`,
		"example.com/server-stdio").Scan(&trustTier, &isFeatured); err != nil {
		t.Fatalf("read after re-sync: %v", err)
	}
	if trustTier != "crewship" {
		t.Errorf("trust_tier clobbered by sync: got %q, want 'crewship'", trustTier)
	}
	if isFeatured != 1 {
		t.Errorf("is_featured clobbered by sync: got %d, want 1", isFeatured)
	}
}
