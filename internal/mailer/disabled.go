package mailer

import "context"

// Disabled is the no-op fallback used when no transport is
// configured. Send always returns ErrDisabled; Configured returns
// false. Callers should treat both as "no email got sent" — the
// /forgot endpoint logs at info level and still returns 200 (no
// account enumeration).
type Disabled struct{}

// Send always returns ErrDisabled without doing any network I/O.
func (Disabled) Send(_ context.Context, _ Message) error {
	return ErrDisabled
}

// Configured is always false for the Disabled stub.
func (Disabled) Configured() bool { return false }
