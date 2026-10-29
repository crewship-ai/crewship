package appengine

// barrierPoint names where a test barrier may park an operation. The barrier
// itself is compiled only with the crewship_testbarrier build tag; release
// builds contain a no-op (barrier_off.go) and CI rejects tagged binaries.
type barrierPoint string

const (
	// barrierPreCommit parks after the candidate is prepared, before the
	// commit transaction re-checks token, lease, base, candidate, authority
	// and effects.
	barrierPreCommit barrierPoint = "pre-commit"
	// barrierPostCommit parks after a successful commit, before the receipt is
	// returned, which models an acknowledgement lost after durability.
	barrierPostCommit barrierPoint = "post-commit"
)

const (
	// TestBarrierDirEnv names the private directory used for .parked/.release
	// files. Ignored by builds without the crewship_testbarrier tag.
	TestBarrierDirEnv = "CREWSHIP_GRIDCRAFT_TEST_BARRIER_DIR"
	// TestBarrierSlotEnv names the development slot; only dev1..dev4 are allowed.
	TestBarrierSlotEnv = "CREWSHIP_GRIDCRAFT_TEST_BARRIER_SLOT"
	// TestBarrierMutationPrefix is the only mutation id prefix that can park.
	TestBarrierMutationPrefix = "barrier-"
)
