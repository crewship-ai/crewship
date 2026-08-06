package apple

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// Apple's `container exec` does NOT attach the caller's stdin unless -i is
// given. Without it `sh` (no operands) reads its commands from a stream that is
// already at EOF: it runs nothing and exits 0.
//
// That is how every preflight write silently vanished on macOS. The merged
// preflight batch deliberately delivers its script over stdin so credentials
// never reach argv (preflight_batch.go), so on this provider the whole batch —
// agent dirs, memory dirs, credentials, claude config, MCP config — executed
// nothing and reported success. The agent then failed with "MCP config file not
// found", three layers from the cause.
//
// Reproduced against container 1.2.0:
//
//	printf 'echo HELLO\nexit 7\n' | container exec    <id> sh  ->  no output, exit 0
//	printf 'echo HELLO\nexit 7\n' | container exec -i <id> sh  ->  HELLO,     exit 7
//
// Docker pins the same contract in docker/exec_stdin_test.go; this provider had
// no counterpart, which is why a non-conforming implementation got through.
func TestExec_AttachesStdinWhenSupplied(t *testing.T) {
	fake := installFakeContainer(t, inspectBody(agentContainerUser)+`exit 0`)
	p := newTestProvider(Config{})

	res, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "c1",
		Cmd:         []string{"sh"},
		Stdin:       strings.NewReader("echo hi\n"),
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	_, _ = io.Copy(io.Discard, res.Reader)
	_ = res.Reader.Close()

	argv := strings.Join(fake.calls(t), "\n")
	if !strings.Contains(argv, "-i") {
		t.Errorf("stdin was supplied but -i was not passed — the script is discarded and sh exits 0:\n%s", argv)
	}
}

// Without stdin the flag must stay off: -i on a command that has nothing to
// read changes how the CLI treats the stream for no reason.
func TestExec_OmitsInteractiveWithoutStdin(t *testing.T) {
	fake := installFakeContainer(t, inspectBody(agentContainerUser)+`exit 0`)
	p := newTestProvider(Config{})

	res, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "c1",
		Cmd:         []string{"sh", "-c", "true"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	_, _ = io.Copy(io.Discard, res.Reader)
	_ = res.Reader.Close()

	for _, call := range fake.calls(t) {
		for _, f := range strings.Fields(call) {
			if f == "-i" || f == "--interactive" {
				t.Errorf("no stdin was supplied, but the call carries %q: %s", f, call)
			}
		}
	}
}
