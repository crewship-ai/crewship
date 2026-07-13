package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-isatty"
)

type colorCap int

const (
	colorNone colorCap = iota
	colorBasic
	colorTruecolor
)

// isTTYWriter reports whether w is a real terminal (an *os.File whose fd is a
// tty). The brand line never prints to pipes, files, or CI so machine-readable
// output stays clean.
func isTTYWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// colorLevel classifies a terminal's color support: none (not a tty, NO_COLOR,
// or TERM=dumb), truecolor (COLORTERM=truecolor/24bit → 24-bit brand blue), or
// basic 16-color otherwise.
func colorLevel(w io.Writer) colorCap {
	if !isTTYWriter(w) || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return colorNone
	}
	if ct := os.Getenv("COLORTERM"); ct == "truecolor" || ct == "24bit" {
		return colorTruecolor
	}
	return colorBasic
}

// startLine renders the compact brand line: a sail glyph, the name in brand
// blue, and the version. It's a pure function of (version, color level) so the
// exact bytes are unit-testable. Every format verb is supplied exactly one
// argument — no stray "%!(EXTRA …)" can leak into the output.
func startLine(version string, lvl colorCap) string {
	if version == "" {
		version = "dev"
	}
	switch lvl {
	case colorTruecolor:
		return fmt.Sprintf("\n  ⛵ \x1b[1;38;2;43;144;255mCrewship\x1b[0m \x1b[38;2;123;140;164m%s\x1b[0m\n\n", version)
	case colorBasic:
		return fmt.Sprintf("\n  ⛵ \x1b[1;34mCrewship\x1b[0m \x1b[2m%s\x1b[0m\n\n", version)
	default:
		return fmt.Sprintf("\n  ⛵ Crewship %s\n\n", version)
	}
}

// PrintStartLine writes the compact brand line to w when w is an interactive
// terminal, adapting to its color support (truecolor → 24-bit blue, basic →
// ANSI blue, NO_COLOR/dumb → plain). It writes nothing when w is not a terminal
// (pipe, file, CI), keeping structured output clean. Used for `crewship start`,
// the bare `crewship` invocation, and the first-run welcome.
func PrintStartLine(w io.Writer, version string) {
	if !isTTYWriter(w) {
		return
	}
	fmt.Fprint(w, startLine(version, colorLevel(w)))
}
