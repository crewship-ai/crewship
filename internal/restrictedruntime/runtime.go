//go:build linux

package restrictedruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// Manager is a host-side watchdog. The state directory must be private,
// durable server storage, never mounted into a runtime. One owner per directory.
type Manager struct {
	Docker     Docker
	Authority  Authority
	Catalog    Catalog
	Limits     Limits
	dir, owner string
	lock       *os.File
	mu         sync.Mutex
	sessions   map[string]*Session
	reconciled bool
}
type Record struct {
	Attempt, Container, Fingerprint, Status, Reason string
	Expires, Updated                                time.Time
}

type Session struct {
	manager    *Manager
	plan       Plan
	handle, id string
	mu         sync.Mutex
	record     Record
	output     bytes.Buffer
	truncated  bool
	secrets    []string
	done       chan struct{}
	once       sync.Once
}

func New(dir string, d Docker, a Authority, c Catalog, l Limits) (*Manager, error) {
	if a == nil || c == nil || !l.valid() {
		return nil, ErrDenied
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	st, e := os.Lstat(dir)
	if e != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, ErrDenied
	}
	f, e := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("restricted runtime owner already active")
	}
	fail := func(err error) (*Manager, error) { f.Close(); return nil, err }
	ownerFile := filepath.Join(dir, "owner")
	b, e := os.ReadFile(ownerFile)
	if os.IsNotExist(e) {
		var r [8]byte
		if _, e = rand.Read(r[:]); e != nil {
			return fail(e)
		}
		b = []byte(hex.EncodeToString(r[:]))
		if e = os.WriteFile(ownerFile, b, 0600); e != nil {
			return fail(e)
		}
	} else if e != nil {
		return fail(e)
	}
	if !identifier.Match(b) {
		return fail(ErrDenied)
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return fail(e)
	}
	ready := true
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			ready = false
		}
	}
	return &Manager{reconciled: ready, Docker: d, Authority: a, Catalog: c, Limits: l, dir: dir, owner: string(b), lock: f, sessions: map[string]*Session{}}, nil
}
func (m *Manager) save(r *Record) error {
	r.Updated = time.Now().UTC()
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(m.dir, ".record-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(name, filepath.Join(m.dir, r.Attempt+".json")); e != nil {
		return e
	}
	dir, e := os.Open(m.dir)
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}

// Start never accepts a plan from its caller. Resolve is repeated after Docker
// provisioning and secret resolution; changed authority discards the container.
func (m *Manager) Start(ctx context.Context, handle string) (s *Session, err error) {
	p, err := resolve(ctx, m.Authority, handle, map[string]bool{})
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lock == nil || !m.reconciled {
		return nil, ErrDenied
	}
	for _, old := range m.sessions {
		if old.Record().Status == "termination_unconfirmed" {
			return nil, ErrDenied
		}
	}
	if _, e := os.Stat(filepath.Join(m.dir, p.Attempt+".json")); !os.IsNotExist(e) {
		return nil, errors.New("attempt already recorded; recover before a new attempt")
	}
	s = &Session{manager: m, plan: p, handle: handle, done: make(chan struct{}), record: Record{Attempt: p.Attempt, Fingerprint: p.fingerprint(), Status: "provisioning", Expires: p.Expires}}
	if err = m.save(&s.record); err != nil {
		return nil, err
	}
	failedSession := s
	defer func() {
		s := failedSession
		if err != nil {
			if s.id != "" {
				clean, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				if m.Docker.remove(clean, s.id) != nil {
					m.reconciled = false
				}
				cancel()
			} else {
				m.reconciled = false
			}
			s.record.Status = "admission_failed"
			if !m.reconciled {
				s.record.Status = "termination_unconfirmed"
			}
			_ = m.save(&s.record)
		}
	}()
	s.id, err = m.Docker.create(ctx, p, m.Catalog, m.Limits, m.owner)
	if err != nil {
		return nil, err
	}
	s.record.Container = s.id
	if err = m.save(&s.record); err != nil {
		return nil, err
	}
	if err = m.Docker.audit(ctx, s.id, p, m.Catalog, m.Limits); err != nil {
		return nil, err
	}
	if _, err = m.Docker.call(ctx, nil, "start", s.id); err != nil {
		return nil, err
	}
	values, err := m.Authority.Secrets(ctx, handle)
	if err != nil {
		return nil, ErrDenied
	}
	if len(values) != len(p.Credentials) {
		return nil, ErrDenied
	}
	launch := Bootstrap{Command: p.Command, Env: map[string]string{}, Files: map[string]string{}}
	size := 0
	for _, c := range p.Credentials {
		v, ok := values[c.ID]
		size += len(v)
		if !ok || v == "" || strings.ContainsRune(v, 0) || !utf8.ValidString(v) || size > 1<<20 {
			return nil, ErrDenied
		}
		if c.Env != "" {
			launch.Env[c.Env] = v
		}
		if c.File != "" {
			launch.Files[c.File] = v
		}
	}
	fresh, e := resolve(ctx, m.Authority, handle, map[string]bool{})
	if e != nil || fresh.fingerprint() != p.fingerprint() {
		return nil, ErrDenied
	}
	s.plan = fresh
	s.record.Expires = fresh.Expires
	s.secrets = secretValues(values)
	payload, e := json.Marshal(launch)
	if e != nil {
		return nil, e
	}
	cmd := m.Docker.command(context.Background(), "exec", "-i", "--user", "1001:1001", s.id, "/opt/crewship-runner", "launch")
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stdout = s
	cmd.Stderr = s
	s.record.Status = "running"
	if err = m.save(&s.record); err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, errors.New("restricted launch failed")
	}
	m.sessions[p.Attempt] = s
	go s.watch()
	go func() { _ = cmd.Wait(); s.Stop("process_exited") }()
	return s, nil
}

// Bootstrap is a private stdin protocol, not an HTTP request or durable config.
type Bootstrap struct {
	Command    []string
	Env, Files map[string]string
}

func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.output.Len()+len(p) <= 1<<20 {
		_, _ = s.output.Write(p)
	} else {
		s.truncated = true
		go s.Stop("output_budget_exceeded")
	}
	return len(p), nil
}
func (s *Session) ID() string            { return s.id }
func (s *Session) Done() <-chan struct{} { return s.done }
func (s *Session) Record() Record        { s.mu.Lock(); defer s.mu.Unlock(); return s.record }

// Output performs current authority verification; application stream/API audience
// checks are still required. Raw output is bounded and private to the host.
func (s *Session) Output(ctx context.Context) (string, error) {
	p, e := resolve(ctx, s.manager.Authority, s.handle, map[string]bool{})
	if e != nil || p.fingerprint() != s.plan.fingerprint() {
		return "", ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.truncated {
		return "", errors.New("restricted output budget exceeded")
	}
	out := s.output.String()
	for _, v := range s.secrets {
		out = strings.ReplaceAll(out, v, "[REDACTED]")
		// Withhold a suffix that could be a credential split across writes.
		for n := len(v) - 1; n > 0; n-- {
			if strings.HasSuffix(out, v[:n]) {
				out = out[:len(out)-n] + "[REDACTED_PARTIAL]"
				break
			}
		}
	}
	return out, nil
}

func (s *Session) watch() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(time.Until(s.plan.Expires))
	next := time.Now().Add(5 * time.Second)
	results := make(chan Plan, 1)
	inflight := false
	for {
		select {
		case <-s.done:
			return
		case p := <-results:
			inflight = false
			if p.fingerprint() != s.plan.fingerprint() || !p.Expires.After(time.Now()) {
				s.Stop("authority_changed")
				return
			}
			// Anchor every renewed deadline to its issued expiry, never receipt+TTL.
			deadline = time.Now().Add(time.Until(p.Expires))
			next = time.Now().Add(5 * time.Second)
			s.mu.Lock()
			s.record.Expires = p.Expires
			e := s.manager.save(&s.record)
			s.mu.Unlock()
			if e != nil {
				s.Stop("journal_unavailable")
				return
			}
		case <-ticker.C:
			if !time.Now().Before(deadline) {
				s.Stop("lease_expired")
				return
			}
			if !inflight && !time.Now().Before(next) {
				inflight = true
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					p, _ := resolve(ctx, s.manager.Authority, s.handle, map[string]bool{})
					select {
					case results <- p:
					case <-s.done:
					}
				}()
			}
		}
	}
}

// Stop kills the whole container, not a shell/tmux session. Failure remains
// durable and blocks recovery; it never reports successful revocation optimistically.
func (s *Session) Stop(reason string) {
	s.once.Do(func() {
		s.mu.Lock()
		s.record.Status = "draining"
		s.record.Reason = reason
		_ = s.manager.save(&s.record)
		s.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, e := s.manager.Docker.call(ctx, nil, "kill", s.id)
		stopped := s.manager.Docker.stopped(ctx, s.id)
		s.mu.Lock()
		s.record.Status = "termination_unconfirmed"
		if stopped {
			s.record.Status = "terminated"
		}
		if e != nil && !stopped {
			s.record.Reason = "docker_unavailable"
		}
		if s.manager.save(&s.record) != nil {
			s.record.Status = "termination_unconfirmed"
			s.record.Reason = "journal_unavailable"
		}
		s.mu.Unlock()
		close(s.done)
	})
}

// Reconcile fences all old attempts before fresh admission. It neither trusts
// old tokens nor resumes old processes. Current authority is required by Start.
func (m *Manager) Reconcile(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lock == nil {
		return ErrDenied
	}
	for _, s := range m.sessions {
		select {
		case <-s.Done():
		default:
			return errors.New("active runtime prevents recovery")
		}
	}
	entries, e := os.ReadDir(m.dir)
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, e := os.ReadFile(filepath.Join(m.dir, entry.Name()))
		if e != nil {
			return e
		}
		var r Record
		if json.Unmarshal(b, &r) != nil || !identifier.MatchString(r.Attempt) {
			return ErrDenied
		}
		if r.Container == "" { // A lost create response is located by the deterministic owned name.
			r.Container = "crewship-rtest-" + m.owner + "-" + r.Attempt
		}
		b, e = m.Docker.call(ctx, nil, "inspect", r.Container)
		if e != nil { // Absence must be established separately; transport failure is not absence.
			ids, le := m.Docker.call(ctx, nil, "ps", "-aq", "--filter", "name=^/crewship-rtest-"+m.owner+"-"+r.Attempt+"$")
			if le != nil || len(bytes.TrimSpace(ids)) != 0 {
				return errors.New("runtime existence unconfirmed")
			}
		} else {
			var v []struct {
				Config struct{ Labels map[string]string }
			}
			if json.Unmarshal(b, &v) != nil || len(v) != 1 || v[0].Config.Labels[labelPrefix+"owner"] != m.owner || v[0].Config.Labels[labelPrefix+"attempt"] != r.Attempt || v[0].Config.Labels[labelPrefix+"plan"] != r.Fingerprint {
				return ErrDenied
			}
			if e = m.Docker.remove(ctx, r.Container); e != nil {
				return e
			}
		}
		r.Status = "terminated"
		r.Reason = "reconciled"
		if e = m.save(&r); e != nil {
			return e
		}
	}
	m.reconciled = true
	m.sessions = map[string]*Session{}
	return nil
}
func (m *Manager) Close() error {
	var result error
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		s.Stop("manager_closed")
		if s.Record().Status != "terminated" {
			result = errors.Join(result, errors.New("restricted runtime termination unconfirmed"))
		}
	}
	if m.lock != nil {
		e := m.lock.Close()
		m.lock = nil
		return errors.Join(result, e)
	}
	return result
}

var _ io.Writer = (*Session)(nil)
