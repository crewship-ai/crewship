package api

import "testing"

func TestTriggerTypeFromTriggeredVia(t *testing.T) {
	for via, want := range map[string]string{
		"":           "",
		"manual":     "USER",
		"cli":        "USER",
		"schedule":   "CRON",
		"wake_check": "CRON",
		"webhook":    "WEBHOOK",
		"Automation": "AUTOMATION",
	} {
		if got := triggerTypeFromTriggeredVia(via); got != want {
			t.Errorf("%q: got %q want %q", via, got, want)
		}
	}
}
