// Package craftapps holds the instance administrator's policy for the
// bundled Craft applications (Word = WordCraft, Sheet = GridCraft) and the
// separate switch for agents working in them (#5315).
//
// The policy is a desired state, stored in the database, that takes effect on
// the next request without a restart. It never deletes or rewrites a
// document. Turning an application off moves it through three modes:
//
//	open      everything is allowed
//	draining  no new document and no new editing; open sessions may keep
//	          saving until the drain deadline, so nobody loses work
//	closed    only reading, downloading, releasing a lease and confirming an
//	          earlier save; editors keep their unsaved drafts locally
//
// Reading is never gated here: a document stays readable whatever the
// policy. Whether the application can run at all (its components, platform
// and clock) is a separate, actual state; see Evaluate.
package craftapps

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Subjects of the policy.
const (
	Word   = "wordcraft"
	Sheet  = "gridcraft"
	Agents = "agents"
)

// Applications lists the bundled applications in display order.
var Applications = []string{Word, Sheet}

// Label is the product name of a subject.
func Label(subject string) string {
	switch subject {
	case Word:
		return "Word"
	case Sheet:
		return "Sheet"
	case Agents:
		return "Agents in Word and Sheet"
	}
	return subject
}

// IsSubject reports whether s names a policy subject.
func IsSubject(s string) bool { return s == Word || s == Sheet || s == Agents }

// IsApplication reports whether s names a bundled application.
func IsApplication(s string) bool { return s == Word || s == Sheet }

// Desired is the administrator's requested state.
type Desired string

const (
	Enabled  Desired = "enabled"
	Disabled Desired = "disabled"
)

// Source says who decides the desired state.
type Source string

const (
	// SourceDefault: nobody has chosen; the application is off.
	SourceDefault Source = "default"
	// SourceAdmin: an instance administrator chose it in Admin or the CLI.
	SourceAdmin Source = "admin"
	// SourceServerConfig: the server's configuration fixes it; Admin shows it
	// locked and refuses a change.
	SourceServerConfig Source = "server_config"
)

// Mode is the effective policy at one moment.
type Mode string

const (
	ModeOpen     Mode = "open"
	ModeDraining Mode = "draining"
	ModeClosed   Mode = "closed"
)

// Operation classifies a request by what it would start.
type Operation int

const (
	// OpCreate creates a new document.
	OpCreate Operation = iota
	// OpStartEdit takes or transfers write custody, or starts an agent task.
	OpStartEdit
	// OpContinue continues work already in progress: renewing a held lease,
	// saving, importing, restoring, publishing a version, deciding a proposal.
	OpContinue
	// OpAlways never loses data and is always allowed: reading, releasing a
	// lease, confirming the outcome of an earlier save.
	OpAlways
)

// Allows reports whether an operation may run in this mode.
func (m Mode) Allows(op Operation) bool {
	switch m {
	case ModeOpen:
		return true
	case ModeDraining:
		return op == OpContinue || op == OpAlways
	default:
		return op == OpAlways
	}
}

// DefaultDrain is how long open sessions may keep saving after an
// application is turned off.
const DefaultDrain = 15 * time.Minute

// MaxDrain bounds an administrator's chosen drain.
const MaxDrain = 24 * time.Hour

// Setting is one subject's stored or fixed desired state.
type Setting struct {
	Subject    string     `json:"subject"`
	Desired    Desired    `json:"desired"`
	Source     Source     `json:"source"`
	DrainUntil *time.Time `json:"drain_until,omitempty"`
	UpdatedBy  string     `json:"updated_by,omitempty"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
}

// Mode is the setting's effective mode at now.
func (s Setting) Mode(now time.Time) Mode {
	if s.Desired == Enabled {
		return ModeOpen
	}
	if s.DrainUntil != nil && now.Before(*s.DrainUntil) {
		return ModeDraining
	}
	return ModeClosed
}

// Errors returned by Set.
var (
	ErrSubject = errors.New("unknown Craft application")
	ErrDesired = errors.New("desired must be enabled or disabled")
	ErrDrain   = errors.New("drain must be between 0 and 24 hours")
	ErrLocked  = errors.New("this setting is fixed by the server configuration")
)

// Querier is satisfied by *sql.DB and *sql.Tx.
type Querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Execer is satisfied by *sql.Tx.
type Execer interface {
	Querier
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Policy reads and writes the desired state. Locked subjects come from the
// server configuration; nil means every subject is decided in Admin.
type Policy struct {
	Locked map[string]bool
	Now    func() time.Time
}

func (p *Policy) now() time.Time {
	if p != nil && p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

const timeLayout = time.RFC3339Nano

// Get returns one subject's setting.
func (p *Policy) Get(ctx context.Context, q Querier, subject string) (Setting, error) {
	if !IsSubject(subject) {
		return Setting{}, ErrSubject
	}
	if p != nil {
		if on, ok := p.Locked[subject]; ok {
			d := Disabled
			if on {
				d = Enabled
			}
			return Setting{Subject: subject, Desired: d, Source: SourceServerConfig}, nil
		}
	}
	var desired, updatedAt, updatedBy string
	var drain sql.NullString
	err := q.QueryRowContext(ctx, `SELECT desired, drain_until, updated_by, updated_at FROM craft_application_settings WHERE subject=?`, subject).Scan(&desired, &drain, &updatedBy, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Setting{Subject: subject, Desired: Disabled, Source: SourceDefault}, nil
	}
	if err != nil {
		return Setting{}, err
	}
	s := Setting{Subject: subject, Desired: Desired(desired), Source: SourceAdmin, UpdatedBy: updatedBy}
	if s.Desired != Enabled && s.Desired != Disabled {
		return Setting{}, fmt.Errorf("craft setting %s: invalid stored state", subject)
	}
	if t, e := time.Parse(timeLayout, updatedAt); e == nil {
		s.UpdatedAt = &t
	}
	if drain.Valid {
		t, e := time.Parse(timeLayout, drain.String)
		if e != nil {
			return Setting{}, fmt.Errorf("craft setting %s: invalid drain deadline", subject)
		}
		s.DrainUntil = &t
	}
	return s, nil
}

// Mode is the subject's effective mode now. A read failure closes it.
func (p *Policy) Mode(ctx context.Context, q Querier, subject string) Mode {
	s, err := p.Get(ctx, q, subject)
	if err != nil {
		return ModeClosed
	}
	return s.Mode(p.now())
}

// Set stores a desired state and returns the setting before and after. Turning
// an enabled application off starts a drain of the given length (0 closes at
// once); turning it on again ends any drain. Setting the same state again
// changes nothing and keeps a drain already running.
func (p *Policy) Set(ctx context.Context, tx Execer, subject string, desired Desired, actor string, drain time.Duration) (before, after Setting, err error) {
	if !IsSubject(subject) {
		return Setting{}, Setting{}, ErrSubject
	}
	if desired != Enabled && desired != Disabled {
		return Setting{}, Setting{}, ErrDesired
	}
	if drain < 0 || drain > MaxDrain {
		return Setting{}, Setting{}, ErrDrain
	}
	if p != nil {
		if _, ok := p.Locked[subject]; ok {
			return Setting{}, Setting{}, ErrLocked
		}
	}
	if before, err = p.Get(ctx, tx, subject); err != nil {
		return Setting{}, Setting{}, err
	}
	now := p.now()
	// Repeating the same state changes nothing, except that "disable now"
	// (no drain) during a drain ends the drain at once.
	if before.Desired == desired && before.Source == SourceAdmin && !(desired == Disabled && drain == 0 && before.Mode(now) == ModeDraining) {
		return before, before, nil
	}
	var drainUntil sql.NullString
	if desired == Disabled && before.Desired == Enabled && drain > 0 {
		drainUntil = sql.NullString{String: now.Add(drain).Format(timeLayout), Valid: true}
	}
	if len(actor) > 256 {
		actor = actor[:256]
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO craft_application_settings(subject, desired, drain_until, updated_by, updated_at) VALUES(?,?,?,?,?)
ON CONFLICT(subject) DO UPDATE SET desired=excluded.desired, drain_until=excluded.drain_until, updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
		subject, string(desired), drainUntil, actor, now.Format(timeLayout)); err != nil {
		return Setting{}, Setting{}, err
	}
	after, err = p.Get(ctx, tx, subject)
	return before, after, err
}
