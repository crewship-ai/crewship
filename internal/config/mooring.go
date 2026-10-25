package config

import (
	"fmt"
	"time"
)

const DefaultMooringDeliveryLease = 5 * time.Second

// MooringConfig contains operator policy, never participant-selected grants.
// A shorter lease is permitted; the five-second revocation ceiling remains.
type MooringConfig struct {
	Enabled                   bool `yaml:"enabled"`
	DeliveryLeaseMilliseconds int  `yaml:"delivery_lease_ms"`
}

func (c MooringConfig) DeliveryLease() time.Duration {
	if c.DeliveryLeaseMilliseconds == 0 {
		return DefaultMooringDeliveryLease
	}
	return time.Duration(c.DeliveryLeaseMilliseconds) * time.Millisecond
}

func (c MooringConfig) validate() error {
	if c.DeliveryLeaseMilliseconds != 0 && (c.DeliveryLeaseMilliseconds < 1 || c.DeliveryLeaseMilliseconds > 5000) {
		return fmt.Errorf("mooring.delivery_lease_ms must be zero (default 5000) or between 1 and 5000")
	}
	return nil
}
