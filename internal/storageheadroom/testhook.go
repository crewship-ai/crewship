package storageheadroom

import "github.com/crewship-ai/crewship/internal/diskusage"

// SetUsageForTest replaces the free-disk measurement for tests in other
// packages that must place free space between two probe sizes. It returns
// the restore function; production code never calls it.
func SetUsageForTest(fn func(string) (diskusage.Stats, error)) (restore func()) {
	next := usageFunc(fn)
	previous := usage.Swap(&next)
	return func() { usage.Store(previous) }
}
