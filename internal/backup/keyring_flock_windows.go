//go:build windows

package backup

// fileLock on Windows is a no-op stub. The unix implementation uses
// flock(2) which has no direct Windows equivalent — LockFileEx exists
// but the production target is Linux (the dogfood server runs Ubuntu)
// and the dev target is macOS. Windows is a contributor convenience,
// not a deployment platform, so the multi-process race the unix lock
// closes simply does not exist where it matters. Documented here so
// the next person who runs CI on windows-latest doesn't add a
// LockFileEx implementation that would have to be tested in a CI
// runner the project does not currently use.
type fileLock struct{}

func newFileLock(_ string) *fileLock { return &fileLock{} }
func (l *fileLock) Lock() error      { return nil }
func (l *fileLock) Unlock() error    { return nil }
