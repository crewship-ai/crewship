package notifyroute

import (
	"context"
	"database/sql"
	"sync"

	"github.com/crewship-ai/crewship/internal/notify"
)

// SourceDeriver rebuilds the message of one outbox row whose producer keeps
// its own durable record — neither an inbox item nor a journal entry — so the
// recovery sweep can retry it the same way it retries those two.
//
// It returns the message to deliver (an empty Title keeps the row's stored
// title). An error wrapping sql.ErrNoRows means the source is gone or no
// longer wants this delivery (the incident was resolved, the channel was
// taken off the route): the sweep marks the row failed with the error's text,
// so it ages out instead of being retried forever. Any other error is
// transient and leaves the row for the next sweep.
type SourceDeriver func(ctx context.Context, db *sql.DB, d Delivery) (notify.CategoryMessage, error)

// SourceGone is the error a SourceDeriver returns to give a row up: it
// matches sql.ErrNoRows and reads as reason alone, which is what the
// delivery log records.
func SourceGone(reason string) error { return sourceGoneError(reason) }

type sourceGoneError string

func (e sourceGoneError) Error() string        { return string(e) }
func (e sourceGoneError) Is(target error) bool { return target == sql.ErrNoRows }

var sourceDerivers sync.Map // source_kind -> SourceDeriver

// RegisterSourceDeriver makes outbox rows of sourceKind recoverable.
// Registering the same kind again replaces the deriver, so wiring it from
// every router a process (or a test) builds is harmless.
//
// A registry rather than an import because the producers (the backup
// scheduler first) sit above this package: notifyroute cannot import them
// without a cycle, and hardcoding their tables here is how the journal case
// came to be special-cased.
func RegisterSourceDeriver(sourceKind string, fn SourceDeriver) {
	if sourceKind == "" || fn == nil {
		return
	}
	sourceDerivers.Store(sourceKind, fn)
}

func sourceDeriverFor(sourceKind string) (SourceDeriver, bool) {
	v, ok := sourceDerivers.Load(sourceKind)
	if !ok {
		return nil, false
	}
	return v.(SourceDeriver), true
}
