package backup

// SetBeforeDatabaseCheckForTest installs the seam and returns its undo.
func SetBeforeDatabaseCheckForTest(f func()) func() {
	prev := beforeDatabaseCheck
	beforeDatabaseCheck = f
	return func() { beforeDatabaseCheck = prev }
}
