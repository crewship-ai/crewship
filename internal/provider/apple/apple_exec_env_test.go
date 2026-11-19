package apple

import (
	"bufio"
	"context"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// envEchoBody is a fake `container` that prints, one KEY=value per line, the
// environment the real CLI would assemble from its flags: every line of each
// --env-file and, for each bare --env KEY, KEY's value in its own process
// environment. --env KEY=value is printed as given. Multi-line values come out
// as several lines; the assertions compare against the same rendering.
const envEchoBody = `
case "$1" in
  exec)
    prev=""
    for a in "$@"; do
      if [ "$prev" = "--env-file" ]; then cat "$a"; fi
      if [ "$prev" = "--env" ]; then
        case "$a" in
          *=*) printf '%s\n' "$a" ;;
          *) eval "printf '%s=%s\n' \"\$a\" \"\${$a}\"" ;;
        esac
      fi
      prev="$a"
    done
    exit 0;;
esac
exit 0`

const (
	secretToken = "sk-live-5497-single-line-secret"
	secretPEM   = "-----BEGIN KEY-----\nc2VjcmV0LTU0OTc=\n-----END KEY-----"
)

// TestExecKeepsEnvValuesOffArgv is the #5497 regression: no credential value
// may appear in the `container` command line, and the CLI must still receive
// every variable with its exact value.
func TestExecKeepsEnvValuesOffArgv(t *testing.T) {
	cases := []struct {
		name    string
		env     []string
		secrets []string
		want    []string // KEY=value lines the CLI must see
	}{
		{
			name:    "single-line credential via env file",
			env:     []string{"API_TOKEN=" + secretToken},
			secrets: []string{secretToken},
			want:    []string{"API_TOKEN=" + secretToken},
		},
		{
			name:    "value with equals and spaces kept verbatim",
			env:     []string{"DSN=postgres://u:p a=ss@h/db?x=1 "},
			secrets: []string{"p a=ss"},
			want:    []string{"DSN=postgres://u:p a=ss@h/db?x=1 "},
		},
		{
			name:    "multi-line credential via inherited environment",
			env:     []string{"PRIVATE_KEY=" + secretPEM},
			secrets: []string{"c2VjcmV0LTU0OTc=", "BEGIN KEY"},
			want:    strings.Split("PRIVATE_KEY="+secretPEM, "\n"),
		},
		{
			name:    "duplicates: last value wins",
			env:     []string{"API_TOKEN=old-5497-value", "API_TOKEN=" + secretToken},
			secrets: []string{"old-5497-value", secretToken},
			want:    []string{"API_TOKEN=" + secretToken},
		},
		{
			name:    "mixed transports in one exec",
			env:     []string{"A=1", "PRIVATE_KEY=" + secretPEM, "API_TOKEN=" + secretToken, "EMPTY="},
			secrets: []string{secretToken, "c2VjcmV0LTU0OTc="},
			want:    append([]string{"A=1", "API_TOKEN=" + secretToken, "EMPTY="}, strings.Split("PRIVATE_KEY="+secretPEM, "\n")...),
		},
	}
	for _, tc := range cases {
		for _, interactive := range []bool{false, true} {
			name := tc.name
			if interactive {
				name += "/interactive"
			}
			t.Run(name, func(t *testing.T) {
				fake := installFakeContainer(t, envEchoBody)
				p := newTestProvider(Config{})
				out := runEnvExec(t, p, interactive, tc.env)

				for _, call := range fake.calls(t) {
					for _, secret := range tc.secrets {
						if strings.Contains(call, secret) {
							t.Fatalf("credential value %q on host argv: %q", secret, call)
						}
					}
				}
				got := strings.Split(strings.TrimRight(out, "\n"), "\n")
				sort.Strings(got)
				want := append([]string(nil), tc.want...)
				sort.Strings(want)
				if strings.Join(got, "\n") != strings.Join(want, "\n") {
					t.Fatalf("CLI environment\n got: %q\nwant: %q", got, want)
				}
			})
		}
	}
}

func runEnvExec(t *testing.T, p *Provider, interactive bool, env []string) string {
	t.Helper()
	ctx := context.Background()
	if !interactive {
		res, err := p.Exec(ctx, provider.ExecConfig{ContainerID: "cid", Cmd: []string{"true"}, Env: env, User: "1001"})
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}
		out, err := io.ReadAll(res.Reader)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if code := waitExecDone(t, p, res.ExecID); code != 0 {
			t.Fatalf("exit code %d, output %q", code, out)
		}
		return string(out)
	}
	res, err := p.ExecInteractive(ctx, provider.InteractiveExecConfig{ContainerID: "cid", Cmd: []string{"sh"}, Env: env, User: "1001"})
	if err != nil {
		t.Fatalf("ExecInteractive: %v", err)
	}
	closeConnWriteHalf(t, res.Conn)
	out, err := io.ReadAll(bufio.NewReader(res.Conn))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if code := waitExecDone(t, p, res.ExecID); code != 0 {
		t.Fatalf("exit code %d, output %q", code, out)
	}
	_ = res.Conn.Close()
	return string(out)
}

func TestBuildExecEnv(t *testing.T) {
	cases := []struct {
		name        string
		env         []string
		wantArgs    []string
		wantFile    string
		wantInherit []string
		wantErr     bool
	}{
		{name: "empty", env: nil},
		{name: "file", env: []string{"A=1", "B=x y"}, wantArgs: []string{"--env-file", "/dev/fd/3"}, wantFile: "A=1\nB=x y\n"},
		{name: "bare key keeps host inheritance", env: []string{"HOSTVAR"}, wantArgs: []string{"--env", "HOSTVAR"}},
		{name: "line breaks inherit", env: []string{"K=a\r\nb", "L=a b"}, wantArgs: []string{"--env", "K", "--env", "L"}, wantInherit: []string{"K=a\r\nb", "L=a b"}},
		{name: "invalid utf8 inherits", env: []string{"K=\xff"}, wantArgs: []string{"--env", "K"}, wantInherit: []string{"K=\xff"}},
		{name: "comment-like key inherits", env: []string{"#K=v"}, wantArgs: []string{"--env", "#K"}, wantInherit: []string{"#K=v"}},
		{name: "value then bare: bare wins", env: []string{"K=v", "K"}, wantArgs: []string{"--env", "K"}},
		{name: "empty name refused", env: []string{"=secret"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildExecEnv(tc.env)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want error")
				}
				if strings.Contains(err.Error(), "secret") {
					t.Fatalf("error leaks the value: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got.args, "\x00") != strings.Join(tc.wantArgs, "\x00") {
				t.Errorf("args = %q, want %q", got.args, tc.wantArgs)
			}
			if string(got.file) != tc.wantFile {
				t.Errorf("file = %q, want %q", got.file, tc.wantFile)
			}
			if strings.Join(got.inherit, "\x00") != strings.Join(tc.wantInherit, "\x00") {
				t.Errorf("inherit = %q, want %q", got.inherit, tc.wantInherit)
			}
		})
	}
}
