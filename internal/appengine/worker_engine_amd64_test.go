//go:build linux && amd64

package appengine

import "github.com/crewship-ai/crewship/internal/appengine/internal/i386probe"

// foreignArchRefused reports whether an i386 call through int $0x80 was
// refused with EPERM.
func foreignArchRefused() bool { return i386probe.Getpid() == -1 }
