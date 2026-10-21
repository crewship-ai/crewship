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
