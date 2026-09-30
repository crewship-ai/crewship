package backup

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestConvertedBundlePath(t *testing.T) {
	v := fmt.Sprintf(".v%d", FormatVersion)
	tests := []struct{ in, want string }{
		{"/b/crewship-workspace-acme.tar.zst", "/b/crewship-workspace-acme" + v + ".tar.zst"},
		{"old.crewship", "old" + v + ".crewship"},
		{"bundle", "bundle" + v + ".tar.zst"},
	}
	for _, tt := range tests {
		if got := ConvertedBundlePath(tt.in); got != tt.want {
			t.Errorf("ConvertedBundlePath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestConvertCommand_QuotesPathsAShellWouldSplit(t *testing.T) {
	got := ConvertCommand("/b/my bundle's.tar.zst")
	want := `crewship backup convert --bundle '/b/my bundle'"'"'s.tar.zst' --out '/b/my bundle'"'"'s.v` +
		fmt.Sprint(FormatVersion) + `.tar.zst'`
	if got != want {
		t.Errorf("ConvertCommand = %s\nwant           %s", got, want)
	}
	if plain := ConvertCommand("/b/x.tar.zst"); strings.Contains(plain, "'") {
		t.Errorf("a plain path must not be quoted: %s", plain)
	}
}

// TestWithConvertHint pins the restore/verify/inspect contract for a
// bundle older than the direct-read window: the error still matches
// ErrFormatTooOld (HTTP 400, metrics label), and its text is the exact
// command that makes the bundle readable — not a dead end.
func TestWithConvertHint(t *testing.T) {
	old := &Manifest{FormatVersion: OldestRecoverableFormatVersion}
	err := WithConvertHint("/b/x.tar.zst", old, ErrFormatTooOld)
	var cre *ConvertRequiredError
	if !errors.As(err, &cre) {
		t.Fatalf("want *ConvertRequiredError, got %T %v", err, err)
	}
	if !errors.Is(err, ErrFormatTooOld) {
		t.Error("ConvertRequiredError must unwrap to ErrFormatTooOld")
	}
	if !strings.Contains(err.Error(), ConvertCommand("/b/x.tar.zst")) {
		t.Errorf("error must carry the exact convert command, got %q", err)
	}

	for name, in := range map[string]error{
		"nil":       nil,
		"too new":   ErrFormatTooNew,
		"unrelated": ErrInvalidChecksum,
	} {
		if got := WithConvertHint("/b/x.tar.zst", old, in); got != in {
			t.Errorf("%s: WithConvertHint changed %v into %v", name, in, got)
		}
	}
	if got := WithConvertHint("/b/x.tar.zst", nil, ErrFormatTooOld); got != ErrFormatTooOld {
		t.Errorf("no manifest: want bare ErrFormatTooOld, got %v", got)
	}
	// Idempotent: wrapping twice does not nest the command.
	if again := WithConvertHint("/other", old, err); again != err {
		t.Errorf("re-wrapping replaced the original hint: %v", again)
	}
}

func TestErrFormatTooOld_PointsAtRealCommand(t *testing.T) {
	if strings.Contains(ErrFormatTooOld.Error(), "migrate") {
		t.Errorf("ErrFormatTooOld names a tool that does not exist: %q", ErrFormatTooOld)
	}
	if !strings.Contains(ErrFormatTooOld.Error(), "crewship backup convert") {
		t.Errorf("ErrFormatTooOld must point at crewship backup convert: %q", ErrFormatTooOld)
	}
}

func TestIsCompatible(t *testing.T) {
	tests := []struct {
		name    string
		written int
		want    bool
	}{
		{"current version", FormatVersion, true},
		{"min supported", MinSupportedFormatVersion, true},
		{"too new", FormatVersion + 1, false},
		{"too old", MinSupportedFormatVersion - 1, false},
		{"zero", 0, false},
		{"negative", -1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCompatible(tt.written); got != tt.want {
				t.Errorf("IsCompatible(%d) = %v, want %v", tt.written, got, tt.want)
			}
		})
	}
}

func TestCompatibilityReason(t *testing.T) {
	if err := CompatibilityReason(FormatVersion); err != nil {
		t.Errorf("current version should be compatible, got %v", err)
	}
	if err := CompatibilityReason(FormatVersion + 1); !errors.Is(err, ErrFormatTooNew) {
		t.Errorf("future version should return ErrFormatTooNew, got %v", err)
	}
	if err := CompatibilityReason(MinSupportedFormatVersion - 1); !errors.Is(err, ErrFormatTooOld) {
		t.Errorf("ancient version should return ErrFormatTooOld, got %v", err)
	}
}

func TestFormatVersionInvariants(t *testing.T) {
	if FormatVersion < 1 {
		t.Errorf("FormatVersion must be >= 1, got %d", FormatVersion)
	}
	if MinSupportedFormatVersion < 1 {
		t.Errorf("MinSupportedFormatVersion must be >= 1, got %d", MinSupportedFormatVersion)
	}
	if MinSupportedFormatVersion > FormatVersion {
		t.Errorf("MinSupportedFormatVersion (%d) must not exceed FormatVersion (%d)",
			MinSupportedFormatVersion, FormatVersion)
	}
	// N-2 policy: never support more than 3 versions back.
	if FormatVersion-MinSupportedFormatVersion > 2 {
		t.Errorf("reader must not support more than 3 versions back; got %d..%d",
			MinSupportedFormatVersion, FormatVersion)
	}
}
