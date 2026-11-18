package orchestrator

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Real OpenSSH against an isolated loopback fixture; no real hosts or secrets.
// Exercises the production writer, the process environment, a child shell,
// canonical and legacy key formats, file permissions and authentication.
func TestNativeCredentialStandardSSH(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Fatal("OpenSSH is required for native credential acceptance")
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	canonical := string(pem.EncodeToMemory(block))
	port := startNativeSSHFixture(t, signer)
	for name, value := range map[string]string{"canonical": canonical, "missing_final_LF": strings.TrimSuffix(canonical, "\n"), "CRLF": strings.ReplaceAll(canonical, "\n", "\r\n")} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			creds := []Credential{{ID: "fixture-key", Type: "SSH_KEY", EnvVarName: "NATIVE_SSH", PlainValue: value}}
			script, _, _, err := buildCredFileScript(creds, dir, true)
			if err != nil {
				t.Fatal(err)
			}
			writer := exec.Command("sh")
			writer.Stdin = strings.NewReader(script)
			if err := writer.Run(); err != nil {
				t.Fatal("credential writer failed", err)
			}
			env, err := appendCredentialFileEnv([]string{"PATH=/usr/bin:/bin"}, creds, dir, true)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(dir, "ssh/NATIVE_SSH"))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("wrong key permissions")
			}
			known := filepath.Join(dir, "known_hosts")
			if err := os.WriteFile(known, []byte(knownhosts.Line([]string{fmt.Sprintf("[127.0.0.1]:%d", port)}, signer.PublicKey())+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", `test -n "$NATIVE_SSH_PATH" && test -f "$NATIVE_SSH_PATH" && exec ssh -F /dev/null -i "$NATIVE_SSH_PATH" -o IdentityAgent=none -o IdentitiesOnly=yes -o BatchMode=yes -o PasswordAuthentication=no -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o ClearAllForwardings=yes -o ConnectTimeout=3 -o UserKnownHostsFile="$1" -p "$2" fixture@127.0.0.1 whoami`, "fixture", known, fmt.Sprint(port))
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != "fixture\\native-user" {
				t.Fatalf("native SSH authentication failed: %v %s", err, out)
			}
		})
	}
}

func startNativeSSHFixture(t *testing.T, signer ssh.Signer, authorized ...ssh.PublicKey) int {
	return startNativeSSHFixtureObserved(t, signer, nil, authorized...)
}

func startNativeSSHFixtureObserved(t *testing.T, signer ssh.Signer, observed func(), authorized ...ssh.PublicKey) int {
	t.Helper()
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, public ssh.PublicKey) (*ssh.Permissions, error) {
		for _, key := range append([]ssh.PublicKey{signer.PublicKey()}, authorized...) {
			if bytes.Equal(public.Marshal(), key.Marshal()) {
				return nil, nil
			}
		}
		return nil, fmt.Errorf("fixture rejected key")
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				server, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					for request := range requests {
						var command struct{ Command string }
						if request.Type != "exec" || ssh.Unmarshal(request.Payload, &command) != nil || command.Command != "whoami" {
							request.Reply(false, nil)
							continue
						}
						if observed != nil {
							observed()
						}
						request.Reply(true, nil)
						fmt.Fprintln(channel, "fixture\\native-user")
						channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						channel.Close()
						break
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { listener.Close(); workers.Wait() })
	return listener.Addr().(*net.TCPAddr).Port
}
