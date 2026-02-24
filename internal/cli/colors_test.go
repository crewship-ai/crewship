package cli

import (
	"testing"
)

func TestInitColorsDisabled(t *testing.T) {
	// Save originals
	origReset := Reset
	origBold := Bold
	origRed := Red
	defer func() {
		Reset = origReset
		Bold = origBold
		Red = origRed
	}()

	// Re-init with defaults
	Reset = "\033[0m"
	Bold = "\033[1m"
	Red = "\033[31m"

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
	origReset := Reset
	origBold := Bold
	defer func() {
		Reset = origReset
		Bold = origBold
	}()

	Reset = "\033[0m"
	Bold = "\033[1m"

	t.Setenv("NO_COLOR", "1")
	InitColors(false)

	if Reset != "" {
		t.Errorf("Reset should be empty with NO_COLOR=1")
	}
}
