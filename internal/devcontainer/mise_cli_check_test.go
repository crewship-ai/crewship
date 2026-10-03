package devcontainer

import (
	"strings"
	"testing"
)

func TestMiseAICLICheckValidationAndAdapterCompletion(t *testing.T) {
	for _, policy := range []string{"unknown", "Required"} {
		cfg, err := ParseMiseConfig(`{"tools":{"node":"22"},"ai_cli_check":"` + policy + `"}`)
		if err == nil {
			err = cfg.Validate()
		}
		if err == nil {
			t.Errorf("unknown check policy %q accepted", policy)
		}
	}
	plan, err := EnsureAdapterCLIs(&Config{}, `{"tools":{"node":"22"},"ai_cli_check":"required"}`, []string{"CLAUDE_CODE"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.MiseConfig, `"ai_cli_check":"required"`) {
		t.Fatalf("adapter completion discarded policy: %s", plan.MiseConfig)
	}
	cfg, err := ParseMiseConfig(plan.MiseConfig)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cfg.ToTOML(), "ai_cli_check") {
		t.Fatal("Crewship policy leaked into native mise TOML")
	}
}
