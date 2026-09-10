package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Run identity — E0 (docs/prd/WEBHOOKS-AGENT-PARALLELISM-MEMORY-1-0-2026-09-10.md
// §4, IMPLEMENTATION §7).
//
// A run id names ONE ATTEMPT at a piece of work. It is the same namespace as
// the journal's trace_id / the old agent_runs.id — see internal/work/work.go's
// package comment for the three identities (work_id, run_id, session_id) and
// why they must not be conflated. Nothing here mints a fourth identifier.
//
// Every dispatch path already mints this id before it calls the orchestrator
// (webhook, chatbridge, scheduler, assignments, peer query) and writes it into
// the journal. AgentRunRequest.RunID is how that id finally reaches the code
// that derives runtime paths from it, so two runs of the SAME agent stop
// writing over each other's tmux session, args file, env file, FIFO and exit
// file.

// validRunIDRe gates every RunID before it is interpolated into an
// in-container path (/tmp/agent-<slug>-<runID>.args, /workspace/<slug>/<runID>),
// a tmux session name, or a shell command — the same duty validSlugRe
// (orchestrator.go) does for AgentSlug, and for the same reason: those strings
// are single-quoted into `sh -c` scripts, so a quote or a shell metacharacter
// in one of them is a command-injection primitive, and a "/" or ".." is a path
// traversal.
//
// The charset is deliberately narrower than the slug's: every id generator
// that feeds this field emits [a-z0-9_] only — internal/api's generateCUID
// ("c" + base36 + hex), internal/scheduler's generateID ("sched_<nanos>_<hex>"),
// internal/chatbridge's generateMsgID ("msg_<nanos>_<hex>") and NewRunID below
// — so nothing legitimate is turned away. "." and ":" are excluded because tmux
// itself parses them as session/window/pane separators.
var validRunIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

// ValidRunID reports whether runID is safe to interpolate into a session name
// or a container path. Exported so a caller that resolves a run id from
// somewhere other than its own dispatch (the terminal attach handler, the
// Tier 2 hard stop) can check before building a name from it.
func ValidRunID(runID string) bool { return validRunIDRe.MatchString(runID) }

// NewRunID mints a run id for a dispatch path that genuinely has none of its
// own.
//
// This is THE fallback, and it is a single named function on purpose: grep for
// it and you have the complete list of places where a run is not correlated
// with a journal trace anyone else minted. Every other path must pass the id it
// already created (AgentRunRequest.RunID) rather than call this. Falling back
// to the agent slug — which is what the code did before E0 by deriving every
// runtime path from the slug alone — is never an option: the slug is shared by
// every concurrent run of that agent, which is the whole defect.
func NewRunID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// A deterministic, still-unique-per-nanosecond fallback beats a panic
		// on a path whose only job is to name a run.
		return "run_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return "run_" + strconv.FormatInt(time.Now().UnixNano(), 10) + "_" + hex.EncodeToString(b)
}

// ensureRunID normalises req.RunID before anything derives a path or a session
// name from it.
//
// Empty is tolerated and filled in (an old caller, or an internal path with no
// journal trace of its own) — with a log line, because a run that mints its own
// id is a run nothing else can correlate. A NON-EMPTY but unsafe id is a hard
// error rather than a silent replacement: replacing it would give the container
// paths one id while the journal, the idempotency table and the run row all
// carry another, and the resulting run would be untraceable in exactly the
// situation where someone is looking.
func (o *Orchestrator) ensureRunID(req *AgentRunRequest) error {
	if req.RunID == "" {
		req.RunID = NewRunID()
		o.logger.Info("agent run dispatched without a run id; minted one",
			"agent_id", req.AgentID, "agent_slug", req.AgentSlug, "run_id", req.RunID)
		return nil
	}
	if !ValidRunID(req.RunID) {
		return fmt.Errorf("invalid run id: %q", req.RunID)
	}
	return nil
}

// RunIDsFromSessionNames picks agentSlug's live run ids out of the raw output
// of provider.TmuxListSessionsCmd (one session name per line).
//
// This is the resolution step E0 forces on every caller that holds a slug and
// needs a run: attach (internal/terminal) and Tier 2 hard stop (internal/api).
// Neither may pick one silently when several come back — attaching to, or
// killing, the wrong run of an agent looks exactly like doing it to the right
// one until it is too late — so this returns ALL of them and leaves the choice
// (or the refusal) to the caller.
//
// A name whose tail is not a well-formed run id is dropped rather than
// returned: tmux sessions in a crew container are not exclusively ours (a user
// can start one from the terminal), and a stray "agent-eva-my notes" must not
// become something a caller then interpolates back into a shell command.
// Sorted so a message listing the candidates is stable to read and to test.
func RunIDsFromSessionNames(out, agentSlug string) []string {
	if agentSlug == "" {
		return nil
	}
	prefix := TmuxSessionPrefix(agentSlug)
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		id := name[len(prefix):]
		if !ValidRunID(id) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
