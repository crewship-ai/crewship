package slidesexport

import (
	"bytes"
	"context"
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

type Runner struct {
	Image      string
	OwnerID    string
	JournalDir string
	mu         sync.Mutex
	journal    *cleanupJournal
	active     int
	closed     bool
}
type Lease struct {
	runner   *Runner
	image    string
	journal  *cleanupJournal
	mu       sync.Mutex
	closed   bool
	acquired bool
}

func (r *Runner) Acquire(ctx context.Context) (*Lease, error) { return r.acquire(ctx, false) }
func (r *Runner) acquire(ctx context.Context, maintenance bool) (*Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !maintenance && (len(r.Image) > 256 || !imagePattern.MatchString(r.Image)) {
		return nil, ErrProfile
	}
	if !maintenance && quarantined() {
		return nil, ErrCleanup
	}
	select {
	case slot <- struct{}{}:
	default:
		return nil, ErrBusy
	}
	keep := false
	defer func() {
		if !keep {
			<-slot
		}
	}()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrCleanup
	}
	if r.journal == nil {
		j, err := openCleanupJournal(r.JournalDir, r.OwnerID)
		if err != nil {
			return nil, err
		}
		r.journal = j
	}
	if r.journal.verify() != nil || r.OwnerID != r.journal.owner {
		return nil, ErrCleanup
	}
	if !maintenance {
		intents, err := r.journal.list()
		if err != nil {
			return nil, err
		}
		if len(intents) > 0 || quarantined() {
			for _, v := range intents {
				uncertainJobs.Store(v.Name, true)
			}
			return nil, ErrCleanup
		}
	}
	r.active++
	keep = true
	return &Lease{runner: r, image: r.Image, journal: r.journal, acquired: true}, nil
}
func (l *Lease) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.acquired && !l.closed {
		l.closed = true
		l.runner.mu.Lock()
		l.runner.active--
		l.runner.mu.Unlock()
		<-slot
	}
}
func (r *Runner) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != 0 {
		return ErrBusy
	}
	if r.closed {
		return nil
	}
	r.closed = true
	if r.journal != nil {
		return r.journal.close()
	}
	return nil
}
func (r *Runner) Run(ctx context.Context, input []byte, b Binding) (Frame, error) {
	l, err := r.Acquire(ctx)
	if err != nil {
		return Frame{}, err
	}
	defer l.Close()
	return l.Run(ctx, input, b)
}
func createArgs(name, token, owner, image, mode, report string) []string {
	return []string{"create", "--rm", "--pull=never", "--name", name, "--label", "crewship.slides.export=true", "--label", "crewship.slides.export.owner=" + owner, "--label", "crewship.slides.export.token=" + token, "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=1001:1001", "--init", "--memory=1g", "--memory-swap=1g", "--cpus=1", "--pids-limit=64", "--ulimit", "nofile=256:256", "--ulimit", "fsize=67108864:67108864", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=32m,uid=1001,gid=1001,mode=0700", "--log-driver=none", "--env", "NODE_ENV=production", "--workdir", "/tmp", "--interactive", "--entrypoint", "timeout", image, "--signal=KILL", "120s", "node", "--max-old-space-size=512", "/opt/slides-export/entry.mjs", mode, report}
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
	intent := newIntent(l.journal.owner)
	if l.journal.write(intent, true) != nil {
		return result, ErrCleanup
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		ambiguous := intent.Phase != "created"
		cleanupErr := l.runner.reconcileOwned(cleanup, intent, ambiguous)
		if ambiguous {
			uncertainJobs.Store(intent.Name, true)
			intent.Phase = "uncertain"
			intent.ContainerID = ""
			_ = l.journal.write(intent, false)
			result = Frame{}
			err = errors.Join(err, ErrCleanup)
			return
		}
		if cleanupErr != nil || l.journal.remove(intent) != nil {
			uncertainJobs.Store(intent.Name, true)
			result = Frame{}
			err = errors.Join(err, ErrCleanup)
		}
	}()
	// Parent cancellation does not abort a submitted create RPC immediately. Give
	// its acknowledgement a short independent deadline, then clean the known ID.
	createCtx, finishCreate := context.WithTimeout(context.Background(), 5*time.Second)
	created, createLogs := &capture{limit: 64 << 10, cancel: finishCreate}, &capture{limit: 64 << 10, cancel: finishCreate}
	create := exec.CommandContext(createCtx, "docker", createArgs(intent.Name, intent.Token, intent.OwnerID, l.image, b.Mode, b.ReportHash)...)
	create.WaitDelay = 3 * time.Second
	create.Stdout = created
	create.Stderr = createLogs
	createErr := create.Run()
	finishCreate()
	id := strings.TrimSpace(created.buffer.String())
	if !created.overflow && !createLogs.overflow && digestPattern.MatchString(id) {
		intent.Phase = "created"
		intent.ContainerID = id
		if l.journal.write(intent, false) != nil {
			return result, ErrCleanup
		}
	} else {
		if parent.Err() != nil {
			return result, parent.Err()
		}
		if createErr != nil {
			return result, ErrWorker
		}
		return result, ErrProtocol
	}
	if createErr != nil {
		if parent.Err() != nil {
			return result, parent.Err()
		}
		return result, ErrWorker
	}
	if err := parent.Err(); err != nil {
		return result, err
	}
	// A provider acknowledgement is not ownership proof. Verify the immutable
	// ID and both independently bound labels before any document bytes are sent.
	owned, inspectErr := inspectOwned(parent, intent)
	if inspectErr != nil || owned == nil {
		return result, ErrCleanup
	}
	if err := parent.Err(); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(parent, 130*time.Second)
	defer cancel()
	out, logs := &capture{limit: MaxFrameBytes, cancel: cancel}, &capture{limit: 64 << 10, cancel: cancel}
	cmd := exec.CommandContext(ctx, "docker", "start", "--attach", "--interactive", intent.ContainerID)
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
