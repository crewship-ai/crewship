// Package mailer provides a minimal transactional email surface for
// system-level flows (password reset, future email verification).
//
// The package intentionally exposes a tiny interface. Application code
// holds a Mailer and calls Send without knowing or caring whether the
// concrete impl is Resend, a future SMTP transport, or the Disabled
// no-op that ships when nothing is configured. The Disabled fallback
// is essential: Crewship is self-hosted, so the *most common* day-one
// state is "no email transport configured." Layered recovery (CLI
// admin reset-password) is what keeps that state operational; this
// package just makes sure callers can fire-and-forget without
// branching everywhere on "do we have email?"
package mailer

import (
	"context"
	"errors"
)

// ErrDisabled is returned by the Disabled stub. Callers that need to
// distinguish "transport not configured" from a real send failure can
// check errors.Is(err, ErrDisabled). The /forgot endpoint uses this
// to log info-level instead of error-level when no transport is wired.
var ErrDisabled = errors.New("mailer: transport not configured")

// Message is the on-the-wire payload. HTML is required; Text is
// optional but recommended (some clients strip HTML). All other
// fields (From, ReplyTo) come from the Mailer's configured defaults
// so callers don't have to thread them through.
type Message struct {
	To      string
	Subject string
	HTML    string
	Text    string
}

// Mailer is the abstraction every consumer codes against.
//
// Send is synchronous on purpose: at the volumes a self-hosted
// Crewship instance handles (one password reset every few minutes at
// peak), there's no reason to enqueue. Callers that want async
// behaviour should wrap the call in a goroutine themselves and accept
// the trade-off of losing the error.
type Mailer interface {
	// Send delivers a Message. Returns ErrDisabled when no transport
	// is configured (the Disabled stub); a non-nil non-ErrDisabled
	// error means the transport tried and failed.
	Send(ctx context.Context, msg Message) error

	// Configured reports whether a real transport is wired. The
	// /forgot endpoint uses this to decide whether to actually
	// attempt a send vs. silently no-op (the "no enumeration"
	// guarantee holds either way — the endpoint always returns 200).
	Configured() bool
}
