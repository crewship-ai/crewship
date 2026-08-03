// Package crewstart holds the one definition of "start a crew".
//
// Before this package there were thirteen. `grep EnsureCrewRuntime` found chat,
// mission dispatch, two pipeline runners, the script runner, the scheduler, the
// webhook handler, the terminal, two server routes and the orchestrator — each
// assembling its own provider.CrewConfig and each calling the provider
// directly. They did not agree, and the disagreements were invisible:
//
//   - Exactly ONE of them (chat) started the crew's declared sidecars, so a
//     crew whose manifest says `services: [redis, postgres]` ran silently
//     database-less on every headless path — issue start, scheduler, webhook,
//     pipeline, routine (#1708).
//   - Three of them never passed CachedImage, so the crew started from the bare
//     default runtime image instead of the devcontainer it had been provisioned
//     into. The web terminal — the tool an operator opens specifically to look
//     at a crew's environment — showed a toolchain that crew never had (#1717).
//
// Adding the missing calls at twelve sites would have left the next field to be
// forgotten at eleven of them. The defect is that the call sites can disagree
// at all, so the contract lives here: Starter.Start resolves what the caller
// could not, starts the runtime, and starts the sidecars, in that order, for
// everybody. A field added to the contract is added once.
//
// # Why a chokepoint and not a decorator
//
// The obvious alternative is to wrap provider.ContainerProvider once at
// bootstrap so that call sites cannot bypass the contract even in principle.
// That is what internal/admission does for the capacity gate, and the argument
// for it (provider/container.go, AdmissionGate) is a good one.
//
// It does not work here. Nine OPTIONAL capabilities are discovered by type
// assertion on the very same value — SidecarProvider, ServiceLister,
// InteractiveExecProvider, CrewContainerLookup, HostAddressProvider,
// CrewConfigReporter, CrewRuntimePruner, LegacyResourcePruner,
// LegacyResourceDetector. A wrapper either
//
//   - does not implement them, and the terminal loses ExecInteractive, `crewship
//     crew services` loses its inventory, and the admin pruners go quiet; or
//   - implements all of them by delegation, and every assertion succeeds even
//     when the wrapped provider cannot do the thing — which erases exactly the
//     degradation reporting internal/provider/capability.go exists to give
//     (the apple-container provider would start advertising sidecars it does
//     not have); or
//   - exposes Unwrap() and asks all nine assertion sites to remember to call
//     it, which is the same forget-me defect wearing a different hat.
//
// So: one function, and a test that fails when a fourteenth caller reaches past
// it (chokepoint_test.go). The guarantee is weaker than a type-level one and
// the test is what closes the gap.
//
// # Degrading cleanly
//
// SidecarProvider is optional and the apple-container provider does not
// implement it. A crew with services on such a provider starts, and the drop is
// reported — one warning per Starter, plus a Notice for callers with a user
// watching. A crew WITHOUT services never touches the sidecar path at all, so
// providers that will never support them pay nothing.
package crewstart

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/crewship-ai/crewship/internal/provider"
)

// ErrNoContainerProvider is returned when a crew start is attempted with no
// container runtime wired (`--no-docker`, tests, a daemon that could not be
// reached at boot). Callers branch on it to say "container provider not
// configured" rather than surfacing a nil-pointer panic.
var ErrNoContainerProvider = errors.New("container provider not configured")

// ErrSidecarStart wraps any failure to bring up a crew's declared sidecars.
// The crew's runtime container IS up when this is returned — the caller gets
// the container id alongside the error and decides whether a crew without its
// declared database is usable for what it was about to do. Chat says no.
var ErrSidecarStart = errors.New("start crew sidecar services")

// Completer fills in the parts of a crew's runtime contract the caller did not
// resolve itself. Implemented by internal/api.CrewConfigCompleter (reads the
// crews row: provisioned image, declared services, resource limits, mounts) and
// by CompleterFunc for callers that already hold a resolver closure.
//
// It is consulted with whatever the caller could assemble and returns the
// crew's full config; Start then takes each field from the completion ONLY
// where the caller left it empty (see mergeConfig), so a caller that resolved
// everything is never overridden and a caller that knows only the crew id gets
// the same container as chat would have started.
type Completer interface {
	CompleteCrewConfig(ctx context.Context, cfg provider.CrewConfig) (provider.CrewConfig, error)
}

// CompleterFunc adapts a resolver closure to Completer.
type CompleterFunc func(ctx context.Context, cfg provider.CrewConfig) (provider.CrewConfig, error)

// CompleteCrewConfig implements Completer.
func (f CompleterFunc) CompleteCrewConfig(ctx context.Context, cfg provider.CrewConfig) (provider.CrewConfig, error) {
	return f(ctx, cfg)
}

// Notice is a user-facing remark about a crew start that is not an error: the
// container came up, but something the crew asked for did not happen. Callers
// with a live stream (chat) render them; callers without one ignore them and
// rely on the log line Start emits regardless.
type Notice struct {
	// Kind is a stable machine-readable tag. Match on this, not on Message.
	Kind string
	// Message is one sentence, written for whoever is watching the start.
	Message string
}

// Notice kinds.
const (
	// NoticeSidecarsUnsupported: the crew declares services and the container
	// provider has no sidecar capability at all.
	NoticeSidecarsUnsupported = "sidecars_unsupported"
	// NoticeServicesUnresolved: the crew's services_json could not be decoded,
	// so the crew starts without the sidecars it declared.
	NoticeServicesUnresolved = "services_unresolved"
)

// Starter is one component's handle on the crew-start contract. Construct it
// once where the component is wired (it is cheap, and the one-time
// "provider cannot do sidecars" warning is per-Starter, not per-start).
type Starter struct {
	container provider.ContainerProvider
	completer Completer
	logger    *slog.Logger

	// warnedNoSidecars keeps the unsupported-provider warning to one line per
	// process rather than one per crew start. The Notice is still delivered
	// every time — a user watching THIS start has not seen the previous one.
	warnedNoSidecars atomic.Bool
}

// New returns a Starter. container may be nil (Start then returns
// ErrNoContainerProvider); completer may be nil (the caller's config is used
// verbatim); logger may be nil.
func New(container provider.ContainerProvider, completer Completer, logger *slog.Logger) *Starter {
	return &Starter{container: container, completer: completer, logger: logger}
}

// Container returns the underlying provider, for the many operations that are
// not "start a crew" (exec, status, stats). Nil-safe.
func (s *Starter) Container() provider.ContainerProvider {
	if s == nil {
		return nil
	}
	return s.container
}

// Configured reports whether a container runtime is wired at all.
func (s *Starter) Configured() bool { return s != nil && s.container != nil }

// Start creates-or-reuses the crew's runtime container AND brings up the
// sidecar services the crew declares. It is idempotent: both halves reattach to
// what is already running.
//
// The returned container id is valid whenever the runtime came up, including
// when the sidecar half then failed — the caller decides whether to proceed.
func (s *Starter) Start(ctx context.Context, cfg provider.CrewConfig) (string, error) {
	return s.StartNotify(ctx, cfg, nil)
}

// StartNotify is Start with a sink for the non-fatal remarks (see Notice).
// notify may be nil and must not block.
func (s *Starter) StartNotify(ctx context.Context, cfg provider.CrewConfig, notify func(Notice)) (string, error) {
	id, _, err := s.StartResolved(ctx, cfg, notify)
	return id, err
}

// StartResolved is StartNotify, additionally returning the config the crew was
// actually started with — what the caller passed, completed from the crews row.
// Callers that register the crew with the idle reaper need the EFFECTIVE
// TTLHours, not the zero they happened to pass in.
func (s *Starter) StartResolved(ctx context.Context, cfg provider.CrewConfig, notify func(Notice)) (string, provider.CrewConfig, error) {
	if s == nil || s.container == nil {
		return "", cfg, ErrNoContainerProvider
	}

	cfg = s.complete(ctx, cfg, notify)

	containerID, err := s.container.EnsureCrewRuntime(ctx, cfg)
	if err != nil {
		return "", cfg, err
	}

	// Sidecars start AFTER the runtime so the crew's bridge network exists.
	if len(cfg.Services) > 0 {
		sp, ok := s.container.(provider.SidecarProvider)
		if !ok {
			// The gap #1708's reproduction hit from the other side: a crew
			// that declares services on a provider that has none. Say so
			// rather than running database-less in silence.
			if s.logger != nil && !s.warnedNoSidecars.Swap(true) {
				s.logger.Warn("container provider does not support sidecars; declared services are dormant",
					"crew_id", cfg.ID, "crew_slug", cfg.Slug, "service_count", len(cfg.Services))
			}
			emit(notify, Notice{
				Kind:    NoticeSidecarsUnsupported,
				Message: "Sidecar services declared but this container provider doesn't support them yet",
			})
			return containerID, cfg, nil
		}
		ids, err := sp.EnsureCrewServices(ctx, cfg)
		if err != nil {
			return containerID, cfg, fmt.Errorf("%w for crew %s: %w", ErrSidecarStart, cfg.Slug, err)
		}
		if s.logger != nil {
			s.logger.Info("sidecar services ready",
				"crew_id", cfg.ID, "crew_slug", cfg.Slug, "count", len(ids))
		}
	}

	return containerID, cfg, nil
}

// complete asks the Completer for the crew's full config and merges it under
// the caller's. A completion failure is logged and the crew still starts,
// exactly as it did before this package existed, rather than a DB hiccup taking
// down every start path at once.
//
// The merge happens EVEN ON ERROR, and that is load-bearing. A completer is
// allowed to answer partially — internal/api returns the crew's full config
// alongside ErrCrewServicesUnresolved when only the services column is
// undecodable — and discarding everything it returned because part of it
// failed is how a stale services_json silently downgraded a provisioned crew to
// the default runtime image. mergeConfig only ever fills fields the caller left
// empty, so a completer that genuinely failed returns its zero value and
// contributes nothing.
func (s *Starter) complete(ctx context.Context, cfg provider.CrewConfig, notify func(Notice)) provider.CrewConfig {
	if s.completer == nil {
		return cfg
	}
	resolved, err := s.completer.CompleteCrewConfig(ctx, cfg)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("crew runtime config partially unresolved; starting with what did resolve",
				"crew_id", cfg.ID, "crew_slug", cfg.Slug, "error", err)
		}
		if len(cfg.Services) == 0 && len(resolved.Services) == 0 {
			emit(notify, Notice{
				Kind: NoticeServicesUnresolved,
				Message: "Part of the crew's runtime config could not be read; any declared sidecar " +
					"services are not being started",
			})
		}
	}
	return mergeConfig(cfg, resolved)
}

func emit(notify func(Notice), n Notice) {
	if notify != nil {
		notify(n)
	}
}

// mergeConfig takes each field from resolved only where base left it empty.
//
// "The caller wins" is the load-bearing half: chat, the dispatch path and the
// pipeline runners resolve their own config (including per-run overrides like
// MemoryMB from the agent request), and a completer that overwrote them would
// silently undo that. "The completer fills the gaps" is the other half, and is
// what gives the terminal and the two server routes the provisioned image they
// never looked up.
func mergeConfig(base, resolved provider.CrewConfig) provider.CrewConfig {
	out := base
	if out.ID == "" {
		out.ID = resolved.ID
	}
	if out.Slug == "" {
		out.Slug = resolved.Slug
	}
	if out.MemoryMB <= 0 {
		out.MemoryMB = resolved.MemoryMB
	}
	if out.CPUs <= 0 {
		out.CPUs = resolved.CPUs
	}
	if out.NetworkMode == "" {
		out.NetworkMode = resolved.NetworkMode
	}
	if len(out.AllowedDomains) == 0 {
		out.AllowedDomains = resolved.AllowedDomains
	}
	if out.TTLHours <= 0 {
		out.TTLHours = resolved.TTLHours
	}
	if out.Image == "" {
		out.Image = resolved.Image
	}
	if out.CachedImage == "" {
		out.CachedImage = resolved.CachedImage
	}
	if len(out.ContainerEnv) == 0 {
		out.ContainerEnv = resolved.ContainerEnv
	}
	if out.LoginPath == "" {
		out.LoginPath = resolved.LoginPath
	}
	if !out.Privileged {
		out.Privileged = resolved.Privileged
	}
	if !out.Init {
		out.Init = resolved.Init
	}
	if len(out.CapAdd) == 0 {
		out.CapAdd = resolved.CapAdd
	}
	if len(out.SecurityOpt) == 0 {
		out.SecurityOpt = resolved.SecurityOpt
	}
	if len(out.ExtraMounts) == 0 {
		out.ExtraMounts = resolved.ExtraMounts
	}
	if len(out.PostStartCommands) == 0 {
		out.PostStartCommands = resolved.PostStartCommands
	}
	if !out.InitHookEnabled {
		out.InitHookEnabled = resolved.InitHookEnabled
	}
	if len(out.Services) == 0 {
		out.Services = resolved.Services
	}
	return out
}
