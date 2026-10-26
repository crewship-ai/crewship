package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Supplied only by release wiring with verified, immutable native artifacts.
// A request cannot select executable paths, engine pins or a runtime identity.
type commandProductPort interface {
	collaborationCommandPort
	InitialSnapshot(context.Context, string, string) (commandSQLSnapshot, error)
	Projection(context.Context) (commandProductProjection, error)
	Close()
}

type commandProductProjection struct {
	Generation string   `json:"generation"`
	Revision   uint64   `json:"revision"`
	IDs        []string `json:"ids"`
	NativeJSON string   `json:"native_json"`
}

// The context bounds startup, not process lifetime (Close owns that lifetime).
type commandProductStart func(open context.Context, runtime string) (commandProductPort, error)

// This registry is separate from the legacy adapter: a C1 failure never opens
// the same document with the old runtime. Enablement is deliberately explicit.
type commandProductFactory struct {
	manager      *collaborationHTTPManager
	enabled      bool
	start        commandProductStart
	editorBundle func(*http.Request) (digest, indexURL string)
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	closed       bool
	docs         map[mooring.SessionKey]*commandProductDocument
	views        map[string]commandProductView
}
type commandProductDocument struct {
	ready  chan struct{}
	err    error
	store  *collaborationCommandStore
	port   commandProductPort
	owner  mooring.OwnerLease
	cancel context.CancelFunc
	once   sync.Once
}

func newCommandProductFactory(enabled bool, manager *collaborationHTTPManager, start commandProductStart) (*commandProductFactory, error) {
	if !enabled || manager == nil || manager.ctx == nil || manager.pages == nil || manager.owners == nil || manager.fence == nil || manager.clock == nil || start == nil {
		return nil, errCommandStore
	}
	ctx, cancel := context.WithCancel(manager.ctx)
	return &commandProductFactory{manager: manager, enabled: true, start: start, ctx: ctx, cancel: cancel, docs: make(map[mooring.SessionKey]*commandProductDocument), views: make(map[string]commandProductView)}, nil
}

// Open includes initialization AND canonical restore in one five-second budget.
// It grants no HTTP delivery authority. The transport must audit private-owner
// access and recheck the original participant at each physical content send.
func (f *commandProductFactory) Open(request *http.Request, create bool) (*commandProductDocument, error) {
	if f == nil || !f.enabled || request == nil {
		return nil, errCommandStore
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(f.ctx, cancel)
	defer stop()
	if f.ctx.Err() != nil {
		return nil, errCommandStore
	}
	r := request.WithContext(ctx)
	key, _, err := f.manager.scope(r)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil, errCommandStore
	}
	if d := f.docs[key]; d != nil {
		f.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.ready:
			f.mu.Lock()
			stopped := f.closed || f.ctx.Err() != nil || ctx.Err() != nil
			f.mu.Unlock()
			if stopped {
				return nil, errCommandStore
			}
			if d.err != nil {
				return nil, d.err
			}
			select {
			case <-d.port.Done():
				return nil, errCommandStore
			default:
			}
			// scope checked this request's current original rights above. The
			// transport still needs its own admission; merely opening a cached
			// runtime must not consume a new participant slot.
			return d, nil
		}
	}
	if len(f.docs) >= collaborationHTTPDocLimit {
		f.mu.Unlock()
		return nil, errCommandStore
	}
	d := &commandProductDocument{ready: make(chan struct{})}
	f.docs[key] = d
	f.mu.Unlock()
	d.err = f.initialize(ctx, r, key, create, d)
	f.mu.Lock()
	if d.err == nil && (f.closed || f.ctx.Err() != nil || ctx.Err() != nil) {
		d.err = errCommandStore
		d.retire(f.manager.owners)
	}
	f.mu.Unlock()
	close(d.ready)
	if d.err != nil {
		return nil, d.err
	}
	return d, nil
}

func (f *commandProductFactory) initialize(ctx context.Context, r *http.Request, key mooring.SessionKey, create bool, d *commandProductDocument) (err error) {
	ref, err := collaborationOpaqueID()
	if err != nil {
		return err
	}
	d.owner, err = f.manager.owners.Acquire(ctx, key, ref)
	if err != nil {
		return err
	}
	lifetime, cancel := context.WithCancel(f.ctx)
	d.cancel = cancel
	defer func() {
		if err != nil {
			// Retiring a failed process must not give an exhausted Open a
			// fresh cleanup budget. Keep the failed entry unavailable while
			// tracked cleanup stops it before releasing ownership.
			d.retire(f.manager.owners)
		}
	}()
	d.port, err = f.start(ctx, ref)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if f.ctx.Err() != nil {
		return errCommandStore
	}
	d.store, err = newCollaborationCommandStore(true, f.manager.pages, f.manager.owners, f.manager.fence, f.manager.clock, d.owner, d.port)
	if err != nil {
		return err
	}
	if create {
		generation, e := collaborationOpaqueID()
		if e != nil {
			return e
		}
		initial, e := d.port.InitialSnapshot(ctx, key.ArtifactID, generation)
		if e != nil {
			return e
		}
		if e = d.store.Create(r, initial); e != nil {
			return e
		}
	} else {
		admission, e := d.store.Admit(ctx, false)
		if e != nil {
			return e
		}
		confirmed, e := d.store.ReadCheckpoint(r, admission)
		if e != nil {
			return e
		}
		if e = d.port.RestoreConfirmed(ctx, confirmed); e != nil {
			return e
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Existing owner duration is unchanged. Failure retires this runtime; it
	// never grants a fresh participant identity or retries an uncertain write.
	done := beginBackgroundWork()
	go func() {
		defer done()
		defer d.close(f.manager.owners)
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-lifetime.Done():
				return
			case <-d.port.Done():
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(lifetime, 5*time.Second)
				_, err := f.manager.owners.Renew(ctx, d.owner)
				cancel()
				if err != nil {
					return
				}
			}
		}
	}()
	return nil
}

func (d *commandProductDocument) close(owners *mooring.OwnerStore) {
	d.once.Do(func() {
		if d.cancel != nil {
			d.cancel()
		}
		if d.port != nil {
			d.port.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = owners.Release(ctx, d.owner)
	})
}

func (d *commandProductDocument) retire(owners *mooring.OwnerStore) {
	done := beginBackgroundWork()
	go func() { defer done(); d.close(owners) }()
}

func (f *commandProductFactory) Close() {
	f.mu.Lock()
	f.closed = true
	f.cancel()
	docs := make([]*commandProductDocument, 0, len(f.docs))
	for _, d := range f.docs {
		docs = append(docs, d)
	}
	f.mu.Unlock()
	for _, d := range docs {
		<-d.ready
		d.close(f.manager.owners)
	}
}
