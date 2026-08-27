package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/buildinfo"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/provider/apple"
	"github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/update"
)

// SystemHandler provides endpoints for system-level health and runtime detection.
type SystemHandler struct {
	logger  *slog.Logger
	version string
	build   buildinfo.Info

	// detectDocker enumerates every Docker-API-compatible runtime that answers
	// on this host. It is a field rather than a direct call so the tests can
	// pin the inventory logic to a fixed set of runtimes instead of whatever
	// the machine running `go test` happens to have installed.
	detectDocker func(context.Context) []docker.DetectResult

	// detectApple reports the Apple Containers version, or an error when the
	// runtime is absent. Apple is probed separately, and composed in here,
	// because it is not a Docker-API daemon at all — it has no socket, so
	// docker.DetectAll neither knows nor can know about it.
	detectApple func(context.Context) (string, error)

	// activeRuntime names the runtime this PROCESS actually connected to.
	//
	//	detected — the docker provider's own DetectResult, for comparison via
	//	           docker.SameRuntimeEndpoint; zero when the provider is Apple
	//	apple    — the active provider is Apple Containers
	//	ok       — false when the server holds no container provider at all
	//	           (--no-docker, or one that failed to build), in which case
	//	           nothing on this host is in use however many runtimes
	//	           answered the probe
	activeRuntime func() (detected docker.DetectResult, apple bool, ok bool)
}

// NewSystemHandler creates a SystemHandler with the given logger and the
// current binary version (used by GET /api/v1/system/version to surface
// "update available" to the web UI).
//
// The build identity defaults to whatever this binary can work out about
// itself — for an ldflags-less `go build` (how dev.sh builds every dev slot)
// that is the VCS stamp, which is the only source that answers at all there.
// Wiring that has the ldflags values calls WithBuild to override.
func NewSystemHandler(logger *slog.Logger, version string) *SystemHandler {
	return &SystemHandler{
		logger:        logger,
		version:       version,
		build:         buildinfo.Resolve(version, "", ""),
		detectDocker:  docker.DetectAll,
		detectApple:   detectAppleVersion,
		activeRuntime: activeRuntimeFrom(nil),
	}
}

func detectAppleVersion(ctx context.Context) (string, error) {
	res, err := apple.Detect(ctx)
	if err != nil {
		return "", err
	}
	return res.Version, nil
}

// dockerDaemonNamer is implemented by *docker.Provider: it is the only
// container provider that can say which daemon it dialled.
type dockerDaemonNamer interface {
	Detected() docker.DetectResult
}

// activeRuntimeFrom derives the in_use accessor from the container provider
// this process is holding (server.go passes deps.Container to the router).
//
// It takes `any` rather than provider.ContainerProvider so that the honest
// answer for "no provider at all" is reachable — and so the mapping can be
// tested without standing up a ten-method fake.
//
// A provider that cannot name a daemon is the Apple one: `container.provider`
// accepts only docker, apple or auto (internal/config/config.go), and auto
// resolves to one of those two, so elimination is exhaustive rather than a
// guess.
func activeRuntimeFrom(cp any) func() (docker.DetectResult, bool, bool) {
	if cp == nil {
		return func() (docker.DetectResult, bool, bool) { return docker.DetectResult{}, false, false }
	}
	if d, isDocker := cp.(dockerDaemonNamer); isDocker {
		return func() (docker.DetectResult, bool, bool) { return d.Detected(), false, true }
	}
	return func() (docker.DetectResult, bool, bool) { return docker.DetectResult{}, true, true }
}

// WithActiveContainer teaches the handler which runtime the running server
// actually connected to, so `in_use` reports ground truth rather than
// re-deriving it from the probe order — the two differ whenever DOCKER_HOST is
// set, and disagree entirely when the server booted with --no-docker.
func (h *SystemHandler) WithActiveContainer(cp any) *SystemHandler {
	h.activeRuntime = activeRuntimeFrom(cp)
	return h
}

// WithBuild replaces the resolved build identity — used by the router so the
// ldflags-injected commit and date from package main reach the endpoint
// (internal/api cannot reference package main's vars directly).
func (h *SystemHandler) WithBuild(b buildinfo.Info) *SystemHandler {
	h.build = b
	return h
}

// installLinks is the set of runtimes an operator can install and Crewship can
// then actually drive — one entry per label the detection vocabulary can
// produce, which is what runtime_install_links_test.go pins against the
// detector's own candidate list rather than against a copy of it.
//
// `rancher` was missing. That was nearly unreachable while Detect labelled by
// candidate path — Rancher Desktop with administrative access on points
// /var/run/docker.sock at ~/.rd/docker.sock, and the entry came back as
// `docker`, quietly using the Docker link. #1688 made the name honest and in
// doing so exposed the hole.
//
// containerd/nerdctl is deliberately NOT here, and is not in the detector's
// candidates either: containerd serves its own gRPC API rather than the Docker
// REST API, so no version of it can ever answer the probe (#1687). Offering an
// install link would be advertising a runtime this server cannot drive — the
// exact dishonesty this endpoint exists to remove.
var installLinks = map[string]string{
	"docker":   "https://docs.docker.com/get-docker/",
	"podman":   "https://podman.io/docs/installation",
	"colima":   "https://github.com/abiosoft/colima",
	"orbstack": "https://orbstack.dev/",
	"rancher":  "https://rancherdesktop.io/",
	"apple":    "https://github.com/apple/container",
}

// runtimeEntry is one container runtime present on the host.
type runtimeEntry struct {
	Runtime string `json:"runtime"`
	Version string `json:"version"`
	Socket  string `json:"socket"`
	// InUse marks the single runtime this server is actually driving. At most
	// one entry carries it, and when the server has no container provider no
	// entry does — a runtime being installed and a runtime being used are
	// different facts, and this endpoint is the only place that can tell them
	// apart.
	InUse bool `json:"in_use"`
	// Gaps are the crew hardening controls this runtime is measured not to
	// deliver (docker.KnownRuntimeGaps). Absent when there are none, which is
	// every runtime but podman below 5.
	//
	// Set on the `in_use` entry ONLY, and computed from the DetectResult the
	// running provider reports rather than from this entry's own probe. That is
	// the same input docker.logRuntimeGaps uses at startup, so the boot WARN and
	// this field cannot drift into contradicting each other. Hanging gaps on an
	// installed-but-unused runtime would also be advice nobody can take:
	// `container.provider` accepts docker | apple | auto only, so there is no
	// switching to the entry it would be warning about (#1689).
	Gaps []docker.Gap `json:"gaps,omitempty"`
}

// inventory probes the host and returns every runtime that answered, with the
// one this process is driving marked.
//
// There is no cache behind this: docker.DetectAll re-probes every candidate on
// every call. It is bounded and fast (single-digit milliseconds, concurrent,
// and a cancelled context yields a partial list rather than a hang), which is
// right for a status panel and wrong for anything on a hot path. Nothing here
// should end up on one — see the fetch-policy comment in
// app/(dashboard)/admin/tabs/runtime-tab.tsx.
func (h *SystemHandler) inventory(ctx context.Context) []runtimeEntry {
	dockerResults := h.detectDocker(ctx)
	entries := make([]runtimeEntry, 0, len(dockerResults)+1)
	for _, d := range dockerResults {
		entries = append(entries, runtimeEntry{Runtime: d.Runtime, Version: d.Version, Socket: d.Socket})
	}

	appleVersion, appleErr := h.detectApple(ctx)
	if appleErr == nil {
		entries = append(entries, runtimeEntry{Runtime: "apple", Version: appleVersion})
	}

	active, activeIsApple, running := h.activeRuntime()
	if !running {
		if len(entries) == 0 {
			h.logger.Debug("no container runtime found", "apple_error", appleErr)
		}
		return entries
	}

	if activeIsApple {
		for i := range entries {
			if entries[i].Runtime == "apple" {
				entries[i].InUse = true
				return entries
			}
		}
		// The Apple provider is running but its CLI probe just failed — a
		// transient `container system status`. It is still what agents run on.
		return append(entries, runtimeEntry{Runtime: "apple", InUse: true})
	}

	// entries[i] is the runtimeEntry built from dockerResults[i] — the docker
	// results were appended first, in order, before Apple.
	for i, d := range dockerResults {
		// NOT a Socket string comparison. Detect stores DOCKER_HOST verbatim
		// ("unix:///var/run/docker.sock") where DetectAll stores a plain path,
		// and one daemon is reachable under several paths that resolve to the
		// same socket. SameRuntimeEndpoint resolves both before comparing;
		// string equality here would silently report nothing in use on exactly
		// the setups this endpoint was built for.
		if docker.SameRuntimeEndpoint(d, active) {
			entries[i].InUse = true
			// From `active`, not from `d`. The two describe the same daemon, but
			// `active` is what the provider actually dialled and what the startup
			// WARN was computed from — deriving the gaps from anything else is an
			// invitation for the two reports to disagree about the same host.
			entries[i].Gaps = docker.KnownRuntimeGaps(active)
			return entries
		}
	}

	// The daemon in use was not among the probed candidates — DOCKER_HOST can
	// point at a remote tcp:// engine that no socket path covers. Listing it is
	// the whole point of the endpoint; dropping it would leave the inventory
	// claiming nothing is in use while agents are running on it.
	return append(entries, runtimeEntry{
		Runtime: active.Runtime, Version: active.Version, Socket: active.Socket, InUse: true,
		Gaps: docker.KnownRuntimeGaps(active),
	})
}

// Runtime reports every container runtime present on this host, and which one
// the server is actually driving.
//
// GET /api/v1/system/runtime
// Accessible to any authenticated user (no workspace role required).
//
// The `runtimes` array is the inventory; `runtime`/`version`/`socket` summarise
// the entry with `in_use` set. Those top-level fields are null when runtimes
// are installed but none is in use — the server booted without a container
// provider — because naming one of them there would report a runtime that is
// running nothing.
//
// The `in_use` entry also carries `gaps[]` when the runtime is measured not to
// honour a crew hardening control (#1672). Deliberately NOT mirrored to the top
// level next to runtime/version/socket: one copy cannot disagree with itself,
// and a consumer that wants it is already selecting the `in_use` entry to get
// the socket. It is host detail, so the redacted arm below carries none of it.
func (h *SystemHandler) Runtime(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	runtimes := h.inventory(ctx)

	if len(runtimes) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"available":     false,
			"runtime":       nil,
			"version":       nil,
			"socket":        nil,
			"runtimes":      []interface{}{},
			"install_links": installLinks,
		})
		return
	}

	// Redact host detail for non-admins (#865). Container versions and socket
	// paths are host infrastructure info; the runtime banner, onboarding, and
	// crew docker tab only need to know whether a runtime is available. ADMIN+
	// callers — resolved by OptionalWorkspaceRole when the request carries a
	// workspace_id (the admin console passes X-Workspace-ID) — get the full
	// detail. A role-less or below-ADMIN caller gets the bare availability flag
	// instead of a 403 so those non-admin surfaces keep working.
	if !canRole(RoleFromContext(r.Context()), "manage") {
		// `in_use` alongside `available`, and the distinction is load-bearing
		// rather than pedantic: `available` means a runtime is installed and
		// answering a ping, which says nothing about whether THIS server is
		// driving one. A host running Docker while crewshipd was started with
		// --no-docker reports available=true and can still run no container at
		// all — and that is not a hypothetical, it is what dev.sh falls back to
		// and what the packaging documents as a supported dashboard-only mode.
		//
		// Onboarding gates the Crew step on this, so it has to be the honest
		// bit. It leaks no host detail — no version, no socket path, no
		// runtime name — so it stays inside the #865 redaction contract that
		// the rest of this branch exists to enforce.
		inUse := false
		for _, rt := range runtimes {
			if rt.InUse {
				inUse = true
				break
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"available": true, "in_use": inUse})
		return
	}

	resp := map[string]interface{}{
		"available": true,
		// in_use on BOTH branches. It was added only to the redacted arm
		// above, which happened to work because the onboarding probe sends no
		// workspace context and so never resolves a role — meaning an owner
		// who did send one, or a switch from serverFetch to apiFetch, would
		// read `undefined` and be stuck forever on "Docker is running, but
		// this Crewship server isn't using it". A field the wizard gates on
		// must not depend on the caller's privilege.
		"in_use":   false,
		"runtime":  nil,
		"version":  nil,
		"socket":   nil,
		"runtimes": runtimes,
		// Alongside an available runtime too, not only when none was found: an
		// operator with one runtime installed still needs to be told what the
		// others are (#1690).
		"install_links": installLinks,
	}
	for _, rt := range runtimes {
		if rt.InUse {
			resp["in_use"] = true
			resp["runtime"], resp["version"], resp["socket"] = rt.Runtime, rt.Version, rt.Socket
			break
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// Version reports the running binary's build identity plus the latest
// available release (cached on disk by internal/update — 24h on the stable
// channel, 1h on nightly, so per-request polling never exhausts GitHub's
// unauthenticated API quota). The web UI uses `current`/`latest`/`newer`/`url`
// to render a "Crewship vX.Y.Z available" banner; the CLI does its own check
// at boot.
//
// `commit` / `build_time` / `dirty` / `go_version` / `os` / `arch` /
// `schema_version` were added by #1645: this is the only way to ask a running
// server WHICH BUILD it is. `current` alone cannot answer that — every build a
// dev slot has ever made reports "dev", which is how dev1 sat a full day
// behind main with nothing able to say so. `dirty` is deliberately nullable:
// a build with ldflags but no VCS stamping does not know, and reporting that
// as `false` would be a confident wrong answer.
//
// Failures from the update package surface as `latest: null` so the client
// can render gracefully (no scary error UI for a transient GitHub outage).
// GET /api/v1/system/version
func (h *SystemHandler) Version(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	resp := map[string]interface{}{
		"current": h.version,
		"latest":  nil,
		"newer":   false,
		"url":     nil,

		"commit":     h.build.Commit,
		"build_time": h.build.BuildTime,
		"dirty":      h.build.Dirty,
		"go_version": h.build.GoVersion,
		"os":         h.build.OS,
		"arch":       h.build.Arch,
		// The schema this BINARY expects, not the one its DB happens to be
		// at. For a server that has booted the two are the same number:
		// database.Migrate applies every registered migration on start and
		// its skew guard refuses to run against anything higher. Reporting
		// the binary's ceiling keeps the endpoint free of a DB round-trip
		// and answers the question a remote CLI actually has — "is the
		// schema I know about the schema you're serving?".
		"schema_version": database.MaxKnownMigrationVersion(),
	}

	// 4s upper bound: the update.Check call itself has a 5s internal HTTP
	// timeout, but we want the API response to feel snappy. If the cache is
	// warm this returns instantly; if it's cold and GitHub is slow, we'd
	// rather respond with "no info" than block the UI render.
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()

	result, err := update.Check(ctx, h.version)
	if err != nil {
		h.logger.Debug("system version check failed", "error", err)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if result == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp["latest"] = result.Latest
	resp["newer"] = result.Newer
	resp["url"] = result.URL
	writeJSON(w, http.StatusOK, resp)
}
