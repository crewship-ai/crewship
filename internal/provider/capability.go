package provider

// Per-crew capability reporting.
//
// CrewConfig is a request, not a command: it carries twenty fields, and any
// given provider acts on a subset of them. Before this file the leftovers were
// simply dropped — no log, no warning, no error — so a caller could not tell
// that most of what it asked for had been thrown away (#1648). The worst case
// was NetworkMode: "restricted", the create-time database default, which the
// crews row, the dashboard and `crewship crew get` all keep reporting while
// nothing enforces it on a provider that has no proxy to enforce it with.
//
// The shape here generalises the one-field precedent in
// internal/chatbridge/bridge.go, which type-asserts SidecarProvider and tells
// the user "Sidecar services declared but provider doesn't support them yet".
//
// Two classes:
//
//   - Degraded — the crew loses a capability. It still starts; the drop is
//     logged by the provider, surfaced to the user by the caller, and — for
//     anything the product reports back as a live setting — reflected in the
//     read surfaces via Drop (see internal/api/crew_egress_enforcement.go).
//   - Refused — the crew does not start at all.
//
// NOTHING IS CLASSIFIED Refused TODAY, and that is a decision rather than an
// oversight: the product is meant to run on every platform it can, so a
// provider that cannot apply a setting reports it instead of blocking the
// crew. The class and its error machinery stay because a future provider may
// hit something it genuinely cannot proceed past, and because the distinction
// is what makes "reported but unenforced" a describable state rather than an
// accident. Before adding to Refused, be sure the answer is not "run it and
// tell every surface the truth" — that is what Degraded plus Drop is for.
//
// Reporting is worth nothing if a read surface goes on repeating the
// configured value as though it were the effective one. Degraded is therefore
// only half the mechanism; Drop is the other half, and it is what the crew
// read paths use so the effective state cannot drift from what the provider
// actually does.
//
// Providers consult their own report inside EnsureCrewRuntime rather than
// leaving it to the eight call sites, because "a caller forgot to check" is the
// defect being fixed, not a mechanism to reproduce.

import (
	"errors"
	"fmt"
	"strings"
)

// ErrCrewConfigRefused is the sentinel every capability refusal wraps, so
// callers can branch on errors.Is rather than sniffing substrings out of a
// wrapped provider error.
var ErrCrewConfigRefused = errors.New("crew config refused by container provider")

// DroppedField names one CrewConfig field a provider will not act on for one
// specific crew, and says what that costs.
//
// Detail is written for an operator reading a log line with nothing else in
// front of them: what is lost, and what they can do instead. Value carries the
// requested value when naming it helps ("crewship-cache:abc123", "restricted")
// and is empty for boolean fields where the field name already says it.
type DroppedField struct {
	Field  string
	Value  string
	Detail string
}

// String renders one report line: "NetworkMode=restricted: <detail>".
func (d DroppedField) String() string {
	head := d.Field
	if d.Value != "" {
		head += "=" + d.Value
	}
	if d.Detail == "" {
		return head
	}
	return head + ": " + d.Detail
}

// CrewConfigSupport is a provider's answer, for one crew, to "what of this
// request can I not honour?". The zero value means "all of it" and is what a
// provider that has not opted in is taken to mean.
type CrewConfigSupport struct {
	Refused  []DroppedField
	Degraded []DroppedField
}

// Empty reports whether the whole config is honoured.
func (s CrewConfigSupport) Empty() bool { return len(s.Refused) == 0 && len(s.Degraded) == 0 }

// Fields is the comma-joined field names across both classes, refused first —
// the compact form for a single structured log attribute.
func (s CrewConfigSupport) Fields() string {
	names := make([]string, 0, len(s.Refused)+len(s.Degraded))
	for _, f := range s.Refused {
		names = append(names, f.Field)
	}
	for _, f := range s.Degraded {
		names = append(names, f.Field)
	}
	return strings.Join(names, ",")
}

// Drop returns the report entry for one CrewConfig field name, and whether the
// provider drops it at all. It searches both classes, because a caller asking
// "is this setting in effect?" gets the same answer either way — a field the
// provider refuses is no more applied than one it degrades.
//
// This is the accessor the read paths use to derive a setting's EFFECTIVE
// state from the same data the provider acts on at create time, so the two can
// never disagree. Re-deriving the rule at the read site — "apple plus
// restricted means unenforced" — would be a second copy that drifts the first
// time a provider changes what it supports.
func (s CrewConfigSupport) Drop(field string) (DroppedField, bool) {
	for _, f := range s.Refused {
		if f.Field == field {
			return f, true
		}
	}
	for _, f := range s.Degraded {
		if f.Field == field {
			return f, true
		}
	}
	return DroppedField{}, false
}

// DegradedMessages renders the non-fatal drops, one line each.
func (s CrewConfigSupport) DegradedMessages() []string {
	if len(s.Degraded) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.Degraded))
	for _, f := range s.Degraded {
		out = append(out, f.String())
	}
	return out
}

// CrewConfigRefusedError is the typed refusal. It keeps the structured fields
// rather than only a rendered string so a caller can build its own message —
// chatbridge names the field and value to the user without re-parsing prose,
// which is what the substring classifier next to it has to do for daemon
// errors it does not own.
type CrewConfigRefusedError struct {
	Provider string
	Refused  []DroppedField
}

func (e *CrewConfigRefusedError) Error() string {
	lines := make([]string, 0, len(e.Refused))
	for _, f := range e.Refused {
		lines = append(lines, f.String())
	}
	return fmt.Sprintf("%s: the %s provider cannot apply %s, and the crew is not being started because "+
		"the rest of the system reports it as applied — %s",
		ErrCrewConfigRefused, e.Provider, e.FieldList(), strings.Join(lines, "; "))
}

// Unwrap makes errors.Is(err, ErrCrewConfigRefused) work through any number of
// %w wrappings on the way up from the provider.
func (e *CrewConfigRefusedError) Unwrap() error { return ErrCrewConfigRefused }

// FieldList is the comma-joined names of the refused fields.
func (e *CrewConfigRefusedError) FieldList() string {
	names := make([]string, 0, len(e.Refused))
	for _, f := range e.Refused {
		if f.Value != "" {
			names = append(names, f.Field+"="+f.Value)
			continue
		}
		names = append(names, f.Field)
	}
	return strings.Join(names, ", ")
}

// RefusedError turns the refused entries into the error EnsureCrewRuntime
// returns, or nil when there are none. providerName is the runtime the
// operator would recognise ("apple-container"), so the message says which
// provider to move off.
func (s CrewConfigSupport) RefusedError(providerName string) error {
	if len(s.Refused) == 0 {
		return nil
	}
	return &CrewConfigRefusedError{Provider: providerName, Refused: s.Refused}
}

// CrewConfigReporter is the optional capability a container provider
// implements to declare, per crew, which CrewConfig fields it will drop.
//
// It takes the config rather than being a static per-provider capability set
// because the answer depends on the values: "free" egress is honoured by every
// provider (there is nothing to enforce), "restricted" is honoured by one. A
// static set would either flag every crew on a provider for a field most of
// them never set, or say nothing useful.
//
// A provider that does not implement it is read as honouring what it is given
// — the docker provider's position, and the reason this is an optional
// interface next to SidecarProvider and ServiceLister rather than a method on
// ContainerProvider that ~50 test fakes would have to grow.
type CrewConfigReporter interface {
	UnsupportedCrewConfig(cfg CrewConfig) CrewConfigSupport
}

// InspectCrewConfigSupport asks any provider what it would drop for this crew,
// returning an empty report for providers that do not answer (and for nil).
// Callers use it to SURFACE the report; they do not have to act on it, because
// the provider already refuses what must be refused.
func InspectCrewConfigSupport(p any, cfg CrewConfig) CrewConfigSupport {
	if p == nil {
		return CrewConfigSupport{}
	}
	r, ok := p.(CrewConfigReporter)
	if !ok {
		return CrewConfigSupport{}
	}
	return r.UnsupportedCrewConfig(cfg)
}
