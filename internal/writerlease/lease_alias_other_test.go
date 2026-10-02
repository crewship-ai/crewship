//go:build !linux && !darwin && !windows

package writerlease

func leaseTestAliases(dir, path string) []string { return []string{path} }
