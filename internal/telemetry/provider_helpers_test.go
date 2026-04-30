package telemetry

import (
	"os"
	"testing"
)

// TestIsURL pins the cheap protocol-prefix heuristic.
func TestIsURL(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"http://localhost:4317", true},
		{"https://otel.example.com:443", true},
		{"localhost:4317", false},
		{"unix:///tmp/otel.sock", false},
		{"", false},
		{"http", false},     // shorter than 7 chars
		{"https:", false},   // shorter than 8 chars
		{"http:/x", false},  // single-slash variant rejected
		{"http://a", true},  // 8 chars with proper http://
		{"https:/a", false}, // single-slash https rejected
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := isURL(tt.in); got != tt.want {
				t.Errorf("isURL(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestServiceVersion_FromEnv exercises the env-driven version stamp.
func TestServiceVersion_FromEnv(t *testing.T) {
	t.Setenv("CREWSHIP_VERSION", "1.2.3-rc.4")
	if got := serviceVersion(); got != "1.2.3-rc.4" {
		t.Errorf("serviceVersion = %q, want from env", got)
	}
}

// TestServiceVersion_DefaultDev — empty env returns "dev".
func TestServiceVersion_DefaultDev(t *testing.T) {
	old := os.Getenv("CREWSHIP_VERSION")
	defer os.Setenv("CREWSHIP_VERSION", old)
	if err := os.Unsetenv("CREWSHIP_VERSION"); err != nil {
		t.Fatal(err)
	}
	if got := serviceVersion(); got != "dev" {
		t.Errorf("serviceVersion default = %q, want dev", got)
	}
}

// TestRegisterJournalResolver — installs the resolver. Subsequent
// journal.Emit calls (covered in journal_test.go's TestTraceResolver)
// will route through ResolveTrace; here we just confirm the
// registration call doesn't panic.
func TestRegisterJournalResolver(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("RegisterJournalResolver panicked: %v", r)
		}
	}()
	RegisterJournalResolver()
}
