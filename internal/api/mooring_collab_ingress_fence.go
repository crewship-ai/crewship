package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
)

var errCollaborationIngress = errors.New("collaboration ingress unavailable")

type collaborationIngressContextKey struct{}

// This stamp is host-only. Never serialize it or accept a client-supplied copy.
// A task/admission retains the ORIGINAL stamp for its entire lifetime.
type collaborationIngressStamp struct {
	fence       *collaborationIngressFence
	incarnation string
}

// The same fence is shared by human admission and task/agent authority. Reset
// is a lifecycle operation, not an acknowledgement of any SQL delivery gate.
// Callers must separately stop/drain content streams and preserve the durable
// delivery barrier before considering a restore complete.
type collaborationIngressFence struct {
	mu          sync.RWMutex
	incarnation string
	suspended   bool
}

func collaborationIngressIncarnation() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func newCollaborationIngressFence() (*collaborationIngressFence, error) {
	id, err := collaborationIngressIncarnation()
	if err != nil {
		return nil, err
	}
	return &collaborationIngressFence{incarnation: id}, nil
}

// Wrap MUST precede RequireAuth, body decoding and readiness waits. It stamps
// original ingress, so an authenticated request waiting across a restore cannot
// mint a new capability by adopting the restored database's current identity.
// A route must ALSO acquire a lease around its complete store operation or
// bounded content send. Merely stamping a long-lived WebSocket is insufficient.
func (f *collaborationIngressFence) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f == nil || next == nil {
			http.Error(w, "collaboration unavailable", http.StatusServiceUnavailable)
			return
		}
		f.mu.RLock()
		if f.suspended || f.incarnation == "" {
			f.mu.RUnlock()
			http.Error(w, "collaboration unavailable", http.StatusServiceUnavailable)
			return
		}
		stamp := collaborationIngressStamp{fence: f, incarnation: f.incarnation}
		f.mu.RUnlock()
		ctx := context.WithValue(r.Context(), collaborationIngressContextKey{}, stamp)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (f *collaborationIngressFence) capture(ctx context.Context) (collaborationIngressStamp, error) {
	if f == nil || ctx == nil {
		return collaborationIngressStamp{}, errCollaborationIngress
	}
	stamp, ok := ctx.Value(collaborationIngressContextKey{}).(collaborationIngressStamp)
	if !ok {
		return collaborationIngressStamp{}, errCollaborationIngress
	}
	release, err := f.acquireOriginal(stamp)
	if err != nil {
		return collaborationIngressStamp{}, err
	}
	release()
	return stamp, nil
}

// acquire requires BOTH fresh authenticated transport ingress and the immutable
// original capability. Keep its lease through the entire transaction/response
// decision; do not release after only an authority callback before SQL commit.
func (f *collaborationIngressFence) acquire(ctx context.Context, original collaborationIngressStamp) (func(), error) {
	if f == nil || ctx == nil {
		return nil, errCollaborationIngress
	}
	current, ok := ctx.Value(collaborationIngressContextKey{}).(collaborationIngressStamp)
	if !ok || current != original {
		return nil, errCollaborationIngress
	}
	return f.acquireOriginal(original)
}

// acquireOriginal is nonblocking: SQL authority callbacks must never wait on
// a restore fence while holding the SQL writer. Contention denies admission.
// acquireOriginal is for trusted dispatchers with an already sealed capability,
// never an HTTP body, bearer projection or manufactured authenticated context.
func (f *collaborationIngressFence) acquireOriginal(original collaborationIngressStamp) (func(), error) {
	if f == nil {
		return nil, errCollaborationIngress
	}
	if !f.mu.TryRLock() {
		return nil, errCollaborationIngress
	}
	if f.suspended || f.incarnation == "" || original.fence != f || original.incarnation != f.incarnation {
		f.mu.RUnlock()
		return nil, errCollaborationIngress
	}
	return f.mu.RUnlock, nil
}

// SuspendBeforeRestore drains bounded leases, invalidates all prior stamps and
// stays closed even if the later restore fails. Never call while holding a
// lease yourself. This method alone does not terminate long-lived transports.
func (f *collaborationIngressFence) SuspendBeforeRestore() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.suspended = true
	f.incarnation = ""
}

// ResumeAfterRestore is called ONLY after restore/drain/barrier completion.
// Entropy failure leaves admission closed. A fresh request can authenticate the
// restored database; this does not assert global nonrevival of HTTP credentials.
func (f *collaborationIngressFence) ResumeAfterRestore() error {
	if f == nil {
		return errCollaborationIngress
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.suspended {
		return errCollaborationIngress
	}
	id, err := collaborationIngressIncarnation()
	if err != nil {
		return err
	}
	f.incarnation = id
	f.suspended = false
	return nil
}
