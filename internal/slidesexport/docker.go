package slidesexport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	exportprofile "github.com/crewship-ai/crewship/tools/slides-export/runtime"
)

var ErrBusy = errors.New("Slides export busy")
var ErrWorker = errors.New("Slides export worker failed")
var ErrOutput = errors.New("Slides export worker output exceeded budget")
var ErrCleanup = errors.New("Slides export owned worker cleanup failed")
var ErrProfile = errors.New("Slides export image does not match server profile")
var imagePattern = regexp.MustCompile(`^(?:[A-Za-z0-9._:/-]+@)?sha256:[a-f0-9]{64}$`)
var slot = make(chan struct{}, 1)

type Runner struct{ Image string }

// Lease starts before admission/asset decode and remains owned until Close,
// including final authorization and bounded HTTP delivery by a future API.
type Lease struct {
	runner   *Runner
	mu       sync.Mutex
	closed   bool
	acquired bool
}

func (r *Runner) Acquire(ctx context.Context) (*Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(r.Image) > 256 || !imagePattern.MatchString(r.Image) {
		return nil, ErrProfile
	}
	select {
	case slot <- struct{}{}:
		return &Lease{runner: r, acquired: true}, nil
	default:
		return nil, ErrBusy
	}
}
func (l *Lease) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.acquired && !l.closed {
		l.closed = true
		<-slot
	}
}
func (r *Runner) Run(ctx context.Context, input []byte, b Binding) (Frame, error) {
	l, err := r.Acquire(ctx)
	if err != nil {
		return Frame{}, err
	}
	defer l.Close()
	return l.Run(ctx, input, b)
}
func containerArgs(name, image, mode, report string) []string {
	return []string{"run", "--rm", "--pull=never", "--name", name, "--label", "crewship.slides.export=true", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=1001:1001", "--init", "--memory=1g", "--memory-swap=1g", "--cpus=1", "--pids-limit=64", "--ulimit", "nofile=256:256", "--ulimit", "fsize=67108864:67108864", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=32m,uid=1001,gid=1001,mode=0700", "--log-driver=none", "--env", "NODE_ENV=production", "--workdir", "/tmp", "--interactive", "--entrypoint", "timeout", image, "--signal=KILL", "120s", "node", "--max-old-space-size=512", "/opt/slides-export/entry.mjs", mode, report}
}
func (l *Lease) Run(parent context.Context, input []byte, b Binding) (result Frame, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || !l.acquired || l.runner == nil {
		return result, ErrBusy
	}
	if err := parent.Err(); err != nil {
		return result, err
	}
	if b.validate() != nil {
		return result, ErrProtocol
	}
	admitted, err := Decode(input)
	if err != nil {
		return result, err
	}
	if admitted.Deck.DeckID != b.PageID || admitted.Revision != b.Revision || admitted.ContentHash != b.ContentHash {
		return result, ErrProtocol
	}
	if _, err = ProfileFingerprint(); err != nil {
		return result, err
	}
	if err := parent.Err(); err != nil {
		return result, err
	}
	name := "crewship-slide-export-" + rand.Text()
	ctx, cancel := context.WithTimeout(parent, 130*time.Second)
	defer cancel()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		out, diagnostic := &capture{limit: 64 << 10, cancel: stop}, &capture{limit: 64 << 10, cancel: stop}
		cmd := exec.CommandContext(cleanup, "docker", "rm", "--force", name)
		cmd.WaitDelay = 3 * time.Second
		cmd.Stdout = out
		cmd.Stderr = diagnostic
		cleanupErr := cmd.Run()
		missing := !out.overflow && !diagnostic.overflow && strings.Contains(diagnostic.buffer.String(), "No such container")
		if out.overflow || diagnostic.overflow || (cleanupErr != nil && !missing) {
			result = Frame{}
			err = errors.Join(err, ErrCleanup)
		}
	}()

	out, logs := &capture{limit: MaxFrameBytes, cancel: cancel}, &capture{limit: 64 << 10, cancel: cancel}
	cmd := exec.CommandContext(ctx, "docker", containerArgs(name, l.runner.Image, b.Mode, b.ReportHash)...)
	cmd.WaitDelay = 3 * time.Second
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = out
	cmd.Stderr = logs
	if runErr := cmd.Run(); runErr != nil {
		if out.overflow || logs.overflow {
			return result, ErrOutput
		}
		if parent.Err() != nil {
			return result, parent.Err()
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, ErrWorker
	}
	if out.overflow || logs.overflow {
		return result, ErrOutput
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return DecodeFrame(out.buffer.Bytes(), b)
}

// No embedding: io.Copy must never discover an unbounded ReaderFrom fast path.
type capture struct {
	buffer   bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	overflow bool
}

func (w *capture) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.buffer.Len() {
		w.overflow = true
		w.cancel()
		return 0, ErrOutput
	}
	return w.buffer.Write(p)
}

func ProfileFingerprint() (string, error) {
	raw, err := exportprofile.ManifestBytes()
	if err != nil {
		return "", ErrProfile
	}
	var manifest exportprofile.Manifest
	if json.Unmarshal(raw, &manifest) != nil || manifest.AdmissionSHA256 != AdmissionSHA256() {
		return "", ErrProfile
	}
	fingerprint, err := exportprofile.Fingerprint()
	if err != nil {
		return "", ErrProfile
	}
	return fingerprint, nil
}
