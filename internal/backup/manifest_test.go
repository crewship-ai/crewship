package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func newValidManifest() *Manifest {
	return &Manifest{
		FormatVersion:           FormatVersion,
		CrewshipVersionAtBackup: "0.5.0",
		SchemaMigrationVersions: []int{46, 47},
		Scope:                   ScopeWorkspace,
		CompatibleTargets:       []Target{TargetAnyInstance},
		CreatedAt:               time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
		CreatedBy: Actor{
			UserID: "cuid_abc123",
			Email:  "admin@example.com",
			Role:   "OWNER",
		},
		SourceInstance: Instance{
			Hostname: "host1",
			Platform: "linux/amd64",
		},
		Contents: Contents{
			Workspace: &WorkspaceSummary{
				ID:   "cuid_ws_1",
				Slug: "my-ws",
				Name: "My Workspace",
			},
		},
		Encryption: Encryption{
			Enabled:   true,
			Algorithm: "age-v1",
		},
		Checksums: Checksums{
			PayloadSHA256: "sha256:abc123",
		},
	}
}

func TestManifestValidate_OK(t *testing.T) {
	m := newValidManifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("valid manifest should pass validation, got %v", err)
	}
}

func TestManifestValidate_Errors(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Manifest)
		wantErr error
	}{
		{"zero format version", func(m *Manifest) { m.FormatVersion = 0 }, ErrInvalidManifest},
		{"negative format version", func(m *Manifest) { m.FormatVersion = -1 }, ErrInvalidManifest},
		{"invalid scope", func(m *Manifest) { m.Scope = "bogus" }, ErrInvalidScope},
		{"empty scope", func(m *Manifest) { m.Scope = "" }, ErrInvalidScope},
		{"no compatible targets", func(m *Manifest) { m.CompatibleTargets = nil }, ErrInvalidManifest},
		{"zero created_at", func(m *Manifest) { m.CreatedAt = time.Time{} }, ErrInvalidManifest},
		{"missing user id", func(m *Manifest) { m.CreatedBy.UserID = "" }, ErrInvalidManifest},
		{"missing checksum", func(m *Manifest) { m.Checksums.PayloadSHA256 = "" }, ErrInvalidManifest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newValidManifest()
			tc.mutate(m)
			err := m.Validate()
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("expected error kind %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestScopeValid(t *testing.T) {
	cases := map[Scope]bool{
		ScopeCrew:      true,
		ScopeWorkspace: true,
		ScopeInstance:  true,
		"":             false,
		"bogus":        false,
	}
	for s, want := range cases {
		if got := s.Valid(); got != want {
			t.Errorf("Scope(%q).Valid() = %v, want %v", s, got, want)
		}
	}
}

func TestManifestRoundTrip(t *testing.T) {
	m := newValidManifest()
	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}
	out, err := ReadManifest(buf.Bytes())
	if err != nil {
		t.Fatalf("ReadManifest failed: %v", err)
	}
	if out.Scope != m.Scope || out.FormatVersion != m.FormatVersion {
		t.Errorf("round-trip mismatch: got scope=%q fv=%d, want scope=%q fv=%d",
			out.Scope, out.FormatVersion, m.Scope, m.FormatVersion)
	}
	if !out.CreatedAt.Equal(m.CreatedAt) {
		t.Errorf("created_at drifted: got %v, want %v", out.CreatedAt, m.CreatedAt)
	}
	if out.Checksums.PayloadSHA256 != m.Checksums.PayloadSHA256 {
		t.Errorf("checksum drifted")
	}
}

func TestReadManifest_UnknownFieldsIgnored(t *testing.T) {
	// Forward-compat: readers must tolerate unknown fields written by
	// future versions of this code.
	raw := map[string]any{
		"format_version":             FormatVersion,
		"crewship_version_at_backup": "0.5.0",
		"schema_migration_versions":  []int{46},
		"scope":                      "workspace",
		"compatible_targets":         []string{"any-instance"},
		"created_at":                 "2026-04-15T12:00:00Z",
		"created_by": map[string]any{
			"user_id": "cuid_x",
			"email":   "a@b",
			"role":    "OWNER",
		},
		"source_instance": map[string]any{
			"hostname": "h1",
			"platform": "linux/amd64",
		},
		"contents": map[string]any{},
		"encryption": map[string]any{
			"enabled":   true,
			"algorithm": "age-v1",
		},
		"checksums": map[string]any{
			"payload_sha256": "sha256:xyz",
		},
		"future_field_added_in_v2": "should be ignored",
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("test setup: %v", err)
	}
	if _, err := ReadManifest(data); err != nil {
		t.Errorf("unknown fields should not cause error, got %v", err)
	}
}

func TestReadManifest_MalformedJSON(t *testing.T) {
	_, err := ReadManifest([]byte("{not json"))
	if !errors.Is(err, ErrInvalidManifest) {
		t.Errorf("expected ErrInvalidManifest, got %v", err)
	}
}

func TestManifestWriteTo_EmitsIndentedJSON(t *testing.T) {
	m := newValidManifest()
	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}
	s := buf.String()
	if !strings.Contains(s, "  \"format_version\"") {
		t.Errorf("expected indented JSON, got: %s", s)
	}
}
