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
// CREWSHIP_NATIVE_TOOLS_GLAB_VERSION asserts every observed selection
// (terminal, child, login and zsh) against the declared version after an update.
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
sh -c 'glab --version && codex --version && claude --version'
code=$?; printf 'CHILD_STATUS:%s\n' "$code"; test "$code" -eq 0 || fail=1
bash -lc 'glab --version && codex --version && claude --version'
code=$?; printf 'LOGIN_STATUS:%s\n' "$code"; test "$code" -eq 0 || fail=1
if command -v zsh >/dev/null; then zsh -c 'glab --version && codex --version && claude --version'; code=$?; printf 'ZSH_STATUS:%s\n' "$code"; test "$code" -eq 0 || fail=1; fi
printf '\nNATIVE_DONE:%s\n' "$fail"; exit "$fail"
`
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
			if expected := os.Getenv("CREWSHIP_NATIVE_TOOLS_GLAB_VERSION"); expected != "" {
				versions := regexp.MustCompile(`glab ([^[:space:]]+) \(`).FindAllStringSubmatch(output.String(), -1)
				if len(versions) == 0 {
					t.Fatal("no actual glab version evidence")
				}
				for _, version := range versions {
					if version[1] != expected {
						t.Fatalf("glab selected %s, declared %s", version[1], expected)
					}
				}
			}
			break
		}
	}
}
