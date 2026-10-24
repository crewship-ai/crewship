// Package appengine prepares offline workbook candidates while Go alone commits
// authorized revisions. It installs no public routes or agent credentials.
package appengine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const MaxSnapshotBytes = 1 << 20

// MaxEngineOutputBytes bounds a raw engine file before compaction. The engine
// writes indented JSON, several times larger than the stored compact snapshot.
const MaxEngineOutputBytes = 4 * MaxSnapshotBytes
const SchemaVersion = 1

var (
	ErrEngineUnavailable = errors.New("isolated workbook engine unavailable")
	ErrDisabled          = errors.New("workbook type disabled")
	ErrForbidden         = errors.New("workbook access denied")
	ErrNotFound          = errors.New("workbook not found")
	ErrInvalid           = errors.New("invalid workbook operation")
	ErrConflict          = errors.New("operation key payload conflict")
	ErrPrecondition      = errors.New("precondition_failed")
	ErrInProgress        = errors.New("in_progress")
	ErrCapacity          = errors.New("capacity_exhausted")
	ErrFence             = errors.New("candidate writer fenced")
	ErrCandidate         = errors.New("invalid candidate binding")
	ErrEffects           = errors.New("effect_out_of_mandate")
	ErrQuota             = errors.New("workbook history quota exceeded")
)

// ErrUnauthenticated means the caller's credential itself stopped being valid,
// for example a token revoked while its operation was running.
var ErrUnauthenticated = errors.New("workbook credential no longer valid")

type Key struct {
	WorkspaceID string `json:"workspace_id"`
	PageID      string `json:"artifact_id"`
}

// Principal is returned by trusted host integration inside the writer transaction.
// Targets are immutable mandate bounds, never caller-supplied permission flags.
// The integration must recheck live credentials, run generation, current Page ACL,
// ownership, private audience and retained intent through the provided transaction.
type Principal struct {
	AuthorityID, MandateID string
	Targets                []string
	// HistoryFloor is the oldest revision whose successors the principal may
	// read through the commits feed: an agent mandate's base revision. Zero
	// leaves the feed bounded by retained history only.
	HistoryFloor int64
}
type Authorize func(context.Context, *sql.Tx) (Principal, error)

type Command struct {
	Kind     string          `json:"kind"`
	Sheet    string          `json:"sheet,omitempty"`
	Cell     string          `json:"cell,omitempty"`
	Input    string          `json:"input,omitempty"`
	Snapshot json.RawMessage `json:"snapshot,omitempty"`
	// Cells is the atomic multi-cell form ("cells.set") used by human edits.
	Cells []CellInput `json:"cells,omitempty"`
}
type Request struct {
	MutationID string   `json:"mutation_id"`
	MandateID  string   `json:"mandate_id"`
	SessionID  string   `json:"session_id,omitempty"`
	BaseRev    int64    `json:"base_rev"`
	Generation int64    `json:"generation"`
	Command    Command  `json:"command"`
	ReadSet    *ReadSet `json:"read_set,omitempty"`
}
type State struct {
	StructureRev  int64           `json:"structure_rev"`
	SchemaVersion int             `json:"schema_version"`
	Rev           int64           `json:"rev"`
	Generation    int64           `json:"generation"`
	Snapshot      json.RawMessage `json:"snapshot"`
}
type OperationKey struct {
	ArtifactID  string `json:"artifact_id"`
	AuthorityID string `json:"authority_id"`
	MutationID  string `json:"mutation_id"`
}
type Binding struct {
	Key         Key    `json:"key"`
	BaseRev     int64  `json:"base_rev"`
	Generation  int64  `json:"generation"`
	CommandHash string `json:"command_hash"`
	MandateID   string `json:"mandate_id"`
	Engine      string `json:"engine"`
}
type Candidate struct {
	Binding    Binding
	Snapshot   json.RawMessage
	ResultHash string
	Effects    []string
	// Derived is set when verified recalculation changed cells outside Effects.
	Derived *Derived
}
type Receipt struct {
	OperationKey OperationKey `json:"operation_key"`
	Outcome      string       `json:"outcome"`
	Rev          int64        `json:"rev"`
	PayloadHash  string       `json:"payload_hash"`
	IssuedBy     string       `json:"issued_by"`
	ValidatedBy  string       `json:"validated_by"`
	Effects      []string     `json:"effects"`
	Durability   string       `json:"durability"`
	At           string       `json:"at"`
}

// Engine is operator-selected compiled code, not installed by artifact content.
// Prepare must honor cancellation, isolate/discard its candidate, and report ALL
// effects. Isolation is not proof of the engine's semantic correctness.
type Engine interface {
	Identity() string
	Prepare(context.Context, Binding, State, Command) (Candidate, error)
}
type Options struct {
	Enabled                                       bool
	MaxWorkers, MaxWorkspaceWorkers, MaxRevisions int
	MaxHistoryBytes                               int64
	LeaseDuration                                 time.Duration

	// Editor is the operator switch of the live editing surface (editor
	// sessions, human cell edits and the commits feed). The store ignores it;
	// the host additionally requires the workspace flag gridcraft_editor.
	Editor bool
}

func hash(data []byte) string { s := sha256.Sum256(data); return "sha256:" + hex.EncodeToString(s[:]) }
func NewCandidate(b Binding, data json.RawMessage, effects []string) Candidate {
	return Candidate{Binding: b, Snapshot: append(json.RawMessage(nil), data...), ResultHash: hash(data), Effects: append([]string(nil), effects...)}
}

var cellPattern = regexp.MustCompile(`^[A-Z]{1,3}[1-9][0-9]{0,6}$`)

func (c Command) validate() error {
	switch c.Kind {
	case "import":
		if len(c.Snapshot) == 0 || len(c.Snapshot) > MaxSnapshotBytes || !json.Valid(c.Snapshot) || c.Sheet != "" || c.Cell != "" || c.Input != "" {
			return ErrInvalid
		}
	case "cell.set":
		if !validCell(c.Sheet, c.Cell, c.Input) || len(c.Snapshot) != 0 || len(c.Cells) != 0 {
			return ErrInvalid
		}
	case "cells.set":
		// One sheet per mutation: the engine edits the active sheet.
		if len(c.Cells) == 0 || len(c.Cells) > MaxEditCells || c.Sheet != "" || c.Cell != "" || c.Input != "" || len(c.Snapshot) != 0 {
			return ErrInvalid
		}
		seen := map[string]bool{}
		for _, cell := range c.Cells {
			if !validCell(cell.Sheet, cell.Cell, cell.Input) || cell.Sheet != c.Cells[0].Sheet || seen[cell.Cell] {
				return ErrInvalid
			}
			seen[cell.Cell] = true
		}
	default:
		return ErrInvalid
	}
	return nil
}

// HumanMandatePrefix marks the mandates of human operations. Agent mandates
// may not use it.
const HumanMandatePrefix = "human-"

// HumanEditMandate is the mandate of human cell edits, distinct from imports.
const HumanEditMandate = HumanMandatePrefix + "edit"

// EditOp is one operation of a human edit. Only "cell.set" is supported.
type EditOp struct {
	Kind  string `json:"kind"`
	Sheet string `json:"sheet"`
	Cell  string `json:"cell"`
	Input string `json:"input"`
}

// Edit is the public body of a human cell edit. The client session is the
// writer for cell-level concurrency, and the mutation ID is "<session_id>:<seq>".
type Edit struct {
	SessionID  string   `json:"session_id"`
	MutationID string   `json:"mutation_id"`
	BaseRev    int64    `json:"base_rev"`
	Generation int64    `json:"generation"`
	Ops        []EditOp `json:"ops"`
}

// sessionPattern bounds a client session ID to an opaque token. It never holds
// the ":" that separates it from the sequence in a mutation ID, nor "#",
// whitespace or control characters that would blur the recorded writer.
var sessionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// ValidSessionID reports whether s is a well-formed client session ID.
func ValidSessionID(s string) bool { return sessionPattern.MatchString(s) }

// Request maps an edit onto one atomic cells.set mutation.
func (e Edit) Request() (Request, error) {
	if !sessionPattern.MatchString(e.SessionID) || !strings.HasPrefix(e.MutationID, e.SessionID+":") || len(e.MutationID) == len(e.SessionID)+1 || len(e.Ops) == 0 || len(e.Ops) > MaxEditCells {
		return Request{}, ErrInvalid
	}
	cells := make([]CellInput, 0, len(e.Ops))
	for _, op := range e.Ops {
		if op.Kind != "cell.set" {
			return Request{}, ErrInvalid
		}
		cells = append(cells, CellInput{Sheet: op.Sheet, Cell: op.Cell, Input: op.Input})
	}
	r := Request{MutationID: e.MutationID, MandateID: HumanEditMandate, SessionID: e.SessionID, BaseRev: e.BaseRev, Generation: e.Generation, Command: Command{Kind: "cells.set", Cells: cells}}
	if r.Command.validate() != nil || !bounded(r.MutationID) {
		return Request{}, ErrInvalid
	}
	return r, nil
}

// MaxEditCells bounds the cells of one atomic human edit.
const MaxEditCells = 64

func validCell(sheet, cell, input string) bool {
	return cellPattern.MatchString(cell) && sheet != "" && len(sheet) <= 128 && len(input) <= 8192
}
func bounded(s string) bool { return len(s) > 0 && len(s) <= 256 }
func (r Request) validate(k Key) error {
	if !bounded(k.WorkspaceID) || !bounded(k.PageID) || !bounded(r.MutationID) || !bounded(r.MandateID) || (r.SessionID != "" && !sessionPattern.MatchString(r.SessionID)) || r.BaseRev < 0 || r.Generation < 0 {
		return ErrInvalid
	}
	if r.BaseRev == 0 && (r.Command.Kind != "import" || r.Generation != 0) {
		return ErrPrecondition
	}
	if r.BaseRev > 0 && r.Generation < 1 {
		return ErrPrecondition
	}
	if err := r.ReadSet.validate(); err != nil {
		return err
	}
	return r.Command.validate()
}
func permits(p Principal, targets []string) bool {
	allowed := map[string]bool{}
	for _, v := range p.Targets {
		allowed[v] = true
	}
	for _, v := range targets {
		if !allowed["*"] && !allowed[v] {
			return false
		}
	}
	return len(targets) > 0
}
func commandTargets(c Command) []string {
	switch c.Kind {
	case "import":
		return []string{"*"}
	case "cells.set":
		targets := make([]string, 0, len(c.Cells))
		for _, cell := range c.Cells {
			targets = append(targets, cell.Sheet+"!"+cell.Cell)
		}
		return targets
	}
	return []string{c.Sheet + "!" + c.Cell}
}

// CheckpointInterval is how often a full snapshot is kept in history besides
// imports. Revisions in between are recorded only as commands.
const CheckpointInterval = 50

// CellInput is one authored cell value in a recorded command.
type CellInput struct {
	Sheet string `json:"sheet"`
	Cell  string `json:"cell"`
	Input string `json:"input"`
}

// commandRecord is the canonical history form of a confirmed command. An import
// is a snapshot revision whose content lives in its checkpoint, not here.
type commandRecord struct {
	Kind  string      `json:"kind"`
	Cells []CellInput `json:"cells,omitempty"`
}

// MaxCommandBytes bounds one recorded command, like a stored snapshot.
const MaxCommandBytes = MaxSnapshotBytes

// recordCommand returns the canonical history form of a command. It is encoded
// without HTML escaping, so "<", ">" and "&" in formulas are stored as written
// and count once against the history quota.
func recordCommand(c Command) (string, []byte, error) {
	record := commandRecord{Kind: "snapshot"}
	switch c.Kind {
	case "import":
	case "cell.set", "cells.set":
		record = commandRecord{Kind: "cells", Cells: commandCells(c)}
	default:
		return "", nil, ErrInvalid
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return "", nil, err
	}
	data := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	if len(data) > MaxCommandBytes {
		return "", nil, ErrInvalid
	}
	return record.Kind, data, nil
}

// commandCells lists the authored cells of a cell command.
func commandCells(c Command) []CellInput {
	if c.Kind == "cell.set" {
		return []CellInput{{c.Sheet, c.Cell, c.Input}}
	}
	return c.Cells
}

// MaxRebaseWindow bounds how many revisions a mutation may lag behind the head.
const MaxRebaseWindow = 256

// CellConflict is a target cell another writer changed after the base revision.
// AuthorityID is internal; the host renders only a display label.
type CellConflict struct {
	Sheet       string `json:"sheet"`
	Cell        string `json:"cell"`
	Input       string `json:"input"`
	Rev         int64  `json:"rev"`
	AuthorityID string `json:"-"`
}

// PreconditionError details a rejected base revision. It matches ErrPrecondition.
type PreconditionError struct {
	Reason           string
	HeadRev          int64
	SnapshotRequired bool
	Conflicts        []CellConflict
}

func (e *PreconditionError) Error() string        { return ErrPrecondition.Error() }
func (e *PreconditionError) Is(target error) bool { return target == ErrPrecondition }

// writerOf identifies who wrote a revision for cell-level concurrency: the
// authority, and for a human also the client session, so two windows of one
// person (or two people) are distinct writers.
func writerOf(p Principal, r Request) string {
	if r.SessionID == "" {
		return p.AuthorityID
	}
	return p.AuthorityID + "#session:" + r.SessionID
}

func payloadHash(k Key, r Request) (string, error) {
	if len(r.Command.Snapshot) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(r.Command.Snapshot))
		decoder.UseNumber()
		var snapshot any
		if err := decoder.Decode(&snapshot); err != nil {
			return "", ErrInvalid
		}
		canonical, err := json.Marshal(snapshot)
		if err != nil {
			return "", ErrInvalid
		}
		r.Command.Snapshot = canonical
	}

	b, e := json.Marshal(struct {
		Key     Key
		Request Request
	}{k, r})
	if e != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, e)
	}
	return hash(b), nil
}
