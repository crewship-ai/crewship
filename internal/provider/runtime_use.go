package provider

import (
	"context"
	"errors"
	"sync"
)

var ErrRuntimeUseReleased = errors.New("runtime use reservation has been released")
var ErrRuntimeUseBusy = errors.New("runtime admission is waiting for an exclusive lifecycle change")

type runtimeUseNoWaitKey struct{}

// WithRuntimeUseNoWait prevents a synchronous child from waiting behind an
// activation that needs its parent to finish. It does not bypass the gate.
func WithRuntimeUseNoWait(ctx context.Context) context.Context {
	return context.WithValue(ctx, runtimeUseNoWaitKey{}, true)
}

func RuntimeUseNoWait(ctx context.Context) bool {
	noWait, _ := ctx.Value(runtimeUseNoWaitKey{}).(bool)
	return noWait
}

// RuntimeUseGate separates concurrent users from exclusive lifecycle changes.
// Its zero value is usable. A queued writer closes admission to new users;
// cancellation releases the queue position. This is resource lifetime, not an
// authorization grant or proof that a recovered runtime is empty.
type RuntimeUseGate struct {
	mu             sync.Mutex
	readers        int
	writer         bool
	waitingWriters int
	changed        chan struct{}
}

func (g *RuntimeUseGate) signalLocked() <-chan struct{} {
	if g.changed == nil {
		g.changed = make(chan struct{})
	}
	return g.changed
}
func (g *RuntimeUseGate) notifyLocked() {
	if g.changed != nil {
		close(g.changed)
	}
	g.changed = make(chan struct{})
}

func (g *RuntimeUseGate) Use(ctx context.Context) (func(), error) {
	g.mu.Lock()
	for {
		if err := ctx.Err(); err != nil {
			g.mu.Unlock()
			return nil, err
		}
		if !g.writer && g.waitingWriters == 0 {
			g.readers++
			g.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { g.mu.Lock(); g.readers--; g.notifyLocked(); g.mu.Unlock() }) }, nil
		}
		if RuntimeUseNoWait(ctx) {
			g.mu.Unlock()
			return nil, ErrRuntimeUseBusy
		}
		changed := g.signalLocked()
		g.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		g.mu.Lock()
	}
}

func (g *RuntimeUseGate) Exclusive(ctx context.Context) (func(), error) {
	g.mu.Lock()
	g.waitingWriters++
	g.notifyLocked()
	for {
		if err := ctx.Err(); err != nil {
			g.waitingWriters--
			g.notifyLocked()
			g.mu.Unlock()
			return nil, err
		}
		if !g.writer && g.readers == 0 {
			g.waitingWriters--
			g.writer = true
			g.mu.Unlock()
			return g.releaseWriter(), nil
		}
		changed := g.signalLocked()
		g.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		g.mu.Lock()
	}
}

// TryExclusive is for optional idle work, which must not jump an activation
// already queued or wait for live users to complete.
func (g *RuntimeUseGate) TryExclusive() (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.writer || g.readers != 0 || g.waitingWriters != 0 {
		return nil, false
	}
	g.writer = true
	return g.releaseWriter(), true
}
func (g *RuntimeUseGate) releaseWriter() func() {
	var once sync.Once
	return func() { once.Do(func() { g.mu.Lock(); g.writer = false; g.notifyLocked(); g.mu.Unlock() }) }
}

type runtimeUseRefs struct {
	mu                  sync.Mutex
	refs                int
	crewID, containerID string
	release             func()
}

// RuntimeUse pins one concrete runtime. Each retained handle is independently
// releasable; a detached invocation can outlive its caller without unlocking
// the runtime. Never copy a handle by value. It does not authorize execution.
type RuntimeUse struct {
	mu       sync.Mutex
	released bool
	state    *runtimeUseRefs
}

func NewRuntimeUse(crewID, containerID string, release func()) *RuntimeUse {
	return &RuntimeUse{state: &runtimeUseRefs{refs: 1, crewID: crewID, containerID: containerID, release: release}}
}
func (u *RuntimeUse) ContainerID() string {
	if u == nil || u.state == nil {
		return ""
	}
	return u.state.containerID
}
func (u *RuntimeUse) Matches(crewID, containerID string) bool {
	if u == nil {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return !u.released && u.state != nil && crewID != "" && containerID != "" && u.state.crewID == crewID && u.state.containerID == containerID
}
func (u *RuntimeUse) Retain() (*RuntimeUse, error) {
	if u == nil {
		return nil, ErrRuntimeUseReleased
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.released || u.state == nil {
		return nil, ErrRuntimeUseReleased
	}
	u.state.mu.Lock()
	u.state.refs++
	u.state.mu.Unlock()
	return &RuntimeUse{state: u.state}, nil
}
func (u *RuntimeUse) Release() {
	if u == nil {
		return
	}
	u.mu.Lock()
	if u.released || u.state == nil {
		u.mu.Unlock()
		return
	}
	u.released = true
	state := u.state
	state.mu.Lock()
	state.refs--
	last := state.refs == 0
	state.mu.Unlock()
	u.mu.Unlock()
	if last && state.release != nil {
		state.release()
	}
}

// CrewRuntimeUseProvider supports runtime acquisition whose lifetime spans
// preparation and execution. A bare EnsureCrewRuntime call supplies no such
// reservation and must not authorize automatic replacement of active work.
type CrewRuntimeUseProvider interface {
	AcquireCrewRuntimeUse(context.Context, CrewConfig) (*RuntimeUse, error)
	RetainCrewRuntimeUse(context.Context, string, string) (*RuntimeUse, error)
}

// CrewIdleStopper atomically excludes new runtime users while checking/stopping
// an idle container. Explicit operator force-stop is a separate operation.
type CrewIdleStopper interface {
	StopUnusedCrewRuntime(context.Context, string, string) (bool, error)
}
