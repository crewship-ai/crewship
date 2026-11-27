//go:build windows

package craftengine

import "os"

func killSelf() { os.Exit(137) }
