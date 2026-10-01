package offsite

import (
	"context"
	"io"
	"sync"
	"time"
)

// Clock is the time source the rate limiter (and tests) use. SystemClock is
// the production implementation; tests pass a fake whose Sleep advances Now
// instantly so throughput bounds are asserted without waiting.
type Clock interface {
	Now() time.Time
	// Sleep blocks for d or until ctx is done, returning ctx.Err() in the
	// latter case.
	Sleep(ctx context.Context, d time.Duration) error
}

// SystemClock is the wall clock.
type SystemClock struct{}

// Now returns time.Now().
func (SystemClock) Now() time.Time { return time.Now() }

// Sleep waits for d or ctx, whichever ends first.
func (SystemClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Limiter is a token bucket measured in bytes. Tokens refill continuously at
// the configured rate up to a burst of one second's worth (at least
// minBurstBytes), so a sustained transfer is bounded by bytesPerSecond while
// short reads are not stalled needlessly.
//
// One Limiter may be shared by several readers (e.g. every upload of one
// backup plan) — the bucket is the cap, not the reader. Safe for concurrent
// use.
type Limiter struct {
	mu     sync.Mutex
	clock  Clock
	rate   float64 // bytes per second
	burst  float64 // bucket capacity in bytes
	tokens float64
	last   time.Time
}

// minBurstBytes keeps a very low limit from degrading into one syscall per
// byte: the bucket always holds at least one 32 KiB read.
const minBurstBytes = 32 << 10

// NewLimiter returns a limiter capped at bytesPerSecond, or nil when the cap
// is zero or negative (unlimited). A nil *Limiter is valid and never waits,
// so callers can pass the result straight through. clock nil → SystemClock.
func NewLimiter(bytesPerSecond int64, clock Clock) *Limiter {
	if bytesPerSecond <= 0 {
		return nil
	}
	if clock == nil {
		clock = SystemClock{}
	}
	burst := float64(bytesPerSecond)
	if burst < minBurstBytes {
		burst = minBurstBytes
	}
	return &Limiter{
		clock:  clock,
		rate:   float64(bytesPerSecond),
		burst:  burst,
		tokens: burst,
		last:   clock.Now(),
	}
}

// Burst is the bucket capacity in bytes — the largest single WaitN that is
// satisfied without splitting. Zero for a nil limiter.
func (l *Limiter) Burst() int {
	if l == nil {
		return 0
	}
	return int(l.burst)
}

// WaitN blocks until n bytes may pass. Requests larger than the burst are
// satisfied in burst-sized slices. Returns ctx.Err() if ctx ends first; the
// tokens already taken for earlier slices are not refunded.
func (l *Limiter) WaitN(ctx context.Context, n int) error {
	if l == nil || n <= 0 {
		return ctx.Err()
	}
	remaining := float64(n)
	for remaining > 0 {
		take := remaining
		if take > l.burst {
			take = l.burst
		}
		if err := l.take(ctx, take); err != nil {
			return err
		}
		remaining -= take
	}
	return nil
}

func (l *Limiter) take(ctx context.Context, n float64) error {
	for {
		l.mu.Lock()
		now := l.clock.Now()
		if elapsed := now.Sub(l.last).Seconds(); elapsed > 0 {
			l.tokens += elapsed * l.rate
			if l.tokens > l.burst {
				l.tokens = l.burst
			}
		}
		l.last = now
		if l.tokens >= n {
			l.tokens -= n
			l.mu.Unlock()
			return nil
		}
		wait := time.Duration((n - l.tokens) / l.rate * float64(time.Second))
		l.mu.Unlock()
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		if err := l.clock.Sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// rateLimitedReader throttles an io.Reader through a Limiter. Each Read is
// capped at the limiter's burst so a large buffer cannot overdraw the bucket
// in one call, and the tokens are taken for the bytes actually read.
type rateLimitedReader struct {
	ctx context.Context
	r   io.Reader
	l   *Limiter
}

// NewRateLimitedReader wraps r so reads proceed at most at l's rate. A nil
// limiter returns r unchanged. ctx cancels a read that is waiting for tokens.
func NewRateLimitedReader(ctx context.Context, r io.Reader, l *Limiter) io.Reader {
	if l == nil {
		return r
	}
	return &rateLimitedReader{ctx: ctx, r: r, l: l}
}

func (r *rateLimitedReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if b := r.l.Burst(); len(p) > b {
		p = p[:b]
	}
	n, err := r.r.Read(p)
	if n > 0 {
		if werr := r.l.WaitN(r.ctx, n); werr != nil {
			return n, werr
		}
	}
	return n, err
}
