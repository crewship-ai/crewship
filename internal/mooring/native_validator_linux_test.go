//go:build linux

package mooring

import (
	"strings"
	"testing"
)

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
