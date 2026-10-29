package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
)

// ErrPrivateRunUnqualified deliberately carries no child-controlled message
// and no wrapped diagnostic. It is not proof of a terminal permission denial.
var ErrPrivateRunUnqualified = errors.New("private runtime outcome requires qualified inspection")

func privateRunError(req AgentRunRequest, err error) error {
	if req.strictContentPrivacy && err != nil {
		return ErrPrivateRunUnqualified
	}
	return err
}

// WithStrictContentPrivacy selects host-side output suppression for a trusted
// caller. It is not a complete private execution environment: native CLI disk
// history, tools, container files and their readouts need separate qualification.
// The unexported bit cannot be populated or cleared by JSON request fields.
func (r AgentRunRequest) WithStrictContentPrivacy() AgentRunRequest {
	r.strictContentPrivacy = true
	r.SuppressSessionStream = true
	r.SkipConvHistory = true
	r.MemoryEnabled = false
	return r
}

// privateRunLogger is request-local sink selection, never a mutation of the
// orchestrator's shared logger. Helpers retain the selection after detachment.
var privateRunLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func (o *Orchestrator) loggerForRun(req AgentRunRequest) *slog.Logger {
	if req.strictContentPrivacy {
		return privateRunLogger
	}
	return o.logger
}

// journalForRun is the publication boundary for private runtime metadata.
// Provider errors, command arguments and child-derived strings never reach
// the generic journal, including direct emission paths before stream setup.
func (o *Orchestrator) journalForRun(req AgentRunRequest) JournalEmitter {
	j := o.getJournal()
	if req.strictContentPrivacy {
		return privateRunJournal{j}
	}
	return j
}

type privateRunJournal struct{ downstream JournalEmitter }

func (j privateRunJournal) Emit(ctx context.Context, e JournalEntry) (string, error) {
	e.Summary = "Private agent runtime metadata"
	fields := make(map[string]any)
	for _, key := range []string{"duration_ms", "exit_code", "refused", "cancelled", "detached", "stopped", "confirmed_gone", "in_band_error"} {
		switch value := e.Payload[key].(type) {
		case bool, int, int64:
			fields[key] = value
		}
	}
	e.Payload = fields
	e.Refs = nil
	return j.downstream.Emit(ctx, e)
}
