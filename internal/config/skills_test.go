package config

import "testing"

func TestSkillPrivateHostsBootConfiguration(t *testing.T) {
	t.Setenv("CREWSHIP_SKILLS_PRIVATE_HOSTS", "git.internal.example, GIT2.Example.")
	cfg := Default()
	applyEnvOverrides(cfg)
	if len(cfg.Skills.PrivateHosts) != 2 || cfg.Skills.PrivateHosts[1] != "GIT2.Example." {
		t.Fatalf("unexpected policy: %+v", cfg.Skills)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{"*.example", "https://git.example", "git.example:443", "git.example,,other.example"} {
		t.Setenv("CREWSHIP_SKILLS_PRIVATE_HOSTS", entry)
		cfg := Default()
		applyEnvOverrides(cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("invalid boot policy accepted: %q", entry)
		}
	}
	t.Setenv("CREWSHIP_SKILLS_PRIVATE_HOSTS", "")
	applyEnvOverrides(cfg)
	if len(cfg.Skills.PrivateHosts) != 0 {
		t.Fatal("empty explicit override did not clear YAML policy")
	}
}
