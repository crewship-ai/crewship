package slidesexport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestLeaseOwnsOneSlotAndZeroValueCannotReleaseIt(t *testing.T) {
	r := &Runner{Image: "sha256:" + strings.Repeat("a", 64)}
	l, err := r.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := r.Acquire(t.Context()); !errors.Is(err, ErrBusy) {
		t.Fatal("parallel admission accepted")
	}
	var empty Lease
	empty.Close()
	if _, err := empty.Run(t.Context(), nil, Binding{}); !errors.Is(err, ErrBusy) {
		t.Fatal("unowned lease accepted")
	}
	l.Close()
	next, err := r.Acquire(t.Context())
	if err != nil {
		t.Fatal("slot not released", err)
	}
	next.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestCaptureDoesNotExposeReaderFromAndPolicyIsFixed(t *testing.T) {
	cancelled := false
	capture := &capture{limit: 4, cancel: func() { cancelled = true }}
	_, err := io.Copy(capture, struct{ io.Reader }{bytes.NewReader(make([]byte, 32))})
	if err == nil || !cancelled || capture.buffer.Len() > 4 {
		t.Fatal("capture limit bypassed")
	}
	args := strings.Join(containerArgs("owned", "sha256:"+strings.Repeat("a", 64), "report", ""), " ")
	for _, required := range []string{"--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=1001:1001", "--memory=1g", "--memory-swap=1g", "--pids-limit=64", "--cpus=1", "--log-driver=none", "--signal=KILL 120s", "--max-old-space-size=512"} {
		if !strings.Contains(args, required) {
			t.Fatal("missing isolation", required)
		}
	}
	for _, forbidden := range []string{"--privileged", "--volume", "--mount", "docker.sock", "HTTP_PROXY", "HTTPS_PROXY"} {
		if strings.Contains(args, forbidden) {
			t.Fatal("unsafe flag", forbidden)
		}
	}
}

func TestEmbeddedRuntimeTracksActualAdmissionSource(t *testing.T) {
	if _, err := ProfileFingerprint(); err != nil {
		t.Fatal("regenerate runtime/profile.json after source change", err)
	}
}
