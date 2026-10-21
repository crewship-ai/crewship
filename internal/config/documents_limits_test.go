package config

import (
	"runtime"
	"testing"
)

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
