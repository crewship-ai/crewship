//go:build native_tools_live

package terminal

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/gorilla/websocket"
)

// Explicit dev acceptance against a deployed build, using the same auth/init
// and binary frames as use-terminal.ts. The CLI token only authenticates the
// connection; it is never printed or supplied to the container.
// CREWSHIP_NATIVE_TOOLS_PROFILE and CREWSHIP_NATIVE_TOOLS_CREW_ID are required.
// Optional CREWSHIP_NATIVE_TOOLS_CLAUDE_PATH checks terminal/routine lookup parity.
// CREWSHIP_NATIVE_TOOLS_BUILD_COMMIT can bind the receipt to a deployed build.
// CREWSHIP_NATIVE_TOOLS_INHERITED_PROBES=1 checks feature/sbin tools on a
// dedicated fixture whose oversized metadata must leave PATH inherited.
// CREWSHIP_NATIVE_TOOLS_GLAB_PATH asserts the glab path selected by the
// terminal, a child sh and non-login zsh (declared pin precedence, #5459).
// CREWSHIP_NATIVE_TOOLS_GLAB_VERSION asserts every non-login selection
// (terminal, child and zsh) against the declared version after an update;
// bash login output is recorded under LOGIN_OUT only.
func TestNativeToolsLiveTerminal(t *testing.T) {
	profile, crewID := os.Getenv("CREWSHIP_NATIVE_TOOLS_PROFILE"), os.Getenv("CREWSHIP_NATIVE_TOOLS_CREW_ID")
	if profile == "" || crewID == "" {
		t.Fatal("explicit dev profile and test crew required")
	}
	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatal("load CLI configuration failed")
	}
	p := cfg.Servers[profile]
	if p == nil || p.Token == "" {
		t.Fatal("authenticated dev profile required")
	}
	u, err := url.Parse(p.Server)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("live gate accepts only an explicit local dev server")
	}
	client := cli.NewClient(p.Server, p.Token, p.Workspace)
	observeBuild := func() string {
		r, err := client.Get("/api/v1/system/version")
		if err != nil {
			t.Fatal("request deployed build identity failed")
		}
		defer r.Body.Close()
		var build struct {
			Commit    string `json:"commit"`
			BuildTime string `json:"build_time"`
		}
		if r.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(r.Body, 16384)).Decode(&build) != nil || build.Commit == "" {
			t.Fatal("deployed build identity required")
		}
		t.Logf("SERVER_BUILD:%s BUILD_TIME:%s", build.Commit, build.BuildTime)
		if want := os.Getenv("CREWSHIP_NATIVE_TOOLS_BUILD_COMMIT"); want != "" && want != build.Commit {
			t.Fatalf("server build %s != requested %s", build.Commit, want)
		}
		return build.Commit
	}
	initialBuild := observeBuild()
	response, err := client.Get("/api/v1/ws-token")
	if err != nil {
		t.Fatal("request terminal ticket failed")
	}
	defer response.Body.Close()
	var ticket struct {
		Token string `json:"token"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 16384)).Decode(&ticket) != nil || ticket.Token == "" {
		t.Fatal("valid terminal ticket required")
	}
	u.Scheme = "ws"
	u.Path = "/ws/terminal"
	c, _, err := websocket.DefaultDialer.Dial(u.String(), http.Header{"X-Crewship-Client": []string{"native-tools-regression"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.WriteJSON(map[string]string{"type": "auth", "token": ticket.Token}); err != nil {
		t.Fatal("terminal auth write failed")
	}
	if err := c.WriteJSON(map[string]any{"crew_id": crewID, "crew_slug": "live-probe", "mode": "shell", "rows": 30, "cols": 120}); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(10 * time.Minute))
	// The protocol does not promise a startup prompt frame. Queue input after
	// auth/init just as a user can type before the first shell output arrives.
	script := `fail=0
id -u
printf 'SHELL_EXE='; readlink /proc/$$/exe
if shopt -q login_shell; then echo MODE=login; else echo MODE=non-login; fi
test -z "${NATIVE_RUN_ONLY_5452+x}"; code=$?; printf 'TERMINAL_RUN_ENV_STATUS:%s\n' "$code"; test "$code" -eq 0 || fail=1
printf 'HOME=%s\nPATH=%s\nMISE_DATA_DIR=%s\nMISE_GLOBAL_CONFIG_FILE=%s\n' "$HOME" "$PATH" "$MISE_DATA_DIR" "$MISE_GLOBAL_CONFIG_FILE"
glab --version
code=$?; printf 'TOOL_STATUS:glab:%s\n' "$code"; test "$code" -eq 0 || fail=1
codex --version
code=$?; printf 'TOOL_STATUS:codex:%s\n' "$code"; test "$code" -eq 0 || fail=1
claude_path=$(command -v claude); code=$?; printf 'TOOL_PATH:claude:%s\n' "$claude_path"; test "$code" -eq 0 || fail=1
claude --version
code=$?; printf 'TOOL_STATUS:claude:%s\n' "$code"; test "$code" -eq 0 || fail=1
printf 'MODE_PATH:terminal:glab:%s\n' "$(command -v glab)"
printf 'MODE_PATH:child:glab:%s\n' "$(sh -c 'command -v glab')"
printf 'MODE_PATH:bash-login:glab:%s\n' "$(bash -lc 'command -v glab' 2>/dev/null)"
if command -v zsh >/dev/null; then printf 'MODE_PATH:zsh:glab:%s\n' "$(zsh -c 'command -v glab' 2>/dev/null)"; printf 'MODE_PATH:zsh-login:glab:%s\n' "$(zsh -l -c 'command -v glab' 2>/dev/null)"; else printf 'MODE_PATH:zsh:absent\n'; fi
sh -c 'glab --version && codex --version && claude --version'
code=$?; printf 'CHILD_STATUS:%s\n' "$code"; test "$code" -eq 0 || fail=1
login_out=$(bash -lc 'glab --version && codex --version && claude --version' 2>&1)
code=$?; printf '%s\n' "$login_out" | sed 's/^/LOGIN_OUT:/'; printf 'LOGIN_STATUS:%s\n' "$code"; test "$code" -eq 0 || fail=1
if command -v zsh >/dev/null; then zsh -c 'glab --version && codex --version && claude --version'; code=$?; printf 'ZSH_STATUS:%s\n' "$code"; test "$code" -eq 0 || fail=1; fi
fp() { "$@" 'env | grep -vE "^(_|SHLVL|PWD|OLDPWD|PATH|LINES|COLUMNS|BASH_EXECUTION_STRING)=" | sort; declare -F | sort; printf "COMPLETIONS=%s\n" "$(complete -p 2>/dev/null | wc -l)"; alias' </dev/null 2>/dev/null; }
fp_dir=$(mktemp -d); fp bash -l -i -c > "$fp_dir/login"; fp bash -i -c > "$fp_dir/nonlogin"
cmp -s "$fp_dir/login" "$fp_dir/nonlogin"; code=$?; printf 'FEATURE_INIT_PARITY:%s:login_lines=%s:nonlogin_lines=%s\n' "$code" "$(wc -l < "$fp_dir/login")" "$(wc -l < "$fp_dir/nonlogin")"
if [ "$code" -ne 0 ]; then diff "$fp_dir/login" "$fp_dir/nonlogin" | head -20 | sed -E 's/^([<>] [^=]*)=.*/\1=/; s/^/FEATURE_INIT_DIFF:/'; fail=1; fi; rm -rf "$fp_dir"
printf '\nNATIVE_DONE:%s\n' "$fail"; exit "$fail"
`
	if os.Getenv("CREWSHIP_NATIVE_TOOLS_INHERITED_PROBES") == "1" {
		probes := `printf 'INHERITED_PATH_BYTES:%s\n' "${#PATH}"
for probe in eslint ldconfig; do
  resolved=$(command -v "$probe"); code=$?
  printf 'INHERITED_TOOL_PATH:%s:%s\n' "$probe" "$resolved"
  test "$code" -eq 0 || fail=1
  "$probe" --version
  code=$?; printf 'INHERITED_TOOL_STATUS:%s:%s\n' "$probe" "$code"
  test "$code" -eq 0 || fail=1
done
`
		script = strings.Replace(script, "glab --version\n", probes+"glab --version\n", 1)
	}
	if err := c.WriteMessage(websocket.BinaryMessage, []byte("{\n"+script+"}\n")); err != nil {
		t.Fatal(err)
	}
	// Real xterm answers terminal color/cursor queries. glab can issue these
	// even for --version; a headless WS client must answer them too. Parse the
	// whole shell block before executing it so query readers cannot consume
	// the remaining commands as terminal responses.
	replies := []struct{ query, response string }{
		{"\x1b]11;?", "\x1b]11;rgb:0000/0000/0000\x1b\\"},
		{"\x1b]10;?", "\x1b]10;rgb:ffff/ffff/ffff\x1b\\"},
		{"\x1b[6n", "\x1b[1;1R"},
	}
	answered := map[string]int{}
	var output strings.Builder
	done := regexp.MustCompile(`(?m)^NATIVE_DONE:([0-9]+)\r?$`)
	for {
		kind, b, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("terminal ended without command receipt: %v", err)
		}
		output.Write(b)
		if output.Len() > 128<<10 {
			t.Fatal("bounded terminal output exceeded")
		}
		for _, reply := range replies {
			count := strings.Count(output.String(), reply.query)
			for answered[reply.query] < count {
				if err := c.WriteMessage(websocket.BinaryMessage, []byte(reply.response)); err != nil {
					t.Fatal(err)
				}
				answered[reply.query]++
			}
		}
		t.Logf("terminal frame %d: %q", kind, b)
		if match := done.FindStringSubmatch(output.String()); match != nil {
			t.Logf("actual terminal result:\n%s", output.String())
			if match[1] != "0" {
				t.Fatalf("native tool commands failed (receipt %s)", match[1])
			}
			if expected := os.Getenv("CREWSHIP_NATIVE_TOOLS_CLAUDE_PATH"); expected != "" {
				match := regexp.MustCompile(`(?m)^TOOL_PATH:claude:([^\r\n]+)\r?$`).FindStringSubmatch(output.String())
				if len(match) != 2 || match[1] != expected {
					t.Fatalf("terminal claude path differs from declared expectation %q: %v", expected, match)
				}
			}
			// Pin precedence (#5459): every Crewship-assembled mode must select
			// the declared path. Login shells run the user's ~/.profile (zsh
			// through ~/.zprofile) and are recorded (MODE_PATH:*-login) only.
			if expected := os.Getenv("CREWSHIP_NATIVE_TOOLS_GLAB_PATH"); expected != "" {
				for _, mode := range []string{"terminal", "child", "zsh"} {
					match := regexp.MustCompile(`(?m)^MODE_PATH:` + mode + `:glab:([^\r\n]*)\r?$`).FindStringSubmatch(output.String())
					if mode == "zsh" && regexp.MustCompile(`(?m)^MODE_PATH:zsh:absent\r?$`).MatchString(output.String()) {
						continue
					}
					if len(match) != 2 || match[1] != expected {
						t.Fatalf("%s selected glab %v, declared %q", mode, match, expected)
					}
				}
			}
			if expected := os.Getenv("CREWSHIP_NATIVE_TOOLS_GLAB_VERSION"); expected != "" {
				// Login shells run ~/.profile and may select a home binary;
				// their LOGIN_OUT lines are recorded, not asserted.
				nonLogin := regexp.MustCompile(`(?m)^LOGIN_OUT:.*$`).ReplaceAllString(output.String(), "")
				versions := regexp.MustCompile(`glab ([^[:space:]]+) \(`).FindAllStringSubmatch(nonLogin, -1)
				if len(versions) == 0 {
					t.Fatal("no actual glab version evidence")
				}
				for _, version := range versions {
					if version[1] != expected {
						t.Fatalf("glab selected %s, declared %s", version[1], expected)
					}
				}
			}
			if finalBuild := observeBuild(); finalBuild != initialBuild {
				t.Fatalf("deployment changed during live test: %s -> %s", initialBuild, finalBuild)
			}
			break
		}
	}
}
