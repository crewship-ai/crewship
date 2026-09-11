package sidecar

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Per-run identity state — E0, with durable revocation (R7).
//
// The sidecar's roster (IPCConfig.AgentToken + crewMembers) is minted once, by
// whichever run happened to start the sidecar, and never refreshed: the restart
// predicate (orchestrator's sidecarNeedsRestart) looks at the credential set,
// the network mode and the egress domains, and at nothing about who is running.
// That is fine for a roster of AGENTS, which changes rarely — and impossible
// for a roster of RUNS, which changes constantly.
//
// So runs are not rostered. A run token verifies against the crew's run key
// (internaltoken.ValidateAgentRunToken), and this registry answers only the
// question a MAC cannot: is that run still current?
//
// # Why this used to be wrong (R7)
//
// The registry was purely in-memory and an UNKNOWN run was ACCEPTED. The crew
// run key is a deterministic derivation of (master, workspace, crew) and a v2
// token carries no expiry, so a token stayed cryptographically valid forever.
// End run A, restart the sidecar, and the new registry has never heard of A:
// the token still verifies, "unknown" still meant "current", and A was admitted
// again. Revocation did not survive a restart of the process that performed it.
//
// # What answers it now
//
// Three sources, in descending order of authority:
//
//  1. A DURABLE JOURNAL of start/end records, replayed before this registry
//     answers its first question (ensureDurable). A revocation recorded by one
//     sidecar process is therefore still a revocation for the next one in the
//     same container. This is the half that closes the named acceptance case —
//     end A, restart, A refused, B unaffected — and it needs no host at all.
//
//  2. An AUTHORITY (the work-ledger owner) consulted for a run the journal
//     cannot account for, and periodically for one it can. This is the only
//     thing that can distinguish "a run that started after I booted" from "a
//     run that ended while I was not running and whose journal I lost", and the
//     only thing that catches a LOST run-end notification — the end notice is
//     delivered best-effort (`curl … || true`, orchestrator sidecarRunEndScript)
//     and nothing local ever learns it was dropped.
//
//  3. Failing both: the declared policy below. It is written down rather than
//     defaulted, because both defaults are wrong in an interesting way.
//
// # Explicit policy when the authority cannot answer
//
// Fail-open restores exactly the bug above. Fail-closed on a transient blip
// kills every live run in the container. So the two cases are separated by
// whether the registry holds POSITIVE EVIDENCE for the run:
//
//   - A run this registry has seen live — seeded at boot, announced by the
//     orchestrator, replayed from the journal, or confirmed by the authority —
//     keeps being admitted while the authority is unreachable, indefinitely. A
//     host outage must not terminate work that is demonstrably running. Its
//     revocation is not weakened by this: an ENDED run is refused on local
//     evidence alone and no authority failure can override that.
//
//   - A run with NO evidence at all is the ambiguous one. It is admitted only
//     within runAuthorityGrace of the authority first becoming unreachable, and
//     refused after that. A blip does not strand a run that is starting; a
//     sustained outage does not become an open door.
//
// Expiry is deliberately NOT used as the mechanism. runAuthorityVerdictTTL
// bounds how long a POSITIVE verdict is reused, which narrows the window in
// which a lost end notification goes unnoticed; it does not close it, and it is
// not what revokes anything. Revocation is immediate and durable: end() refuses
// the very next call and every call after any number of restarts.
//
// Nothing here touches key derivation. Rotating the crew run key would
// invalidate every live run of the crew at once, which is precisely the
// coordination-free disconnect this must not cause, so revocation is expressed
// as registry state and never as a key change.
type runRegistry struct {
	mu   sync.RWMutex
	runs map[string]*runState

	// verifiedAt is the last time the AUTHORITY confirmed a run is an active
	// attempt. Kept beside runs rather than inside runState so a verdict can be
	// recorded for a run whose descriptive state (agent, slug, chat) is filled
	// in separately by registerRunFromToken.
	verifiedAt map[string]time.Time

	// authority is the work-ledger owner, or nil when none is configured. See
	// hostRunAuthority for why it is off unless explicitly enabled.
	authority runAuthority

	// authorityDownSince is when the authority first failed to answer, zero
	// while it is answering. It is the clock the grace window is measured on.
	authorityDownSince time.Time

	journal  *runJournal
	logger   *slog.Logger
	initOnce sync.Once

	// now is the clock, swappable in tests so the grace window and the verdict
	// TTL can be exercised without sleeping.
	now func() time.Time
}

// runState is what the sidecar knows about one run beyond what its token
// proves. ChatID is the interesting field: it is the per-request context that
// replaces the boot-frozen s.ipc.ChatID on every route that stamps a chat onto
// an escalation, a peer query or an issue.
type runState struct {
	AgentID   string
	AgentSlug string
	ChatID    string
	MissionID string
	StartedAt time.Time
	EndedAt   time.Time // zero while live
}

func newRunRegistry() *runRegistry {
	return &runRegistry{
		runs:       make(map[string]*runState),
		verifiedAt: make(map[string]time.Time),
		now:        time.Now,
	}
}

// start records a run as live. Idempotent: the orchestrator may retry, and a
// repeat must not resurrect a run that has since ended — re-starting an ended
// run id would be indistinguishable from a replay of a stale notification.
func (r *runRegistry) start(runID string, st runState) {
	if runID == "" {
		return
	}
	r.mu.Lock()
	if existing, ok := r.runs[runID]; ok && !existing.EndedAt.IsZero() {
		r.mu.Unlock()
		return
	}
	st.StartedAt = r.now()
	r.runs[runID] = &st
	j := r.journal
	r.mu.Unlock()

	j.append(runRecord{Op: runRecordStart, RunID: runID, AgentID: st.AgentID,
		AgentSlug: st.AgentSlug, ChatID: st.ChatID, MissionID: st.MissionID, At: st.StartedAt})
}

// end marks a run finished. Recorded even for a run this sidecar never saw
// start: the end notification is the authoritative one, and a run that started
// before this sidecar booted must still become un-current when it finishes.
//
// The durable append happens on the way out, so a revocation is on disk before
// the caller is told it succeeded. A failed append is logged and the in-memory
// revocation still stands — degrading to the pre-R7 behaviour for the next
// process is worse than refusing to revoke at all now.
func (r *runRegistry) end(runID string) {
	if runID == "" {
		return
	}
	r.mu.Lock()
	st, ok := r.runs[runID]
	if !ok {
		st = &runState{}
		r.runs[runID] = st
	}
	if st.EndedAt.IsZero() {
		st.EndedAt = r.now()
	}
	delete(r.verifiedAt, runID)
	endedAt := st.EndedAt
	j := r.journal
	r.mu.Unlock()

	j.append(runRecord{Op: runRecordEnd, RunID: runID, At: endedAt})
}

// current reports whether runID may still act, consulting the authority when
// local state cannot answer. See the type comment for the full policy.
func (r *runRegistry) current(ctx context.Context, runID string) bool {
	ok, _ := r.decide(ctx, runID)
	return ok
}

// decide is current() plus the reason, which is what gets logged and what the
// tests assert on: "accepted" and "refused" are each reachable four different
// ways and a bare bool cannot tell them apart.
func (r *runRegistry) decide(ctx context.Context, runID string) (bool, string) {
	if runID == "" {
		return false, "empty run id"
	}

	r.mu.RLock()
	st, known := r.runs[runID]
	var ended bool
	if known {
		ended = !st.EndedAt.IsZero()
	}
	verified := r.verifiedAt[runID]
	authority := r.authority
	r.mu.RUnlock()

	// (1) Local revocation. Unconditional, and deliberately checked before
	// anything that can fail: an ended run is refused whether or not the
	// authority is reachable, and no later branch can re-admit it.
	if known && ended {
		return false, "run has ended"
	}

	// (2) No authority configured: local state is all there is. A known-live
	// run is admitted; an unknown one is admitted for the reason the original
	// E0 comment gives — a dropped control message must not look like a forged
	// token — but it is now RECORDED (registerRunFromToken) and its later
	// revocation is durable, which is the part that was missing.
	if authority == nil {
		if known {
			return true, "known live run; no authority configured"
		}
		return true, "unknown run admitted; no authority configured"
	}

	// (3) A recently confirmed run needs no round trip.
	if known && !verified.IsZero() && r.now().Sub(verified) < runAuthorityVerdictTTL {
		return true, "authority verdict still within its validity window"
	}

	active, err := authority.runIsActive(ctx, runID)
	switch {
	case err == nil && active:
		r.mu.Lock()
		r.verifiedAt[runID] = r.now()
		r.authorityDownSince = time.Time{}
		r.mu.Unlock()
		return true, "authority confirms an active attempt"

	case err == nil && !active:
		// The authority is the work ledger. "Not an active attempt" covers the
		// ended run whose end notification was lost, the run of a work item
		// that was cancelled, and the run that never existed. All three are
		// revocations, and all three are recorded durably so the next process
		// refuses without asking again.
		r.mu.Lock()
		r.authorityDownSince = time.Time{}
		r.mu.Unlock()
		r.end(runID)
		r.logf("run refused: the work ledger does not list it as an active attempt", "run_id", runID)
		return false, "authority reports no active attempt"
	}

	// (4) The authority could not answer. Split by evidence — see the policy in
	// the type comment.
	now := r.now()
	r.mu.Lock()
	if r.authorityDownSince.IsZero() {
		r.authorityDownSince = now
	}
	downSince := r.authorityDownSince
	r.mu.Unlock()

	if known {
		r.logf("run authority unreachable; keeping a known-live run admitted",
			"run_id", runID, "error", err)
		return true, "authority unreachable; run has local evidence of being live"
	}
	if now.Sub(downSince) < runAuthorityGrace {
		r.logf("run authority unreachable; admitting an unverified run inside the grace window",
			"run_id", runID, "error", err)
		return true, "authority unreachable; inside the grace window for an unverified run"
	}
	r.logf("run authority unreachable beyond the grace window; refusing an unverified run",
		"run_id", runID, "error", err)
	return false, "authority unreachable beyond the grace window; run has no local evidence"
}

// lookup returns what is known about a run, and whether anything is.
func (r *runRegistry) lookup(runID string) (runState, bool) {
	if runID == "" {
		return runState{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	st, ok := r.runs[runID]
	if !ok {
		return runState{}, false
	}
	return *st, true
}

func (r *runRegistry) logf(msg string, args ...any) {
	r.mu.RLock()
	l := r.logger
	r.mu.RUnlock()
	if l != nil {
		l.Warn(msg, args...)
	}
}

// runRegistryMaxEntries caps the map. A sidecar lives as long as its crew
// container, which on a busy workspace can be days, and every run leaves an
// entry — so without a cap this grows without bound for the same reason the
// orchestrator's tmux cache needed one.
const runRegistryMaxEntries = 4096

// sweep drops ended runs once the map grows past the cap, oldest-ended first.
// LIVE runs are never dropped: forgetting a live run would downgrade it to
// "unknown", and forgetting its ChatID would silently re-point its escalations
// at the boot chat — the exact bug per-run context exists to fix.
//
// Dropping an ended run forgets a revocation, which is a real (bounded) hole:
// past the cap, the oldest revocation stops being enforced locally. It is the
// same trade the cap itself makes, it is why the authority exists, and it is
// why the oldest — not the newest — goes first.
func (r *runRegistry) sweep() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.runs) <= runRegistryMaxEntries {
		return
	}
	var oldest string
	var oldestAt time.Time
	for len(r.runs) > runRegistryMaxEntries {
		oldest, oldestAt = "", time.Time{}
		for id, st := range r.runs {
			if st.EndedAt.IsZero() {
				continue
			}
			if oldest == "" || st.EndedAt.Before(oldestAt) {
				oldest, oldestAt = id, st.EndedAt
			}
		}
		if oldest == "" {
			return // everything left is live; nothing safe to drop
		}
		delete(r.runs, oldest)
		delete(r.verifiedAt, oldest)
	}
}

// runEndPath is the loopback endpoint that marks a run finished.
//
// It is a ROUTE, not a push from crewshipd, because there is no inbound
// channel: the sidecar listens on 127.0.0.1:9119 INSIDE the crew container and
// crewshipd runs outside it. Every existing crewshipd→container interaction is
// an exec, and this is one too — the orchestrator delivers a one-line curl on
// an exec's stdin at run end (notifyRunEnded, internal/orchestrator).
// Under /agent rather than a namespace of its own because
// llmroute.ReservedPathSegments() is the list of top-level segments a provider
// route may not shadow, and "agent" is already on it. A fresh segment would
// have to be added there — in a package this change does not own — and until it
// was, a provider prefix could shadow the route that ends runs.
//
// Kept in step with the literal in buildHandler's switch by
// TestRunEndPathMatchesTheRoute.
const runEndPath = "/agent/run/end"

// ---------------------------------------------------------------------------
// Durable journal
// ---------------------------------------------------------------------------

const (
	// runStateDirEnv overrides where the journal lives. Production does not set
	// it; it exists so a test gets its own directory instead of sharing the
	// container-shaped default.
	runStateDirEnv = "CREWSHIP_SIDECAR_STATE_DIR"

	// defaultRunStateDir is inside the crew container. /tmp is what survives a
	// sidecar RESTART — the launch script explicitly contemplates "a sidecar
	// restarted inside a long-lived crew container" — and dies with the
	// container, which is the correct lifetime: the runs it describes cannot
	// outlive the container either.
	//
	// The directory is created 0700 owned by the sidecar UID (1002). /tmp is
	// sticky (1777), so the agent at UID 1001 can neither read the journal nor
	// unlink the directory out from under it; without the sticky bit a
	// world-writable parent would let the agent delete the file and un-revoke
	// its own finished run's token.
	defaultRunStateDir = "/tmp/crewship-sidecar-state"

	// runJournalMaxRecords triggers compaction. The file is append-only, so a
	// long-lived container with thousands of runs would otherwise grow a file
	// that is replayed in full at every restart.
	runJournalMaxRecords = 8192
)

type runRecordOp string

const (
	runRecordStart runRecordOp = "start"
	runRecordEnd   runRecordOp = "end"
)

// runRecord is one line of the journal. JSON lines, because a torn final line
// from a killed process must be skippable without losing everything before it.
type runRecord struct {
	Op        runRecordOp `json:"op"`
	RunID     string      `json:"run"`
	AgentID   string      `json:"agent,omitempty"`
	AgentSlug string      `json:"slug,omitempty"`
	ChatID    string      `json:"chat,omitempty"`
	MissionID string      `json:"mission,omitempty"`
	At        time.Time   `json:"at"`
}

// runJournal is the append-only file behind the registry. A nil *runJournal is
// a working no-op, which is what a sidecar with no container identity gets.
type runJournal struct {
	path   string
	logger *slog.Logger

	mu      sync.Mutex
	records int
	broken  bool // a write already failed; stop logging the same error per call
}

// runJournalPath names the file for (container, crew). Both are required: the
// journal describes the runs of one crew inside one container, and a sidecar
// that cannot name both — a test server, an embedded instance — is not in a
// deployment whose filesystem outlives it, so it keeps the in-memory registry
// and writes nothing.
func runJournalPath(ipc *IPCConfig) string {
	if ipc == nil || ipc.ContainerID == "" || ipc.CrewID == "" {
		return ""
	}
	dir := os.Getenv(runStateDirEnv)
	if dir == "" {
		dir = defaultRunStateDir
	}
	return filepath.Join(dir, safeStateName(ipc.ContainerID)+"."+safeStateName(ipc.CrewID)+".runs")
}

// safeStateName reduces a host-supplied id to something that cannot escape the
// state directory or collide with a path separator. Ids are opaque to us, so
// this is validation, not cosmetics.
func safeStateName(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	var b strings.Builder
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unnamed"
	}
	return b.String()
}

// ensureDurable attaches the journal and the authority, replaying the journal
// before the registry answers its first admission question.
//
// Called from identityForRunToken — the ONLY caller of current() — rather than
// from NewServer, so the replay is guaranteed to precede any decision this
// registry makes for a run token, which is the ordering the requirement
// ("restore the registry and its revocations before serving") actually asks
// for. Once per process.
func (r *runRegistry) ensureDurable(ipc *IPCConfig, logger *slog.Logger) {
	r.initOnce.Do(func() {
		r.mu.Lock()
		r.logger = logger
		r.mu.Unlock()

		if path := runJournalPath(ipc); path != "" {
			j := &runJournal{path: path, logger: logger}
			if err := j.open(); err != nil {
				if logger != nil {
					logger.Error("run journal unavailable; run revocations will not survive a sidecar restart",
						"path", path, "error", err)
				}
			} else {
				r.replay(j)
			}
		}
		if a := newHostRunAuthority(ipc, logger); a != nil {
			r.mu.Lock()
			r.authority = a
			r.mu.Unlock()
		}
	})
}

// replay merges the journal into the in-memory registry and then writes back
// anything memory knows that the journal does not (the boot run, seeded by
// NewServer before this ran). END always wins over START regardless of order,
// for the same reason start() refuses to resurrect an ended run.
func (r *runRegistry) replay(j *runJournal) {
	records, err := j.load()
	if err != nil && j.logger != nil {
		j.logger.Error("run journal partially unreadable; replaying what parsed",
			"path", j.path, "error", err)
	}

	r.mu.Lock()
	seen := make(map[string]bool, len(records))
	live, ended := 0, 0
	for _, rec := range records {
		if rec.RunID == "" {
			continue
		}
		seen[rec.RunID] = true
		st, ok := r.runs[rec.RunID]
		if !ok {
			st = &runState{
				AgentID:   rec.AgentID,
				AgentSlug: rec.AgentSlug,
				ChatID:    rec.ChatID,
				MissionID: rec.MissionID,
				StartedAt: rec.At,
			}
			r.runs[rec.RunID] = st
		}
		if rec.Op == runRecordEnd && st.EndedAt.IsZero() {
			st.EndedAt = rec.At
			if st.EndedAt.IsZero() {
				st.EndedAt = r.now()
			}
		}
	}
	for id, st := range r.runs {
		if st.EndedAt.IsZero() {
			live++
		} else {
			ended++
		}
		// A verdict from a previous process is not inherited: the authority is
		// re-asked once per process per run.
		delete(r.verifiedAt, id)
	}
	var backfill []runRecord
	for id, st := range r.runs {
		if seen[id] {
			continue
		}
		backfill = append(backfill, runRecord{Op: runRecordStart, RunID: id, AgentID: st.AgentID,
			AgentSlug: st.AgentSlug, ChatID: st.ChatID, MissionID: st.MissionID, At: st.StartedAt})
	}
	r.journal = j
	r.mu.Unlock()

	for _, rec := range backfill {
		j.append(rec)
	}
	if j.logger != nil {
		j.logger.Info("run journal replayed", "path", j.path, "live", live, "revoked", ended)
	}
}

// open creates the state directory and the file, and counts what is already
// there so compaction has a starting point.
func (j *runJournal) open() error {
	if err := os.MkdirAll(filepath.Dir(j.path), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}
	return f.Close()
}

// load reads every parsable record. A truncated or corrupt line is skipped
// rather than fatal: the process that wrote it may have been killed mid-write,
// and the records before it are still good revocations.
func (j *runJournal) load() ([]runRecord, error) {
	f, err := os.Open(j.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var (
		out     []runRecord
		skipped int
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec runRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			skipped++
			continue
		}
		out = append(out, rec)
	}
	j.mu.Lock()
	j.records = len(out) + skipped
	j.mu.Unlock()
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return out, err
	}
	if skipped > 0 {
		return out, fmt.Errorf("%d unparsable journal line(s) skipped", skipped)
	}
	return out, nil
}

// append writes one record and fsyncs it. Nil-receiver safe: a registry with no
// journal calls this on every start/end.
//
// fsync on every record is affordable because records are per-RUN, not per
// request, and a revocation that is only in the page cache is exactly the
// revocation a crashing process loses.
func (j *runJournal) append(rec runRecord) {
	if j == nil || rec.RunID == "" {
		return
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		j.fail("append to run journal", err)
		return
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		j.fail("write run journal record", err)
		return
	}
	if err := f.Sync(); err != nil {
		f.Close()
		j.fail("sync run journal", err)
		return
	}
	if err := f.Close(); err != nil {
		j.fail("close run journal", err)
		return
	}
	j.records++
	j.broken = false
}

// fail logs once per failure streak. A journal that has gone read-only would
// otherwise log on every run start for the life of the container.
func (j *runJournal) fail(what string, err error) {
	if j.broken || j.logger == nil {
		j.broken = true
		return
	}
	j.broken = true
	j.logger.Error("run journal write failed; revocations may not survive a restart",
		"op", what, "path", j.path, "error", err)
}

// compact rewrites the journal from the registry's current state, dropping
// records for runs the registry no longer holds. Called after sweep, which is
// the only thing that removes entries.
//
// Rename onto the same path inside the same directory, so a crash leaves either
// the old complete file or the new complete file and never a half-written one.
func (r *runRegistry) compact() {
	r.mu.RLock()
	j := r.journal
	snapshot := make([]runRecord, 0, len(r.runs)*2)
	for id, st := range r.runs {
		snapshot = append(snapshot, runRecord{Op: runRecordStart, RunID: id, AgentID: st.AgentID,
			AgentSlug: st.AgentSlug, ChatID: st.ChatID, MissionID: st.MissionID, At: st.StartedAt})
		if !st.EndedAt.IsZero() {
			snapshot = append(snapshot, runRecord{Op: runRecordEnd, RunID: id, At: st.EndedAt})
		}
	}
	r.mu.RUnlock()
	if j == nil {
		return
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	if j.records <= runJournalMaxRecords {
		return
	}

	tmp := j.path + ".compact"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		j.fail("open compaction temp", err)
		return
	}
	w := bufio.NewWriter(f)
	for _, rec := range snapshot {
		line, err := json.Marshal(rec)
		if err != nil {
			continue
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			f.Close()
			os.Remove(tmp)
			j.fail("write compaction temp", err)
			return
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		j.fail("flush compaction temp", err)
		return
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		j.fail("sync compaction temp", err)
		return
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		j.fail("close compaction temp", err)
		return
	}
	if err := os.Rename(tmp, j.path); err != nil {
		os.Remove(tmp)
		j.fail("rename compacted journal", err)
		return
	}
	j.records = len(snapshot)
}

// ---------------------------------------------------------------------------
// Authority
// ---------------------------------------------------------------------------

const (
	// runAuthorityVerdictTTL is the validity window of a POSITIVE verdict: how
	// long "the ledger says this run is an active attempt" is reused before it
	// is asked again. It bounds how long a LOST run-end notification goes
	// unnoticed — one minute — and it is not what revokes anything. An explicit
	// end() is immediate and does not wait for a TTL to lapse.
	runAuthorityVerdictTTL = 60 * time.Second

	// runAuthorityGrace is how long a run with NO local evidence is admitted
	// while the authority is unreachable, measured from the first failure. Long
	// enough to cover a crewshipd restart or a redeploy; short enough that a
	// sustained outage does not become the pre-R7 "unknown means current".
	runAuthorityGrace = 2 * time.Minute

	// runAuthorityTimeout bounds one probe. The sidecar is on the request path
	// of an agent's call, so this cannot be generous.
	runAuthorityTimeout = 5 * time.Second

	// runAuthorityEnv turns the host authority on. It is OFF by default and
	// that is a deliberate, temporary state — see newHostRunAuthority.
	runAuthorityEnv = "CREWSHIP_SIDECAR_RUN_AUTHORITY"

	// runAuthorityPathFmt is the contract the host must serve. GET, with the
	// sidecar's crew-bound internal token, answering 200 with
	// {"run_id": "...", "active": <bool>} where `active` is true only while the
	// run is the live attempt of a live work item — the same predicate
	// MemoryMutationHandler.authorizeRun already applies against work_attempts
	// / work_items.
	runAuthorityPathFmt = "/api/v1/internal/runs/%s/status"
)

// runAuthority answers, for the owner of the work ledger, whether a run is
// still an active attempt. An error means "could not answer" and is never an
// answer: see the policy in the runRegistry type comment.
type runAuthority interface {
	runIsActive(ctx context.Context, runID string) (active bool, err error)
}

// hostRunAuthority asks crewshipd over the sidecar's existing outbound IPC
// channel. There is no inbound channel, so this is a pull.
//
// # Why it is opt-in
//
// The route above DOES NOT EXIST YET. Building it belongs to internal/api,
// which this change does not own, and until it lands the sidecar cannot safely
// enable the probe — not because a missing route is hard to detect, but because
// it is UNDETECTABLE here by design: internal/api's serveInternal answers an
// unregistered path and a refused caller with the same byte-identical JSON 404,
// precisely so the internal surface cannot be mapped. So a 404 is either "this
// host predates the endpoint" or "this sidecar's token was revoked", and the
// two demand opposite responses.
//
// Enabling the probe by default would therefore pick one of two bad outcomes on
// every crew today: treat 404 as "unsupported" and a revoked sidecar token
// silently disables revocation checking, or treat it as unreachable and every
// unverified run in every crew is refused once the grace window lapses.
//
// So: the client is complete, the contract is written down, the policy is
// implemented and tested, and the switch is off until the host serves the
// route. Flip CREWSHIP_SIDECAR_RUN_AUTHORITY=1 then; nothing else changes.
type hostRunAuthority struct {
	baseURL string
	token   string
	client  *http.Client
}

func newHostRunAuthority(ipc *IPCConfig, logger *slog.Logger) runAuthority {
	if ipc == nil || ipc.BaseURL == "" || ipc.Token == "" {
		return nil
	}
	if os.Getenv(runAuthorityEnv) != "1" {
		return nil
	}
	if logger != nil {
		logger.Info("run authority enabled", "base_url", ipc.BaseURL)
	}
	return &hostRunAuthority{
		baseURL: strings.TrimSuffix(ipc.BaseURL, "/"),
		token:   ipc.Token,
		client:  &http.Client{Timeout: runAuthorityTimeout},
	}
}

func (a *hostRunAuthority) runIsActive(ctx context.Context, runID string) (bool, error) {
	url := a.baseURL + fmt.Sprintf(runAuthorityPathFmt, runID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("X-Internal-Token", a.token)
	req.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		// Every non-200 is "could not answer", INCLUDING 404. See the type
		// comment: a 404 from this surface is indistinguishable from a refused
		// caller, and reading it as "no such run" would let anyone who can
		// break the sidecar's credential revoke every run in the crew.
		return false, fmt.Errorf("%w: run authority answered %d", errRunAuthorityUnanswered, resp.StatusCode)
	}
	var body struct {
		RunID  string `json:"run_id"`
		Active bool   `json:"active"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&body); err != nil {
		return false, fmt.Errorf("%w: decode run authority answer: %v", errRunAuthorityUnanswered, err)
	}
	if body.RunID != "" && body.RunID != runID {
		return false, fmt.Errorf("%w: run authority answered for %q, asked about %q",
			errRunAuthorityUnanswered, body.RunID, runID)
	}
	return body.Active, nil
}

// errRunAuthorityUnanswered marks a failure to OBTAIN a verdict, as opposed to
// a negative verdict. The distinction is the whole of the unreachable-host
// policy, so it is a sentinel rather than a bare error string.
var errRunAuthorityUnanswered = errors.New("run authority did not answer")
