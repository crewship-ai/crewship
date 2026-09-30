package offsite

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// fakeClock advances instantly on Sleep, so elapsed fake time is exactly the
// time a limiter made its caller wait.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	slept  time.Duration
	sleeps int
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.slept += d
	c.sleeps++
	return nil
}

func (c *fakeClock) elapsed() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.slept
}

func TestRateLimitedReaderBoundsThroughput(t *testing.T) {
	cases := []struct {
		name  string
		rate  int64
		total int
		buf   int
	}{
		{"1 MiB/s, 10 MiB, 256 KiB reads", 1 << 20, 10 << 20, 256 << 10},
		{"64 KiB/s, 1 MiB, 1 MiB reads (capped at burst)", 64 << 10, 1 << 20, 1 << 20},
		{"10 KiB/s floors burst at 32 KiB", 10 << 10, 200 << 10, 4 << 10},
		{"100 MB/s, 1 GB-ish", 100_000_000, 500_000_000, 4 << 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := newFakeClock()
			lim := NewLimiter(tc.rate, clk)
			r := NewRateLimitedReader(context.Background(), io.LimitReader(zeroReader{}, int64(tc.total)), lim)
			n, err := io.CopyBuffer(io.Discard, struct{ io.Reader }{r}, make([]byte, tc.buf))
			if err != nil || n != int64(tc.total) {
				t.Fatalf("copied %d, %v", n, err)
			}
			// The bucket starts full (one burst free), then refills at rate.
			burst := int64(lim.Burst())
			want := time.Duration(float64(int64(tc.total)-burst) / float64(tc.rate) * float64(time.Second))
			got := clk.elapsed()
			if got < want-10*time.Millisecond {
				t.Fatalf("elapsed %v < lower bound %v: throughput exceeded %d B/s", got, want, tc.rate)
			}
			if got > want+want/20+50*time.Millisecond {
				t.Fatalf("elapsed %v far above %v: limiter over-throttles", got, want)
			}
		})
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestLimiterSharedAcrossReaders(t *testing.T) {
	clk := newFakeClock()
	lim := NewLimiter(1<<20, clk)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		r := NewRateLimitedReader(ctx, io.LimitReader(zeroReader{}, 1<<20), lim)
		if _, err := io.Copy(io.Discard, r); err != nil {
			t.Fatal(err)
		}
	}
	// 4 MiB through one 1 MiB/s bucket: ~3 s after the free first burst.
	if got := clk.elapsed(); got < 3*time.Second-10*time.Millisecond {
		t.Fatalf("elapsed %v: the shared bucket was not shared", got)
	}
}

func TestLimiterNilAndZero(t *testing.T) {
	if NewLimiter(0, nil) != nil || NewLimiter(-5, nil) != nil {
		t.Fatal("non-positive rate must mean unlimited (nil)")
	}
	var l *Limiter
	if err := l.WaitN(context.Background(), 1<<30); err != nil {
		t.Fatal(err)
	}
	src := bytes.NewReader([]byte("x"))
	if NewRateLimitedReader(context.Background(), src, nil) != src {
		t.Fatal("nil limiter must return the reader unchanged")
	}
}

func TestLimiterContextCancel(t *testing.T) {
	lim := NewLimiter(1, SystemClock{}) // 1 B/s, burst 32 KiB
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	// Drain the burst, then ask for more than can refill before the deadline.
	if err := lim.WaitN(ctx, lim.Burst()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := lim.WaitN(ctx, 1000)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("WaitN ignored the context")
	}
	r := NewRateLimitedReader(ctx, bytes.NewReader(make([]byte, 10)), lim)
	if _, err := r.Read(make([]byte, 10)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Read after cancel = %v", err)
	}
}
