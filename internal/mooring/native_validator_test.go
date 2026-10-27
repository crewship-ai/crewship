package mooring

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func nativeCLIProbe(t *testing.T, app, body string) NativeCLIValidator {
	t.Helper()
	path := filepath.Join(t.TempDir(), "engine")
	raw := []byte("#!/bin/sh\nset -eu\n" + body + "\n")
	if err := os.WriteFile(path, raw, 0700); err != nil {
		t.Fatal(err)
	}
	return NativeCLIValidator{Application: app, BinaryPath: path, SHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), Timeout: time.Second}
}
func TestNativeCLIValidatorUsesPinnedEngineAndPrivateEnvironment(t *testing.T) {
	t.Setenv("CRAFT_TEST_SECRET", "never-inherit")
	for _, app := range []string{"wordcraft", "gridcraft"} {
		t.Run(app, func(t *testing.T) {
			body := `test -z "${CRAFT_TEST_SECRET:-}"
test "$HOME" = "$TMPDIR"
test "$(stat -c %a "$HOME")" = 700
test "$(stat -c %a "$2")" = 600
case "$1" in
 convert) test "$3" != "$2"; cat "$2" > "$3"; printf 'wrote %s\n' "$3" >&2;;
 info) test "$3" = --json; printf '{"valid":true}';;
 *) exit 1;;
esac`
			if app == "gridcraft" {
				body = strings.ReplaceAll(body, `printf 'wrote %s\n' "$3" >&2`, `printf 'wrote %s (%s bytes)\n' "$3" "$(wc -c < "$3")" >&2`)
			}
			v := nativeCLIProbe(t, app, body)
			raw := []byte(`{"native":"original bytes"}`)
			if err := v.Validate(t.Context(), raw); err != nil {
				t.Fatal(err)
			}
			if string(raw) != `{"native":"original bytes"}` {
				t.Fatal("validator mutated candidate")
			}
		})
	}
}
func TestNativeCLIValidatorRejectsWrongPinAndNonNativeInputBeforeExecution(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "called")
	v := nativeCLIProbe(t, "gridcraft", fmt.Sprintf("touch '%s'", marker))
	for _, raw := range [][]byte{nil, []byte("PK\x03\x04xlsx"), []byte("[]"), []byte(`{"broken":`), []byte(strings.Repeat(" ", 32<<20+1))} {
		if err := v.Validate(t.Context(), raw); err == nil {
			t.Fatal("invalid input admitted")
		}
	}
	v.SHA256 = strings.Repeat("0", 64)
	if err := v.Validate(t.Context(), []byte(`{}`)); err == nil {
		t.Fatal("changed engine admitted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("invalid candidate or pin reached engine")
	}
}
func TestNativeCLIValidatorRejectsWarningsFailureAndTimeout(t *testing.T) {
	for _, body := range []string{"echo 'Warning: repaired' >&2; exit 0", "exit 2", "exec sleep 10"} {
		v := nativeCLIProbe(t, "gridcraft", body)
		v.Timeout = 30 * time.Millisecond
		started := time.Now()
		if err := v.Validate(context.Background(), []byte(`{}`)); err == nil {
			t.Fatal("unqualified candidate admitted", body)
		}
		if time.Since(started) > 2*time.Second {
			t.Fatal("engine deadline not bounded")
		}
	}
}

func TestNativeCLIValidatorRejectsSilentLossButAllowsAddedDefaults(t *testing.T) {
	for _, app := range []string{"wordcraft", "gridcraft"} {
		t.Run(app, func(t *testing.T) {
			body := `if [ "$1" = info ]; then printf '{"valid":true}'; exit 0; fi
printf '{"known":1,"defaulted":true}' > "$3"
`
			if app == "wordcraft" {
				body += `printf 'wrote %s\n' "$3" >&2`
			} else {
				body += `printf 'wrote %s (%s bytes)\n' "$3" "$(wc -c < "$3")" >&2`
			}
			v := nativeCLIProbe(t, app, body)
			if err := v.Validate(t.Context(), []byte(`{"known":1,"futureUserContent":"must not disappear"}`)); err == nil {
				t.Fatal("engine silently dropped authored state")
			}
			if err := v.Validate(t.Context(), []byte(`{"known":1.0}`)); err != nil {
				t.Fatal("equal numeric model with added defaults refused", err)
			}
		})
	}
}

func TestNativeCLIValidatorWordSaveMetadataDoesNotHideContentLoss(t *testing.T) {
	before := []byte(`{"body":[{"text":"authored"}],"core":{"title":"preserve","created":"","modified":"old","revision":1,"lastModifiedBy":"author"},"settings":{"zoom":1.2}}`)
	after := []byte(`{"body":[{"text":"authored"}],"core":{"title":"preserve","created":"new","modified":"new","revision":2,"lastModifiedBy":"WordCraft User"},"settings":{"zoom":1.0}}`)
	if !preservesNativeModel(before, after, "wordcraft") {
		t.Fatal("validation-only save metadata incorrectly rejected")
	}
	if preservesNativeModel(before, after, "gridcraft") {
		t.Fatal("Word-only metadata exception leaked into Grid")
	}
	for _, mutated := range [][]byte{
		[]byte(strings.Replace(string(after), `"preserve"`, `"lost"`, 1)),
		[]byte(strings.Replace(string(after), `"authored"`, `"lost"`, 1)),
	} {
		if preservesNativeModel(before, mutated, "wordcraft") {
			t.Fatal("metadata exception hid content loss")
		}
	}
	unknown := []byte(strings.Replace(string(before), `"title":"preserve"`, `"title":"preserve","futureAuthorData":"must survive"`, 1))
	if preservesNativeModel(unknown, after, "wordcraft") {
		t.Fatal("metadata exception hid unknown core field loss")
	}
}
