//go:build linux

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

	"github.com/crewship-ai/crewship/internal/craftengine"
)

func nativeCLIProbe(t *testing.T, app, body string) NativeCLIValidator {
	t.Helper()
	path := filepath.Join(t.TempDir(), "engine")
	raw := []byte("#!/bin/sh\nset -eu\n" + body + "\n")
	if err := os.WriteFile(path, raw, 0700); err != nil {
		t.Fatal(err)
	}
	engine, err := craftengine.NewNative(app, path, fmt.Sprintf("%x", sha256.Sum256(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return NativeCLIValidator{Application: app, Engine: engine, Timeout: time.Second}
}
func TestNativeCLIValidatorRejectsWrongPinAndNonNativeInputBeforeExecution(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "called")
	v := nativeCLIProbe(t, "gridcraft", fmt.Sprintf("touch '%s'", marker))
	for _, raw := range [][]byte{nil, []byte("PK\x03\x04xlsx"), []byte("[]"), []byte(`{"broken":`), []byte(strings.Repeat(" ", 32<<20+1))} {
		if err := v.Validate(t.Context(), raw); err == nil {
			t.Fatal("invalid input admitted")
		}
	}
	// The engine hashes the opened file on every run: a replaced engine is refused.
	if err := os.WriteFile(enginePath(t, v), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
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

// enginePath returns the file behind a probe's native engine.
func enginePath(t *testing.T, v NativeCLIValidator) string {
	t.Helper()
	n, ok := v.Engine.(*craftengine.Native)
	if !ok {
		t.Fatal("probe is not a native engine")
	}
	return n.Path()
}

// prettyProbe is an engine whose output is the candidate behind pad bytes of indentation, like
// the engine's indented export of a large document.
func prettyProbe(t *testing.T, pad int64, extra string) NativeCLIValidator {
	v := nativeCLIProbe(t, "gridcraft", fmt.Sprintf(`%s
{ head -c %d /dev/zero | tr '\0' ' '; cat "$2"; } > "$3"
echo "wrote $3 ($(stat -c %%s "$3") bytes)" >&2`, extra, pad))
	v.Timeout = time.Minute
	return v
}

// A document under 32 MiB whose indented export is larger than 64 MiB is still validated: the
// export is compacted while it is read, under its own limits for the file and the compact model.
func TestNativeCLIValidatorAcceptsAnIndentedExportOver64MiB(t *testing.T) {
	if err := prettyProbe(t, 70<<20, "").Validate(t.Context(), []byte(`{"sheets":[{"name":"A"}]}`)); err != nil {
		t.Fatalf("a 70 MiB indented export: %v", err)
	}
}

func TestNativeCLIValidatorRefusesAnExportOverItsFileLimit(t *testing.T) {
	if err := prettyProbe(t, MaxValidationOutputBytes+1, "").Validate(t.Context(), []byte(`{}`)); err == nil {
		t.Fatal("an export over the file limit was admitted")
	}
}

func TestNativeCLIValidatorRefusesACompactExportOverItsLimit(t *testing.T) {
	v := nativeCLIProbe(t, "gridcraft", `{ printf '{"a":"'; head -c `+fmt.Sprint(MaxValidatedCompactBytes)+` /dev/zero | tr '\0' x; printf '"}'; } > "$3"; echo "wrote $3 ($(stat -c %s "$3") bytes)" >&2`)
	v.Timeout = time.Minute
	if err := v.Validate(t.Context(), []byte(`{}`)); err == nil {
		t.Fatal("a compact export over its limit was admitted")
	}
}

// At most MaxConcurrentValidations engines validate at once; the others wait.
func TestNativeCLIValidatorBoundsConcurrentValidations(t *testing.T) {
	shared := t.TempDir()
	v := prettyProbe(t, 0, fmt.Sprintf(`touch '%[1]s/run.'$$; ls '%[1]s' | grep -c '^run\.' >> '%[1]s/counts' || true; sleep 0.3; rm -f '%[1]s/run.'$$`, shared))
	done := make(chan error, 5)
	for i := 0; i < 5; i++ {
		go func() { done <- v.Validate(context.Background(), []byte(`{}`)) }()
	}
	for i := 0; i < 5; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(shared, "counts"))
	for _, n := range strings.Fields(string(raw)) {
		if n > fmt.Sprint(MaxConcurrentValidations) {
			t.Fatalf("%s engines validated at once: %q", n, raw)
		}
	}
}
