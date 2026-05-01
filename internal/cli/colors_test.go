package cli

import (
	"testing"
)

// saveColors snapshots all package-level color variables and returns a
// restorer. Tests that mutate any color via InitColors must use this so that
// later tests in the same process don't see leaked empty-string values.
func saveColors() func() {
	saved := struct {
		reset, bold, dim, red, green, yellow, blue, magenta, cyan, white, gray string
	}{Reset, Bold, Dim, Red, Green, Yellow, Blue, Magenta, Cyan, White, Gray}
	return func() {
		Reset, Bold, Dim = saved.reset, saved.bold, saved.dim
		Red, Green, Yellow = saved.red, saved.green, saved.yellow
		Blue, Magenta, Cyan = saved.blue, saved.magenta, saved.cyan
		White, Gray = saved.white, saved.gray
	}
}

// resetDefaults sets all color vars to their default ANSI escape sequences.
// Used by tests that want a known starting state.
func resetDefaults() {
	Reset = "\033[0m"
	Bold = "\033[1m"
	Dim = "\033[2m"
	Red = "\033[31m"
	Green = "\033[32m"
	Yellow = "\033[33m"
	Blue = "\033[34m"
	Magenta = "\033[35m"
	Cyan = "\033[36m"
	White = "\033[37m"
	Gray = "\033[90m"
}

func TestInitColorsDisabled(t *testing.T) {
	defer saveColors()()
	resetDefaults()

	InitColors(true)

	if Reset != "" {
		t.Errorf("Reset = %q, want empty", Reset)
	}
	if Bold != "" {
		t.Errorf("Bold = %q, want empty", Bold)
	}
	if Red != "" {
		t.Errorf("Red = %q, want empty", Red)
	}
}

func TestInitColorsNoColorEnv(t *testing.T) {
	defer saveColors()()
	resetDefaults()

	t.Setenv("NO_COLOR", "1")
	InitColors(false)

	if Reset != "" {
		t.Errorf("Reset should be empty with NO_COLOR=1")
	}
}
