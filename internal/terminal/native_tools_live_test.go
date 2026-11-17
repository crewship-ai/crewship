//go:build integration

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
func TestNativeToolsLiveTerminal(t *testing.T) {
	profile, crewID := os.Getenv("CREWSHIP_NATIVE_TOOLS_PROFILE"), os.Getenv("CREWSHIP_NATIVE_TOOLS_CREW_ID")
	if profile == "" || crewID == "" {
		t.Skip("explicit dev profile and test crew required")
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
	// Wait for actual shell output before input; text frames are server events.
	for {
		kind, b, err := c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if kind == websocket.TextMessage {
			t.Logf("terminal event: %s", b)
		}
		if kind == websocket.BinaryMessage {
			break
		}
	}
	script := `fail=0
id -u
printf 'SHELL_EXE='; readlink /proc/$$/exe
if shopt -q login_shell; then echo MODE=login; else echo MODE=non-login; fi
printf 'HOME=%s\nPATH=%s\nMISE_DATA_DIR=%s\nMISE_GLOBAL_CONFIG_FILE=%s\n' "$HOME" "$PATH" "$MISE_DATA_DIR" "$MISE_GLOBAL_CONFIG_FILE"
glab --version
code=$?; printf 'TOOL_STATUS:glab:%s\n' "$code"; test "$code" -eq 0 || fail=1
codex --version
code=$?; printf 'TOOL_STATUS:codex:%s\n' "$code"; test "$code" -eq 0 || fail=1
claude --version
code=$?; printf 'TOOL_STATUS:claude:%s\n' "$code"; test "$code" -eq 0 || fail=1
sh -c 'glab --version && codex --version && claude --version'
code=$?; printf 'CHILD_STATUS:%s\n' "$code"; test "$code" -eq 0 || fail=1
bash -lc 'glab --version && codex --version && claude --version'
code=$?; printf 'LOGIN_STATUS:%s\n' "$code"; test "$code" -eq 0 || fail=1
if command -v zsh >/dev/null; then zsh -c 'glab --version && codex --version && claude --version'; code=$?; printf 'ZSH_STATUS:%s\n' "$code"; test "$code" -eq 0 || fail=1; fi
printf '\nNATIVE_DONE:%s\n' "$fail"
`
	if err := c.WriteMessage(websocket.BinaryMessage, []byte(script)); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	done := regexp.MustCompile(`(?m)^NATIVE_DONE:([0-9]+)\r?$`)
	for {
		_, b, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("terminal ended without command receipt: %v", err)
		}
		output.Write(b)
		if output.Len() > 128<<10 {
			t.Fatal("bounded terminal output exceeded")
		}
		if match := done.FindStringSubmatch(output.String()); match != nil {
			t.Logf("actual terminal result:\n%s", output.String())
			if match[1] != "0" {
				t.Fatalf("native tool commands failed (receipt %s)", match[1])
			}
			break
		}
	}
}
