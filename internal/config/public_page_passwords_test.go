package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPublicPagePasswordLimits(t *testing.T) {
	automatic, err := (PublicPagePasswordsConfig{}).Resolve()
	if err != nil || automatic.MaxConcurrent != max(1, min(4, runtime.GOMAXPROCS(0)/2)) || automatic.MaxConcurrentPerTarget != 1 {
		t.Fatalf("automatic=%+v err=%v", automatic, err)
	}
	for _, c := range []PublicPagePasswordsConfig{{-1, 1}, {1, -1}, {1025, 1}, {1, 1025}, {1, 2}} {
		if _, err := c.Resolve(); err == nil {
			t.Errorf("accepted invalid limits %+v", c)
		}
	}
	explicit := PublicPagePasswordsConfig{3, 2}
	resolved, err := explicit.Resolve()
	if err != nil || resolved != explicit {
		t.Fatalf("explicit limits=%+v err=%v", resolved, err)
	}
}

func TestPublicPagePasswordLimitsConfiguration(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(file, []byte("auth:\n  public_page_passwords:\n    max_concurrent: 3\n    max_concurrent_per_target: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.PublicPagePasswords != (PublicPagePasswordsConfig{3, 2}) {
		t.Fatalf("yaml limits=%+v", cfg.Auth.PublicPagePasswords)
	}
	t.Setenv("CREWSHIP_PUBLIC_PAGE_PASSWORD_MAX_CONCURRENT", "5")
	t.Setenv("CREWSHIP_PUBLIC_PAGE_PASSWORD_MAX_CONCURRENT_PER_TARGET", "4")
	cfg, err = Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.PublicPagePasswords != (PublicPagePasswordsConfig{5, 4}) {
		t.Fatalf("env limits=%+v", cfg.Auth.PublicPagePasswords)
	}
	t.Setenv("CREWSHIP_PUBLIC_PAGE_PASSWORD_MAX_CONCURRENT", "-1")
	if _, err := Load(file); err == nil {
		t.Fatal("negative env limit accepted")
	}
}

func TestPublicPagePasswordCPUCapacityPolicy(t *testing.T) {
	for _, tc := range []struct{ cpus, want int }{{1, 1}, {2, 1}, {3, 1}, {4, 2}, {8, 4}, {64, 4}} {
		if got := defaultPublicPagePasswordConcurrency(tc.cpus); got != tc.want {
			t.Errorf("cpus%d got%d want%d", tc.cpus, got, tc.want)
		}
	}
}
