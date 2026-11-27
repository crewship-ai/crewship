package craftsession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/craftengine"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// ErrBusy means every session slot is in use and none can be given up.
var ErrBusy = errors.New("craftsession: all sessions are busy")

// Launcher starts an engine worker that sees dir as its working directory and
// runs engineArgs (for example "mcp", "--in", "/work/doc.json").
type Launcher func(ctx context.Context, dir string, engineArgs []string) (*craftengine.Worker, error)

// Config bounds a Manager.
type Config struct {
	Launch Launcher
	// TempRoot holds one private directory per open document ("" = os.TempDir).
	TempRoot string
	// MaxSessions caps open documents at once (default 2). A document held
	// open costs RSS: about 0.4 GiB for 200 000 cells, 2.3 GiB measured for a
	// real 235 000 cell workbook with 95 000 formulas, more while it serialises.
	MaxSessions int
	// IdleTTL closes a session nobody used (default 10 min).
	IdleTTL time.Duration
	// InputPolicy gates every cell the sessions write (default: plain values
	// only, no formulas).
	InputPolicy InputPolicy
	// MaxSnapshots caps snapshots running at once (default 1): the engine's
	// memory peaks to several times the held size while it serialises.
	MaxSnapshots int
	// OpenTimeout bounds loading a document into the engine (default 3 min).
	OpenTimeout time.Duration
	// OpenSavedValues opens a document that this server's engine wrote after recalculating
	// (see computedDocuments) with the values it holds instead of recalculating it (engine
	// option mcp --saved-values; off for an engine without it).
	OpenSavedValues bool
}

// Manager keeps at most MaxSessions documents open, one session per key.
type Manager struct {
	cfg  Config
	mu   sync.Mutex
	open map[string]*entry
	// computed holds the SHA-256 of documents this manager's engine wrote with every value
	// recalculated (oldest first, at most maxComputed); only those open with saved values.
	computed []([32]byte)
	snap     chan struct{}
	stop     chan struct{}
	wg       sync.WaitGroup
}

type entry struct {
	key   string
	ready chan struct{} // closed when opening ended
	sess  *Session
	err   error
	refs  int
}

// NewManager starts the idle reaper.
func NewManager(cfg Config) *Manager {
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = 2
	}
	if cfg.IdleTTL <= 0 {
		cfg.IdleTTL = 10 * time.Minute
	}
	if cfg.OpenTimeout <= 0 {
		cfg.OpenTimeout = 3 * time.Minute
	}
	if cfg.TempRoot == "" {
		cfg.TempRoot = os.TempDir()
	}
	if cfg.MaxSnapshots <= 0 {
		cfg.MaxSnapshots = 1
	}
	m := &Manager{cfg: cfg, open: map[string]*entry{}, snap: make(chan struct{}, cfg.MaxSnapshots), stop: make(chan struct{})}
	m.wg.Add(1)
	go m.reap()
	return m
}

// Acquire returns the open session for key, opening it from load() when there
// is none or the engine died. The caller must call release when done; a session
// in use is never closed to make room. load returns the last SAVED version of
// the document, so a lost session never loses anything saved.
func (m *Manager) Acquire(ctx context.Context, key string, load func() ([]byte, error)) (s *Session, release func(), err error) {
	for {
		m.mu.Lock()
		e := m.open[key]
		if e != nil {
			select {
			case <-e.ready:
				if e.err == nil && e.sess.Alive() {
					e.refs++
					m.mu.Unlock()
					return e.sess, m.releaser(e), nil
				}
				delete(m.open, key) // failed or dead: open again below
				m.mu.Unlock()
				continue
			default:
				// somebody else is opening it: wait, then re-check
				m.mu.Unlock()
				select {
				case <-e.ready:
					continue
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				}
			}
		}
		if len(m.open) >= m.cfg.MaxSessions && !m.evictOneLocked() {
			m.mu.Unlock()
			return nil, nil, ErrBusy
		}
		e = &entry{key: key, ready: make(chan struct{}), refs: 1}
		m.open[key] = e
		m.mu.Unlock()

		sess, openErr := m.openSession(ctx, key, load)
		m.mu.Lock()
		e.sess, e.err = sess, openErr
		if openErr != nil {
			delete(m.open, key)
		}
		close(e.ready)
		m.mu.Unlock()
		if openErr != nil {
			return nil, nil, openErr
		}
		return sess, m.releaser(e), nil
	}
}

func (m *Manager) releaser(e *entry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			e.refs--
			m.mu.Unlock()
		})
	}
}

// evictOneLocked closes the least recently used session nobody holds.
func (m *Manager) evictOneLocked() bool {
	var victim *entry
	for _, e := range m.open {
		select {
		case <-e.ready:
		default:
			continue
		}
		if e.refs > 0 || e.err != nil || e.sess == nil {
			continue
		}
		if victim == nil || e.sess.LastUsed().Before(victim.sess.LastUsed()) {
			victim = e
		}
	}
	if victim == nil {
		return false
	}
	delete(m.open, victim.key)
	go victim.sess.Close()
	return true
}

func (m *Manager) openSession(ctx context.Context, key string, load func() ([]byte, error)) (*Session, error) {
	doc, err := load()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(m.cfg.TempRoot, "craft-session-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	// A Sheet kept as its workbook file opens as that file; anything else is the native document.
	format, name := "", "doc.json"
	if f, ferr := mooring.DetectWorkbook(doc); ferr == nil && (f == "xlsx" || f == "xlsm") {
		format, name = f, "doc."+f
	}
	if err = os.WriteFile(filepath.Join(dir, name), doc, 0o600); err != nil {
		cleanup()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, m.cfg.OpenTimeout)
	defer cancel()
	args := []string{"mcp", "--in", craftengine.GuestRoot + "/" + name}
	saved := format == "" && m.cfg.OpenSavedValues && m.isComputed(doc)
	if saved {
		args = append(args, "--saved-values")
	}
	sum := sha256.Sum256(doc)
	m.mu.Lock()
	known := len(m.computed)
	m.mu.Unlock()
	slog.Default().Info("Sheet session opens", "doc_sha256", hex.EncodeToString(sum[:4]), "bytes", len(doc), "saved_values", saved, "option", m.cfg.OpenSavedValues, "computed_known", known)
	w, err := m.cfg.Launch(ctx, dir, args)
	if err != nil {
		cleanup()
		return nil, err
	}
	// The engine opens the document while answering the first request.
	if _, err = w.Call(ctx, "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "crewship", "version": "1"}}); err != nil {
		w.Kill()
		_ = w.Close()
		cleanup()
		return nil, ErrLost
	}
	var sess *Session
	sess = newSession(w, func() {
		cleanup()
		m.forget(sess.Key())
	}, m.cfg.InputPolicy, m.snap)
	sess.format = format
	if format == "" {
		sess.computed = m.Computed
	}
	sess.rekey(key)
	return sess, nil
}

// Rekey renames the open session oldKey to newKey, for a caller that keys
// sessions by the content of the saved version: after a snapshot has been
// stored as a new version, the same warm engine serves the next call. It
// returns false (and changes nothing) when oldKey is not open and idle-safe,
// or newKey is already taken.
func (m *Manager) Rekey(oldKey, newKey string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.open[oldKey]
	if e == nil || m.open[newKey] != nil || oldKey == newKey {
		return false
	}
	select {
	case <-e.ready:
	default:
		return false
	}
	if e.err != nil || e.sess == nil || !e.sess.Alive() {
		return false
	}
	delete(m.open, oldKey)
	e.key = newKey
	m.open[newKey] = e
	e.sess.rekey(newKey)
	return true
}

// forget drops a closed session from the table.
func (m *Manager) forget(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.open[key]; e != nil && e.sess != nil && !e.sess.Alive() {
		delete(m.open, key)
	}
}

func (m *Manager) reap() {
	defer m.wg.Done()
	tick := time.NewTicker(m.cfg.IdleTTL / 4)
	defer tick.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-tick.C:
			m.mu.Lock()
			var idle []*Session
			for k, e := range m.open {
				select {
				case <-e.ready:
				default:
					continue
				}
				if e.err == nil && e.sess != nil && (!e.sess.Alive() || (e.refs == 0 && time.Since(e.sess.LastUsed()) > m.cfg.IdleTTL)) {
					idle = append(idle, e.sess)
					delete(m.open, k)
				}
			}
			m.mu.Unlock()
			for _, s := range idle {
				s.Close()
			}
		}
	}
}

// Len is the number of open (or opening) sessions.
func (m *Manager) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.open)
}

// Close ends every session.
func (m *Manager) Close() {
	close(m.stop)
	m.wg.Wait()
	m.mu.Lock()
	var all []*Session
	for k, e := range m.open {
		if e.sess != nil {
			all = append(all, e.sess)
		}
		delete(m.open, k)
	}
	m.mu.Unlock()
	for _, s := range all {
		s.Close()
	}
}

// maxComputed bounds the documents a manager remembers as computed by its own engine.
const maxComputed = 256

// Computed records doc as written by this server's engine with every value recalculated: an
// import (the engine recalculated the workbook when it opened it) or a snapshot taken in
// automatic calculation. A later open of exactly these bytes keeps their values (OpenSavedValues)
// instead of recalculating them. What a restart forgets is recalculated again.
func (m *Manager) Computed(doc []byte) {
	sum := sha256.Sum256(doc)
	slog.Default().Info("Sheet document recorded as computed", "doc_sha256", hex.EncodeToString(sum[:4]), "bytes", len(doc), "option", m.cfg.OpenSavedValues)
	if !m.cfg.OpenSavedValues {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if slices.Contains(m.computed, sum) {
		return
	}
	if len(m.computed) >= maxComputed {
		m.computed = m.computed[1:]
	}
	m.computed = append(m.computed, sum)
}

// IsComputed reports whether doc is exactly a document this manager recorded as computed by its
// engine (Computed), e.g. for the validator to skip recalculating it (NativeCLIValidator.Trusted).
func (m *Manager) IsComputed(doc []byte) bool { return m.cfg.OpenSavedValues && m.isComputed(doc) }

func (m *Manager) isComputed(doc []byte) bool {
	sum := sha256.Sum256(doc)
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Contains(m.computed, sum)
}
