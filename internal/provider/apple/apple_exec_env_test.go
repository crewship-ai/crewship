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
//
// The env-file reader is stricter than upstream Parser.envFile on purpose: it
// exits 97 on any line Swift's grapheme-cluster parsing could rewrite (a name
// that is not an ASCII identifier, which upstream would whitespace-strip or
// report with the whole line; a value whose first byte is not ASCII, which
// could fuse with the "=" separator). A line break inside a value splits the
// line and trips the same check. So a routing regression fails the exec test
// instead of passing a byte-for-byte cat.
const envEchoBody = `
LC_ALL=C
export LC_ALL
case "$1" in
  exec)
    prev=""
    for a in "$@"; do
      if [ "$prev" = "--env-file" ]; then
        while IFS= read -r line || [ -n "$line" ]; do
          case "$line" in *=*) ;; *) echo "fake-parser: line without =" >&2; exit 97 ;; esac
          key=${line%%=*}
          val=${line#*=}
          case "$key" in
            ''|[!A-Za-z_]*|*[!A-Za-z0-9_]*) echo "fake-parser: name is not an ASCII identifier" >&2; exit 97 ;;
          esac
          first=$(printf '%s' "$val" | od -An -tu1 -N1 | tr -d ' ')
          if [ -n "$first" ] && [ "$first" -ge 128 ]; then
            echo "fake-parser: value starts with a non-ASCII byte" >&2; exit 97
          fi
          printf '%s\n' "$line"
        done < "$a"
      fi
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
			name:    "combining mark at value start uses inherited environment",
			env:     []string{"K=\u0301" + secretToken},
			secrets: []string{secretToken},
			want:    []string{"K=\u0301" + secretToken},
		},
		{
			name:    "combining mark then space uses inherited environment",
			env:     []string{"K=\u0301a b-" + secretToken},
			secrets: []string{secretToken},
			want:    []string{"K=\u0301a b-" + secretToken},
		},
		{
			name:    "ZWJ at value start uses inherited environment",
			env:     []string{"K=\u200d" + secretToken},
			secrets: []string{secretToken},
			want:    []string{"K=\u200d" + secretToken},
		},
		{
			name:    "non-ASCII later in the value stays in the env file",
			env:     []string{"K=a\u0301" + secretToken},
			secrets: []string{secretToken},
			want:    []string{"K=a\u0301" + secretToken},
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
		{name: "file", env: []string{"A=1", "B=x y", "_c9= lead"}, wantArgs: []string{"--env-file", "/dev/fd/3"}, wantFile: "A=1\nB=x y\n_c9= lead\n"},
		{name: "bare key keeps host inheritance", env: []string{"HOSTVAR"}, wantArgs: []string{"--env", "HOSTVAR"}},
		{name: "line breaks inherit", env: []string{"K=a\r\nb", "L=a\u2028b", "M=a\u2029b", "N=a\u0085b"}, wantArgs: []string{"--env", "K", "--env", "L", "--env", "M", "--env", "N"}, wantInherit: []string{"K=a\r\nb", "L=a\u2028b", "M=a\u2029b", "N=a\u0085b"}},
		{name: "invalid utf8 inherits", env: []string{"K=\xff"}, wantArgs: []string{"--env", "K"}, wantInherit: []string{"K=\xff"}},
		{name: "combining mark at value start inherits", env: []string{"K=\u0301x"}, wantArgs: []string{"--env", "K"}, wantInherit: []string{"K=\u0301x"}},
		{name: "ZWJ at value start inherits", env: []string{"K=\u200dx"}, wantArgs: []string{"--env", "K"}, wantInherit: []string{"K=\u200dx"}},
		{name: "value then bare: bare wins", env: []string{"K=v", "K"}, wantArgs: []string{"--env", "K"}},
		{name: "empty name refused", env: []string{"=secret"}, wantErr: true},
		{name: "leading space name refused", env: []string{" PATH=secret"}, wantErr: true},
		{name: "leading NBSP name refused", env: []string{"\u00a0PATH=secret"}, wantErr: true},
		{name: "whitespace-only name refused", env: []string{"\u3000=secret"}, wantErr: true},
		{name: "comment-like name refused", env: []string{"#K=secret"}, wantErr: true},
		{name: "digit-first name refused", env: []string{"1K=secret"}, wantErr: true},
		{name: "non-ASCII bare name refused", env: []string{"K\u0301secret"}, wantErr: true},
		{name: "fallback refuses PATH", env: []string{"PATH=secret\nx"}, wantErr: true},
		{name: "fallback refuses HOME", env: []string{"HOME=secret\nx"}, wantErr: true},
		{name: "fallback refuses CONTAINER_DEBUG", env: []string{"CONTAINER_DEBUG=secret\nx"}, wantErr: true},
		{name: "fallback refuses CONTAINER_APP_ROOT", env: []string{"CONTAINER_APP_ROOT=\u0301secret"}, wantErr: true},
		{name: "fallback refuses DYLD_", env: []string{"DYLD_INSERT_LIBRARIES=secret\nx"}, wantErr: true},
		{name: "fallback refuses LD_", env: []string{"LD_PRELOAD=secret\nx"}, wantErr: true},
		{name: "fallback refuses XDG_", env: []string{"XDG_CONFIG_HOME=secret\nx"}, wantErr: true},
		{name: "fallback refuses CF", env: []string{"CFFIXED_USER_HOME=secret\nx"}, wantErr: true},
		{name: "PATH on the env file is allowed", env: []string{"PATH=/usr/bin:/bin"}, wantArgs: []string{"--env-file", "/dev/fd/3"}, wantFile: "PATH=/usr/bin:/bin\n"},
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

func TestCheckExecEnvCLIVersion(t *testing.T) {
	cases := []struct {
		version string
		ok      bool
	}{
		{"1.0.0", true},
		{"1.7.1", true},
		{"2.0", true},
		{"container CLI version 1.2.0 (build: release, commit: abc1234)", true},
		{"0.11.0", false},
		{"0.8.0", false},
		{"0.5.0", false},
		{"container CLI version 0.9.9 (build: release)", false},
		{"", false},
		{"unknown", false},
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			err := checkExecEnvCLIVersion(tc.version)
			if (err == nil) != tc.ok {
				t.Fatalf("checkExecEnvCLIVersion(%q) = %v, want ok=%v", tc.version, err, tc.ok)
			}
		})
	}
}

// TestExecEnvRefusedOnUnverifiedCLI: below the verified version, or when the
// version is unknown, an exec that carries environment is refused before the
// CLI runs; one without environment is unaffected.
func TestExecEnvRefusedOnUnverifiedCLI(t *testing.T) {
	cases := []struct {
		name    string
		version string
		env     []string
		wantErr bool
	}{
		{name: "old CLI with env", version: "0.9.0", env: []string{"API_TOKEN=" + secretToken}, wantErr: true},
		{name: "unknown version with env", version: "", env: []string{"API_TOKEN=" + secretToken}, wantErr: true},
		{name: "old CLI without env", version: "0.9.0", env: nil},
		{name: "verified CLI with env", version: "1.0.0", env: []string{"API_TOKEN=" + secretToken}},
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
				p.cliVersion = tc.version
				var err error
				if interactive {
					var res *provider.InteractiveExecResult
					res, err = p.ExecInteractive(context.Background(), provider.InteractiveExecConfig{ContainerID: "cid", Cmd: []string{"sh"}, Env: tc.env, User: "1001"})
					if err == nil {
						closeConnWriteHalf(t, res.Conn)
						_, _ = io.ReadAll(res.Conn)
						waitExecDone(t, p, res.ExecID)
						_ = res.Conn.Close()
					}
				} else {
					var res *provider.ExecResult
					res, err = p.Exec(context.Background(), provider.ExecConfig{ContainerID: "cid", Cmd: []string{"true"}, Env: tc.env, User: "1001"})
					if err == nil {
						_, _ = io.ReadAll(res.Reader)
						waitExecDone(t, p, res.ExecID)
					}
				}
				if tc.wantErr {
					if err == nil {
						t.Fatal("want refusal")
					}
					if strings.Contains(err.Error(), secretToken) {
						t.Fatalf("error leaks the value: %v", err)
					}
					if len(fake.calls(t)) != 0 {
						t.Fatalf("CLI ran despite refusal: %v", fake.calls(t))
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			})
		}
	}
}
