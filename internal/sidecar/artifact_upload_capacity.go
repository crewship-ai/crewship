package sidecar

import "net/http"

// The sidecar buffers a bounded JSON request and response for the host IPC
// transport. Bound concurrency as well as bytes; the host has its own decoder
// admission limit for requests arriving through other transports.
var artifactForwardSlots = make(chan struct{}, 2)

func acquireArtifactForward(w http.ResponseWriter, r *http.Request) (func(), bool) {
	select {
	case artifactForwardSlots <- struct{}{}:
		return func() { <-artifactForwardSlots }, true
	case <-r.Context().Done():
		http.Error(w, "artifact request cancelled while waiting for capacity", http.StatusRequestTimeout)
		return nil, false
	}
}
