//go:build !linux

package writerlease

import "os"

func lockGeneration(string) (*os.File, error) { return nil, nil }
