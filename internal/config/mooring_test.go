package config

import (
	"testing"
	"time"
)

func TestMooringDeliveryLeaseOperatorConfig(t *testing.T) {
	if Default().Mooring.Enabled {
		t.Fatal("runtime must remain default off")
	}
	if Default().Mooring.DeliveryLease() != 5*time.Second {
		t.Fatal("default must be five seconds")
	}
	for _, value := range []string{"3000", "5000", "0", "5001", "-1", "bad"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CREWSHIP_MOORING_DELIVERY_LEASE_MS", value)
			c := Default()
			applyEnvOverrides(c)
			valid := value == "3000" || value == "5000" || value == "0"
			if (c.Mooring.validate() == nil) != valid {
				t.Fatal("invalid operator value accepted or valid value rejected", c.Mooring)
			}
			if value == "3000" && c.Mooring.DeliveryLease() != 3*time.Second {
				t.Fatal("operator lease not applied")
			}
		})
	}
}
