package containerstate

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

type partialProbeContainer struct{ *stubContainer }

func (s partialProbeContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if len(cfg.Cmd) >= 3 && cfg.Cmd[2] == osScript {
		return s.stubContainer.Exec(ctx, cfg)
	}
	return nil, errors.New("probe unavailable")
}

func TestCapturePreservesSuccessfulProbeWhenOthersFail(t *testing.T) {
	c := partialProbeContainer{&stubContainer{scripted: map[string]string{osScript: "Alpine"}}}
	snap, err := Capture(t.Context(), c, "container")
	if err != nil || snap.OS != "Alpine" || len(snap.Errs) != 3 {
		t.Fatalf("partial snapshot lost: %#v, %v", snap, err)
	}
	for i, prefix := range []string{"apt:", "pip:", "npm:"} {
		if !strings.HasPrefix(snap.Errs[i], prefix) {
			t.Errorf("unattributed probe failure: %q", snap.Errs[i])
		}
	}
	if len(snap.APT)+len(snap.Pip)+len(snap.Npm) != 0 {
		t.Fatalf("failed probes invented packages: %#v", snap)
	}
}

func TestCaptureFailsWhenContainerCannotBeProbed(t *testing.T) {
	c := &stubContainer{execErr: errors.New("container stopped")}
	snap, err := Capture(t.Context(), c, "stopped-container")
	if err == nil || !strings.Contains(err.Error(), "no probes succeeded") {
		t.Fatalf("total failure accepted: %#v, %v", snap, err)
	}
	if snap.OS != "" || len(snap.APT)+len(snap.Pip)+len(snap.Npm) != 0 {
		t.Fatalf("failure returned snapshot data: %#v", snap)
	}
}

func TestCaptureIgnoresMalformedPackageRows(t *testing.T) {
	c := &stubContainer{scripted: map[string]string{
		aptScript: "valid\t1\n\t2\nlast\t3",
		pipScript: "valid==1\n==2\nlast==3",
		npmScript: "{truncated JSON",
	}}
	snap, err := Capture(t.Context(), c, "container")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.APT) != 2 || len(snap.Pip) != 2 || len(snap.Npm) != 0 {
		t.Fatalf("malformed package names became entries: %#v", snap)
	}
}

type failingProbeStream struct{ closed bool }

func (*failingProbeStream) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (s *failingProbeStream) Close() error           { s.closed = true; return nil }

type streamProbeContainer struct {
	*stubContainer
	stream *failingProbeStream
}

func (s streamProbeContainer) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{Reader: s.stream}, nil
}

func TestProbeReadFailureClosesStream(t *testing.T) {
	stream := &failingProbeStream{}
	c := streamProbeContainer{&stubContainer{}, stream}
	if out, err := exec(t.Context(), c, "container", "probe"); out != "" || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("broken stream accepted: %q, %v", out, err)
	}
	if !stream.closed {
		t.Fatal("broken probe stream leaked")
	}
}
