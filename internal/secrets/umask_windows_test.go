//go:build windows

package secrets

// syscallUmask is a no-op on Windows; TestWriteFile_PermissionsOnDirAndFile
// short-circuits via runtime.GOOS before this helper is ever exercised.
func syscallUmask(int) int { return 0 }
