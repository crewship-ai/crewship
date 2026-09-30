package config

import "testing"

func TestQuotaHelperConfiguration(t *testing.T) {
	cfg := Default()
	cfg.Container.QuotaHelperSocket = "/run/crewship-quota/a/helper.sock"
	if cfg.Validate() == nil {
		t.Fatal("socket without database namespace admitted")
	}
	cfg.Container.QuotaHelperNamespace = "database-a"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Container.QuotaHelperNamespace = "../other"
	if cfg.Validate() == nil {
		t.Fatal("namespace path admitted")
	}
	t.Setenv("CREWSHIP_QUOTA_HELPER_SOCKET", "/run/crewship-quota/b/helper.sock")
	t.Setenv("CREWSHIP_QUOTA_HELPER_NAMESPACE", "database-b")
	applyEnvOverrides(cfg)
	if cfg.Container.QuotaHelperNamespace != "database-b" || cfg.Container.QuotaHelperSocket != "/run/crewship-quota/b/helper.sock" {
		t.Fatal("helper environment override missing")
	}
}
