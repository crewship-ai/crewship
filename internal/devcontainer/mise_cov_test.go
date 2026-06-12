package devcontainer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseMiseConfig_TOMLFull(t *testing.T) {
	t.Parallel()

	toml := `
# leading comment
[tools]
node = "22"   # inline comment
python = "3.12"
weird = "a#b" # value containing a hash inside quotes

[env]
NODE_OPTIONS = "max"
`
	cfg, err := ParseMiseConfig(toml)
	if err != nil {
		t.Fatalf("ParseMiseConfig: %v", err)
	}
	if cfg.Tools["node"] != "22" || cfg.Tools["python"] != "3.12" {
		t.Errorf("tools = %#v", cfg.Tools)
	}
	if cfg.Tools["weird"] != "a#b" {
		t.Errorf("quoted hash mishandled: weird = %q, want a#b", cfg.Tools["weird"])
	}
	if cfg.Env["NODE_OPTIONS"] != "max" {
		t.Errorf("env = %#v", cfg.Env)
	}
}

func TestParseMiseConfig_TOMLErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantSub string
	}{
		{"unsupported section", "[weird]\nx = \"1\"", "unsupported TOML section"},
		{"missing equals", "[tools]\nnode 22", "expected 'key = "},
		{"unquoted value", "[tools]\nnode = 22", "must be a quoted string"},
		{"key outside section", "node = \"22\"", "outside any section"},
		{"value comment leaves nothing", "[tools]\nnode = # gone", "must be a quoted string"},
	}
	for _, tt := range tests {
		_, err := ParseMiseConfig(tt.input)
		if err == nil {
			t.Errorf("%s: expected error", tt.name)
			continue
		}
		if !strings.Contains(err.Error(), tt.wantSub) {
			t.Errorf("%s: error = %v, want substring %q", tt.name, err, tt.wantSub)
		}
	}
}

func TestParseMiseConfig_TOMLEmptyInput(t *testing.T) {
	t.Parallel()

	cfg, err := ParseMiseConfig("")
	if err != nil {
		t.Fatalf("ParseMiseConfig(\"\"): %v", err)
	}
	if !cfg.IsEmpty() {
		t.Errorf("expected empty config, got %#v", cfg)
	}
}

func TestParseMiseConfig_JSONWithoutTools(t *testing.T) {
	t.Parallel()

	cfg, err := ParseMiseConfig(`{"env":{"FOO":"bar"}}`)
	if err != nil {
		t.Fatalf("ParseMiseConfig: %v", err)
	}
	if cfg.Tools == nil {
		t.Error("Tools map must be initialized even when absent from JSON")
	}
	if cfg.Env["FOO"] != "bar" {
		t.Errorf("env = %#v", cfg.Env)
	}
}

func TestMiseFindCommentIndex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want int
	}{
		{`no comment here`, -1},
		{`# leading`, 0},
		{`"a#b" # c`, 6},  // hash inside quotes is not a comment
		{`"a\"b" # c`, 7}, // escaped quote does not close the span
		{`"unterminated # inside`, -1},
		{`plain # tail`, 6},
		{``, -1},
	}
	for _, tt := range tests {
		if got := miseFindCommentIndex(tt.in); got != tt.want {
			t.Errorf("miseFindCommentIndex(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestMiseConfig_Validate_EnvValueErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		wantSub string
	}{
		{"newline", "a\nb", "newline"},
		{"carriage return", "a\rb", "newline"},
		{"reserved delimiter", "xMISE_EOFy", "reserved delimiter"},
		{"double quote", `a"b`, "unescaped quotes"},
	}
	for _, tt := range tests {
		cfg := &MiseConfig{Env: map[string]string{"GOOD_KEY": tt.value}}
		err := cfg.Validate()
		if err == nil {
			t.Errorf("%s: expected error", tt.name)
			continue
		}
		if !strings.Contains(err.Error(), tt.wantSub) {
			t.Errorf("%s: error = %v, want substring %q", tt.name, err, tt.wantSub)
		}
	}
}

// covStepExec returns an ExecFunc that succeeds for every call except the
// failAt-th (0-based), which either returns an error or a non-zero exit code.
func covStepExec(failAt int, exitCode int, withErr bool, calls *[][]string) ExecFunc {
	n := 0
	return func(_ context.Context, _ string, cmd []string, _ string, _ []string) (string, int, error) {
		idx := n
		n++
		if calls != nil {
			*calls = append(*calls, cmd)
		}
		if idx == failAt {
			if withErr {
				return "", -1, errors.New("transport down")
			}
			return "step-output", exitCode, nil
		}
		return "", 0, nil
	}
}

func TestInstallMise_StepFailures(t *testing.T) {
	t.Parallel()

	// InstallMise performs 3 exec calls: download(0), symlink(1), verify(2).
	tests := []struct {
		name    string
		failAt  int
		withErr bool
		wantSub string
	}{
		{"download error", 0, true, "download"},
		{"download exit", 0, false, "download exited"},
		{"symlink error", 1, true, "symlink"},
		{"symlink exit", 1, false, "symlink exited"},
		{"verify error", 2, true, "verify"},
		{"verify exit", 2, false, "verify exited"},
	}
	for _, tt := range tests {
		err := InstallMise(context.Background(), "cid", covStepExec(tt.failAt, 9, tt.withErr, nil))
		if err == nil {
			t.Errorf("%s: expected error", tt.name)
			continue
		}
		if !errors.Is(err, ErrMiseInstallFailed) {
			t.Errorf("%s: error %v is not ErrMiseInstallFailed", tt.name, err)
		}
		if !strings.Contains(err.Error(), tt.wantSub) {
			t.Errorf("%s: error = %v, want substring %q", tt.name, err, tt.wantSub)
		}
	}
}

func TestInstallMiseTools_StepFailures(t *testing.T) {
	t.Parallel()

	// InstallMiseTools performs 4 exec calls:
	// write config(0), chown(1), mise install(2), mise reshim(3).
	cfg := &MiseConfig{Tools: map[string]string{"node": "22"}}
	tests := []struct {
		name    string
		failAt  int
		withErr bool
		wantSub string
	}{
		{"write config error", 0, true, "write config"},
		{"write config exit", 0, false, "write config exited"},
		{"chown error", 1, true, "chown"},
		{"chown exit", 1, false, "chown exited"},
		{"install error", 2, true, "install tools"},
		{"install exit", 2, false, "install tools exited"},
		{"reshim error", 3, true, "reshim"},
		{"reshim exit", 3, false, "reshim exited"},
	}
	for _, tt := range tests {
		err := InstallMiseTools(context.Background(), "cid", cfg, covStepExec(tt.failAt, 3, tt.withErr, nil))
		if err == nil {
			t.Errorf("%s: expected error", tt.name)
			continue
		}
		if !strings.Contains(err.Error(), tt.wantSub) {
			t.Errorf("%s: error = %v, want substring %q", tt.name, err, tt.wantSub)
		}
	}
}

func TestInstallMiseTools_WritesTOMLConfig(t *testing.T) {
	t.Parallel()

	cfg := &MiseConfig{
		Tools: map[string]string{"node": "22"},
		Env:   map[string]string{"FOO": "bar"},
	}
	var calls [][]string
	if err := InstallMiseTools(context.Background(), "cid", cfg, covStepExec(-1, 0, false, &calls)); err != nil {
		t.Fatalf("InstallMiseTools: %v", err)
	}
	if len(calls) != 4 {
		t.Fatalf("expected 4 exec calls, got %d", len(calls))
	}
	// The first call writes the TOML config via heredoc.
	writeCmd := strings.Join(calls[0], " ")
	for _, want := range []string{`node = "22"`, "[env]", `FOO = "bar"`, "MISE_EOF"} {
		if !strings.Contains(writeCmd, want) {
			t.Errorf("config write command missing %q:\n%s", want, writeCmd)
		}
	}
	if calls[2][0] != "mise" || calls[2][1] != "install" {
		t.Errorf("third call = %v, want mise install", calls[2])
	}
	if calls[3][0] != "mise" || calls[3][1] != "reshim" {
		t.Errorf("fourth call = %v, want mise reshim", calls[3])
	}
}
