package apple

// Tests for Exec / ExecInteractive's fail-closed privileged-user guard — #1158.
//
// The Docker provider refuses to exec as an empty or root user: an empty
// cfg.User is resolved to the container's REAL configured run-as user and
// refused if that cannot be proven non-root, and an explicitly supplied
// privileged user ("0", "root", "0:0", …) is refused on every path. The single
// audited exception is cfg.AllowPrivileged, which the orchestrator's
// root-requiring preflight steps set explicitly.
//
// This provider had none of it: `container exec --user root` went straight
// through, and cfg.AllowPrivileged was read nowhere in the package — so the
// containment guarantee the whole /execute path assumes simply did not exist
// on macOS. These tests pin the Docker semantics on the Apple provider.

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// drainExec consumes an exec's output so the process can finish, then closes.
func drainExec(t *testing.T, res *provider.ExecResult) {
	t.Helper()
	_, _ = io.Copy(io.Discard, res.Reader)
	_ = res.Reader.Close()
}

// An omitted User must resolve to the container's configured run-as user
// rather than being passed through as "no --user at all", which lets the CLI
// pick the image's default (root on almost every base image).
//
// The container here reports a user that is NOT agentContainerUser. That is
// deliberate: while ContainerUser returned the create-time constant, this test
// passed without the resolve reading anything at all, so it could not tell
// "resolved from the container" from "guessed the usual value". Only a
// distinctive user distinguishes them.
func TestExec_EmptyUser_ResolvesContainerUser(t *testing.T) {
	const customUser = "1500:1500"
	fake := installFakeContainer(t, inspectBody(customUser)+`exit 0`)
	p := newTestProvider(Config{})

	res, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "cid-1",
		Cmd:         []string{"true"},
		// User intentionally empty.
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	drainExec(t, res)

	want := "exec --user " + customUser + " cid-1 true"
	if !fake.hasCall(t, want) {
		t.Errorf("expected CLI call %q, got %v", want, fake.calls(t))
	}
}

func TestExec_RefusesPrivilegedUser(t *testing.T) {
	for _, user := range []string{"root", "0", "0:0", "1001:0"} {
		t.Run(user, func(t *testing.T) {
			fake := installFakeContainer(t, `exit 0`)
			p := newTestProvider(Config{})

			_, err := p.Exec(context.Background(), provider.ExecConfig{
				ContainerID: "cid-1",
				Cmd:         []string{"id"},
				User:        user,
			})
			if err == nil {
				t.Fatalf("Exec with user %q succeeded; want refusal", user)
			}
			if !strings.Contains(err.Error(), "privileged") {
				t.Errorf("err = %v, want a privileged-user refusal", err)
			}
			// The refusal has to happen before the process starts, not after.
			if calls := fake.calls(t); len(calls) != 0 {
				t.Errorf("refused exec still invoked the CLI: %v", calls)
			}
		})
	}
}

// cfg.AllowPrivileged is the audited exception the orchestrator's
// root-requiring preflight steps use; without it being honoured those steps
// break, so the guard must not be an unconditional ban.
func TestExec_AllowPrivilegedPermitsRoot(t *testing.T) {
	fake := installFakeContainer(t, `exit 0`)
	p := newTestProvider(Config{})

	res, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID:     "cid-1",
		Cmd:             []string{"id"},
		User:            "root",
		AllowPrivileged: true,
	})
	if err != nil {
		t.Fatalf("Exec with AllowPrivileged: %v", err)
	}
	drainExec(t, res)

	want := "exec --user root cid-1 id"
	if !fake.hasCall(t, want) {
		t.Errorf("expected CLI call %q, got %v", want, fake.calls(t))
	}
}

func TestExecInteractive_EmptyUser_ResolvesContainerUser(t *testing.T) {
	const customUser = "1500:1500" // see TestExec_EmptyUser_ResolvesContainerUser
	fake := installFakeContainer(t, inspectBody(customUser)+`exit 0`)
	p := newTestProvider(Config{})

	res, err := p.ExecInteractive(context.Background(), provider.InteractiveExecConfig{
		ContainerID: "cid-2",
		Cmd:         []string{"sh"},
	})
	if err != nil {
		t.Fatalf("ExecInteractive: %v", err)
	}
	// Close the stdin half so the process sees EOF, then read to EOF: that is
	// what proves the CLI ran and its argv reached the call log.
	closeConnWriteHalf(t, res.Conn)
	_, _ = io.Copy(io.Discard, res.Conn)
	_ = res.Conn.Close()

	want := "exec --tty --user " + customUser + " cid-2 sh"
	if !fake.hasCall(t, want) {
		t.Errorf("expected CLI call %q, got %v", want, fake.calls(t))
	}
}

// The interactive path is the web terminal: there is no AllowPrivileged on
// InteractiveExecConfig at all, so a privileged user is refused outright —
// same as Docker.
func TestExecInteractive_RefusesPrivilegedUser(t *testing.T) {
	fake := installFakeContainer(t, `exit 0`)
	p := newTestProvider(Config{})

	_, err := p.ExecInteractive(context.Background(), provider.InteractiveExecConfig{
		ContainerID: "cid-2",
		Cmd:         []string{"sh"},
		User:        "0:0",
	})
	if err == nil {
		t.Fatal("ExecInteractive as root succeeded; want refusal")
	}
	if !strings.Contains(err.Error(), "privileged") {
		t.Errorf("err = %v, want a privileged-user refusal", err)
	}
	if calls := fake.calls(t); len(calls) != 0 {
		t.Errorf("refused interactive exec still invoked the CLI: %v", calls)
	}
}
