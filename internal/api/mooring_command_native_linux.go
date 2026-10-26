//go:build linux

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/mooring"
	"golang.org/x/sys/unix"
)

const commandNativeFrameLimit = 32 << 20

// Ceiling for one private command RPC, never an Open budget or a lease.
// A future factory MUST use one <=5s context across start + init/restore.
// exchange always chooses the earlier caller deadline, and never refreshes it.
const commandNativeCallLimit = 30 * time.Second

// Operator-selected configuration only. Never populate this from request JSON.
// Both executables must be immutable release artifacts, not writable user files.
type commandNativeConfig struct {
	Enabled                                            bool
	HostBinary, EngineBinary, EngineSHA256, RuntimeRef string
}

type commandNativePort struct {
	conn                                     net.Conn
	cmd                                      *exec.Cmd
	done                                     chan struct{}
	slot                                     chan struct{}
	kill                                     sync.Once
	mu                                       sync.Mutex
	failed                                   bool
	sequence                                 uint64 // accessed only while holding slot
	runtime, engineSHA, document, generation string
	stage                                    *commandNativeHandle // only accessed while holding slot
}

type commandNativeHandle struct {
	port            *commandNativePort
	ref, op, digest string
	binding         string
	resolved        bool
	receipt         []byte
}

type commandNativeMetadata struct {
	RuntimeRef     string `json:"runtime_ref"`
	Format         string `json:"format"`
	EngineRevision string `json:"engine_revision"`
	EngineSHA256   string `json:"engine_sha256"`
	PatchSHA256    string `json:"patch_sha256"`
	Catalog        string `json:"catalog"`
	CheckpointJSON string `json:"checkpoint_json"`
	Revision       uint64 `json:"revision"`
	Generation     string `json:"generation"`
}

type commandNativeProjection struct {
	Generation string   `json:"generation"`
	Revision   uint64   `json:"revision"`
	IDs        []string `json:"ids"`
	NativeJSON string   `json:"native_json"`
}

func commandNativeFileDigest(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errCommandStore
	}
	f, e := os.Open(path)
	if e != nil {
		return "", errCommandStore
	}
	defer f.Close()
	stat, e := f.Stat()
	if e != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0111 == 0 {
		return "", errCommandStore
	}
	// Trusted release files only, bounded independently of wire/input limits.
	if stat.Size() <= 0 || stat.Size() > 512<<20 {
		return "", errCommandStore
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(f, 512<<20+1))
	if e != nil || n > 512<<20 {
		return "", errCommandStore
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// No HTTP/CLI registration: an explicitly enabled trusted runtime factory owns
// this process and its lifetime. Stderr is discarded rather than logging native
// payloads or engine errors. The child receives no provider credentials.
func startCommandNativePort(ctx context.Context, c commandNativeConfig) (*commandNativePort, error) {
	if !c.Enabled || !filepath.IsAbs(c.HostBinary) || !commandNativeID(c.RuntimeRef) || !commandDigestValid(c.EngineSHA256) {
		return nil, errCommandStore
	}
	hash, e := commandNativeFileDigest(c.EngineBinary)
	if e != nil || hash != c.EngineSHA256 {
		return nil, errCommandStore
	}
	if _, e = commandNativeFileDigest(c.HostBinary); e != nil {
		return nil, errCommandStore
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	fds, e := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if e != nil {
		return nil, errCommandStore
	}
	parent := os.NewFile(uintptr(fds[0]), "c1-parent")
	child := os.NewFile(uintptr(fds[1]), "c1-child")
	defer child.Close()
	conn, e := net.FileConn(parent)
	_ = parent.Close()
	if e != nil {
		return nil, errCommandStore
	}
	cmd := exec.Command(c.HostBinary, "--engine", c.EngineBinary, "--engine-sha256", c.EngineSHA256, "--runtime-ref", c.RuntimeRef)
	cmd.Env = []string{"LANG=C.UTF-8", "TZ=UTC", "MOORING_C1_HOST_FD=3"}
	cmd.ExtraFiles = []*os.File{child}
	if e = cmd.Start(); e != nil {
		_ = conn.Close()
		return nil, errCommandStore
	}
	p := &commandNativePort{conn: conn, cmd: cmd, done: make(chan struct{}), slot: make(chan struct{}, 1), runtime: c.RuntimeRef, engineSHA: c.EngineSHA256}
	p.slot <- struct{}{}
	go func() {
		_ = cmd.Wait()
		p.mu.Lock()
		p.failed = true
		p.mu.Unlock()
		_ = conn.Close()
		close(p.done)
	}()
	return p, nil
}
func (p *commandNativePort) Done() <-chan struct{} { return p.done }
func (p *commandNativePort) RuntimeRef() string    { return p.runtime }
func (p *commandNativePort) EngineSHA256() string  { return p.engineSHA }
func (p *commandNativePort) poison() {
	p.kill.Do(func() { p.mu.Lock(); p.failed = true; p.mu.Unlock(); _ = p.conn.Close(); _ = p.cmd.Process.Kill() })
}
func (p *commandNativePort) Close() { p.poison(); <-p.done }

// Closed object decoder also rejects duplicate keys and Go's case-insensitive
// struct-field aliases. Every wire object here has flat, explicitly named keys.
func commandNativeDecode(raw []byte, out any, required ...string) error {
	canonical, e := mooring.CanonicalJSON(raw)
	if e != nil {
		return errCommandStore
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(canonical, &fields) != nil || fields == nil || len(fields) != len(required) {
		return errCommandStore
	}
	for _, key := range required {
		if value, ok := fields[key]; !ok || bytes.Equal(value, []byte("null")) {
			return errCommandStore
		}
	}
	d := json.NewDecoder(bytes.NewReader(canonical))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return errCommandStore
	}
	return nil
}

// exchange holds single-flight ownership through semantic validation. Any
// uncertainty poisons this channel permanently: a late reply can never be
// attributed to a new operation. No RPC method is automatically replayed.
func (p *commandNativePort) exchange(ctx context.Context, kind string, fields map[string]any, validate func(json.RawMessage) error) error {
	return p.exchangeChecked(ctx, kind, fields, nil, validate)
}
func (p *commandNativePort) exchangeChecked(ctx context.Context, kind string, fields map[string]any, before func() error, validate func(json.RawMessage) error) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return errCommandStore
	case <-p.slot:
	}
	defer func() { p.slot <- struct{}{} }()
	p.mu.Lock()
	failed := p.failed
	p.mu.Unlock()
	if failed {
		return errCommandStore
	}
	if before != nil {
		if e := before(); e != nil {
			return e
		}
	}
	deadline := time.Now().Add(commandNativeCallLimit)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if p.conn.SetDeadline(deadline) != nil {
		p.poison()
		return errCommandUnknown
	}
	cancelDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = p.conn.SetDeadline(time.Now()); close(cancelDone) })
	defer func() {
		if !stop() {
			<-cancelDone
		}
	}()
	p.sequence++
	requestID := stringUint(p.sequence)
	fields["kind"] = kind
	fields["protocol"] = 1
	fields["request_id"] = requestID
	raw, e := json.Marshal(fields)
	if e != nil || len(raw) == 0 || len(raw) > commandNativeFrameLimit {
		p.poison()
		return errCommandUnknown
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
	if e = commandNativeWrite(p.conn, header[:]); e == nil {
		e = commandNativeWrite(p.conn, raw)
	}
	if e == nil {
		_, e = io.ReadFull(p.conn, header[:])
	}
	size := binary.BigEndian.Uint32(header[:])
	if e != nil || size == 0 || size > commandNativeFrameLimit {
		p.poison()
		return errCommandUnknown
	}
	raw = make([]byte, int(size))
	if _, e = io.ReadFull(p.conn, raw); e != nil || !utf8.Valid(raw) {
		p.poison()
		return errCommandUnknown
	}
	var envelope map[string]json.RawMessage
	canonical, e := mooring.CanonicalJSON(raw)
	if e != nil || json.Unmarshal(canonical, &envelope) != nil {
		p.poison()
		return errCommandUnknown
	}
	var ok bool
	var id string
	var protocol int
	if json.Unmarshal(envelope["ok"], &ok) != nil || json.Unmarshal(envelope["request_id"], &id) != nil || id != requestID || json.Unmarshal(envelope["protocol"], &protocol) != nil || protocol != 1 {
		p.poison()
		return errCommandUnknown
	}
	if ok {
		var success struct {
			Protocol  int             `json:"protocol"`
			RequestID string          `json:"request_id"`
			OK        bool            `json:"ok"`
			Value     json.RawMessage `json:"value"`
		}
		if commandNativeDecode(canonical, &success, "protocol", "request_id", "ok", "value") != nil || validate(success.Value) != nil {
			p.poison()
			return errCommandUnknown
		}
		return nil
	}
	var refusal struct {
		Protocol    int    `json:"protocol"`
		RequestID   string `json:"request_id"`
		OK          bool   `json:"ok"`
		Error       string `json:"error"`
		NoCandidate bool   `json:"no_candidate"`
	}
	if commandNativeDecode(canonical, &refusal, "protocol", "request_id", "ok", "error", "no_candidate") != nil || !commandNativeReason(refusal.Error) {
		p.poison()
		return errCommandUnknown
	}
	if kind == "stage" && refusal.NoCandidate && refusal.Error == "validation_refused" {
		return errCommandRefused
	}
	// Even a complete application refusal is not an ACK or a rollback proof.
	// It leaves native K intact, requiring canonical SQL reconciliation.
	if refusal.NoCandidate && (kind != "stage" || (refusal.Error != "validation_refused" && refusal.Error != "base_conflict" && refusal.Error != "admission_denied")) {
		p.poison()
	}
	return errCommandUnknown
}
func stringUint(v uint64) string { // no client-selected request correlation
	const digits = "0123456789"
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = digits[v%10]
		v /= 10
		if v == 0 {
			return string(b[i:])
		}
	}
}
func commandNativeReason(s string) bool {
	switch s {
	case "validation_refused", "invalid_request", "invalid_state", "engine_failed", "base_conflict", "pending", "known_operation", "unknown_stage", "admission_denied", "restore_failed", "capacity":
		return true
	}
	return false
}
func commandNativeWrite(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil {
			return e
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func nativeAdmission(a commandHostAdmission) map[string]any {
	return map[string]any{"participant": a.Participant, "author": a.Author, "time": a.Time, "epoch": a.Epoch, "writer": a.Writer}
}
func (p *commandNativePort) metadata(raw []byte) (commandNativeMetadata, error) {
	var m commandNativeMetadata
	e := commandNativeDecode(raw, &m, "runtime_ref", "format", "engine_revision", "engine_sha256", "patch_sha256", "catalog", "checkpoint_json", "revision", "generation")
	if e != nil || m.RuntimeRef != p.runtime || m.Format != commandFormat || m.EngineRevision != commandEngineRevision || m.EngineSHA256 != p.engineSHA || m.PatchSHA256 != commandClockPatch || m.Catalog != "wordcraft-c1-10-v1" || !commandNativeID(m.Generation) || len(m.CheckpointJSON) == 0 || len(m.CheckpointJSON) > commandNativeFrameLimit || !json.Valid([]byte(m.CheckpointJSON)) {
		return m, errCommandStore
	}
	return m, nil
}
func (p *commandNativePort) InitialSnapshot(ctx context.Context, document, generation string) (commandSQLSnapshot, error) {
	var snapshot commandSQLSnapshot
	if !commandNativeID(document) || !commandNativeID(generation) {
		return snapshot, errCommandStore
	}
	e := p.exchangeChecked(ctx, "init", map[string]any{"document": document, "generation": generation}, func() error {
		if p.document != "" {
			return errCommandStore
		}
		return nil
	}, func(raw json.RawMessage) error {
		if p.document != "" {
			return errCommandStore
		}
		m, e := p.metadata(raw)
		if e != nil || m.Revision != 0 || m.Generation != generation || commandNativeCheckpoint([]byte(m.CheckpointJSON), document, generation, 0) != nil {
			return errCommandStore
		}
		snapshot = commandSQLSnapshot{m.Revision, []byte(m.CheckpointJSON)}
		p.document = document
		p.generation = generation
		return nil
	})
	return snapshot, e
}
func (p *commandNativePort) Stage(ctx context.Context, a commandHostAdmission, base commandSQLSnapshot, input []byte) (commandSQLCandidate, error) {
	var c commandSQLCandidate
	if len(input) == 0 || len(input) > commandInputLimit || !utf8.Valid(input) || len(base.Checkpoint) > commandCheckpointLimit {
		return c, errCommandStore
	}
	// Validate only: the exact caller bytes, not the canonicalized output,
	// remain the native input and its retry/fingerprint identity.
	if _, e := mooring.CanonicalJSON(input); e != nil {
		return c, errCommandStore
	}
	e := p.exchange(ctx, "stage", map[string]any{"base_revision": base.Revision, "base_digest": commandDigest(base.Checkpoint), "input_json": string(input), "admission": nativeAdmission(a)}, func(raw json.RawMessage) error {
		var wire struct {
			State          string `json:"state"`
			StageRef       string `json:"stage_ref"`
			OpID           string `json:"op_id"`
			Document       string `json:"document"`
			Generation     string `json:"generation"`
			Format         string `json:"format"`
			EngineRevision string `json:"engine_revision"`
			EngineSHA256   string `json:"engine_sha256"`
			BaseRevision   uint64 `json:"base_revision"`
			Revision       uint64 `json:"revision"`
			BaseDigest     string `json:"base_digest"`
			Digest         string `json:"digest"`
			Fingerprint    string `json:"fingerprint"`
			CheckpointJSON string `json:"checkpoint_json"`
			ReceiptJSON    string `json:"receipt_json"`
		}
		if commandNativeDecode(raw, &wire, "state", "stage_ref", "op_id", "document", "generation", "format", "engine_revision", "engine_sha256", "base_revision", "revision", "base_digest", "digest", "fingerprint", "checkpoint_json", "receipt_json") != nil || wire.State != "staged" || !commandNativeID(wire.StageRef) || wire.Document != p.document || wire.Generation != p.generation || wire.Format != commandFormat || wire.EngineRevision != commandEngineRevision || wire.EngineSHA256 != p.engineSHA || wire.BaseRevision != base.Revision || wire.BaseDigest != commandDigest(base.Checkpoint) || wire.Digest != commandDigest([]byte(wire.CheckpointJSON)) || commandNativeCheckpoint([]byte(wire.CheckpointJSON), wire.Document, wire.Generation, wire.Revision) != nil || wire.Revision != base.Revision+1 || !commandDigestValid(wire.Fingerprint) || !commandNativeID(wire.OpID) || len(wire.ReceiptJSON) > commandInputLimit*3 || !json.Valid([]byte(wire.ReceiptJSON)) {
			return errCommandStore
		}
		var in struct {
			OpID string `json:"op_id"`
		}
		if json.Unmarshal(input, &in) != nil || in.OpID != wire.OpID || commandNativeReceipt([]byte(wire.ReceiptJSON), wire.OpID, wire.Revision, wire.Fingerprint, a) != nil {
			return errCommandStore
		}
		h := &commandNativeHandle{port: p, ref: wire.StageRef, op: wire.OpID, digest: wire.Digest, receipt: []byte(wire.ReceiptJSON)}
		if p.stage != nil && !p.stage.resolved && p.stage.ref != h.ref {
			return errCommandStore
		}
		if p.stage != nil && p.stage.ref == h.ref {
			if p.stage.op != h.op || p.stage.digest != h.digest || !bytes.Equal(p.stage.receipt, h.receipt) {
				return errCommandStore
			}
			h = p.stage
		}
		p.stage = h
		c = commandSQLCandidate{OpID: wire.OpID, Document: wire.Document, Generation: wire.Generation, Format: wire.Format, EngineRevision: wire.EngineRevision, EngineSHA256: wire.EngineSHA256, BaseRevision: wire.BaseRevision, Revision: wire.Revision, BaseDigest: wire.BaseDigest, Digest: wire.Digest, Fingerprint: wire.Fingerprint, Checkpoint: []byte(wire.CheckpointJSON), Receipt: []byte(wire.ReceiptJSON), Handle: h}
		h.binding = commandNativeCandidateBinding(c)
		return nil
	})
	return c, e
}
func commandNativeCandidateBinding(c commandSQLCandidate) string {
	c.Handle = nil
	raw, _ := json.Marshal(c)
	return commandDigest(raw)
}

func (p *commandNativePort) handle(c commandSQLCandidate) (*commandNativeHandle, error) {
	h, ok := c.Handle.(*commandNativeHandle)
	if !ok || h == nil || h.port != p || h != p.stage || h.op != c.OpID || h.digest != c.Digest || h.binding != commandNativeCandidateBinding(c) || commandDigest(c.Checkpoint) != h.digest || !bytes.Equal(c.Receipt, h.receipt) {
		return nil, errCommandStore
	}
	return h, nil
}
func (p *commandNativePort) Confirm(ctx context.Context, c commandSQLCandidate, a commandHostAdmission) error {
	// Handle validation must be under the same single-flight lock as the write.
	return p.exchangeChecked(ctx, "confirm", map[string]any{"stage_ref": nativeHandleRef(c), "admission": nativeAdmission(a)}, func() error { _, e := p.handle(c); return e }, func(raw json.RawMessage) error {
		h, e := p.handle(c)
		if e != nil {
			return e
		}
		var v struct {
			ReceiptJSON string `json:"receipt_json"`
		}
		if commandNativeDecode(raw, &v, "receipt_json") != nil || !bytes.Equal([]byte(v.ReceiptJSON), h.receipt) {
			return errCommandStore
		}
		h.resolved = true
		return nil
	})
}
func nativeHandleRef(c commandSQLCandidate) string {
	if h, ok := c.Handle.(*commandNativeHandle); ok && h != nil {
		return h.ref
	}
	return ""
}
func (p *commandNativePort) Reject(ctx context.Context, c commandSQLCandidate) error {
	return p.exchangeChecked(ctx, "reject", map[string]any{"stage_ref": nativeHandleRef(c)}, func() error { _, e := p.handle(c); return e }, func(raw json.RawMessage) error {
		if _, e := p.handle(c); e != nil {
			return e
		}
		var v struct {
			State string `json:"state"`
		}
		if commandNativeDecode(raw, &v, "state") != nil || v.State != "rejected" {
			return errCommandStore
		}
		p.stage = nil
		return nil
	})
}
func (p *commandNativePort) RestoreConfirmed(ctx context.Context, s commandSQLSnapshot) error {
	if len(s.Checkpoint) == 0 || len(s.Checkpoint) > commandCheckpointLimit {
		return errCommandStore
	}
	var original struct {
		Document   string `json:"document"`
		Generation string `json:"generation"`
	}
	if json.Unmarshal(s.Checkpoint, &original) != nil || commandNativeCheckpoint(s.Checkpoint, original.Document, original.Generation, s.Revision) != nil {
		return errCommandStore
	}
	generation, e := collaborationOpaqueID()
	if e != nil {
		return errCommandStore
	}
	return p.exchangeChecked(ctx, "restore", map[string]any{"revision": s.Revision, "checkpoint_json": string(s.Checkpoint), "generation": generation}, func() error {
		if p.document != "" && p.document != original.Document {
			return errCommandStore
		}
		return nil
	}, func(raw json.RawMessage) error {
		m, e := p.metadata(raw)
		if e != nil || m.Revision != s.Revision || m.Generation != generation || commandNativeCheckpoint([]byte(m.CheckpointJSON), original.Document, generation, s.Revision) != nil {
			return errCommandStore
		}
		p.document = original.Document
		p.generation = generation
		p.stage = nil
		return nil
	})
}
func (p *commandNativePort) Projection(ctx context.Context) (commandNativeProjection, error) {
	var v commandNativeProjection
	e := p.exchange(ctx, "projection", map[string]any{}, func(raw json.RawMessage) error {
		if commandNativeDecode(raw, &v, "generation", "revision", "ids", "native_json") != nil || v.Generation != p.generation || len(v.IDs) == 0 || len(v.IDs) > 4096 || len(v.NativeJSON) > 4<<20 || !json.Valid([]byte(v.NativeJSON)) {
			return errCommandStore
		}
		var native struct {
			Body []json.RawMessage `json:"body"`
		}
		if json.Unmarshal([]byte(v.NativeJSON), &native) != nil || len(native.Body) != len(v.IDs) {
			return errCommandStore
		}
		seen := map[string]bool{}
		for _, id := range v.IDs {
			if !commandNativeID(id) || seen[id] {
				return errCommandStore
			}
			seen[id] = true
		}
		return nil
	})
	return v, e
}

var _ collaborationCommandPort = (*commandNativePort)(nil)

// Validate the checkpoint's scoped/pinned header and bounded native P, without
// reconstructing the checkpoint bytes used for SQL CAS. Native engine schema
// and history validation remain the pinned Rust host's responsibility.
func commandNativeCheckpoint(raw []byte, document, generation string, revision uint64) error {
	if len(raw) == 0 || len(raw) > commandNativeFrameLimit || !commandNativeID(document) || !commandNativeID(generation) {
		return errCommandStore
	}
	canonical, e := mooring.CanonicalJSON(raw)
	if e != nil {
		return errCommandStore
	}
	var cp struct {
		Format         string `json:"format"`
		Document       string `json:"document"`
		Generation     string `json:"generation"`
		Revision       uint64 `json:"revision"`
		EngineIdentity struct {
			Revision string `json:"revision"`
			Patch    string `json:"patch_sha256"`
			Catalog  string `json:"catalog"`
		} `json:"engine_identity"`
		P struct {
			Native json.RawMessage `json:"native"`
			IDs    []string        `json:"ids"`
		} `json:"p"`
	}
	if json.Unmarshal(canonical, &cp) != nil || cp.Format != commandFormat || cp.Document != document || cp.Generation != generation || cp.Revision != revision || cp.EngineIdentity.Revision != commandEngineRevision || cp.EngineIdentity.Patch != commandClockPatch || cp.EngineIdentity.Catalog != "wordcraft-c1-10-v1" || len(cp.P.Native) > 4<<20 || len(cp.P.IDs) == 0 || len(cp.P.IDs) > 4096 {
		return errCommandStore
	}
	var native struct {
		Body []json.RawMessage `json:"body"`
	}
	if json.Unmarshal(cp.P.Native, &native) != nil || len(native.Body) != len(cp.P.IDs) {
		return errCommandStore
	}
	seen := map[string]bool{}
	for _, id := range cp.P.IDs {
		if !commandNativeID(id) || seen[id] {
			return errCommandStore
		}
		seen[id] = true
	}
	return nil
}

func commandNativeID(s string) bool { return len(s) <= 200 && mooring.ValidWireIdentifier(s) }

func commandNativeReceipt(raw []byte, op string, revision uint64, fingerprint string, a commandHostAdmission) error {
	var r struct {
		State       string `json:"state"`
		OpID        string `json:"op_id"`
		Revision    uint64 `json:"revision"`
		Actor       string `json:"actor"`
		Author      string `json:"author"`
		Time        string `json:"command_time"`
		Fingerprint string `json:"fingerprint"`
	}
	if commandNativeDecode(raw, &r, "state", "op_id", "revision", "actor", "author", "command_time", "fingerprint") != nil || r.State != "host_committed" || r.OpID != op || r.Revision != revision || r.Actor != a.Participant || r.Author != a.Author || r.Time != a.Time || r.Fingerprint != fingerprint {
		return errCommandStore
	}
	return nil
}
