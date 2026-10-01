//go:build !clionly

package main

import (
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/quota"
	"testing"
)

func TestDockerProviderQuotaHelperConfig(t *testing.T) {
	cfg := config.Default()
	if dockerProviderConfig(cfg, nil).QuotaCatalog != nil {
		t.Fatal("unset helper became configured")
	}
	cfg.Container.QuotaHelperSocket = "/run/private/helper.sock"
	cfg.Container.QuotaHelperNamespace = "database-a"
	c, ok := dockerProviderConfig(cfg, nil).QuotaCatalog.(quota.Client)
	if !ok || c.Socket != cfg.Container.QuotaHelperSocket || c.Namespace != "database-a" {
		t.Fatal("helper instance configuration lost")
	}
}
