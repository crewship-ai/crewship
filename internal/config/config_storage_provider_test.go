package config

import "testing"

// TestValidation_S3StorageProviderRejected pins the half of #1768 item 7 that
// is a live nil-pointer waiting for a caller: `s3` used to pass Validate()
// while cmd_start.go's provider switch had a case only for `localfs`, so an
// operator who set CREWSHIP_STORAGE_PROVIDER=s3 got a started server with
// deps.Storage == nil and a single WARN line about it.
//
// Nothing dereferenced it yet, which is the only reason it was survivable.
// Attachments (#1768 item 7) are the first feature that would, so the value
// has to stop validating BEFORE that lands, not after.
//
// The choice here is "refuse" rather than "implement": there is no s3
// implementation in internal/provider/ (localfs, bbolt, docker, apple), and a
// config that accepts a value the binary cannot honour is worse than one that
// refuses it — the operator who set it believes their files are on S3.
func TestValidation_S3StorageProviderRejected(t *testing.T) {
	cfg := Default()
	cfg.Storage.Provider = "s3"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("storage.provider=s3 must be rejected: no s3 provider is implemented, " +
			"so accepting it leaves deps.Storage nil at runtime")
	}
}

// TestValidation_LocalfsStillValid guards the obvious over-correction — the
// only implemented provider must keep validating.
func TestValidation_LocalfsStillValid(t *testing.T) {
	cfg := Default()
	cfg.Storage.Provider = "localfs"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("storage.provider=localfs must stay valid, got: %v", err)
	}
}

// TestEnvOverride_StorageProvider proves CREWSHIP_STORAGE_PROVIDER still lands
// on the right field. It calls applyEnvOverrides directly rather than Load(),
// because with localfs the only valid value there is no longer a
// distinguishable value that survives Validate() — and asserting the override
// with the same string as the default would prove nothing at all.
func TestEnvOverride_StorageProvider(t *testing.T) {
	t.Setenv("CREWSHIP_STORAGE_PROVIDER", "not-a-real-provider")

	cfg := Default()
	applyEnvOverrides(cfg)

	if cfg.Storage.Provider != "not-a-real-provider" {
		t.Errorf("CREWSHIP_STORAGE_PROVIDER must reach Storage.Provider, got %q", cfg.Storage.Provider)
	}
	if err := cfg.Validate(); err == nil {
		t.Error("...and the value must then be rejected by Validate")
	}
}
