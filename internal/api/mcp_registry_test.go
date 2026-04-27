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
