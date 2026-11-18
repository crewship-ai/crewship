package main

import "testing"

func TestKeeperRuntimeCommandDiscovery(t *testing.T) {
	for _, name := range []string{"request", "execute", "request-status"} {
		cmd, remaining, err := keeperCmd.Find([]string{name})
		if err != nil || cmd == nil || cmd.Name() != name || len(remaining) != 0 {
			t.Errorf("native agent Keeper command %q unavailable", name)
		}
	}
}
