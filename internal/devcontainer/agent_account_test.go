//go:build unix

package devcontainer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runAccountScript executes the real ensureAgentAccountScript with sh against
// scratch copies of passwd/group, so the behaviour is tested, not the text.
func runAccountScript(t *testing.T, passwd, group string) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("awk not available")
	}
	dir := t.TempDir()
	p, g := filepath.Join(dir, "passwd"), filepath.Join(dir, "group")
	mustWrite(t, p, passwd, 0o644)
	mustWrite(t, g, group, 0o644)
	script := strings.NewReplacer("/etc/passwd", p, "/etc/group", g).Replace(ensureAgentAccountScript)
	for i := 0; i < 2; i++ { // second pass proves idempotence
		if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("script failed: %v\n%s", err, out)
		}
	}
	pb, _ := os.ReadFile(p)
	gb, _ := os.ReadFile(g)
	return string(pb), string(gb)
}

func TestEnsureAgentAccount(t *testing.T) {
	const root = "root:x:0:0:root:/root:/bin/sh\n"
	cases := []struct {
		name, passwd, group  string
		wantPasswd, wantGrp  string
		wantPasswdLinesTotal int
	}{
		{
			name:   "image without uid 1001 gets an agent account",
			passwd: root, group: "root:x:0:\n",
			wantPasswd: "agent:x:1001:1001:Crewship agent:/home/agent:", wantGrp: "agent:x:1001:", wantPasswdLinesTotal: 2,
		},
		{
			name:   "common-utils automatic reused uid 1000 named node; agent name free",
			passwd: root + "node:x:1000:1000::/home/node:/bin/bash\n", group: "root:x:0:\nnode:x:1000:\n",
			wantPasswd: "agent:x:1001:1001:", wantGrp: "agent:x:1001:", wantPasswdLinesTotal: 3,
		},
		{
			name:   "name agent already owned by another uid",
			passwd: root + "agent:x:1000:1000::/home/agent:/bin/sh\n", group: "root:x:0:\nagent:x:1000:\n",
			wantPasswd: "crewship-agent:x:1001:1001:", wantGrp: "crewship-agent:x:1001:", wantPasswdLinesTotal: 3,
		},
		{
			name:   "existing 1001 entry is left untouched",
			passwd: root + "vscode:x:1001:1001::/home/vscode:/bin/bash\n", group: "root:x:0:\nvscode:x:1001:\n",
			wantPasswd: "vscode:x:1001:1001::/home/vscode:/bin/bash", wantGrp: "vscode:x:1001:", wantPasswdLinesTotal: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, g := runAccountScript(t, tc.passwd, tc.group)
			if !strings.Contains(p, tc.wantPasswd) {
				t.Errorf("passwd missing %q:\n%s", tc.wantPasswd, p)
			}
			if !strings.Contains(g, tc.wantGrp) {
				t.Errorf("group missing %q:\n%s", tc.wantGrp, g)
			}
			if n := strings.Count(p, "\n"); n != tc.wantPasswdLinesTotal {
				t.Errorf("passwd has %d lines, want %d (duplicate append?):\n%s", n, tc.wantPasswdLinesTotal, p)
			}
			if strings.Count(g, ":1001:") != 1 {
				t.Errorf("group must hold exactly one gid 1001 entry:\n%s", g)
			}
		})
	}
}

func TestEnsureAgentHomeOwnership_CreatesAccountBeforeChown(t *testing.T) {
	var got string
	exec := func(_ context.Context, _ string, cmd []string, _ string, _ []string) (string, int, error) {
		got = strings.Join(cmd, " ")
		return "", 0, nil
	}
	if err := EnsureAgentHomeOwnership(context.Background(), "c", exec); err != nil {
		t.Fatal(err)
	}
	acct, chown := strings.Index(got, "/etc/passwd"), strings.Index(got, "chown -R 1001:1001")
	if acct < 0 || chown < 0 || acct > chown {
		t.Fatalf("account step must run before ownership step: %q", got)
	}
}
