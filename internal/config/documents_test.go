package config

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDocumentsDisabledDoesNotRequireOrReadServicePaths(t *testing.T) {
	c := Default()
	if c.Documents.Enabled {
		t.Fatal("Docs must be disabled by default")
	}
	// These invalid/nonexistent paths would fail validation or file reads if the
	// dormant integration tried to initialize its private transport or secret.
	c.Documents = DocumentsConfig{AuthoritySocket: "relative/authority", ServiceSocket: "relative/authority", ServiceTokenFile: filepath.Join(t.TempDir(), "missing-secret")}
	if err := c.Validate(); err != nil {
		t.Fatalf("disabled Docs affected startup: %v", err)
	}
}
func TestDocumentsEnabledPathValidation(t *testing.T) {
	good := DocumentsConfig{Enabled: true, AuthoritySocket: "/tmp/docs-authority.sock", ServiceSocket: "/tmp/docs-service.sock", ServiceTokenFile: "/tmp/docs-service.token"}
	if runtime.GOOS == "windows" {
		if err := good.validate(); err == nil || !strings.Contains(err.Error(), "Unix sockets") {
			t.Fatalf("Windows accepted Unix-only service: %v", err)
		}
		return
	}
	if err := good.validate(); err != nil {
		t.Fatalf("valid paths: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*DocumentsConfig)
	}{
		{"missing", func(c *DocumentsConfig) { c.AuthoritySocket = "" }},
		{"relative", func(c *DocumentsConfig) { c.ServiceSocket = "relative.sock" }},
		{"unclean", func(c *DocumentsConfig) { c.ServiceTokenFile = "/tmp/../tmp/token" }},
		{"too long", func(c *DocumentsConfig) { c.AuthoritySocket = "/" + strings.Repeat("x", 100) }},
		{"same sockets", func(c *DocumentsConfig) { c.ServiceSocket = c.AuthoritySocket }},
		{"secret equals authority", func(c *DocumentsConfig) { c.ServiceTokenFile = c.AuthoritySocket }},
		{"secret equals service", func(c *DocumentsConfig) { c.ServiceTokenFile = c.ServiceSocket }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := good
			tc.change(&c)
			if err := c.validate(); err == nil {
				t.Fatal("invalid enabled service configuration accepted")
			}
		})
	}
}

func TestDocumentOperationalLimitsValidate(t *testing.T) {
	c := DocumentsConfig{Enabled: true, AuthoritySocket: "/tmp/a", ServiceSocket: "/tmp/s", ServiceTokenFile: "/tmp/t"}
	if runtime.GOOS == "windows" {
		return
	}
	for _, bad := range []DocumentsConfig{
		{MaxDocumentsPerWorkspace: -1}, {MaxOperationsPerWindow: -1}, {OperationWindowSeconds: -1}, {OperationWindowSeconds: 86401},
	} {
		changed := c
		changed.MaxDocumentsPerWorkspace = bad.MaxDocumentsPerWorkspace
		changed.MaxOperationsPerWindow = bad.MaxOperationsPerWindow
		changed.OperationWindowSeconds = bad.OperationWindowSeconds
		if err := changed.validate(); err == nil {
			t.Fatalf("invalid limits accepted: %+v", bad)
		}
	}
	c.MaxDocumentsPerWorkspace = 256
	c.MaxOperationsPerWindow = 2000
	c.OperationWindowSeconds = 600
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDocumentOperationalLimitsEnvironment(t *testing.T) {
	t.Setenv("CREWSHIP_DOCUMENTS_MAX_DOCUMENTS_PER_WORKSPACE", "256")
	t.Setenv("CREWSHIP_DOCUMENTS_MAX_OPERATIONS_PER_WINDOW", "4000")
	t.Setenv("CREWSHIP_DOCUMENTS_OPERATION_WINDOW_SECONDS", "900")
	var c Config
	applyEnvOverrides(&c)
	if c.Documents.MaxDocumentsPerWorkspace != 256 || c.Documents.MaxOperationsPerWindow != 4000 || c.Documents.OperationWindowSeconds != 900 {
		t.Fatalf("env limits lost: %+v", c.Documents)
	}
	t.Setenv("CREWSHIP_DOCUMENTS_MAX_OPERATIONS_PER_WINDOW", "invalid")
	applyEnvOverrides(&c)
	if c.Documents.MaxOperationsPerWindow != -1 {
		t.Fatal("invalid limit silently defaulted")
	}
}
