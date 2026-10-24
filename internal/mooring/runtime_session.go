package mooring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

const PrivateSessionProfile = "mooring.session-causal.v1"
const sessionControlCount = 128
const sessionControlBytes = 4 * 1024 * 1024

var ErrSessionRuntime = errors.New("invalid Mooring session transport")

type SessionRuntimeConfig struct {
	Runtime RuntimeConfig
	Scope   SessionKey
}
type sessionPending struct {
	kind, session               string
	expected                    string
	sent                        bool
	requestBytes, responseBytes int
	reply                       chan []byte
}
type sessionControlEntry struct {
	id                 uint64
	requestID, kind    string
	bytes              []byte
	borrowed, replying bool
}

// SessionControl is an opaque owned child-request/notification token. Its bytes
// convey no participant or authorship authority; the root host ledger must
// authenticate original ingress and final delivery independently.
type SessionControl struct {
	runtime *SessionRuntime
	id      uint64
}

// SessionRuntime alone owns the sole FD reader. Its underlying *Runtime and RPC
// are private: neither legacy Exchange nor a copied readiness body can be used
// to construct a session adapter or race this reader.
type SessionRuntime struct {
	process                    *Runtime
	scope                      SessionKey
	versions                   RuntimeVersions
	runtimeRef                 string
	validator                  *privateControlValidator
	mu                         sync.Mutex
	pending                    map[string]*sessionPending
	child                      map[uint64]*sessionControlEntry
	childIDs                   map[string]uint64
	queue                      []uint64
	next                       uint64
	count, bytes               int
	sessionID                  string
	observed                   sessionObserved
	openSent                   bool
	closed                     bool
	err                        error
	done, closedCh, readerDone chan struct{}
	notify                     chan struct{}
	writer                     chan struct{}
}

func StartSessionRuntime(ctx context.Context, c SessionRuntimeConfig) (*SessionRuntime, error) {
	if !c.Runtime.Enabled {
		return nil, ErrRuntimeDisabled
	}
	if c.Runtime.Binary == "" || c.Runtime.ControlProfile != PrivateSessionProfile || c.Scope.ApplicationID != "wordcraft" || !batchID(c.Scope.WorkspaceID) || !batchID(c.Scope.ArtifactID) || c.Runtime.Versions != (RuntimeVersions{"0.4.0", "0.2.0", "0.10.0"}) {
		return nil, ErrRuntimeHandshake
	}
	return startSessionRuntimeCommand(ctx, c, exec.CommandContext(ctx, c.Runtime.Binary, "--rpc-fd", "3"))
}
func newSessionRuntime(process *Runtime, c SessionRuntimeConfig) (*SessionRuntime, error) {
	if process == nil || process.RPC == nil || process.cmd == nil || process.cmd.Process == nil || process.selectedControlProfile != PrivateSessionProfile || process.selectedVersions != c.Runtime.Versions {
		return nil, ErrRuntimeHandshake
	}
	v, err := newPrivateControlValidator()
	if err != nil {
		return nil, err
	}
	ref, err := prepareRequestID()
	if err != nil {
		return nil, err
	}
	s := &SessionRuntime{process: process, scope: c.Scope, versions: c.Runtime.Versions, runtimeRef: string(ref), validator: v, pending: make(map[string]*sessionPending), child: make(map[uint64]*sessionControlEntry), childIDs: make(map[string]uint64), done: make(chan struct{}), closedCh: make(chan struct{}), readerDone: make(chan struct{}), notify: make(chan struct{}), writer: make(chan struct{}, 1)}
	s.writer <- struct{}{}
	go s.readLoop()
	go func() { <-process.Done(); s.fail(process.Err()); <-s.readerDone; close(s.done) }()
	return s, nil
}

// Open uses the one host-registry session identity. It waits while the caller
// services OwnerAcquire/Restore controls through the same multiplexer. A reply
// is transport-correlated; the lifecycle manager still verifies owner/basis.
func (s *SessionRuntime) Open(ctx context.Context, request protocol.SessionOpen) (protocol.SessionOpened, error) {
	var result protocol.SessionOpened
	raw, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	reply, err := s.request(ctx, raw, 5*time.Second)
	if err == nil {
		err = json.Unmarshal(reply, &result)
	}
	return result, err
}

// Restore preserves the exact authoritative checkpoint wrapper. No local
// candidate is installed or given a durability receipt by this transport API.
func (s *SessionRuntime) Restore(ctx context.Context, request protocol.SessionRestore) (protocol.SessionRestored, error) {
	var result protocol.SessionRestored
	raw, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	reply, err := s.request(ctx, raw, 5*time.Second)
	if err == nil {
		err = json.Unmarshal(reply, &result)
	}
	return result, err
}

func (s *SessionRuntime) Done() <-chan struct{} { return s.done }
func (s *SessionRuntime) RuntimeRef() string    { return s.runtimeRef }
func (s *SessionRuntime) Err() error            { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *SessionRuntime) Close() error {
	s.fail(ErrSessionRuntime)
	<-s.done
	return s.Err()
}
func (s *SessionRuntime) fail(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if err == nil {
		err = io.EOF
	}
	s.err = err
	close(s.closedCh)
	s.signalLocked()
	s.mu.Unlock()
	_ = s.process.RPC.Close()
	_ = s.process.cmd.Process.Kill()
}
func (s *SessionRuntime) signalLocked() { close(s.notify); s.notify = make(chan struct{}) }
func (s *SessionRuntime) reserveLocked(n int) bool {
	if n < 0 || s.count >= sessionControlCount || n > sessionControlBytes-s.bytes {
		return false
	}
	s.count++
	s.bytes += n
	return true
}
func (s *SessionRuntime) removePending(id string, p *sessionPending) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending[id] == p {
		delete(s.pending, id)
		s.count--
		s.bytes -= p.requestBytes + p.responseBytes
		if p.kind == "session_open" && !p.sent && s.sessionID == p.session {
			s.sessionID = ""
		}
	}
}

// Only closed host request purposes enter this correlated lane. Caller-provided
// JSON cannot choose arbitrary reply types or a separate stream reader.
func sessionExpected(kind string) string {
	switch kind {
	case "validate_checkpoint_causal":
		return "checkpoint_validated_causal"
	case "prepare_atomic_causal":
		return "atomic_prepared_causal"
	case "session_open":
		return "session_opened"
	case "session_restore":
		return "session_restored"
	}
	return ""
}
func (s *SessionRuntime) request(ctx context.Context, raw []byte, maximum time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, maximum)
	defer cancel()
	normalized, header, err := s.validate(raw, false)
	if err != nil {
		return nil, err
	}
	expected := sessionExpected(header.kind)
	if expected == "" {
		return nil, ErrSessionRuntime
	}
	p := &sessionPending{kind: header.kind, session: header.session, expected: expected, requestBytes: len(normalized), reply: make(chan []byte, 1)}
	s.mu.Lock()
	if s.closed || s.pending[header.id] != nil || (header.kind != "session_open" && !s.openSent) || !s.reserveLocked(len(normalized)) {
		s.mu.Unlock()
		return nil, ErrSessionRuntime
	}
	if header.kind == "session_open" {
		if s.sessionID != "" {
			s.count--
			s.bytes -= len(normalized)
			s.mu.Unlock()
			return nil, ErrSessionRuntime
		}
		s.sessionID = header.session
	}
	s.pending[header.id] = p
	s.mu.Unlock()
	defer s.removePending(header.id, p)
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		s.mu.Lock()
		sent := s.pending[header.id] == p && p.sent
		s.mu.Unlock()
		if sent {
			s.fail(ctx.Err())
		}
		close(stopped)
	})
	defer func() {
		if !stop() {
			<-stopped
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closedCh:
		return nil, s.Err()
	case <-s.writer:
	}
	if err = ctx.Err(); err != nil {
		s.writer <- struct{}{}
		return nil, err
	}
	s.mu.Lock()
	if s.closed || ctx.Err() != nil {
		s.mu.Unlock()
		s.writer <- struct{}{}
		return nil, ErrSessionRuntime
	}
	p.sent = true
	s.observeSentLocked(header.kind, normalized)
	if header.kind == "session_open" {
		s.openSent = true
	}
	s.mu.Unlock()
	deadline, _ := ctx.Deadline()
	err = s.process.RPC.conn.SetWriteDeadline(deadline)
	if err == nil {
		err = s.process.RPC.Write(normalized)
	}
	if err == nil {
		err = s.process.RPC.conn.SetWriteDeadline(time.Time{})
	}
	s.writer <- struct{}{}
	if err != nil {
		s.fail(err)
		return nil, err
	}
	select {
	case <-ctx.Done():
		s.fail(ctx.Err())
		return nil, ctx.Err()
	case <-s.closedCh:
		return nil, s.Err()
	case reply := <-p.reply:
		return reply, nil
	}
}
func (s *SessionRuntime) readLoop() {
	defer close(s.readerDone)
	for {
		raw, err := s.process.RPC.Read()
		if err != nil {
			s.fail(err)
			return
		}
		normalized, header, err := s.validate(raw, true)
		if err != nil {
			s.fail(err)
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		if sessionReplyType(header.kind) {
			p := s.pending[header.id]
			if p == nil || !p.sent || p.expected != header.kind || p.responseBytes != 0 || len(normalized) > sessionControlBytes-s.bytes {
				s.mu.Unlock()
				s.fail(ErrSessionRuntime)
				return
			}
			s.observeResponseLocked(header.kind, normalized)
			p.responseBytes = len(normalized)
			s.bytes += len(normalized)
			p.reply <- normalized
			s.mu.Unlock()
			continue
		}
		if previous, ok := s.childIDs[header.id]; ok {
			entry := s.child[previous]
			same := entry != nil && bytes.Equal(entry.bytes, normalized)
			s.mu.Unlock()
			if same {
				continue
			}
			s.fail(ErrSessionRuntime)
			return
		}
		if s.next == ^uint64(0) || !s.reserveLocked(len(normalized)) {
			s.mu.Unlock()
			s.fail(ErrSessionRuntime)
			return
		}
		s.next++
		entry := &sessionControlEntry{id: s.next, requestID: header.id, kind: header.kind, bytes: normalized}
		s.child[entry.id] = entry
		s.childIDs[entry.requestID] = entry.id
		s.queue = append(s.queue, entry.id)
		s.signalLocked()
		s.mu.Unlock()
	}
}
func sessionReplyType(kind string) bool {
	return kind == "checkpoint_validated_causal" || kind == "atomic_prepared_causal" || kind == "session_opened" || kind == "session_restored"
}
func (s *SessionRuntime) NextControl(ctx context.Context) (SessionControl, error) {
	for {
		if err := ctx.Err(); err != nil {
			return SessionControl{}, err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return SessionControl{}, s.Err()
		}
		if len(s.queue) > 0 {
			id := s.queue[0]
			s.queue = s.queue[1:]
			s.child[id].borrowed = true
			s.mu.Unlock()
			return SessionControl{s, id}, nil
		}
		notify := s.notify
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return SessionControl{}, ctx.Err()
		case <-s.closedCh:
			return SessionControl{}, s.Err()
		case <-notify:
		}
	}
}
func (c SessionControl) Frame() ([]byte, error) {
	s := c.runtime
	if s == nil {
		return nil, ErrSessionRuntime
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.child[c.id]
	if s.closed || entry == nil || !entry.borrowed {
		return nil, ErrSessionRuntime
	}
	return append([]byte(nil), entry.bytes...), nil
}
func childExpected(kind string) string {
	switch kind {
	case "owner_acquire", "owner_renew":
		return "owner_grant"
	case "ticket_validate":
		return "ticket_validated"
	case "delivery_renew":
		return "delivery_grant"
	case "session_submit_batch":
		return "session_commit_result"
	case "session_status_request":
		return "session_status_result"
	}
	return ""
}
func (s *SessionRuntime) ReplyControl(ctx context.Context, c SessionControl, raw []byte) error {
	normalized, header, err := s.validate(raw, false)
	if err != nil {
		return err
	}
	s.mu.Lock()
	entry := s.child[c.id]
	if c.runtime != s || s.closed || entry == nil || !entry.borrowed || entry.replying || header.id != entry.requestID || childExpected(entry.kind) != header.kind {
		s.mu.Unlock()
		return ErrSessionRuntime
	}
	entry.replying = true
	s.mu.Unlock()
	if err = s.write(ctx, normalized); err != nil {
		s.fail(err)
		return err
	}
	s.mu.Lock()
	s.observeOwnerLocked(entry, normalized)
	s.mu.Unlock()
	return s.complete(c, true)
}

// Notifications require explicit completion; request purposes cannot silently
// disappear and allow a dependent FIFO prefix to continue after uncertainty.
func (s *SessionRuntime) CompleteControl(c SessionControl) error { return s.complete(c, false) }
func (s *SessionRuntime) complete(c SessionControl, replied bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.child[c.id]
	if c.runtime != s || s.closed || entry == nil || !entry.borrowed || (childExpected(entry.kind) != "" && !replied) {
		return ErrSessionRuntime
	}
	delete(s.child, c.id)
	delete(s.childIDs, entry.requestID)
	s.count--
	s.bytes -= len(entry.bytes)
	return nil
}
func (s *SessionRuntime) write(ctx context.Context, raw []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closedCh:
		return s.Err()
	case <-s.writer:
	}
	defer func() { s.writer <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return err
	}
	cancelled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { s.fail(ctx.Err()); close(cancelled) })
	defer func() {
		if !stop() {
			<-cancelled
		}
	}()
	deadline, _ := ctx.Deadline()
	err := s.process.RPC.conn.SetWriteDeadline(deadline)
	if err == nil {
		err = s.process.RPC.Write(raw)
	}
	if err == nil {
		err = s.process.RPC.conn.SetWriteDeadline(time.Time{})
	}
	if err != nil {
		s.fail(err)
	}
	return err
}
func runtimeSessionHandshake(rpc *RPC, versions RuntimeVersions, profile string) error {
	if profile != PrivateSessionProfile {
		return ErrRuntimeHandshake
	}
	return runtimeHandshakeSelected(rpc, versions, profile, validateSessionRuntimeReady)
}
func validateSessionRuntimeReady(raw []byte, versions RuntimeVersions, profile string) bool {
	if profile != PrivateSessionProfile {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if !runtimeDelimiter(d, '{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return false
		}
		seen[name] = true
		if name == "selected" {
			if !runtimeReadyVersions(d, versions) {
				return false
			}
			continue
		}
		want := ""
		switch name {
		case "type":
			want = "runtime_ready"
		case "request_id":
			want = "host-start"
		case "selected_control_profile":
			want = PrivateSessionProfile
		default:
			return false
		}
		value, err := d.Token()
		if err != nil || value != want {
			return false
		}
	}
	if !runtimeDelimiter(d, '}') || len(seen) != 4 {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

type sessionHeader struct{ kind, id, session string }

func (s *SessionRuntime) validate(raw []byte, fromRuntime bool) ([]byte, sessionHeader, error) {
	var empty sessionHeader
	normalized, err := canonicalPrivateControlJSON(raw)
	if err != nil {
		return nil, empty, err
	}
	var value map[string]any
	if json.Unmarshal(normalized, &value) != nil {
		return nil, empty, ErrSessionRuntime
	}
	kind, _ := value["type"].(string)
	id, _ := value["request_id"].(string)
	if !batchID(id) {
		return nil, empty, ErrSessionRuntime
	}
	allowed := false
	large := false
	if fromRuntime {
		switch kind {
		case "checkpoint_validated_causal", "atomic_prepared_causal":
			allowed = true
			large = true
		case "session_opened", "session_restored", "session_submit_batch", "session_participant_output", "session_status_request", "owner_acquire", "owner_renew", "ticket_validate", "delivery_renew", "revocation_ack":
			allowed = true
		}
	} else {
		switch kind {
		case "validate_checkpoint_causal", "prepare_atomic_causal":
			allowed = true
			large = true
		case "session_restore", "session_commit_result":
			allowed = true
			large = true
		case "session_open", "session_participant_open", "session_participant_input", "session_status_result", "owner_grant", "ticket_validated", "delivery_grant", "revocation_notice":
			allowed = true
		}
	}
	if !allowed {
		return nil, empty, ErrSessionRuntime
	}
	if !large {
		normalized, err = CanonicalE3WireJSON(raw)
		if err != nil {
			return nil, empty, err
		}
	}
	if !s.validator.schema.matches(s.validator.schema.root, value, 0) {
		return nil, empty, ErrSessionRuntime
	}
	var scope protocol.Scope
	if convertPrepareValue(value["scope"], &scope) != nil || string(scope.WorkspaceID) != s.scope.WorkspaceID || string(scope.ArtifactID) != s.scope.ArtifactID || string(scope.ApplicationID) != s.scope.ApplicationID {
		return nil, empty, ErrSessionRuntime
	}
	if ref, ok := value["runtime_ref"]; ok && ref != s.runtimeRef {
		return nil, empty, ErrSessionRuntime
	}
	session, _ := value["session_id"].(string)
	s.mu.Lock()
	pinned := s.sessionID
	openSent := s.openSent
	s.mu.Unlock()
	if fromRuntime && !openSent {
		return nil, empty, ErrSessionRuntime
	}
	if pinned != "" && session != "" && session != pinned {
		return nil, empty, ErrSessionRuntime
	}
	if pinned == "" && kind != "session_open" {
		return nil, empty, ErrSessionRuntime
	}
	for _, field := range []string{"versions", "selected"} {
		if v, ok := value[field]; ok {
			var versions RuntimeVersions
			if convertPrepareValue(v, &versions) != nil || versions != s.versions {
				return nil, empty, ErrSessionRuntime
			}
		}
	}
	return normalized, sessionHeader{kind, id, session}, nil
}
