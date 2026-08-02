package api

// GET /api/v1/runtime/capacity — what admission control is doing right now
// (#1668).
//
// This endpoint is the difference between a queue and a hang. A container
// start held for host capacity looks, from every other surface, exactly like
// a slow one: the run sits in RUNNING, the chat shows nothing, the container
// never appears. Without somewhere to ask "is anything being held, and why",
// the honest answer an operator can give is "it's stuck" — and a feature that
// reads as a hang earns a bug report rather than trust.
//
// Read-only and instance-scoped: the host is a property of the instance, not
// of a workspace, so there is nothing here to scope and nothing to mutate.

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/crewship-ai/crewship/internal/admission"
)

// AdmissionSnapshotter is the read side of the admission controller. An
// interface so the handler is testable without building a controller, and so
// nothing here can accidentally acquire a slot.
type AdmissionSnapshotter interface {
	Snapshot(ctx context.Context) admission.Snapshot
}

// RuntimeCapacityHandler serves the admission-control status surface.
type RuntimeCapacityHandler struct {
	snap AdmissionSnapshotter
}

func NewRuntimeCapacityHandler(s AdmissionSnapshotter) *RuntimeCapacityHandler {
	return &RuntimeCapacityHandler{snap: s}
}

// runtimeCapacityResponse is the wire shape.
//
// `enabled` is separate from the limits on purpose: an instance with
// admission control not wired at all and one with every leg set to 0 are
// different situations, and an operator debugging a hold needs to tell them
// apart.
//
// `host_signal_available` is the macOS answer made visible. There, neither
// /proc/meminfo nor /proc/pressure/memory exists, the host-memory leg is
// inactive, and this field says so rather than reporting a gate that is
// quietly doing nothing.
type runtimeCapacityResponse struct {
	Enabled bool `json:"enabled"`
	admission.Snapshot
}

func (h *RuntimeCapacityHandler) Get(w http.ResponseWriter, r *http.Request) {
	resp := runtimeCapacityResponse{Enabled: h.snap != nil}
	if h.snap != nil {
		resp.Snapshot = h.snap.Snapshot(r.Context())
	} else {
		resp.HostSignalError = "admission control not configured on this instance"
	}
	if resp.Held == nil {
		// An empty list, never null: a client rendering "nothing is held"
		// should not have to distinguish the two.
		resp.Held = []admission.Hold{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
