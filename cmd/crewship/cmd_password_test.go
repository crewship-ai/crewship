package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadPasswordFromStdin(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{name: "plain", in: "hunter2", want: "hunter2"},
		{name: "trailing LF stripped", in: "hunter2\n", want: "hunter2"},
		{name: "trailing CRLF stripped", in: "hunter2\r\n", want: "hunter2"},
		{name: "embedded spaces preserved", in: "a b c\n", want: "a b c"},
		{name: "leading space preserved", in: "  pw\n", want: "  pw"},
		{name: "internal newline preserved", in: "a\nb\n", want: "a\nb"},
		{name: "tab preserved", in: "pw\twith\ttab\n", want: "pw\twith\ttab"},
		{name: "unicode preserved", in: "héslo🦀\n", want: "héslo🦀"},
		{name: "empty rejected", in: "", wantErr: "empty password"},
		{name: "only newline rejected", in: "\n", wantErr: "empty password"},
		{name: "only CRLF rejected", in: "\r\n", wantErr: "empty password"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := readPasswordFromStdin(strings.NewReader(tc.in))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (result %q)", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReadPasswordFromStdin_HugePayloadAccepted confirms we do not
// cap at the 4 KiB read-buffer size — a long passphrase or pasted
// JWT-shaped credential must round-trip. bufio.Reader's buffer is the
// READ chunk, not a total cap.
func TestReadPasswordFromStdin_HugePayloadAccepted(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("x", 16*1024) + "\n"
	got, err := readPasswordFromStdin(strings.NewReader(big))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 16*1024 {
		t.Errorf("len = %d, want 16384", len(got))
	}
}

// TestReadPasswordFromStdin_ReaderError propagates I/O failure rather
// than silently returning an empty password (which would let the
// caller's len < 8 check translate I/O failure into a misleading
// "password must be at least 8 characters" surface).
func TestReadPasswordFromStdin_ReaderError(t *testing.T) {
	t.Parallel()
	r := &failingReader{err: errors.New("disk on fire")}
	_, err := readPasswordFromStdin(r)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "disk on fire") {
		t.Errorf("err = %v, want wrapped reader error", err)
	}
}

func TestResolvePasswordInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		flag       string
		stdinFlag  bool
		stdin      io.Reader
		wantPW     string
		wantSource string
		wantErr    string
	}{
		{
			name:       "stdin flag wins when only stdin set",
			stdinFlag:  true,
			stdin:      strings.NewReader("from-stdin\n"),
			wantPW:     "from-stdin",
			wantSource: "stdin",
		},
		{
			name:       "flag value used when stdin flag not set",
			flag:       "from-flag",
			wantPW:     "from-flag",
			wantSource: "flag",
		},
		{
			name:    "both set is a configuration error",
			flag:    "from-flag",
			stdinFlag: true,
			stdin:   strings.NewReader("from-stdin\n"),
			wantErr: "mutually exclusive",
		},
		{
			name:       "neither set returns empty (caller prompts)",
			wantPW:     "",
			wantSource: "",
		},
		{
			name:      "stdin flag with empty stdin propagates the empty error",
			stdinFlag: true,
			stdin:     strings.NewReader(""),
			wantErr:   "empty password",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pw, source, err := resolvePasswordInput(tc.flag, tc.stdinFlag, tc.stdin)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pw != tc.wantPW {
				t.Errorf("password = %q, want %q", pw, tc.wantPW)
			}
			if source != tc.wantSource {
				t.Errorf("source = %q, want %q", source, tc.wantSource)
			}
		})
	}
}

// TestInitCmd_PasswordStdinFlagRegistered guards the flag wiring on
// crewship init — a refactor that drops the flag would silently
// regress the documented "preferred for CI" path.
func TestInitCmd_PasswordStdinFlagRegistered(t *testing.T) {
	t.Parallel()
	f := initCmd.Flags().Lookup("password-stdin")
	if f == nil {
		t.Fatal("crewship init missing --password-stdin flag")
	}
	if f.Value.Type() != "bool" {
		t.Errorf("--password-stdin type = %s, want bool", f.Value.Type())
	}
	if f.DefValue != "false" {
		t.Errorf("--password-stdin default = %s, want false (must opt in)", f.DefValue)
	}
}

// TestAdminResetPasswordCmd_PasswordStdinFlagRegistered same guard for
// the admin recovery path.
func TestAdminResetPasswordCmd_PasswordStdinFlagRegistered(t *testing.T) {
	t.Parallel()
	f := adminResetPasswordCmd.Flags().Lookup("password-stdin")
	if f == nil {
		t.Fatal("crewship admin reset-password missing --password-stdin flag")
	}
	if f.Value.Type() != "bool" {
		t.Errorf("--password-stdin type = %s, want bool", f.Value.Type())
	}
}

// failingReader returns err on every Read so we can test propagation.
type failingReader struct{ err error }

func (r *failingReader) Read(p []byte) (int, error) { return 0, r.err }

var _ io.Reader = (*failingReader)(nil)
