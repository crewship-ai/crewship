//go:build linux

package mooring

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These shell executables are controlled process fixtures, not DeckCraft.
// Original-engine qualification is a separate explicit test against a pinned CLI.
func TestDeckValidatorFixturePinnedFDAndEnvironment(t *testing.T) {
	t.Setenv("CRAFT_TEST_SECRET", "never inherit")
	dirRecord := filepath.Join(t.TempDir(), "dir")
	v := nativeCLIProbe(t, "deckcraft", fmt.Sprintf(`test "$0" = /proc/self/fd/3
 test "$#" = 3
 test "$1" = convert
 test -z "${CRAFT_TEST_SECRET:-}"
 test "$HOME" = "$TMPDIR"
 test "$PWD" = "$HOME"
 test "$(stat -c %%a "$HOME")" = 700
 test "$(stat -c %%a "$2")" = 600
 case "$2:$3" in *.deckcraft:*.deckcraft) ;; *) exit 2;; esac
 printf '%%s' "$HOME" > %q
 cat "$2" > "$3"
 printf '%%s\n' "$3"`, dirRecord))
	raw := deckTestZIP(t, deckTestParts()...)
	original := bytes.Clone(raw)
	if err := v.validateDeck(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, original) {
		t.Fatal("original archive mutated")
	}
	privateDir, err := os.ReadFile(dirRecord)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(string(privateDir)); !os.IsNotExist(err) {
		t.Fatal("private conversion files retained")
	}
}
func TestDeckValidatorFixtureRejectsBeforeExecution(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executed")
	valid := deckTestZIP(t, deckTestParts()...)
	for _, tc := range []struct {
		name   string
		raw    []byte
		change func(*NativeCLIValidator)
	}{
		{"malformed archive", []byte("not a ZIP"), nil},
		{"unsafe archive", deckTestZIP(t, append(deckTestParts(), deckTestPart{name: "../escaped", body: "x"})...), nil},
		{"wrong hash", valid, func(v *NativeCLIValidator) { v.SHA256 = strings.Repeat("0", 64) }},
		{"invalid hash", valid, func(v *NativeCLIValidator) { v.SHA256 = strings.Repeat("z", 64) }},
		{"relative path", valid, func(v *NativeCLIValidator) { v.BinaryPath = "relative" }},
		{"wrong app", valid, func(v *NativeCLIValidator) { v.Application = "wordcraft" }},
		{"invalid timeout", valid, func(v *NativeCLIValidator) { v.Timeout = 2 * time.Minute }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := nativeCLIProbe(t, "deckcraft", fmt.Sprintf("touch %q", marker))
			if tc.change != nil {
				tc.change(&v)
			}
			if err := v.validateDeck(t.Context(), tc.raw); !errors.Is(err, ErrNativeValidation) {
				t.Fatalf("candidate admitted: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("invalid candidate reached CLI")
			}
		})
	}
}
func TestDeckValidatorFixtureRejectsUnqualifiedExport(t *testing.T) {
	raw := deckTestZIP(t, deckTestParts()...)
	changed := deckTestParts()
	changed[2].body = "lost asset"
	damaged := filepath.Join(t.TempDir(), "loss.deckcraft")
	if err := os.WriteFile(damaged, deckTestZIP(t, changed...), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, body string }{
		{"warning", `cat "$2" > "$3"; printf '%s\n' "$3"; echo 'warning: repaired' >&2`},
		{"wrong stdout", `cat "$2" > "$3"; printf 'wrote %s\n' "$3"`},
		{"stdout overflow", `cat "$2" > "$3"; head -c 1048577 /dev/zero`},
		{"stderr overflow", `cat "$2" > "$3"; head -c 65537 /dev/zero >&2`},
		{"exit failure", `exit 2`},
		{"missing export", `printf '%s\n' "$3"`},
		{"symlink export", `ln -s "$2" "$3"; printf '%s\n' "$3"`},
		{"malformed export", `printf 'not zip' > "$3"; printf '%s\n' "$3"`},
		{"expanded export", `head -c 33554433 /dev/zero > "$3"; printf '%s\n' "$3"`},
		{"asset loss", fmt.Sprintf(`cat %q > "$3"; printf '%%s\n' "$3"`, damaged)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := nativeCLIProbe(t, "deckcraft", tc.body)
			if err := v.validateDeck(t.Context(), raw); !errors.Is(err, ErrNativeValidation) {
				t.Fatalf("unqualified export admitted: %v", err)
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		v := nativeCLIProbe(t, "deckcraft", `exec sleep 10`)
		v.Timeout = 20 * time.Millisecond
		start := time.Now()
		if err := v.validateDeck(context.Background(), raw); !errors.Is(err, ErrNativeValidation) {
			t.Fatal("timeout accepted")
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("deadline not bounded")
		}
	})
}
