package backup

import (
	"context"
	"errors"
	"fmt"
	"github.com/klauspost/compress/zstd"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

// ErrRestoreLimit means the operator's configured work allowance was exhausted.
var ErrRestoreLimit = errors.New("backup restore resource limit exceeded")
var ErrRestoreBusy = errors.New("another backup read operation is running")

type RestoreLimits struct {
	Concurrency     int
	CompressedBytes int64
	DecodedBytes    int64
	Entries         int64
	GeneratedBytes  int64
	BufferedBytes   int64
	HeaderBytes     int64
	SectionSinks    int64
	DecoderMemory   uint64
	DecoderWindow   uint64
	Depth           int
	Timeout         time.Duration
}

func DefaultRestoreLimits() RestoreLimits {
	return RestoreLimits{Concurrency: 1, CompressedBytes: 64 << 30, DecodedBytes: 128 << 30, Entries: 1_000_000, GeneratedBytes: 64 << 30, BufferedBytes: 64 << 20, HeaderBytes: 256 << 20, SectionSinks: 256, DecoderMemory: 64 << 20, DecoderWindow: 64 << 20, Depth: 4, Timeout: 2 * time.Hour}
}

// RestoreLimitsFromEnv is shared by online and offline backup readers. These
// are operator capacity limits, not inferred compression ratios or RSS bounds.
func RestoreLimitsFromEnv() (RestoreLimits, error) {
	l := DefaultRestoreLimits()
	fields := []struct {
		name  string
		value *int64
	}{
		{"COMPRESSED_BYTES", &l.CompressedBytes}, {"DECODED_BYTES", &l.DecodedBytes},
		{"ENTRIES", &l.Entries}, {"GENERATED_BYTES", &l.GeneratedBytes},
		{"BUFFERED_BYTES", &l.BufferedBytes}, {"HEADER_BYTES", &l.HeaderBytes}, {"SECTION_SINKS", &l.SectionSinks},
	}
	for _, f := range fields {
		if s := os.Getenv("CREWSHIP_BACKUP_RESTORE_" + f.name); s != "" {
			n, e := strconv.ParseInt(s, 10, 64)
			if e != nil || n < 1 || n > 1<<50 {
				return l, fmt.Errorf("invalid CREWSHIP_BACKUP_RESTORE_%s", f.name)
			}
			*f.value = n
		}
	}
	for _, f := range []struct {
		name  string
		value *uint64
	}{{"DECODER_MEMORY_BYTES", &l.DecoderMemory}, {"DECODER_WINDOW_BYTES", &l.DecoderWindow}} {
		if s := os.Getenv("CREWSHIP_BACKUP_RESTORE_" + f.name); s != "" {
			n, e := strconv.ParseUint(s, 10, 64)
			if e != nil || n < 1<<20 || n > 1<<30 {
				return l, fmt.Errorf("invalid CREWSHIP_BACKUP_RESTORE_%s", f.name)
			}
			*f.value = n
		}
	}
	if s := os.Getenv("CREWSHIP_BACKUP_RESTORE_DEPTH"); s != "" {
		n, e := strconv.Atoi(s)
		if e != nil || n < 1 || n > 8 {
			return l, fmt.Errorf("invalid CREWSHIP_BACKUP_RESTORE_DEPTH")
		}
		l.Depth = n
	}
	if s := os.Getenv("CREWSHIP_BACKUP_RESTORE_TIMEOUT"); s != "" {
		n, e := time.ParseDuration(s)
		if e != nil || n < time.Second || n > 24*time.Hour {
			return l, fmt.Errorf("invalid CREWSHIP_BACKUP_RESTORE_TIMEOUT")
		}
		l.Timeout = n
	}
	if raw := os.Getenv("CREWSHIP_BACKUP_RESTORE_CONCURRENCY"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 4 {
			return l, fmt.Errorf("invalid CREWSHIP_BACKUP_RESTORE_CONCURRENCY")
		}
		l.Concurrency = n
	}
	return l, nil
}

type restoreBudgetKey struct{}
type restoreBudget struct {
	parent                      *restoreBudget
	mu                          sync.Mutex
	limits                      RestoreLimits
	decoded, entries, generated int64
	buffered, headers, sinks    int64
	active                      int
	failure                     error
}

var restoreAdmission struct {
	sync.Mutex
	active int
}

// beginRestoreRead returns a nested no-op release when a caller already owns
// the operation. Thus Verify/reopen/nested readers share work and admission.
func BeginRestoreRead(ctx context.Context) (context.Context, func(), error) {
	if _, ok := ctx.Value(restoreBudgetKey{}).(*restoreBudget); ok {
		return ctx, func() {}, nil
	}
	l, e := RestoreLimitsFromEnv()
	if e != nil {
		return ctx, nil, e
	}
	restoreAdmission.Lock()
	if restoreAdmission.active >= l.Concurrency {
		restoreAdmission.Unlock()
		return ctx, nil, ErrRestoreBusy
	}
	restoreAdmission.active++
	restoreAdmission.Unlock()
	ctx, cancel := context.WithTimeout(ctx, l.Timeout)
	b := &restoreBudget{limits: l}
	var once sync.Once
	return context.WithValue(ctx, restoreBudgetKey{}, b), func() {
		once.Do(func() { cancel(); restoreAdmission.Lock(); restoreAdmission.active--; restoreAdmission.Unlock() })
	}, nil
}
func readBudget(ctx context.Context) *restoreBudget {
	b, _ := ctx.Value(restoreBudgetKey{}).(*restoreBudget)
	return b
}
func (b *restoreBudget) charge(kind string, n int64) error {
	if b.parent != nil {
		if e := b.parent.charge(kind, n); e != nil {
			return e
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failure != nil {
		return b.failure
	}
	var used *int64
	var limit int64
	switch kind {
	case "decoded":
		used = &b.decoded
		limit = b.limits.DecodedBytes
	case "entries":
		used = &b.entries
		limit = b.limits.Entries
	case "generated":
		used = &b.generated
		limit = b.limits.GeneratedBytes
	case "buffered":
		used = &b.buffered
		limit = b.limits.BufferedBytes
	case "headers":
		used = &b.headers
		limit = b.limits.HeaderBytes
	case "sinks":
		used = &b.sinks
		limit = b.limits.SectionSinks
	default:
		panic("unknown budget counter")
	}
	if n < 0 || n > limit-*used {
		b.failure = fmt.Errorf("%w: %s allowance %d bytes/entries", ErrRestoreLimit, kind, limit)
		return b.failure
	}
	*used += n
	return nil
}
func (b *restoreBudget) err() error { b.mu.Lock(); defer b.mu.Unlock(); return b.failure }

type restoreReadGuard struct {
	ctx       context.Context
	r         io.Reader
	b         *restoreBudget
	decoded   bool
	generated bool
	buffered  bool
	read, max int64
}

func (r *restoreReadGuard) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	if r.b != nil {
		if e := r.b.err(); e != nil {
			return 0, e
		}
	}
	if r.max > 0 {
		remaining := r.max - r.read
		if remaining < 0 {
			return 0, ErrRestoreLimit
		}
		if int64(len(p)) > remaining+1 {
			p = p[:remaining+1]
		}
	}
	n, e := r.r.Read(p)
	r.read += int64(n)
	if r.max > 0 && r.read > r.max {
		e := fmt.Errorf("%w: compressed source allowance %d", ErrRestoreLimit, r.max)
		if r.b != nil {
			r.b.mu.Lock()
			r.b.failure = e
			r.b.mu.Unlock()
		}
		return 0, e
	}
	if r.decoded && r.b != nil {
		if chargeErr := r.b.charge("decoded", int64(n)); chargeErr != nil {
			return 0, chargeErr
		}
	}
	if r.buffered && r.b != nil {
		if err := r.b.charge("buffered", int64(n)); err != nil {
			return 0, err
		}
	}
	if r.generated && r.b != nil {
		if err := r.b.charge("generated", int64(n)); err != nil {
			return 0, err
		}
	}
	if e == nil {
		e = r.ctx.Err()
	}
	return n, e
}

type restoreWriteGuard struct {
	ctx context.Context
	w   io.Writer
	b   *restoreBudget
}

func (w restoreWriteGuard) Write(p []byte) (int, error) {
	if e := w.ctx.Err(); e != nil {
		return 0, e
	}
	if w.b != nil {
		if e := w.b.charge("generated", int64(len(p))); e != nil {
			return 0, e
		}
	}
	return w.w.Write(p)
}
func resourceFailure(err error) bool {
	return errors.Is(err, zstd.ErrWindowSizeExceeded) || errors.Is(err, zstd.ErrDecoderSizeExceeded) || errors.Is(err, ErrRestoreLimit) || errors.Is(err, ErrRestoreBusy) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// RestoreWriter charges every attempted generated byte, including rewrites.
// It intentionally does not refund removed files: this is a work/output budget.
func RestoreWriter(ctx context.Context, w io.Writer) io.Writer {
	return restoreWriteGuard{ctx, w, readBudget(ctx)}
}

// RestoreBufferedReader limits aggregate source bytes retained/parsing in RAM.
// This is deliberately monotonic across passes, not a claimed heap/RSS bound.
func RestoreBufferedReader(ctx context.Context, r io.Reader) io.Reader {
	return &restoreReadGuard{ctx: ctx, r: r, b: readBudget(ctx), buffered: true}
}

func reserveRestoreGenerated(ctx context.Context, n int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b := readBudget(ctx); b != nil {
		return b.charge("generated", n)
	}
	return nil
}

// Reader constructors are reusable library helpers, not top-level operations.
// They retain finite decoder/work bounds without taking process admission.
// Only Inspect/Verify/extract/restore/recover/land/convert take that slot.
func ensureReadBudget(ctx context.Context) (context.Context, func(), error) {
	if readBudget(ctx) != nil {
		return ctx, func() {}, nil
	}
	limits, err := RestoreLimitsFromEnv()
	if err != nil {
		return ctx, nil, err
	}
	bounded, cancel := context.WithTimeout(ctx, limits.Timeout)
	return context.WithValue(bounded, restoreBudgetKey{}, &restoreBudget{limits: limits}), cancel, nil
}
func restoreOperationError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for b := readBudget(ctx); b != nil; b = b.parent {
		if err := b.err(); err != nil {
			return err
		}
	}
	return nil
}
func markRestoreFailure(ctx context.Context, err error) {
	if !resourceFailure(err) {
		return
	}
	for b := readBudget(ctx); b != nil; b = b.parent {
		b.mu.Lock()
		if b.failure == nil {
			b.failure = err
		}
		b.mu.Unlock()
	}
}
