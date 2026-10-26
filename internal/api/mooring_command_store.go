package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

const commandFormat = "wordcraft-c1-v2"
const commandEngineRevision = "7ef2633a7e84956522d1ca47b99fe6feef16c1db"
const commandClockPatch = "9c2ff901ae408c72d12bbb864895a63791f000551978bf615734a6b41e381581"
const commandCheckpointLimit = 4 << 20
const commandInputLimit = 65536
const commandOperationLimit = 256

var errCommandStore = errors.New("command document unavailable")
var errCommandConflict = errors.New("command original or base conflicts")
var errCommandUnknown = errors.New("command outcome requires reconciliation")
var errCommandBusy = errors.New("command document has an unresolved operation")

// Only a qualified, complete port refusal proving no K was created may use
// this sentinel. Transport errors, partial replies and pending_limit may not.
var errCommandRefused = errors.New("command refused without candidate")

// These types belong to the trusted host port, never a JSON handler. In
// particular no client-supplied role, author, clock or credential enters them.
type commandHostAdmission struct {
	Participant, Author, Time string
	TransportParticipant      string
	Epoch                     uint64
	Writer                    bool
}

// Trusted integration metadata, not a grant or an identity supplied in JSON.
// The product factory owns the stable logical human/agent mapping; this store
// does not mint one or silently use a random transport admission for history.
type commandEngineActorBinding struct {
	transportParticipant, sourceUser, actor string
}
type commandSQLSnapshot struct {
	Revision   uint64
	Checkpoint []byte
}
type commandSQLCandidate struct {
	OpID, Document, Generation, Format, EngineRevision, EngineSHA256 string
	BaseRevision, Revision                                           uint64
	BaseDigest, Digest, Fingerprint                                  string
	Checkpoint, Receipt                                              []byte
	Handle                                                           any // original opaque stage; never a serialized authority
}

// The product runtime factory must supply an actually negotiated C1 port.
// There is deliberately no CLI/listener/process factory here. Stage owns K;
// Confirm is called ONLY after durable SQL success. Restore never re-executes.
type collaborationCommandPort interface {
	Done() <-chan struct{}
	RuntimeRef() string
	EngineSHA256() string
	Stage(context.Context, commandHostAdmission, commandSQLSnapshot, []byte) (commandSQLCandidate, error)
	Confirm(context.Context, commandSQLCandidate, commandHostAdmission) error
	Reject(context.Context, commandSQLCandidate) error
	RestoreConfirmed(context.Context, commandSQLSnapshot) error
}

type commandSQLOutcome struct {
	OpID        string          `json:"op_id"`
	Fingerprint string          `json:"fingerprint"`
	InputDigest string          `json:"input_digest"`
	Revision    uint64          `json:"revision"`
	Receipt     json.RawMessage `json:"receipt"`
}

// Private operation adapter shares durability/order without sharing human and
// agent authority. Each source installs only its existing canonical checks.
type commandOperationSource struct {
	participant string
	run         func(func(context.Context) error) error
	admit       func(context.Context, *sql.Tx, bool) (commandHostAdmission, error)
	lookup      func(context.Context, *sql.Tx, string) (*commandSQLOutcome, error)
}

func (s *collaborationCommandStore) humanOperation(r *http.Request, original collaborationHumanAdmission) commandOperationSource {
	return commandOperationSource{
		participant: original.participant,
		run: func(call func(context.Context) error) error {
			return s.humans.runHumanOperation(r, original.participant, call)
		},
		admit: func(ctx context.Context, tx *sql.Tx, writer bool) (commandHostAdmission, error) {
			return s.admission(ctx, tx, original, writer)
		},
		lookup: s.lookup,
	}
}

type collaborationCommandStore struct {
	mu        sync.Mutex // only host busy/suspended flags; never held over I/O
	busy      bool
	suspended bool
	pages     *PageHandler
	humans    *collaborationHumanRegistry
	owners    *mooring.OwnerStore
	owner     mooring.OwnerLease
	port      collaborationCommandPort
	engineSHA string
}

func newCollaborationCommandStore(enabled bool, pages *PageHandler, owners *mooring.OwnerStore, fence *collaborationIngressFence, clock mooring.OwnerClock, owner mooring.OwnerLease, port collaborationCommandPort) (*collaborationCommandStore, error) {
	if !enabled || pages == nil || pages.db == nil || owners == nil || fence == nil || clock == nil || port == nil || port.Done() == nil || owner.Key.ApplicationID != "wordcraft" || owner.OwnerID != port.RuntimeRef() || !commandDigestValid(port.EngineSHA256()) {
		return nil, errCommandStore
	}
	select {
	case <-port.Done():
		return nil, errCommandStore
	default:
	}
	lifecycle, err := collaborationHumanLifecycleStamp(fence)
	if err != nil {
		return nil, err
	}
	session, err := collaborationOpaqueID()
	if err != nil {
		return nil, err
	}
	// Reuse the same original-source registry, current Page policy, authority
	// vectors, monotonic horizon and ingress operation leases. This binding is
	// specifically a trusted command port, not a forged legacy RuntimeReady.
	humans := &collaborationHumanRegistry{lifecycle: lifecycle, bindingID: session, fence: fence, pages: pages, owners: owners, runtime: port, clock: clock, scope: owner.Key, sessionID: session, limit: 64, admissions: make(map[string]collaborationHumanAdmission), tickets: make(map[string]collaborationHumanJoinTicket)}
	return &collaborationCommandStore{pages: pages, humans: humans, owners: owners, owner: owner, port: port, engineSHA: port.EngineSHA256()}, nil
}

func commandDigest(raw []byte) string {
	h := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(h[:])
}
func commandDigestValid(s string) bool {
	if len(s) != 71 || s[:7] != "sha256:" {
		return false
	}
	_, err := hex.DecodeString(s[7:])
	return err == nil && s == "sha256:"+string(bytes.ToLower([]byte(s[7:])))
}

func (s *collaborationCommandStore) live() error {
	select {
	case <-s.port.Done():
		return errCommandStore
	default:
		return nil
	}
}

// The canonical SQL writer and FULL durability contract are shared with the
// existing collaboration stores. A callback failure is a definitive rollback;
// an error returned by Commit itself is explicitly uncertain.
func (s *collaborationCommandStore) transaction(ctx context.Context, fn func(*sql.Tx) error) (uncertain bool, err error) {
	conn, err := s.pages.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() {
		if e := tx.Rollback(); e != nil && !errors.Is(e, sql.ErrTxDone) {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if err = s.owners.Check(ctx, tx, s.owner); err != nil {
		return false, err
	}
	var synchronous int
	if err = tx.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&synchronous); err != nil || synchronous < 2 {
		return false, errCommandStore
	}
	if err = s.live(); err != nil {
		return false, err
	}
	if err = fn(tx); err != nil {
		return false, err
	}
	if err = s.owners.Check(ctx, tx, s.owner); err != nil {
		return false, err
	}
	err = tx.Commit()
	if err != nil {
		// A failed COMMIT is not permission to recycle a possibly active SQL
		// transaction into the pool. Close only this owned connection; recovery
		// queries the canonical DB through a fresh connection and never replays.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	return err != nil, err
}

// Create attaches only to a genuinely empty human-owned Page. The existing
// custody triggers exclude Yjs, JSON-pilot and all other application engines.
// The initial bytes must already have been qualified by the trusted C1 port.
func (s *collaborationCommandStore) Create(r *http.Request, initial commandSQLSnapshot) error {
	if r == nil || initial.Revision != 0 || !s.validCheckpoint(initial, "", nil) {
		return errCommandStore
	}
	stamp, err := s.humans.fence.capture(r.Context())
	if err != nil || stamp != s.humans.lifecycle {
		return errCommandStore
	}
	release, err := s.humans.fence.acquire(r.Context(), stamp)
	if err != nil {
		return err
	}
	defer release()
	uncertain, err := s.transaction(r.Context(), func(tx *sql.Tx) error {
		if _, e := s.pages.authorizeCollaborationHumanPage(r.Context(), tx, s.owner.Key, "initialize"); e != nil {
			return e
		}
		_, e := tx.ExecContext(r.Context(), `INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64,format_profile,engine_revision,engine_sha256) VALUES(?,?,'wordcraft','0',?,?,?,?)`, s.owner.Key.ArtifactID, s.owner.Key.WorkspaceID, base64.StdEncoding.EncodeToString(initial.Checkpoint), commandFormat, commandEngineRevision, s.engineSHA)
		if e != nil {
			return e
		}
		if e = s.insertCheckpoint(r.Context(), tx, initial); e != nil {
			return e
		}
		_, e = s.pages.authorizeCollaborationHumanPage(r.Context(), tx, s.owner.Key, "initialize_commit")
		return e
	})
	if uncertain {
		s.suspend()
		return errCommandUnknown
	}
	return err
}

// Admit never treats an agent bearer as an interactive human. The ticket is
// deliberately not exposed: command transport admission is supplied separately.
func (s *collaborationCommandStore) Admit(ctx context.Context, writer bool) (collaborationHumanAdmission, error) {
	ticket, admission, err := s.humans.issue(ctx, s.owner, writer)
	if err == nil {
		// A C1 admission is not a legacy session_join capability.
		s.humans.mu.Lock()
		delete(s.humans.tickets, ticket)
		s.humans.mu.Unlock()
	}
	return admission, err
}

// ReadCheckpoint reads confirmed SQL P, never the engine's candidate K. The
// runtime factory may restore these exact bytes through its trusted C1 port;
// this is neither an acknowledgement of an operation nor permission to write.
func (s *collaborationCommandStore) ReadCheckpoint(r *http.Request, original collaborationHumanAdmission) (commandSQLSnapshot, error) {
	return s.readCheckpoint(s.humanOperation(r, original))
}

func (s *collaborationCommandStore) readCheckpoint(source commandOperationSource) (commandSQLSnapshot, error) {
	var value commandSQLSnapshot
	err := source.run(func(ctx context.Context) error {
		_, err := s.transaction(ctx, func(tx *sql.Tx) error {
			if _, e := source.admit(ctx, tx, false); e != nil {
				return e
			}
			var e error
			value, e = s.snapshot(ctx, tx)
			if e != nil {
				return e
			}
			_, e = source.admit(ctx, tx, false)
			return e
		})
		return err
	})
	if err != nil {
		return commandSQLSnapshot{}, err
	}
	return value, nil
}

func (s *collaborationCommandStore) snapshot(ctx context.Context, tx *sql.Tx) (commandSQLSnapshot, error) {
	var out commandSQLSnapshot
	var revision, encoded, profile, engine, digest, workspace, application string
	err := tx.QueryRowContext(ctx, `SELECT workspace_id,application_id,revision,checkpoint_base64,format_profile,engine_revision,engine_sha256 FROM mooring_collab_documents WHERE page_id=?`, s.owner.Key.ArtifactID).Scan(&workspace, &application, &revision, &encoded, &profile, &engine, &digest)
	if err != nil {
		return out, err
	}
	if workspace != s.owner.Key.WorkspaceID || application != "wordcraft" || profile != commandFormat || engine != commandEngineRevision || digest != s.engineSHA || len(encoded) > base64.StdEncoding.EncodedLen(commandCheckpointLimit) {
		return out, errCommandStore
	}
	out.Revision, err = strconv.ParseUint(revision, 10, 64)
	if err != nil || strconv.FormatUint(out.Revision, 10) != revision {
		return commandSQLSnapshot{}, errCommandStore
	}
	out.Checkpoint, err = base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || !s.validCheckpoint(out, "", nil) {
		return commandSQLSnapshot{}, errCommandStore
	}
	return out, nil
}

func (s *collaborationCommandStore) validCheckpoint(snapshot commandSQLSnapshot, op string, receipt []byte) bool {
	if len(snapshot.Checkpoint) == 0 || len(snapshot.Checkpoint) > commandCheckpointLimit {
		return false
	}
	var header struct {
		Format         string `json:"format"`
		Document       string `json:"document"`
		Generation     string `json:"generation"`
		Revision       uint64 `json:"revision"`
		EngineIdentity struct {
			Revision string `json:"revision"`
			Patch    string `json:"patch_sha256"`
			Catalog  string `json:"catalog"`
		} `json:"engine_identity"`
		Seen map[string]struct {
			Result json.RawMessage `json:"result"`
		} `json:"seen"`
	}
	if json.Unmarshal(snapshot.Checkpoint, &header) != nil || header.Format != commandFormat || header.Document != s.owner.Key.ArtifactID || !mooring.ValidWireIdentifier(header.Generation) || header.Revision != snapshot.Revision || header.EngineIdentity.Revision != commandEngineRevision || header.EngineIdentity.Patch != commandClockPatch || header.EngineIdentity.Catalog != "wordcraft-c1-10-v1" {
		return false
	}
	if op != "" {
		stored, ok := header.Seen[op]
		a, ae := mooring.CanonicalJSON(stored.Result)
		b, be := mooring.CanonicalJSON(receipt)
		return ok && ae == nil && be == nil && bytes.Equal(a, b)
	}
	return true
}

func (s *collaborationCommandStore) insertCheckpoint(ctx context.Context, tx *sql.Tx, value commandSQLSnapshot) error {
	var count int
	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(decoded_bytes),0) FROM mooring_collab_checkpoints WHERE page_id=?`, s.owner.Key.ArtifactID).Scan(&count, &total); err != nil {
		return err
	}
	if count >= commandOperationLimit+1 || total+int64(len(value.Checkpoint)) > 64<<20 {
		return mooring.ErrBatchCapacity
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO mooring_collab_checkpoints(page_id,workspace_id,revision,checkpoint_base64,decoded_bytes,checkpoint_generation) VALUES(?,?,?,?,?,?)`, s.owner.Key.ArtifactID, s.owner.Key.WorkspaceID, strconv.FormatUint(value.Revision, 10), base64.StdEncoding.EncodeToString(value.Checkpoint), len(value.Checkpoint), strconv.FormatInt(s.owner.Generation, 10))
	return err
}

func commandInput(raw []byte, participant string) (op string, base uint64, canonical []byte, err error) {
	if len(raw) == 0 || len(raw) > commandInputLimit {
		return "", 0, nil, errCommandStore
	}
	canonical, err = mooring.CanonicalJSON(raw)
	if err != nil {
		return "", 0, nil, err
	}
	var fields map[string]json.RawMessage
	var actor, generation string
	if json.Unmarshal(canonical, &fields) != nil {
		return "", 0, nil, errCommandStore
	}
	allowed := map[string]bool{"op_id": true, "generation": true, "participant": true, "base_revision": true, "target": true, "command": true, "params": true}
	for key := range fields {
		if !allowed[key] {
			return "", 0, nil, errCommandStore
		}
	}
	var command string
	if json.Unmarshal(fields["command"], &command) != nil || command == "" || len(command) > 64 || fields["params"] == nil {
		return "", 0, nil, errCommandStore
	}
	if json.Unmarshal(canonical, &fields) != nil || json.Unmarshal(fields["op_id"], &op) != nil || json.Unmarshal(fields["participant"], &actor) != nil || json.Unmarshal(fields["generation"], &generation) != nil || json.Unmarshal(fields["base_revision"], &base) != nil || actor != participant || !mooring.ValidWireIdentifier(op) || !mooring.ValidWireIdentifier(generation) {
		return "", 0, nil, errCommandStore
	}
	return op, base, canonical, nil
}

func (s *collaborationCommandStore) admission(ctx context.Context, tx *sql.Tx, original collaborationHumanAdmission, writer bool) (commandHostAdmission, error) {
	s.humans.mu.Lock()
	current, exists := s.humans.admissions[original.participant]
	s.humans.mu.Unlock()
	if !exists || current != original {
		return commandHostAdmission{}, errCommandStore
	}
	if _, err := s.humans.checkCaptured(ctx, tx, original, writer, 0); err != nil {
		return commandHostAdmission{}, err
	}
	return commandHostAdmission{TransportParticipant: original.participant, Author: original.source.UserID, Epoch: uint64(original.memberRevision), Writer: writer && original.write}, nil
}

func (s *collaborationCommandStore) lookup(ctx context.Context, tx *sql.Tx, op string) (*commandSQLOutcome, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT result_json FROM mooring_collab_operations WHERE page_id=? AND op_id=?`, s.owner.Key.ArtifactID, op).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out commandSQLOutcome
	if len(raw) > commandInputLimit*4 || json.Unmarshal([]byte(raw), &out) != nil || out.OpID != op || !commandDigestValid(out.Fingerprint) || !commandDigestValid(out.InputDigest) || !json.Valid(out.Receipt) {
		return nil, errCommandStore
	}
	var receipt struct {
		State       string `json:"state"`
		OpID        string `json:"op_id"`
		Revision    uint64 `json:"revision"`
		Fingerprint string `json:"fingerprint"`
	}
	if json.Unmarshal(out.Receipt, &receipt) != nil || receipt.State != "host_committed" || receipt.OpID != out.OpID || receipt.Revision != out.Revision || receipt.Fingerprint != out.Fingerprint {
		return nil, errCommandStore
	}
	return &out, nil
}

// Submit holds the original ingress/runtime operation throughout native Stage,
// final SQL COMMIT and Confirm. No client can call the SQL commit half alone.
func (s *collaborationCommandStore) Submit(r *http.Request, original collaborationHumanAdmission, actor commandEngineActorBinding, raw []byte) (commandSQLOutcome, error) {
	var outcome commandSQLOutcome
	if actor.transportParticipant != original.participant || actor.sourceUser != original.source.UserID || !mooring.ValidWireIdentifier(actor.actor) {
		return outcome, errCommandStore
	}
	return s.submit(s.humanOperation(r, original), actor.actor, raw)
}

func (s *collaborationCommandStore) submit(source commandOperationSource, actor string, raw []byte) (commandSQLOutcome, error) {
	var outcome commandSQLOutcome
	op, base, canonical, err := commandInput(raw, actor)
	if err != nil {
		return outcome, err
	}
	s.mu.Lock()
	if s.busy || s.suspended {
		s.mu.Unlock()
		return outcome, errCommandBusy
	}
	s.busy = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.busy = false; s.mu.Unlock() }()
	err = source.run(func(ctx context.Context) error {
		var snapshot commandSQLSnapshot
		var trusted commandHostAdmission
		var known *commandSQLOutcome
		_, e := s.transaction(ctx, func(tx *sql.Tx) error {
			var e error
			trusted, e = source.admit(ctx, tx, true)
			if e != nil {
				return e
			}
			if snapshot, e = s.snapshot(ctx, tx); e != nil {
				return e
			}
			known, e = source.lookup(ctx, tx, op)
			if e == nil && known != nil && known.InputDigest != commandDigest(canonical) {
				return errCommandConflict
			}
			return e
		})
		if e != nil {
			return e
		}
		if known != nil {
			outcome = *known
			return nil
		}
		if base != snapshot.Revision {
			return errCommandConflict
		}
		trusted.Time = time.Now().UTC().Format("2006-01-02T15:04:05Z")
		trusted.Participant = actor
		stageBase := snapshot
		stageBase.Checkpoint = append([]byte(nil), snapshot.Checkpoint...)
		candidate, e := s.port.Stage(ctx, trusted, stageBase, append([]byte(nil), canonical...))
		if e != nil {
			if errors.Is(e, errCommandRefused) {
				return e
			}
			s.suspend()
			return errCommandUnknown // The port may already own an unacknowledged K.
		}
		if len(candidate.Checkpoint) > commandCheckpointLimit {
			if s.port.Reject(ctx, candidate) != nil {
				s.suspend()
			}
			return mooring.ErrBatchCapacity
		}
		if len(candidate.Receipt) > commandInputLimit*3 {
			s.suspend()
			return mooring.ErrBatchCapacity
		}
		// SQL validation and persistence own their bounded bytes even if the
		// port reuses its receive buffer. Handle stays opaque on the port.
		candidate.Checkpoint = append([]byte(nil), candidate.Checkpoint...)
		candidate.Receipt = append([]byte(nil), candidate.Receipt...)
		if e = s.validateCandidate(candidate, snapshot, trusted, op, canonical); e != nil {
			s.suspend()
			return e
		}
		outcome = commandSQLOutcome{OpID: op, Fingerprint: candidate.Fingerprint, InputDigest: commandDigest(canonical), Revision: candidate.Revision, Receipt: append([]byte(nil), candidate.Receipt...)}
		uncertain, commitErr := s.transaction(ctx, func(tx *sql.Tx) error {
			fresh, e := source.admit(ctx, tx, true)
			if e != nil {
				return e
			}
			if fresh.TransportParticipant != trusted.TransportParticipant || fresh.Author != trusted.Author || fresh.Epoch != trusted.Epoch {
				return errCommandStore
			}
			current, e := s.snapshot(ctx, tx)
			if e != nil {
				return e
			}
			if current.Revision != snapshot.Revision || !bytes.Equal(current.Checkpoint, snapshot.Checkpoint) {
				return errCommandConflict
			}
			if prior, e := source.lookup(ctx, tx, op); e != nil || prior != nil {
				if e != nil {
					return e
				}
				return errCommandConflict
			}
			if e = s.persist(ctx, tx, source.participant, snapshot, candidate, outcome); e != nil {
				return e
			}
			_, e = source.admit(ctx, tx, true)
			return e
		})
		if commitErr != nil {
			if uncertain {
				s.suspend()
				outcome.Receipt = nil
				return errCommandUnknown
			}
			if s.port.Reject(ctx, candidate) != nil {
				s.suspend()
			}
			outcome = commandSQLOutcome{}
			return commitErr
		}
		// SQL is durable. An ACK failure must NEVER reject K or resubmit.
		_, e = s.transaction(ctx, func(tx *sql.Tx) error {
			fresh, err := source.admit(ctx, tx, true)
			if err != nil {
				return err
			}
			fresh.Time = trusted.Time // immutable clock installed before Stage
			fresh.Participant = actor
			if fresh != trusted {
				return errCommandStore
			}
			return nil
		})
		if e != nil {
			s.suspend()
			outcome.Receipt = nil
			return errCommandUnknown
		}
		if e = s.port.Confirm(ctx, candidate, trusted); e != nil {
			s.suspend()
			outcome.Receipt = nil
			return errCommandUnknown
		}
		return nil
	})
	return outcome, err
}

func (s *collaborationCommandStore) suspend() { s.mu.Lock(); s.suspended = true; s.mu.Unlock() }

func (s *collaborationCommandStore) validateCandidate(c commandSQLCandidate, base commandSQLSnapshot, a commandHostAdmission, op string, original []byte) error {
	if c.OpID != op || c.Document != s.owner.Key.ArtifactID || !mooring.ValidWireIdentifier(c.Generation) || c.Format != commandFormat || c.EngineRevision != commandEngineRevision || c.EngineSHA256 != s.engineSHA || c.BaseRevision != base.Revision || c.Revision != base.Revision+1 || c.Revision == 0 || c.BaseDigest != commandDigest(base.Checkpoint) || c.Digest != commandDigest(c.Checkpoint) || !commandDigestValid(c.Fingerprint) || len(c.Receipt) == 0 || len(c.Receipt) > commandInputLimit*3 || !s.validCheckpoint(commandSQLSnapshot{c.Revision, c.Checkpoint}, op, c.Receipt) {
		return errCommandStore
	}
	var input, checkpoint struct {
		Generation string `json:"generation"`
	}
	if json.Unmarshal(original, &input) != nil || json.Unmarshal(c.Checkpoint, &checkpoint) != nil || c.Generation != input.Generation || c.Generation != checkpoint.Generation {
		return errCommandStore
	}
	var receipt struct {
		State, OpID, Actor, Author, Time, Fingerprint string
		Revision                                      uint64
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(c.Receipt, &fields) != nil {
		return errCommandStore
	}
	for key, target := range map[string]*string{"state": &receipt.State, "op_id": &receipt.OpID, "actor": &receipt.Actor, "author": &receipt.Author, "command_time": &receipt.Time, "fingerprint": &receipt.Fingerprint} {
		if json.Unmarshal(fields[key], target) != nil {
			return errCommandStore
		}
	}
	if json.Unmarshal(fields["revision"], &receipt.Revision) != nil || receipt.State != "host_committed" || receipt.OpID != op || receipt.Revision != c.Revision || receipt.Actor != a.Participant || receipt.Author != a.Author || receipt.Time != a.Time || receipt.Fingerprint != c.Fingerprint {
		return errCommandStore
	}
	return nil
}

func (s *collaborationCommandStore) persist(ctx context.Context, tx *sql.Tx, participant string, base commandSQLSnapshot, c commandSQLCandidate, out commandSQLOutcome) error {
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	key := s.owner.Key
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM mooring_collab_operations WHERE page_id=?`, key.ArtifactID).Scan(&count); err != nil {
		return err
	}
	if count >= commandOperationLimit {
		return mooring.ErrBatchCapacity
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO mooring_collab_changes(page_id,workspace_id,change_id,membership_hash,result_json) VALUES(?,?,?,?,?)`, key.ArtifactID, key.WorkspaceID, c.OpID, out.InputDigest, string(raw)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO mooring_collab_operations(page_id,workspace_id,op_id,change_id,participant_ref,fingerprint,result_json) VALUES(?,?,?,?,?,?,?)`, key.ArtifactID, key.WorkspaceID, c.OpID, c.OpID, participant, out.Fingerprint, string(raw)); err != nil {
		return err
	}
	if err = s.insertCheckpoint(ctx, tx, commandSQLSnapshot{c.Revision, c.Checkpoint}); err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, `UPDATE mooring_collab_documents SET revision=?,checkpoint_base64=? WHERE page_id=? AND workspace_id=? AND application_id='wordcraft' AND format_profile=? AND engine_revision=? AND engine_sha256=? AND revision=? AND checkpoint_base64=?`, strconv.FormatUint(c.Revision, 10), base64.StdEncoding.EncodeToString(c.Checkpoint), key.ArtifactID, key.WorkspaceID, commandFormat, commandEngineRevision, s.engineSHA, strconv.FormatUint(c.BaseRevision, 10), base64.StdEncoding.EncodeToString(base.Checkpoint))
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil || n != 1 {
		return errCommandConflict
	}
	// Verify the actual rows after SQL trigger effects, not merely our INSERT
	// arguments. SQL P and the original immutable receipt must describe exactly
	// the staged candidate before the final current-rights/owner fences commit.
	var exact bool
	err = tx.QueryRowContext(ctx, `SELECT d.revision=? AND d.checkpoint_base64=? AND o.participant_ref=? AND o.fingerprint=? AND o.result_json=? AND c.membership_hash=? AND c.result_json=? AND p.checkpoint_base64=? AND p.decoded_bytes=? FROM mooring_collab_documents d JOIN mooring_collab_operations o ON o.page_id=d.page_id JOIN mooring_collab_changes c ON c.page_id=d.page_id AND c.change_id=o.change_id JOIN mooring_collab_checkpoints p ON p.page_id=d.page_id WHERE d.page_id=? AND o.op_id=? AND c.change_id=? AND p.revision=?`, strconv.FormatUint(c.Revision, 10), base64.StdEncoding.EncodeToString(c.Checkpoint), participant, out.Fingerprint, string(raw), out.InputDigest, string(raw), base64.StdEncoding.EncodeToString(c.Checkpoint), len(c.Checkpoint), key.ArtifactID, c.OpID, c.OpID, strconv.FormatUint(c.Revision, 10)).Scan(&exact)
	if err != nil {
		return err
	}
	if !exact {
		return errCommandStore
	}
	return nil
}

// Reconcile is read-only SQL lookup plus trusted restore of canonical P. It
// never calls Stage/Confirm/Reject and never replays an operation. The caller
// must retain op/fingerprint from the unknown result of Submit.
func (s *collaborationCommandStore) Reconcile(r *http.Request, original collaborationHumanAdmission, op, fingerprint string) (*commandSQLOutcome, error) {
	return s.reconcile(s.humanOperation(r, original), op, fingerprint, "")
}

// If Stage's transport failed before returning a native fingerprint, the SQL
// commit half was never entered. Reconcile using the retained ORIGINAL input
// digest instead, without reconstructing a command or invoking the engine.
func (s *collaborationCommandStore) ReconcileOriginal(r *http.Request, original collaborationHumanAdmission, actor commandEngineActorBinding, raw []byte) (*commandSQLOutcome, error) {
	if actor.transportParticipant != original.participant || actor.sourceUser != original.source.UserID || !mooring.ValidWireIdentifier(actor.actor) {
		return nil, errCommandStore
	}
	op, _, canonical, err := commandInput(raw, actor.actor)
	if err != nil {
		return nil, err
	}
	return s.reconcile(s.humanOperation(r, original), op, "", commandDigest(canonical))
}

func (s *collaborationCommandStore) reconcile(source commandOperationSource, op, fingerprint, inputDigest string) (*commandSQLOutcome, error) {
	if !mooring.ValidWireIdentifier(op) || (fingerprint == "" && inputDigest == "") || (fingerprint != "" && !commandDigestValid(fingerprint)) || (inputDigest != "" && !commandDigestValid(inputDigest)) {
		return nil, errCommandStore
	}
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return nil, errCommandBusy
	}
	restore := s.suspended
	s.busy = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.busy = false; s.mu.Unlock() }()
	var out *commandSQLOutcome
	err := source.run(func(ctx context.Context) error {
		var snapshot commandSQLSnapshot
		_, err := s.transaction(ctx, func(tx *sql.Tx) error {
			if _, e := source.admit(ctx, tx, false); e != nil {
				return e
			}
			var e error
			if snapshot, e = s.snapshot(ctx, tx); e != nil {
				return e
			}
			if out, e = source.lookup(ctx, tx, op); e != nil {
				return e
			}
			if out != nil && ((fingerprint != "" && out.Fingerprint != fingerprint) || (inputDigest != "" && out.InputDigest != inputDigest)) {
				return errCommandConflict
			}
			return nil
		})
		if err != nil {
			return err
		}
		if restore {
			if err = s.port.RestoreConfirmed(ctx, snapshot); err != nil {
				return errCommandUnknown
			}
		}
		_, err = s.transaction(ctx, func(tx *sql.Tx) error {
			if _, e := source.admit(ctx, tx, false); e != nil {
				return e
			}
			current, e := s.snapshot(ctx, tx)
			if e != nil {
				return e
			}
			if current.Revision != snapshot.Revision || !bytes.Equal(current.Checkpoint, snapshot.Checkpoint) {
				return errCommandUnknown
			}
			return nil
		})
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.suspended = false
		s.mu.Unlock()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, err
}
