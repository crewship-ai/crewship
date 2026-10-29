//go:build !linux

package mooring

import "errors"

// NewBootOwnerClock fails closed where no suspend-aware native clock is provided.
func NewBootOwnerClock() (OwnerClock, error) {
	return nil, errors.New("Mooring owner leases require Linux CLOCK_BOOTTIME")
}
