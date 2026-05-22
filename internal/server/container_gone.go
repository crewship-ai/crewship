package server

import "strings"

// containerGone reports whether the error message from a Docker API
// call indicates the targeted container no longer exists or has stopped
// permanently. The stats collector and listening-port scanner used to
// log a generic "scan failed" and keep polling indefinitely on these
// errors, so a `docker rm -f <crew_container>` or an unrecoverable
// container exit produced a 15-second-loop spam of "No such container"
// debug lines forever — and worse, kept the dead container ID alive in
// the in-memory tracked-set so a fresh `crewship ask` would happily
// return the stale ID instead of triggering a re-create. (Issue #534.)
//
// We match on the Docker SDK's textual error shape rather than the
// errdefs types because the errors travel through one fmt.Errorf wrap
// before reaching us; errors.Is would require unwrapping the chain
// every call. The literals are stable parts of the SDK contract.
func containerGone(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "No such container") ||
		strings.Contains(s, "is not running") ||
		strings.Contains(s, "container is not running") ||
		strings.Contains(s, "removal of container")
}
