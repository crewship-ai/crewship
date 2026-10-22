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
}
type Authorize func(context.Context, *sql.Tx) (Principal, error)

type Command struct {
	Kind     string          `json:"kind"`
	Sheet    string          `json:"sheet,omitempty"`
	Cell     string          `json:"cell,omitempty"`
	Input    string          `json:"input,omitempty"`
	Snapshot json.RawMessage `json:"snapshot,omitempty"`
}
type Request struct {
	MutationID string  `json:"mutation_id"`
	MandateID  string  `json:"mandate_id"`
	BaseRev    int64   `json:"base_rev"`
	Generation int64   `json:"generation"`
	Command    Command `json:"command"`
}
type State struct {
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
}

func hash(data []byte) string { s := sha256.Sum256(data); return "sha256:" + hex.EncodeToString(s[:]) }
func NewCandidate(b Binding, data json.RawMessage, effects []string) Candidate {
	return Candidate{b, append(json.RawMessage(nil), data...), hash(data), append([]string(nil), effects...)}
}

var cellPattern = regexp.MustCompile(`^[A-Z]{1,3}[1-9][0-9]{0,6}$`)

func (c Command) validate() error {
	switch c.Kind {
	case "import":
		if len(c.Snapshot) == 0 || len(c.Snapshot) > MaxSnapshotBytes || !json.Valid(c.Snapshot) || c.Sheet != "" || c.Cell != "" || c.Input != "" {
			return ErrInvalid
		}
	case "cell.set":
		if !cellPattern.MatchString(c.Cell) || c.Sheet == "" || len(c.Sheet) > 128 || len(c.Input) > 8192 || len(c.Snapshot) != 0 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func bounded(s string) bool { return len(s) > 0 && len(s) <= 256 }
func (r Request) validate(k Key) error {
	if !bounded(k.WorkspaceID) || !bounded(k.PageID) || !bounded(r.MutationID) || !bounded(r.MandateID) || r.BaseRev < 0 || r.Generation < 0 {
		return ErrInvalid
	}
	if r.BaseRev == 0 && (r.Command.Kind != "import" || r.Generation != 0) {
		return ErrPrecondition
	}
	if r.BaseRev > 0 && r.Generation < 1 {
		return ErrPrecondition
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
	if c.Kind == "import" {
		return []string{"*"}
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

func recordCommand(c Command) (string, []byte, error) {
	record := commandRecord{Kind: "snapshot"}
	switch c.Kind {
	case "import":
	case "cell.set":
		record = commandRecord{Kind: "cells", Cells: []CellInput{{c.Sheet, c.Cell, c.Input}}}
	default:
		return "", nil, ErrInvalid
	}
	data, err := json.Marshal(record)
	return record.Kind, data, err
}

// writerOf identifies who wrote a revision for cell-level concurrency.
func writerOf(p Principal, _ Request) string { return p.AuthorityID }

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
