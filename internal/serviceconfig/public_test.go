package serviceconfig

import "testing"

func TestPublic(t *testing.T) {
	for _, raw := range []string{
		`[{"env":{"ORDINARY_NAME":"secret"}}]`,
		`[{"command":["redis-server","--requirepass","secret"]}]`,
		`[{"healthcheck":{"test":["CMD","secret"]}}]`,
		`[{"future_field":"secret"}]`, `not json`, Redacted,
		`[{"image":{"nested":"secret"}}]`,
		`[{"volumes":[{"name":"data","password":"secret"}]}]`,
		`[] {"secret":"extra document"}`,
	} {
		if got := Public(raw); got != Redacted {
			t.Errorf("private configuration was not withheld")
		}
	}
	for _, raw := range []string{"", "[]", `[{"name":"cache","image":"redis:7","env_refs":["CACHE_PASSWORD"]}]`} {
		if got := Public(raw); got != raw {
			t.Errorf("topology unexpectedly changed: %q", got)
		}
	}
}

func TestPublicQuotaConfiguration(t *testing.T) {
	raw := `[{"name":"database","image":"alpine:3","quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":67108864,"generation":1}]}]`
	if got := Public(raw); got != raw {
		t.Fatalf("nonsecret quota schema withheld: %s", got)
	}
}
